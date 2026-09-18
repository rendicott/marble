package agentproc

import (
	"strings"
	"testing"
)

func TestOpencodeArgvKeepsPromptLast(t *testing.T) {
	d := opencodeDriver{}
	tt := true
	argv, err := d.BuildArgv(Request{
		Prompt: "hello world", CWD: "/tmp", OutputFormat: "json",
	}, DriverConfig{Command: "/bin/true", AutoApprove: &tt})
	if err != nil {
		t.Fatal(err)
	}
	if argv[len(argv)-1] != "hello world" {
		t.Fatalf("%q", argv)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "run") || !strings.Contains(joined, "--format json") {
		t.Fatalf("%q", argv)
	}
}

func TestProbeCommandTrue(t *testing.T) {
	p := ProbeCommand("/bin/true")
	if !p.Detected || p.Path == "" {
		t.Fatalf("%+v", p)
	}
}

func TestProbeCommandMissing(t *testing.T) {
	p := ProbeCommand("definitely-not-a-binary-xyz")
	if p.Detected {
		t.Fatalf("%+v", p)
	}
}
