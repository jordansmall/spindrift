package seedblock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptfence"
	"spindrift.dev/launcher/internal/runstate"
)

func TestZeroStateYieldsNoBlock(t *testing.T) {
	var s runstate.RunState
	if got := Handoff(s); got != "" {
		t.Errorf("Handoff(zero) = %q, want empty", got)
	}
	if got := Review(s); got != "" {
		t.Errorf("Review(zero) = %q, want empty", got)
	}
}

func TestHandoffPopulated(t *testing.T) {
	got := Handoff(runstate.RunState{LastVerdict: "BLOCK", PassSummaryPath: "/tmp/sum.md"})
	if !strings.HasPrefix(got, Separator+"## Run-state handoff\n\n") {
		t.Errorf("Handoff = %q, want Separator then the handoff header", got)
	}
	for _, want := range []string{"- Last reviewer verdict: BLOCK\n", "- Pass summary: /tmp/sum.md\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("Handoff missing %q in %q", want, got)
		}
	}
}

func TestHandoffSkipsBulletsForMissingFiles(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "scout.md")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Handoff(runstate.RunState{
		LastVerdict:     "APPROVE",
		ScoutBriefPath:  present,
		FindingsLogPath: filepath.Join(dir, "missing-findings.md"),
	})
	if !strings.Contains(got, "- Scout brief: "+present) {
		t.Errorf("want scout brief bullet for existing file in %q", got)
	}
	if strings.Contains(got, "Findings log") {
		t.Errorf("missing findings log must not render a bullet: %q", got)
	}
}

func TestHandoffFencesDecisionsRecord(t *testing.T) {
	dir := t.TempDir()
	decisions := filepath.Join(dir, "decisions.md")
	if err := os.WriteFile(decisions, []byte("## Round 1\n\nchose X\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Handoff(runstate.RunState{DecisionsLogPath: decisions})
	want := "- Decisions record so far (what prior passes chose, rejected, and why):\n\n" + promptfence.Block("## Round 1\n\nchose X\n")
	if !strings.Contains(got, want) {
		t.Errorf("Handoff = %q, want fenced decisions record %q", got, want)
	}

	blank := filepath.Join(dir, "blank.md")
	if err := os.WriteFile(blank, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Handoff(runstate.RunState{DecisionsLogPath: blank}); got != "" {
		t.Errorf("Handoff(whitespace-only decisions) = %q, want empty", got)
	}
}

func TestReviewAnchorOnly(t *testing.T) {
	const sha = "0123456789abcdef"
	got := Review(runstate.RunState{ReviewedCommitAnchor: sha})
	if !strings.HasPrefix(got, Separator+"## Prior-round claims to verify\n\n") {
		t.Errorf("Review = %q, want Separator then the claims header", got)
	}
	if !strings.Contains(got, "### Delta focus") || strings.Contains(got, "### Prior verdict") {
		t.Errorf("anchor-only Review wrong sections: %q", got)
	}
	if got := Review(runstate.RunState{ReviewedCommitAnchor: "not-a-sha"}); got != "" {
		t.Errorf("Review(invalid anchor only) = %q, want empty", got)
	}
}

func TestValidReviewedCommitAnchor(t *testing.T) {
	tests := []struct {
		name   string
		anchor string
		want   bool
	}{
		{"6 chars", "abcdef", false},
		{"7 chars", "abcdef0", true},
		{"40 chars", strings.Repeat("a", 40), true},
		{"64 chars", strings.Repeat("a", 64), true},
		{"65 chars", strings.Repeat("a", 65), false},
		{"uppercase", "ABCDEF0", false},
		{"non-hex", "abcdefg", false},
		{"empty", "", false},
		{"embedded newline", "abcdef0\nabcdef0", false},
		{"trailing newline", "abcdef0\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidReviewedCommitAnchor(tt.anchor); got != tt.want {
				t.Errorf("ValidReviewedCommitAnchor(%q) = %v, want %v", tt.anchor, got, tt.want)
			}
		})
	}
}

func TestReviewAnchorGating(t *testing.T) {
	for _, tt := range []struct {
		name       string
		anchor     string
		wantAnchor bool
	}{
		{"valid 64 chars", strings.Repeat("a", 64), true},
		{"too long", strings.Repeat("a", 65), false},
		{"uppercase", "ABCDEF0", false},
		{"non-hex", "not-a-sha", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Review(runstate.RunState{ReviewFindings: "BLOCK: x", ReviewedCommitAnchor: tt.anchor})
			if !strings.Contains(got, "### Prior verdict") {
				t.Errorf("findings section must survive an anchor decision: %q", got)
			}
			if has := strings.Contains(got, "### Delta focus"); has != tt.wantAnchor {
				t.Errorf("Delta focus present = %v, want %v", has, tt.wantAnchor)
			}
			if tt.wantAnchor && !strings.Contains(got, "git diff "+tt.anchor+"..HEAD") {
				t.Errorf("anchor not rendered in diff command: %q", got)
			}
		})
	}
}
