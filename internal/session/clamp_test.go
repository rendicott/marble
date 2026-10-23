package session

import "testing"

func TestAdaptiveMarginIncidentSkew(t *testing.T) {
	// Catalog budget ceiling estimate 598,592 against the provider window.
	// A fixed limit/64 margin still asks for 433,600 and the wire stays over.
	// The default 1.10 ratio has to leave positive slack on its own.
	const (
		limit     = 1048576
		est       = 598592
		maxOut    = 393216
		providerM = 655440
	)
	cl := ClampMaxTokens(limit, maxOut, est, defaultObsRatio)
	if !cl.OK {
		t.Fatal("expected a completion that still fits")
	}
	if cl.MaxTokens >= maxOut {
		t.Fatalf("max_tokens %d was not clamped under max_output %d", cl.MaxTokens, maxOut)
	}
	wire := providerM + cl.MaxTokens
	if wire >= limit {
		t.Fatalf("wire %d still over %d (max_tokens %d margin %d)", wire, limit, cl.MaxTokens, cl.Margin)
	}
	if slack := limit - wire; slack <= 0 {
		t.Fatalf("slack %d", slack)
	}
	bare := limit - est - limit/64
	if providerM+bare < limit {
		t.Fatal("fixture no longer demonstrates the fixed-margin miss")
	}
}

func TestClampMaxTokens(t *testing.T) {
	const limit = 1048576
	const maxOut = 393216

	matched := ClampMaxTokens(limit, maxOut, 655440, defaultObsRatio)
	if !matched.OK || matched.MaxTokens != 311206 || matched.Margin != 81930 {
		t.Fatalf("matched estimate: %+v", matched)
	}
	if matched.MaxTokens >= maxOut {
		t.Fatalf("matched max_tokens %d", matched.MaxTokens)
	}

	under := ClampMaxTokens(limit, maxOut, 655360, defaultObsRatio)
	if !under.OK || under.MaxTokens != 311296 {
		t.Fatalf("80-under estimate: %+v", under)
	}
	// Provider still counts 655,440. The clamped first request stays under the window.
	if 655440+under.MaxTokens >= limit {
		t.Fatalf("first wire still over: max_tokens %d", under.MaxTokens)
	}

	over := ClampMaxTokens(20000, 8000, 18000, 1)
	if over.OK {
		t.Fatalf("room below floor should refuse the POST: %+v", over)
	}

	mid := ClampMaxTokens(200000, 200000, 100000, 1)
	if !mid.OK || mid.MaxTokens != 95904 || mid.MaxTokens == 200000 {
		t.Fatalf("room between floor and max_output: %+v", mid)
	}

	wide := ClampMaxTokens(limit, maxOut, 1000, 1)
	if !wide.OK || wide.MaxTokens != maxOut {
		t.Fatalf("room above max_output: %+v", wide)
	}
}

func TestRetryMaxTokens(t *testing.T) {
	const limit = 1048576
	retry, ok := RetryMaxTokens(limit, 655440, 393216, 16384, 400000)
	if !ok || retry != 376752 {
		t.Fatalf("retry=%d ok=%v", retry, ok)
	}
	if 655440+retry >= limit {
		t.Fatal("retry does not fit")
	}
	if _, ok := RetryMaxTokens(limit, 1048000, 393216, 16384, 400000); ok {
		t.Fatal("prompt itself over the window must not retry")
	}
	if _, ok := RetryMaxTokens(limit, 655440, 393216, 16384, 376752); ok {
		t.Fatal("retry must be strictly smaller than the max_tokens already sent")
	}
}
