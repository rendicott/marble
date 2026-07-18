package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rendicott/marble/internal/api"
	"github.com/rendicott/marble/internal/bgtask"
	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/continuation"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/mcp"
	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/mpub"
	"github.com/rendicott/marble/internal/session"
	"github.com/rendicott/marble/internal/shellpolicy"
	"github.com/rendicott/marble/internal/tools"
	"github.com/rendicott/marble/internal/workspacefs"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("marble ")

	cfg, err := config.ParseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	if cfg.MemoryCreated {
		log.Printf("WARNING: memory directory did not exist; created %s", cfg.Memory)
		log.Printf("WARNING: sessions under %s/session/ · digests under %s/daily/ · db %s/marble.db",
			cfg.Memory, cfg.Memory, cfg.Memory)
	}

	// Ensure knowledge/skills/mpub dirs exist
	_ = os.MkdirAll(cfg.Memory+"/knowledge", 0o755)
	_ = os.MkdirAll(cfg.Memory+"/skills", 0o755)
	_ = os.MkdirAll(cfg.Memory+"/mpub", 0o755)

	store, err := memory.New(cfg.Memory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: memory: %v\n", err)
		os.Exit(2)
	}

	sqldb, err := db.Open(cfg.Memory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: database: %v\n", err)
		os.Exit(2)
	}
	defer sqldb.Close()

	if sqldb.Mode == db.ModeLimp {
		log.Printf("WARNING: LIMP MODE — %s", sqldb.Reason)
		log.Printf("WARNING: chat and Markdown still work; database writes disabled")
	} else {
		log.Printf("database ok: %s (schema v%d)", sqldb.Path, db.CurrentSchemaVersion)
	}

	policy := shellpolicy.New(cfg.Workspace, cfg.Memory, cfg.DisableShell, cfg.ShellDefaultTimeout, cfg.ShellMaxTimeout)
	if sqldb.Writable() {
		policy.BindSettings(sqldb)
	}
	bg := bgtask.New(policy)

	// MCP client (ADR-0006) — process-global; degrade on failure
	mcpMgr := mcp.NewManager(cfg.MCPTimeout, cfg.MCPDisable)
	mcpPath := mcp.ResolveConfigPath(cfg.MCPConfig, cfg.Memory)
	if !cfg.MCPDisable {
		fc, err := mcp.LoadFile(mcpPath)
		if err != nil {
			log.Printf("WARNING: mcp config %s: %v", mcpPath, err)
		} else if len(fc.MCPServers) == 0 {
			log.Printf("mcp: no servers in %s (optional)", mcpPath)
		} else {
			ctxMCP, cancelMCP := context.WithTimeout(context.Background(), 30*time.Second)
			mcpMgr.Start(ctxMCP, fc)
			cancelMCP()
		}
	} else {
		log.Printf("mcp: DISABLED (--mcp-disable)")
	}
	defer mcpMgr.Close()

	mpubStore, err := mpub.New(cfg.Memory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: mpub: %v\n", err)
		os.Exit(2)
	}

	client := model.New(cfg.BaseURL, cfg.Model, cfg.MaxOutput)
	toolReg := &tools.Registry{
		Workspace:      cfg.Workspace,
		Memory:         cfg.Memory,
		Addr:           cfg.Addr,
		MaxResultChars: cfg.MaxToolResult,
		Policy:         policy,
		BG:             bg,
		MCP:            mcpMgr,
		ShellDefault:   cfg.ShellDefaultTimeout,
		ShellMax:       cfg.ShellMaxTimeout,
	}
	runner := &session.Runner{
		Cfg:    cfg,
		Client: client,
		Tools:  toolReg,
	}
	reg := session.NewRegistry(runner, store, sqldb, cfg.Workspace, cfg.Model)
	runner.Reg = reg

	cont := continuation.New(func(sessionID, prompt string) {
		s, err := reg.EnsureLoaded(sessionID)
		if err != nil {
			log.Printf("continuation: session %s: %v", sessionID, err)
			return
		}
		if s.Status == "closed" {
			return
		}
		if !runner.PostContinuation(s, prompt) {
			// busy: re-schedule shortly
			log.Printf("continuation: session %s busy; retry in 5s", sessionID)
			go func() {
				time.Sleep(5 * time.Second)
				if !runner.PostContinuation(s, prompt) {
					log.Printf("continuation: session %s still busy; dropped", sessionID)
				}
			}()
		}
	}, func(taskID string) bool {
		t, ok := bg.Get(taskID)
		if !ok {
			return false
		}
		return t.Status != bgtask.StatusRunning
	})
	toolReg.Cont = cont
	defer cont.Stop()

	reg.OnSessionClose = func(sessionID string) {
		bg.KillSession(sessionID)
		cont.CancelSession(sessionID)
	}

	daemon := session.NewDaemon(reg, cfg.PersistEvery)
	daemon.Start()

	wsfs, err := workspacefs.New(cfg.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: workspace: %v\n", err)
		os.Exit(2)
	}

	srv := api.New(cfg, client, reg, daemon, wsfs)
	srv.MCP = mcpMgr
	srv.Policy = policy
	srv.Tools = toolReg
	srv.Mpub = mpubStore

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := client.Health(ctx); err != nil {
			log.Printf("model health: %v (will retry from UI)", err)
		} else {
			log.Printf("model ok: %s", cfg.Model)
		}
	}()

	go func() {
		log.Printf("listening on http://%s", displayAddr(cfg.Addr))
		log.Printf("workspace=%s", cfg.Workspace)
		log.Printf("memory=%s mode=%s", cfg.Memory, sqldb.Mode)
		log.Printf("persist every %s", cfg.PersistEvery)
		log.Printf("model=%s base=%s ctx=%d max_out=%d budget=%d max_tool_iters=%d",
			cfg.Model, cfg.BaseURL, cfg.ContextLimit, cfg.MaxOutput, cfg.Budget(), cfg.MaxToolIters)
		if cfg.DisableShell {
			log.Printf("shell: DISABLED (--disable-shell)")
		} else {
			log.Printf("shell: enabled mode=%s", "deny_list")
		}
		log.Printf("mcp: config=%s servers_ok=%d tools=%d", mcpPath, mcpMgr.ServerOKCount(), mcpMgr.ToolCount())
		log.Printf("mpub: /mpub · %d published · %s", mpubStore.Count(), mpubStore.Root)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	daemon.Stop()
	cont.Stop()
	mcpMgr.Close()
}

func displayAddr(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "127.0.0.1" + addr
	}
	return addr
}
