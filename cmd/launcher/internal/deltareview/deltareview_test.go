package deltareview

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/landdelta"
)

func TestFindingLocations(t *testing.T) {
	cases := []struct {
		name     string
		findings string
		want     []Location
	}{
		{
			name: "both sections with distinct paths",
			findings: "VERDICT: BLOCK\n\n" +
				"## Blocking\n" +
				"- run.go:120 — wrong outcome\n\n" +
				"## Non-blocking\n" +
				"- other/file.go:5 — nit\n",
			want: []Location{{Path: "other/file.go", Line: 5}, {Path: "run.go", Line: 120}},
		},
		{
			name: "none bullets contribute nothing",
			findings: "VERDICT: APPROVE\n\n" +
				"## Blocking\n" +
				"- none\n\n" +
				"## Non-blocking\n" +
				"- none\n",
			want: nil,
		},
		{
			name: "path with line and bare path",
			findings: "## Blocking\n" +
				"- cmd/launcher/run.go:42 — bug\n" +
				"- cmd/launcher/other.go — smell\n",
			want: []Location{{Path: "cmd/launcher/other.go", Line: 0}, {Path: "cmd/launcher/run.go", Line: 42}},
		},
		{
			name: "path with line and column suffix",
			findings: "## Blocking\n" +
				"- cmd/launcher/run.go:42:7 — bug\n",
			want: []Location{{Path: "cmd/launcher/run.go", Line: 42}},
		},
		{
			name: "backticked and emphasized locations",
			findings: "## Blocking\n" +
				"- `cmd/launcher/run.go:42` — bug\n" +
				"## Non-blocking\n" +
				"- **cmd/launcher/other.go** — nit\n",
			want: []Location{{Path: "cmd/launcher/other.go", Line: 0}, {Path: "cmd/launcher/run.go", Line: 42}},
		},
		{
			name: "prose bullet with no path contributes nothing",
			findings: "## Blocking\n" +
				"- the reviewer forgot to name a file — bug\n",
			want: nil,
		},
		{
			name: "bullets outside any section are ignored",
			findings: "- run.go:1 — not under a heading\n\n" +
				"## Probed (APPROVE only)\n" +
				"- checked error handling\n",
			want: nil,
		},
		{
			name:     "empty findings",
			findings: "",
			want:     nil,
		},
		{
			name: "indented bullets and star markers",
			findings: "## Blocking\n" +
				"  * nested/dir/file.go:3 — bug\n",
			want: []Location{{Path: "nested/dir/file.go", Line: 3}},
		},
		{
			name: "same path different lines yields two locations",
			findings: "## Blocking\n" +
				"- run.go:1 — bug one\n" +
				"- run.go:2 — bug two\n",
			want: []Location{{Path: "run.go", Line: 1}, {Path: "run.go", Line: 2}},
		},
		{
			name: "exact duplicate location collapses to one",
			findings: "## Blocking\n" +
				"- run.go:1 — bug one\n" +
				"- run.go:1 — bug one, again\n",
			want: []Location{{Path: "run.go", Line: 1}},
		},
		{
			name: "same path cited bare and with a line yields both locations",
			findings: "## Blocking\n" +
				"- run.go — smell\n" +
				"- run.go:42 — bug\n",
			want: []Location{{Path: "run.go", Line: 0}, {Path: "run.go", Line: 42}},
		},
		{
			name: "doubly-wrapped locations unwrap fully regardless of nesting order",
			findings: "## Blocking\n" +
				"- **`cmd/launcher/run.go:42`** — bug\n" +
				"- `**cmd/launcher/other.go:5**` — nit\n",
			want: []Location{{Path: "cmd/launcher/other.go", Line: 5}, {Path: "cmd/launcher/run.go", Line: 42}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FindingLocations(c.findings)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("FindingLocations(%q) = %#v, want %#v", c.findings, got, c.want)
			}
		})
	}
}

func TestGateWorkDeclared(t *testing.T) {
	cases := []struct {
		name      string
		decisions string
		want      bool
	}{
		{"empty decisions", "", false},
		{"no mention", "rebased onto origin/main and re-ran the gate.", false},
		{
			"exact phrase lowercase",
			"fixed gate-discovered work inline: touched run.go, needed to unbreak the build.",
			true,
		},
		{
			"case-insensitive match",
			"Gate-Discovered work: touched run.go.",
			true,
		},
		{
			"phrase from the fragment itself",
			"Gate-discovered work — touched cmd/launcher/run.go, formatter left a stray import.",
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := GateWorkDeclared(c.decisions); got != c.want {
				t.Errorf("GateWorkDeclared(%q) = %v, want %v", c.decisions, got, c.want)
			}
		})
	}
}

func TestGateWorkPhraseNonEmpty(t *testing.T) {
	if GateWorkPhrase == "" {
		t.Fatal("GateWorkPhrase must be non-empty")
	}
	if !strings.Contains(strings.ToLower(GateWorkPhrase), "gate-discovered") {
		t.Fatalf("GateWorkPhrase = %q, want it to contain \"gate-discovered\"", GateWorkPhrase)
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name       string
		delta      landdelta.Delta
		findings   string
		decisions  string
		wantFire   bool
		wantBeyond []string
	}{
		{
			name:      "gate-work declared fires regardless of delta",
			delta:     landdelta.Delta{Known: false, Reason: "no anchor"},
			findings:  "",
			decisions: "gate-discovered work: touched run.go to unbreak the gate.",
			wantFire:  true,
		},
		{
			name:      "unknown delta does not fire on its own",
			delta:     landdelta.Delta{Known: false, Reason: "no anchor"},
			findings:  "## Blocking\n- run.go:1 — bug\n",
			decisions: "",
			wantFire:  false,
		},
		{
			name:      "zero delta does not fire",
			delta:     landdelta.Delta{Known: true},
			findings:  "",
			decisions: "",
			wantFire:  false,
		},
		{
			name:  "delta confined to findings does not fire",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"}},
			findings: "## Blocking\n" +
				"- run.go:1 — bug\n",
			decisions: "",
			wantFire:  false,
		},
		{
			name:  "delta partially beyond findings fires",
			delta: landdelta.Delta{Known: true, Files: 2, Paths: []string{"other.go", "run.go"}},
			findings: "## Blocking\n" +
				"- run.go:1 — bug\n",
			decisions:  "",
			wantFire:   true,
			wantBeyond: []string{"other.go"},
		},
		{
			name:       "empty findings means every delta path is beyond",
			delta:      landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"}},
			findings:   "",
			decisions:  "",
			wantFire:   true,
			wantBeyond: []string{"run.go"},
		},
		{
			name:      "known delta with empty paths but nonzero counts does not fire",
			delta:     landdelta.Delta{Known: true, Files: 1, Insertions: 3},
			findings:  "",
			decisions: "",
			wantFire:  false,
		},
		{
			name: "delta inside tolerance of cited line does not fire",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 12, Count: 1}}}},
			findings: "## Blocking\n- run.go:10 — bug\n",
			wantFire: false,
		},
		{
			name: "delta beyond tolerance of cited line fires",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 20, Count: 1}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:20"},
		},
		{
			name: "bare path citation covers a far-away range",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 900, Count: 1}}}},
			findings: "## Blocking\n- run.go — general concern\n",
			wantFire: false,
		},
		{
			name:  "line-cited path absent from Ranges fails open",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"}},
			findings: "## Blocking\n" +
				"- run.go:10 — bug\n",
			wantFire: false,
		},
		{
			name: "multi-line touched span renders as a range",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 20, Count: 3}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:20-22"},
		},
		{
			name: "pure insertion beyond tolerance renders its anchor point",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 20, Count: 0}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:20"},
		},
		{
			name: "prepend hunk renders pre-image line 0 as line 1",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 0, Count: 0}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:1"},
		},
		{
			name: "multiple hunks within one path sort numerically by start line",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 100, Count: 1}, {Start: 20, Count: 1}}}},
			findings:   "## Blocking\n- run.go:1 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:20", "run.go:100"},
		},
		{
			name: "mixed bare and line citation on one path still vouches for the whole path",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 900, Count: 1}}}},
			findings: "## Blocking\n" +
				"- run.go — general concern\n" +
				"- run.go:10 — bug\n",
			wantFire: false,
		},
		{
			// Replay of the PR #3499 approving round (issue #3478): the land
			// delta was "2 files changed, 8 insertions(+), 1 deletion(-)"
			// over nix/checks/commit-fragment-parity.nix and
			// nix/checks/prompts.nix, but the reviewer's findings cited
			// lines elsewhere in both files, so both landed hunks should
			// have fired a delta-review pass rather than passing silently.
			name: "issue 3478 replay: PR 3499 approving round",
			delta: landdelta.Delta{
				Known: true, Files: 2, Insertions: 8, Deletions: 1,
				Paths: []string{"nix/checks/commit-fragment-parity.nix", "nix/checks/prompts.nix"},
				Ranges: map[string][]landdelta.Range{
					"nix/checks/commit-fragment-parity.nix": {{Start: 44, Count: 1}},
					"nix/checks/prompts.nix":                {{Start: 2118, Count: 0}},
				},
			},
			findings: "## Blocking\n" +
				"- nix/checks/prompts.nix:2125 — reword\n" +
				"- nix/checks/commit-fragment-parity.nix:60 — nit\n",
			wantFire: true,
			wantBeyond: []string{
				"nix/checks/commit-fragment-parity.nix:44",
				"nix/checks/prompts.nix:2118",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(c.delta, c.findings, c.decisions)
			if got.Fire != c.wantFire {
				t.Errorf("Decide(...).Fire = %v, want %v (Reason=%q)", got.Fire, c.wantFire, got.Reason)
			}
			if got.Reason == "" {
				t.Error("Decide(...).Reason must never be empty")
			}
			if !reflect.DeepEqual(got.Beyond, c.wantBeyond) {
				t.Errorf("Decide(...).Beyond = %#v, want %#v", got.Beyond, c.wantBeyond)
			}
		})
	}
}

func TestMergeWindows(t *testing.T) {
	cases := []struct {
		name string
		locs []Location
		want []window
	}{
		{
			name: "single line widens by tolerance both ways",
			locs: []Location{{Path: "a", Line: 10}},
			want: []window{{start: 8, end: 12}},
		},
		{
			name: "adjacent windows merge into one",
			locs: []Location{{Path: "a", Line: 10}, {Path: "a", Line: 14}},
			want: []window{{start: 8, end: 16}},
		},
		{
			name: "far-apart windows stay disjoint",
			locs: []Location{{Path: "a", Line: 10}, {Path: "a", Line: 100}},
			want: []window{{start: 8, end: 12}, {start: 98, end: 102}},
		},
		{
			name: "bare citation (Line 0) contributes no window",
			locs: []Location{{Path: "a", Line: 0}},
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mergeWindows(c.locs)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("mergeWindows(%v) = %v, want %v", c.locs, got, c.want)
			}
		})
	}
}

func TestCoveredBy(t *testing.T) {
	windows := []window{{start: 8, end: 12}, {start: 98, end: 102}}
	cases := []struct {
		name       string
		start, end int
		want       bool
	}{
		{name: "wholly inside first window", start: 9, end: 11, want: true},
		{name: "wholly inside second window", start: 100, end: 102, want: true},
		{name: "straddles the gap between windows", start: 12, end: 98, want: false},
		{name: "wholly outside every window", start: 50, end: 51, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := coveredBy(window{c.start, c.end}, windows); got != c.want {
				t.Errorf("coveredBy(%d, %d, %v) = %v, want %v", c.start, c.end, windows, got, c.want)
			}
		})
	}
}
