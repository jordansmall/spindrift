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

// MatchTransient must check extras before the base table (issue #2149). Under
// base-first matching it finds "connection refused" (Network) first and returns
// that instead.
func TestMatchTransientExtrasBeatBase(t *testing.T) {
	reason, ok := MatchTransient("connection refused after 429 retries", []Pattern{{Substr: "429", Reason: RateLimit}})
	if !ok {
		t.Fatalf("MatchTransient: got no match, want a match")
	}
	if reason != RateLimit {
		t.Errorf("reason = %q, want %q", reason, RateLimit)
	}
}

// MatchExtras scans only the passed patterns and never falls through to
// BaseTransientPatterns, so a caller matching a terminal pattern list cannot
// pick up a network Reason by accident.
func TestMatchExtras(t *testing.T) {
	patterns := []Pattern{{Substr: "unsupported flag", Reason: UnsupportedFlag}}

	reason, ok := MatchExtras("error: unsupported flag --foo", patterns)
	if !ok {
		t.Fatalf("MatchExtras: got no match, want a match")
	}
	if reason != UnsupportedFlag {
		t.Errorf("reason = %q, want %q", reason, UnsupportedFlag)
	}

	reason, ok = MatchExtras("dial tcp 1.2.3.4:443: connection refused", patterns)
	if ok {
		t.Fatalf("MatchExtras: got a match (%q), want none (must not fall through to BaseTransientPatterns)", reason)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}

	reason, ok = MatchExtras("nothing interesting here", patterns)
	if ok {
		t.Fatalf("MatchExtras: got a match (%q), want none", reason)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
}

func TestMatchTransientBasePrecedence(t *testing.T) {
	// "rate_limit_error" comes before "overloaded_error" in these extras, so a
	// line containing both must classify as RateLimit: first match wins.
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
