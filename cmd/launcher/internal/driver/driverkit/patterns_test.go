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

// TestMatchTransientExtrasBeatBase locks in that a per-Driver extras marker
// beats a shared BaseTransientPatterns marker when both appear on the same
// line — extras must be checked before the base table (issue #2149). Under
// base-first matching "connection refused" (Network) is found first and
// wins instead.
func TestMatchTransientExtrasBeatBase(t *testing.T) {
	reason, ok := MatchTransient("connection refused after 429 retries", []Pattern{{Substr: "429", Reason: RateLimit}})
	if !ok {
		t.Fatalf("MatchTransient: got no match, want a match")
	}
	if reason != RateLimit {
		t.Errorf("reason = %q, want %q", reason, RateLimit)
	}
}

func TestMatchTransientBasePrecedence(t *testing.T) {
	// "rate_limit_error" appears earlier than "overloaded_error" in the
	// extras passed here -- a line containing both should classify as the
	// first (RateLimit), confirming first-match-wins through extras.
	reason, ok := MatchTransient("rate_limit_error and overloaded_error both present", []Pattern{
		{Substr: "rate_limit_error", Reason: RateLimit},
		{Substr: "overloaded_error", Reason: Overloaded},
	})
	if !ok {
		t.Fatalf("MatchTransient: got no match, want a match")
	}
	if reason != RateLimit {
		t.Errorf("reason = %q, want %q", reason, RateLimit)
	}
}
