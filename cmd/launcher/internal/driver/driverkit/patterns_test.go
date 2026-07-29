package driverkit

import "testing"

func TestMatchTransientBaseMatch(t *testing.T) {
	reason, ok := MatchTransient("dial tcp 1.2.3.4:443: connection refused", nil)
	if !ok {
		t.Fatalf("MatchTransient: got no match, want a match")
	}
	if reason != Network {
		t.Errorf("reason = %q, want %q", reason, Network)
	}
}

func TestMatchTransientExtrasMatch(t *testing.T) {
	reason, ok := MatchTransient("thing 429 thing", []Pattern{{Substr: "429", Reason: RateLimit}})
	if !ok {
		t.Fatalf("MatchTransient: got no match, want a match")
	}
	if reason != RateLimit {
		t.Errorf("reason = %q, want %q", reason, RateLimit)
	}
}

func TestMatchTransientNoMatch(t *testing.T) {
	reason, ok := MatchTransient("nothing interesting here", nil)
	if ok {
		t.Fatalf("MatchTransient: got a match (%q), want none", reason)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
}

func TestMatchTransientBasePrecedence(t *testing.T) {
	// "rate_limit_error" appears earlier in BaseTransientPatterns than
	// "overloaded_error" -- a line containing both should classify as the
	// first (RateLimit), confirming order-of-first-match wins.
	reason, ok := MatchTransient("rate_limit_error and overloaded_error both present", nil)
	if !ok {
		t.Fatalf("MatchTransient: got no match, want a match")
	}
	if reason != RateLimit {
		t.Errorf("reason = %q, want %q", reason, RateLimit)
	}
}
