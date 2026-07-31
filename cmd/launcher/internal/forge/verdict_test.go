package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestParseVerdict(t *testing.T) {
	vl := forge.ResearchVerdictLabels()
	cases := []struct {
		status string
		want   forge.Verdict
		wantOK bool
	}{
		{"recommend", forge.Recommend, true},
		{"reject", forge.Reject, true},
		{"unclear", forge.Unclear, true},
		{"blocked", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := vl.Parse(c.status)
		if ok != c.wantOK {
			t.Errorf("Parse(%q) ok = %v, want %v", c.status, ok, c.wantOK)
			continue
		}
		if ok && got != c.want {
			t.Errorf("Parse(%q) = %v, want %v", c.status, got, c.want)
		}
	}
}

// TestResearchVerdictLabels_DefaultSet verifies the compiled-default verdict
// set's tokens, labels, and Parse behavior match ADR 0022.
func TestResearchVerdictLabels_DefaultSet(t *testing.T) {
	vl := forge.ResearchVerdictLabels()

	for _, c := range []struct {
		status string
		want   forge.Verdict
	}{
		{"recommend", forge.Recommend},
		{"reject", forge.Reject},
		{"unclear", forge.Unclear},
	} {
		got, ok := vl.Parse(c.status)
		if !ok || got != c.want {
			t.Errorf("Parse(%q) = %v, %v, want %v, true", c.status, got, ok, c.want)
		}
	}

	wantLabels := map[forge.Verdict]string{
		forge.Recommend: "agent-research-recommend",
		forge.Reject:    "agent-research-reject",
		forge.Unclear:   "agent-research-unclear",
	}
	for verdict, want := range wantLabels {
		if got := vl.Label(verdict); got != want {
			t.Errorf("Label(%v) = %q, want %q", verdict, got, want)
		}
	}

	for _, status := range []string{"blocked", "bogus"} {
		if _, ok := vl.Parse(status); ok {
			t.Errorf("Parse(%q) ok = true, want false", status)
		}
	}
}

// TestParseResearchVerdicts_Empty verifies an empty knob returns the
// compiled default set, ordered, with the default labels.
func TestParseResearchVerdicts_Empty(t *testing.T) {
	vl, err := forge.ParseResearchVerdicts("")
	if err != nil {
		t.Fatalf("ParseResearchVerdicts(\"\"): %v", err)
	}
	entries := vl.Entries()
	if len(entries) != 3 {
		t.Fatalf("Entries() len = %d, want 3", len(entries))
	}
	wantVerdicts := []forge.Verdict{forge.Recommend, forge.Reject, forge.Unclear}
	wantLabels := []string{"agent-research-recommend", "agent-research-reject", "agent-research-unclear"}
	for i, e := range entries {
		if e.Verdict != wantVerdicts[i] {
			t.Errorf("entries[%d].Verdict = %v, want %v", i, e.Verdict, wantVerdicts[i])
		}
		if e.Label != wantLabels[i] {
			t.Errorf("entries[%d].Label = %q, want %q", i, e.Label, wantLabels[i])
		}
	}
}

// TestParseResearchVerdicts_Custom verifies a custom JSON array configures a
// distinct verdict set, order preserved, and the default tokens no longer
// parse.
func TestParseResearchVerdicts_Custom(t *testing.T) {
	vl, err := forge.ParseResearchVerdicts(`[
		{"verdict":"approve","label":"agent-research-approve","description":"looks good"},
		{"verdict":"skip","label":"agent-research-skip","description":"not worth it"}
	]`)
	if err != nil {
		t.Fatalf("ParseResearchVerdicts: %v", err)
	}

	got, ok := vl.Parse("approve")
	if !ok || got != forge.Verdict("approve") {
		t.Errorf("Parse(approve) = %v, %v, want approve, true", got, ok)
	}
	if label := vl.Label(forge.Verdict("approve")); label != "agent-research-approve" {
		t.Errorf("Label(approve) = %q, want agent-research-approve", label)
	}
	if _, ok := vl.Parse("recommend"); ok {
		t.Error("Parse(recommend) ok = true, want false (not part of custom set)")
	}

	wantOrder := []forge.Verdict{"approve", "skip"}
	if got := vl.Verdicts(); len(got) != len(wantOrder) || got[0] != wantOrder[0] || got[1] != wantOrder[1] {
		t.Errorf("Verdicts() = %v, want %v", got, wantOrder)
	}
}

// TestParseResearchVerdicts_Invalid verifies every documented validation
// failure returns an error.
func TestParseResearchVerdicts_Invalid(t *testing.T) {
	cases := map[string]string{
		"not json":               `not json`,
		"empty array":            `[]`,
		"empty verdict":          `[{"verdict":"","label":"x"}]`,
		"empty label":            `[{"verdict":"x","label":""}]`,
		"duplicate token":        `[{"verdict":"x","label":"a"},{"verdict":"x","label":"b"}]`,
		"whitespace in token":    `[{"verdict":"has space","label":"x"}]`,
		"reserved blocked token": `[{"verdict":"blocked","label":"x"}]`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := forge.ParseResearchVerdicts(input); err == nil {
				t.Errorf("ParseResearchVerdicts(%q) err = nil, want error", input)
			}
		})
	}
}
