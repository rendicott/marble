package sink

import "testing"

func TestPreviewOf(t *testing.T) {
	if g := PreviewOf("hello\nworld"); g != "hello" {
		t.Fatalf("got %q", g)
	}
	if g := PreviewOf("  a   b  "); g != "a b" {
		t.Fatalf("got %q", g)
	}
	long := make([]rune, 200)
	for i := range long {
		long[i] = 'x'
	}
	g := PreviewOf(string(long))
	if len([]rune(g)) != previewMaxRunes {
		t.Fatalf("len %d", len([]rune(g)))
	}
}

func TestValidOrbReturnURL(t *testing.T) {
	if !ValidOrbReturnURL("http://rinux.tail.example:8080/s/abc") {
		t.Fatal("http should pass")
	}
	if ValidOrbReturnURL("/s/abc") {
		t.Fatal("relative must be rejected")
	}
	if !ValidOrbReturnURL("https://rinux.tail.example:8080/s/abc") {
		t.Fatal("https should pass")
	}
	if ValidOrbReturnURL("") {
		t.Fatal("empty is not a return_url")
	}
	if ValidOrbReturnURL("javascript:alert(1)") {
		t.Fatal("javascript must be rejected")
	}
}

func TestIdempotencyKeyStable(t *testing.T) {
	a := IdempotencyKey("orb", "sess", "turn-1")
	b := IdempotencyKey("orb", "sess", "turn-1")
	c := IdempotencyKey("orb", "sess", "turn-2")
	if a != b || a == c || len(a) != 32 {
		t.Fatalf("a=%s b=%s c=%s", a, b, c)
	}
}
