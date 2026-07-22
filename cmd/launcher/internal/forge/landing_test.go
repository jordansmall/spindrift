package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestParseLanding_IntegrationRef verifies ParseLanding recognizes the
// post-merge "<branch>@<sha>" grammar (ADR 0029/0033) as LandingIntegrationRef.
func TestParseLanding_IntegrationRef(t *testing.T) {
	l, err := forge.ParseLanding("integration/1694@abc123")
	if err != nil {
		t.Fatalf("ParseLanding: %v", err)
	}
	if l.Kind != forge.LandingIntegrationRef {
		t.Errorf("Kind = %v, want LandingIntegrationRef", l.Kind)
	}
	if l.Branch != "integration/1694" {
		t.Errorf("Branch = %q, want %q", l.Branch, "integration/1694")
	}
	if l.SHA != "abc123" {
		t.Errorf("SHA = %q, want %q", l.SHA, "abc123")
	}
}

// TestParseLanding_BranchRef verifies ParseLanding recognizes a raw branch
// name — CODE_FORGE=local's pre-merge landing record, and CODE_FORGE=git's
// only landing shape — as LandingBranchRef.
func TestParseLanding_BranchRef(t *testing.T) {
	l, err := forge.ParseLanding("agent/issue-42")
	if err != nil {
		t.Fatalf("ParseLanding: %v", err)
	}
	if l.Kind != forge.LandingBranchRef {
		t.Errorf("Kind = %v, want LandingBranchRef", l.Kind)
	}
	if l.Branch != "agent/issue-42" {
		t.Errorf("Branch = %q, want %q", l.Branch, "agent/issue-42")
	}
}

// TestParseLanding_PRURL verifies ParseLanding recognizes a github PR URL —
// CODE_FORGE=github's landing grammar — as LandingPRURL.
func TestParseLanding_PRURL(t *testing.T) {
	const url = "https://github.com/o/r/pull/7"
	l, err := forge.ParseLanding(url)
	if err != nil {
		t.Fatalf("ParseLanding: %v", err)
	}
	if l.Kind != forge.LandingPRURL {
		t.Errorf("Kind = %v, want LandingPRURL", l.Kind)
	}
	if l.URL != url {
		t.Errorf("URL = %q, want %q", l.URL, url)
	}
}

// TestParseLanding_EmptyIsError verifies ParseLanding rejects an empty
// string rather than minting a zero-value Landing for it — every caller
// already guards against writing/reading one.
func TestParseLanding_EmptyIsError(t *testing.T) {
	if _, err := forge.ParseLanding(""); err == nil {
		t.Fatal("ParseLanding(\"\"): want error, got nil")
	}
}

// TestParseLanding_MalformedIntegrationRefFallsBackToBranchRef verifies a
// string that merely contains "@" without both a non-empty branch and a
// non-empty, non-option-like sha falls back to LandingBranchRef rather than
// being misparsed as an IntegrationRef.
func TestParseLanding_MalformedIntegrationRefFallsBackToBranchRef(t *testing.T) {
	for _, s := range []string{"@abc123", "branch@", "branch@-opt"} {
		l, err := forge.ParseLanding(s)
		if err != nil {
			t.Fatalf("ParseLanding(%q): %v", s, err)
		}
		if l.Kind != forge.LandingBranchRef {
			t.Errorf("ParseLanding(%q).Kind = %v, want LandingBranchRef", s, l.Kind)
		}
	}
}

// TestLanding_StringRoundTrips verifies ParseLanding(l.String()) reproduces
// l for every Landing ParseLanding itself can produce.
func TestLanding_StringRoundTrips(t *testing.T) {
	for _, s := range []string{
		"integration/1694@abc123",
		"agent/issue-42",
		"https://github.com/o/r/pull/7",
	} {
		l, err := forge.ParseLanding(s)
		if err != nil {
			t.Fatalf("ParseLanding(%q): %v", s, err)
		}
		if got := l.String(); got != s {
			t.Errorf("ParseLanding(%q).String() = %q, want %q", s, got, s)
		}
	}
}
