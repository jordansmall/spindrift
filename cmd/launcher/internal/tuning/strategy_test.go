package tuning

import (
	"errors"
	"slices"
	"strconv"

	"spindrift.dev/launcher/internal/chore"
	"testing"

	"spindrift.dev/launcher/internal/recordsdigest"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/signalwire"
)

func trackedSet(files ...string) func() (map[string]bool, error) {
	return func() (map[string]bool, error) {
		set := map[string]bool{}
		for _, f := range files {
			set[f] = true
		}
		return set, nil
	}
}

func TestStrategyValidate(t *testing.T) {
	digest := strategyDigest(4)
	var anchor string
	for a, r := range Rows(digest) {
		if r.N == 4 {
			anchor = a
			break
		}
	}
	if anchor == "" {
		t.Fatalf("no row with n=4 in:\n%s", digest)
	}
	limits := recordsdigest.Limits{MinSample: 4}
	cases := []struct {
		name   string
		in     signalwire.IssueIntent
		limits recordsdigest.Limits
		want   string
	}{
		{"no target", signalwire.IssueIntent{Class: "prompt-gap", Cites: []string{anchor}}, limits, "missing-target"},
		{"untracked target", signalwire.IssueIntent{Class: "prompt-gap", Cites: []string{anchor}, DedupTerms: []string{"a.go:F", "gone.md:k"}}, limits, "missing-target"},
		{"thin evidence", signalwire.IssueIntent{Class: "prompt-gap", Cites: []string{anchor}, DedupTerms: []string{"a.go:F"}}, recordsdigest.Limits{MinSample: 5}, "thin-evidence"},
		{"evidence gap is exempt from the floor", signalwire.IssueIntent{Class: "evidence-gap", Cites: []string{anchor}, DedupTerms: []string{"a.go:F"}}, recordsdigest.Limits{MinSample: 5}, ""},
		{"well sampled", signalwire.IssueIntent{Class: "prompt-gap", Cites: []string{anchor}, DedupTerms: []string{"a.go:F"}}, limits, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Strategy.Validate(tc.in, digest, tc.limits, trackedSet("a.go"))
			if err != nil || got != tc.want {
				t.Errorf("Validate = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestStrategyValidateTrackedErrorPropagates(t *testing.T) {
	digest := strategyDigest(4)
	var anchor string
	for a := range Rows(digest) {
		anchor = a
		break
	}
	boom := errors.New("boom")
	in := signalwire.IssueIntent{Class: "prompt-gap", Cites: []string{anchor}, DedupTerms: []string{"a.go:F"}}
	_, err := Strategy.Validate(in, digest, recordsdigest.Limits{MinSample: 1}, func() (map[string]bool, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the tracked-files error", err)
	}
}

func TestStrategyDedupTerms(t *testing.T) {
	f := settle.Finding{Class: "prompt-gap", Metric: "usd", DedupTerms: []string{"a.go:F", "b.md:k", "a.go:G", "-"}}
	want := []string{"prompt-gap:a.go:usd", "prompt-gap:b.md:usd"}
	if got := Strategy.DedupTerms(f); !slices.Equal(got, want) {
		t.Errorf("DedupTerms = %v, want %v", got, want)
	}
}

// strategyDigest is a minimal rendered digest whose one aggregate row is
// backed by n window Records.
func strategyDigest(n int) string {
	return summaryHeading + "\n\n" + tableHeader +
		"| summary:usd-per-record | usd | " + strconv.Itoa(n) + " | 1.00 | 1.00 | 0 | |\n"
}

// evidenceGapClass repeats a class the generated chore descriptor declares, so
// a rename there must fail here instead of silently dropping the sample-floor
// exemption.
func TestEvidenceGapClassIsATuningChoreClass(t *testing.T) {
	cs, err := chore.Load(chore.Knobs{Chores: "tuning"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cs[0].ClassList, evidenceGapClass) {
		t.Errorf("tuning Chore classes = %v, want %q among them", cs[0].ClassList, evidenceGapClass)
	}
}
