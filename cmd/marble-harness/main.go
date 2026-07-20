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
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rendicott/marble/internal/agentproc"
	"github.com/rendicott/marble/internal/api"
	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/bgtask"
	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/continuation"
	"github.com/rendicott/marble/internal/cron"
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

	// Lightweight version check before full flag parse (also works with -h flow below).
	for _, a := range os.Args[1:] {
		if a == "-version" || a == "--version" || a == "version" {
			fmt.Println("marble-harness", versionString())
			os.Exit(0)
		}
	}

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

	agents, err := agentproc.New(cfg.Memory, cfg.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: agent_process: %v\n", err)
		os.Exit(2)
	}

	client := model.New(cfg.BaseURL, cfg.Model, cfg.MaxOutput, cfg.APIKey)
	if strings.TrimSpace(cfg.APIKeyEnv) == "" {
		log.Printf("model auth: none")
	} else if cfg.APIKeyEnvConfigured {
		log.Printf("model auth: bearer from env %s (set)", cfg.APIKeyEnvUsed)
	} else {
		log.Printf("WARNING: model auth: --api-key-env=%s but no non-empty value found (running without Authorization)", cfg.APIKeyEnv)
	}
	toolReg := &tools.Registry{
		Workspace:      cfg.Workspace,
		Memory:         cfg.Memory,
		Addr:           cfg.Addr,
		MaxResultChars: cfg.MaxToolResult,
		Policy:         policy,
		BG:             bg,
		MCP:            mcpMgr,
		Agents:         agents,
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

	// ADR-0015 cron: durable schedules → inject prompt + start turn
	var modelOK atomicBool
	modelOK.set(true)
	cronMgr := cron.New(sqldb, func(jobID, jobName, sessionID, prompt string) cron.FireResult {
		var s *session.Session
		created := false
		if strings.TrimSpace(sessionID) != "" {
			if loaded, err := reg.EnsureLoaded(sessionID); err == nil {
				s = loaded
			}
		}
		if s == nil {
			title := "cron: " + jobName
			if len(title) > 48 {
				title = title[:48]
			}
			s = reg.Create(title)
			created = true
			log.Printf("cron: job %s created session %s", jobID, s.ID)
		}
		if s.Status == "closed" {
			s.Reopen()
			_ = reg.PersistSession(s)
		}
		if s.IsBusy() {
			return cron.FireResult{SessionID: s.ID, CreatedSession: created, Status: "skipped_busy"}
		}
		if !runner.PostCron(s, prompt) {
			return cron.FireResult{SessionID: s.ID, CreatedSession: created, Status: "skipped_busy"}
		}
		st := "ok"
		if created {
			st = "created_session"
		}
		return cron.FireResult{SessionID: s.ID, CreatedSession: created, Status: st}
	}, func() bool {
		if sqldb != nil && sqldb.Mode == db.ModeLimp {
			return false
		}
		return modelOK.get()
	})
	toolReg.Cron = cronMgr
	defer cronMgr.Stop()
	if sqldb.Writable() {
		log.Printf("cron: scheduler enabled (max %d jobs)", cron.MaxJobs)
	} else {
		log.Printf("cron: scheduler idle (database not writable)")
	}

	reg.OnSessionClose = func(sessionID string) {
		bg.KillSession(sessionID)
		agents.KillSession(sessionID)
		cont.CancelSession(sessionID)
	}

	daemon := session.NewDaemon(reg, cfg.PersistEvery)
	daemon.Start()

	wsfs, err := workspacefs.New(cfg.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: workspace: %v\n", err)
		os.Exit(2)
	}

	// ADR-0017 auth
	var authMgr *auth.Manager
	if cfg.AuthMode == "google" {
		allow := make(map[string]struct{}, len(cfg.AuthAllowlist))
		for _, e := range cfg.AuthAllowlist {
			allow[e] = struct{}{}
		}
		authMgr = &auth.Manager{
			Mode:   "google",
			Secure: cfg.CookieSecure(),
			Google: &auth.Google{
				ClientID:     cfg.OAuthClientID,
				ClientSecret: cfg.OAuthClientSecret,
				RedirectURL:  cfg.OAuthRedirectURL,
				Allowlist:    allow,
			},
			Store: auth.NewSessionStore(),
		}
		log.Printf("auth: mode=google allowlist=%d accounts redirect=%s", len(cfg.AuthAllowlist), cfg.OAuthRedirectURL)
		if strings.HasPrefix(strings.ToLower(cfg.OAuthRedirectURL), "https://") && !cfg.TLSEnabled() {
			log.Printf("WARNING: oauth redirect is https but --tls-cert-file/--tls-key-file not set — ensure a reverse proxy (Caddy/ALB/Tailscale Serve) terminates TLS")
		}
	} else {
		log.Printf("auth: mode=open")
	}

	srv := api.New(cfg, client, reg, daemon, wsfs)
	srv.MCP = mcpMgr
	srv.Policy = policy
	srv.Tools = toolReg
	srv.Mpub = mpubStore
	srv.Cron = cronMgr
	srv.Auth = authMgr
	if authMgr != nil {
		authMgr.RegisterRoutes(srv.Mux)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := client.Health(ctx); err != nil {
			modelOK.set(false)
			log.Printf("model health: %v (will retry from UI)", err)
		} else {
			modelOK.set(true)
			log.Printf("model ok: %s", cfg.Model)
		}
		// periodic model health for cron pause (ADR-0015 Q14)
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for range t.C {
			cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := client.Health(cctx)
			ccancel()
			modelOK.set(err == nil)
		}
	}()

	go func() {
		log.Printf("version %s", versionString())
		scheme := "http"
		if cfg.TLSEnabled() {
			scheme = "https"
			log.Printf("tls: cert=%s", cfg.TLSCertFile)
		}
		log.Printf("listening on %s://%s", scheme, displayAddr(cfg.Addr))
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
		var err error
		if cfg.TLSEnabled() {
			err = httpSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
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

// atomicBool is a small helper for model-health flag used by cron.
type atomicBool struct{ v int32 }

func (a *atomicBool) set(ok bool) {
	if ok {
		atomic.StoreInt32(&a.v, 1)
	} else {
		atomic.StoreInt32(&a.v, 0)
	}
}

func (a *atomicBool) get() bool { return atomic.LoadInt32(&a.v) == 1 }
