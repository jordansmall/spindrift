package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// ADR 0029/0033 defines the post-merge "<branch>@<sha>" grammar.
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

// A raw branch name is CODE_FORGE=local's pre-merge landing record and
// CODE_FORGE=git's only landing shape.
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

// A PR URL is CODE_FORGE=github's landing grammar.
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

// An empty string is an error rather than a zero-value Landing, because every
// caller already guards against writing or reading one.
func TestParseLanding_EmptyIsError(t *testing.T) {
	if _, err := forge.ParseLanding(""); err == nil {
		t.Fatal("ParseLanding(\"\"): want error, got nil")
	}
}

// An "@" alone is not enough: an IntegrationRef needs both a non-empty branch
// and a non-empty, non-option-like sha.
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
