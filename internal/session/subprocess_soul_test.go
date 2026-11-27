package session

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rendicott/marble/internal/agentproc"
	"github.com/rendicott/marble/internal/memory"
)

func blockSig(b agentproc.ContextBlock) string {
	return b.Marker + "\x00" + b.Text + "\x00" + strings.Join(b.Sources, ",")
}

func TestBuildSubprocessBlockSoul(t *testing.T) {
	root := t.TempDir()
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	s := newSession("soulblock1", "t")
	s.appendUI(Message{Role: "user", Content: "hello-from-ui"})
	opts := TurnOpts{}

	bare := (&Runner{}).buildSubprocessBlock(s, opts, nil, "hello-from-ui", nil)
	nilStore := (&Runner{Reg: NewRegistry(nil, nil, nil, root, "m")}).buildSubprocessBlock(s, opts, nil, "hello-from-ui", nil)
	r := &Runner{Reg: NewRegistry(nil, store, nil, root, "m")}
	missing := r.buildSubprocessBlock(s, opts, nil, "hello-from-ui", nil)
	if err := os.WriteFile(store.SoulPath(), []byte(" \n\t"), 0o644); err != nil {
		t.Fatal(err)
	}
	blank := r.buildSubprocessBlock(s, opts, nil, "hello-from-ui", nil)
	if got, want := blockSig(nilStore), blockSig(bare); got != want {
		t.Fatalf("nil store changed the block\n got %q\nwant %q", got, want)
	}
	if got, want := blockSig(missing), blockSig(bare); got != want {
		t.Fatalf("missing soul.md changed the block\n got %q\nwant %q", got, want)
	}
	if got, want := blockSig(blank), blockSig(bare); got != want {
		t.Fatalf("blank soul changed the block\n got %q\nwant %q", got, want)
	}
	if strings.Contains(bare.Text, "Every-turn context") || strings.Contains(bare.Marker, "soul") {
		t.Fatalf("bare block mentions soul: marker %q text %q", bare.Marker, bare.Text)
	}

	if err := os.WriteFile(store.SoulPath(), []byte("FIRST-SOUL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	with := r.buildSubprocessBlock(s, opts, nil, "hello-from-ui", nil)
	if !strings.HasPrefix(with.Text, soulContextHeading+"\nFIRST-SOUL\n\n") {
		t.Fatalf("soul not prepended: %q", with.Text)
	}
	if idx := strings.Index(with.Text, "hello-from-ui"); idx < 0 || idx < strings.Index(with.Text, "FIRST-SOUL") {
		t.Fatalf("transcript should follow soul: %q", with.Text)
	}
	if !strings.HasPrefix(with.Marker, "soul+") {
		t.Fatalf("marker %q", with.Marker)
	}
	if len(with.Sources) == 0 || with.Sources[0] != "soul" {
		t.Fatalf("sources %v", with.Sources)
	}

	empty := newSession("soulblock0", "t")
	only := r.buildSubprocessBlock(empty, opts, nil, "task", nil)
	section := soulContextHeading + "\nFIRST-SOUL"
	wantMarker := fmt.Sprintf("soul · %d chars", utf8.RuneCountInString(section))
	if only.Text != section || only.Marker != wantMarker {
		t.Fatalf("soul-only block marker %q text %q", only.Marker, only.Text)
	}

	if err := os.WriteFile(store.SoulPath(), []byte("SECOND-SOUL"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := r.buildSubprocessBlock(s, opts, nil, "hello-from-ui", nil)
	if strings.Contains(next.Text, "FIRST-SOUL") || !strings.Contains(next.Text, "SECOND-SOUL") {
		t.Fatalf("stale soul in block: %q", next.Text)
	}
}
