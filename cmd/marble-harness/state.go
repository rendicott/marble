package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/rendicott/marble/internal/state"
)

func runState(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: marble-harness state export|import [flags]\n")
		fmt.Fprintf(os.Stderr, "  marble-harness state export [-memory DIR] [-out DIR] [--stats] [--secrets=omit|age] [--age-recipient age1...]\n")
		fmt.Fprintf(os.Stderr, "  marble-harness state import [-memory DIR] [-in DIR] [-apply] [--rewrite-workspace FROM=TO] [--age-identity FILE]\n")
		return 2
	}
	switch args[0] {
	case "export":
		return runStateExport(args[1:])
	case "import":
		return runStateImport(args[1:])
	case "-h", "--help", "help":
		fmt.Fprintf(os.Stderr, "usage: marble-harness state export|import [flags]\n")
		fmt.Fprintf(os.Stderr, "  marble-harness state export [-memory DIR] [-out DIR] [--stats] [--secrets=omit|age] [--age-recipient age1...]\n")
		fmt.Fprintf(os.Stderr, "  marble-harness state import [-memory DIR] [-in DIR] [-apply] [--rewrite-workspace FROM=TO] [--age-identity FILE]\n")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown state command %q\n", args[0])
		return 2
	}
}

func runStateExport(args []string) int {
	fs := flag.NewFlagSet("state export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	memory := fs.String("memory", os.Getenv("MEMORY"), "memory directory (default: $MEMORY)")
	out := fs.String("out", "", "bundle directory to write (omit for a stats-only dry run)")
	stats := fs.Bool("stats", false, "print per-prefix file counts and bytes")
	secrets := fs.String("secrets", "omit", "omit (default) or age; there is no include")
	var recipients multiFlag
	fs.Var(&recipients, "age-recipient", "age recipient (age1...); repeat for more than one; required with --secrets=age")
	workspace := fs.String("workspace", "", "recorded source workspace (optional)")
	publicURL := fs.String("public-url", os.Getenv("MARBLE_PUBLIC_URL"), "recorded source public URL (default: $MARBLE_PUBLIC_URL)")
	model := fs.String("model", os.Getenv("MARBLE_MODEL"), "recorded source model (default: $MARBLE_MODEL)")
	baseURL := fs.String("base-url", os.Getenv("MARBLE_BASE_URL"), "recorded source base URL (default: $MARBLE_BASE_URL)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: marble-harness state export [-memory DIR] [-out DIR] [--stats] [--secrets=omit|age] [--age-recipient age1...]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	if strings.TrimSpace(*memory) == "" {
		fmt.Fprintf(os.Stderr, "error: memory dir required (-memory or $MEMORY)\n")
		return 2
	}
	res, err := state.Export(state.ExportRequest{
		MemoryDir:      *memory,
		OutDir:         *out,
		Stats:          *stats,
		Secrets:        *secrets,
		AgeRecipients:  recipients,
		PublicURL:      *publicURL,
		Workspace:      *workspace,
		Model:          *model,
		BaseURL:        *baseURL,
		HarnessVersion: versionString(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, ln := range res.Lines {
		fmt.Println(ln)
	}
	return 0
}

func runStateImport(args []string) int {
	fs := flag.NewFlagSet("state import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	memory := fs.String("memory", os.Getenv("MEMORY"), "destination memory directory (default: $MEMORY)")
	in := fs.String("in", "", "bundle directory")
	apply := fs.Bool("apply", false, "copy into the memory directory (default: verify and print the plan)")
	rewrite := fs.String("rewrite-workspace", "", "FROM=TO prefix for sessions.workspace and sessions.md_path")
	ident := fs.String("age-identity", "", "age identity file for secrets.age")
	forceEnv := fs.Bool("force-env", false, "overwrite an existing memory env file")
	forceDB := fs.Bool("force-db", false, "replace an existing memory marble.db (default: refuse; move it aside)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: marble-harness state import [-memory DIR] [-in DIR] [-apply] [--rewrite-workspace FROM=TO] [--age-identity FILE]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	if strings.TrimSpace(*memory) == "" {
		fmt.Fprintf(os.Stderr, "error: memory dir required (-memory or $MEMORY)\n")
		return 2
	}
	if strings.TrimSpace(*in) == "" {
		fmt.Fprintf(os.Stderr, "error: bundle dir required (-in)\n")
		return 2
	}
	from, to, err := splitRewrite(*rewrite)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	res, err := state.Import(state.ImportRequest{
		MemoryDir:       *memory,
		InDir:           *in,
		Apply:           *apply,
		RewriteFrom:     from,
		RewriteTo:       to,
		AgeIdentityFile: *ident,
		ForceEnv:        *forceEnv,
		ForceDB:         *forceDB,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	for _, ln := range res.Lines {
		fmt.Println(ln)
	}
	return 0
}

func splitRewrite(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", nil
	}
	from, to, ok := strings.Cut(s, "=")
	if !ok || from == "" || to == "" {
		return "", "", fmt.Errorf("rewrite-workspace must be FROM=TO")
	}
	return from, to, nil
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("empty value")
	}
	*m = append(*m, v)
	return nil
}
