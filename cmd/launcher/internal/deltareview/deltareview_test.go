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
			want: []Location{{Path: "other/file.go", Line: 5, End: 5}, {Path: "run.go", Line: 120, End: 120}},
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
			want: []Location{{Path: "cmd/launcher/other.go", Line: 0}, {Path: "cmd/launcher/run.go", Line: 42, End: 42}},
		},
		{
			name: "path with line and column suffix",
			findings: "## Blocking\n" +
				"- cmd/launcher/run.go:42:7 — bug\n",
			want: []Location{{Path: "cmd/launcher/run.go", Line: 42, End: 42}},
		},
		{
			name: "backticked and emphasized locations",
			findings: "## Blocking\n" +
				"- `cmd/launcher/run.go:42` — bug\n" +
				"## Non-blocking\n" +
				"- **cmd/launcher/other.go** — nit\n",
			want: []Location{{Path: "cmd/launcher/other.go", Line: 0}, {Path: "cmd/launcher/run.go", Line: 42, End: 42}},
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
			want: []Location{{Path: "nested/dir/file.go", Line: 3, End: 3}},
		},
		{
			name: "same path different lines yields two locations",
			findings: "## Blocking\n" +
				"- run.go:1 — bug one\n" +
				"- run.go:2 — bug two\n",
			want: []Location{{Path: "run.go", Line: 1, End: 1}, {Path: "run.go", Line: 2, End: 2}},
		},
		{
			name: "exact duplicate location collapses to one",
			findings: "## Blocking\n" +
				"- run.go:1 — bug one\n" +
				"- run.go:1 — bug one, again\n",
			want: []Location{{Path: "run.go", Line: 1, End: 1}},
		},
		{
			name: "same path cited bare and with a line yields both locations",
			findings: "## Blocking\n" +
				"- run.go — smell\n" +
				"- run.go:42 — bug\n",
			want: []Location{{Path: "run.go", Line: 0}, {Path: "run.go", Line: 42, End: 42}},
		},
		{
			name: "doubly-wrapped locations unwrap fully regardless of nesting order",
			findings: "## Blocking\n" +
				"- **`cmd/launcher/run.go:42`** — bug\n" +
				"- `**cmd/launcher/other.go:5**` — nit\n",
			want: []Location{{Path: "cmd/launcher/other.go", Line: 5, End: 5}, {Path: "cmd/launcher/run.go", Line: 42, End: 42}},
		},
		{
			name: "zero range start is dropped rather than read as whole-file",
			findings: "## Blocking\n" +
				"- run.go:0-5 — bug\n",
			want: nil,
		},
		{
			name: "zero range end is dropped rather than read as whole-file",
			findings: "## Blocking\n" +
				"- run.go:5-0 — bug\n",
			want: nil,
		},
		{
			name: "zero line is dropped rather than read as whole-file",
			findings: "## Blocking\n" +
				"- run.go:0 — bug\n",
			want: nil,
		},
		{
			name: "overflowing range start is dropped rather than read as whole-file",
			findings: "## Blocking\n" +
				"- run.go:99999999999999999999-5 — bug\n",
			want: nil,
		},
		{
			name: "overflowing range end is dropped rather than read as whole-file",
			findings: "## Blocking\n" +
				"- run.go:5-99999999999999999999 — bug\n",
			want: nil,
		},
		{
			name: "overflowing line is dropped rather than read as whole-file",
			findings: "## Blocking\n" +
				"- run.go:99999999999999999999 — bug\n",
			want: nil,
		},
		{
			name: "line suffix outside bold emphasis",
			findings: "## Blocking\n" +
				"- **run.go**:7 — bug\n",
			want: []Location{{Path: "run.go", Line: 7, End: 7}},
		},
		{
			name: "line range suffix",
			findings: "## Blocking\n" +
				"- run.go:12-15 — bug\n",
			want: []Location{{Path: "run.go", Line: 12, End: 15}},
		},
		{
			name: "backticked line range",
			findings: "## Blocking\n" +
				"- `run.go:12-15` — bug\n",
			want: []Location{{Path: "run.go", Line: 12, End: 15}},
		},
		{
			name: "suffix outside the backticks",
			findings: "## Blocking\n" +
				"- `run.go`:12 — bug\n" +
				"- **`other.go`**:7-9 — nit\n",
			want: []Location{{Path: "other.go", Line: 7, End: 9}, {Path: "run.go", Line: 12, End: 12}},
		},
		{
			name: "reversed range is normalized",
			findings: "## Blocking\n" +
				"- run.go:15-12 — bug\n",
			want: []Location{{Path: "run.go", Line: 12, End: 15}},
		},
		{
			name: "unparseable suffix drops the bullet",
			findings: "## Blocking\n" +
				"- run.go:abc — bug\n",
			want: nil,
		},
		{
			name: "same start with different ends sorts by end",
			findings: "## Blocking\n" +
				"- run.go:12-20 — bug\n" +
				"- run.go:12-15 — bug\n",
			want: []Location{{Path: "run.go", Line: 12, End: 15}, {Path: "run.go", Line: 12, End: 20}},
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
		wantReason string
	}{
		{
			name:       "gate-work declared fires regardless of delta",
			delta:      landdelta.Delta{Known: false, Reason: "no anchor"},
			findings:   "",
			decisions:  "gate-discovered work: touched run.go to unbreak the gate.",
			wantFire:   true,
			wantReason: GateWorkReason,
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
			name: "delta inside a cited range and its tolerance does not fire",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 10, Count: 1}, {Start: 14, Count: 1}, {Start: 22, Count: 1}}}},
			findings: "## Blocking\n- run.go:12-20 — bug\n",
			wantFire: false,
		},
		{
			name: "delta past a cited range's end plus tolerance fires",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 23, Count: 1}}}},
			findings:   "## Blocking\n- run.go:12-20 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:23"},
		},
		{
			name: "dropped zero-start range cite fires on a delta inside it",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 3, Count: 1}}}},
			findings:   "## Blocking\n- run.go:0-5 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go"},
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
			name: "pure insertion beyond tolerance with no PostCount renders its anchor point",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 20, Count: 0}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:20"},
		},
		{
			name: "pure insertion beyond tolerance renders its anchor point plus added-line count",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 20, Count: 0, PostCount: 1}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:20 (+1)"},
		},
		{
			name: "prepend hunk with no PostCount renders pre-image line 0 as line 1",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 0, Count: 0}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:1"},
		},
		{
			name: "multi-line prepend hunk renders pre-image line 0 as line 1 plus added-line count",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 0, Count: 0, PostCount: 5}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:1 (+5)"},
		},
		{
			name: "12-line insertion after a line inside tolerance fires (issue 3532)",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 11, Count: 0, PostCount: 12}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:11 (+12)"},
		},
		{
			name: "growing modification spans its post-image count past tolerance",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 10, Count: 2, PostCount: 20}}}},
			findings:   "## Blocking\n- run.go:10 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:10-11 (+20)"},
		},
		{
			name: "short insertion within tolerance still skips despite a PostCount",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 10, Count: 0, PostCount: 2}}}},
			findings: "## Blocking\n- run.go:10 — bug\n",
			wantFire: false,
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
			name: "prepend hunk with a count renders its real pre-image span, not line 1",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 0, Count: 4}}}},
			findings:   "## Blocking\n- run.go:100 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:1-3"},
		},
		{
			name: "equal-start hunks sort by shown end, given shorter first",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 500, Count: 1}, {Start: 500, Count: 9}}}},
			findings:   "## Blocking\n- run.go:3 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:500", "run.go:500-508"},
		},
		{
			name: "equal-start hunks sort by shown end, given longer first",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 500, Count: 9}, {Start: 500, Count: 1}}}},
			findings:   "## Blocking\n- run.go:3 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:500", "run.go:500-508"},
		},
		{
			name: "equal-start hunks order by shown end, not PostCount, given growing hunk first",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 500, Count: 1, PostCount: 9}, {Start: 500, Count: 9}}}},
			findings:   "## Blocking\n- run.go:3 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:500 (+9)", "run.go:500-508"},
		},
		{
			name: "equal-start hunks order by shown end, not PostCount, given growing hunk last",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 500, Count: 9}, {Start: 500, Count: 1, PostCount: 9}}}},
			findings:   "## Blocking\n- run.go:3 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:500 (+9)", "run.go:500-508"},
		},
		{
			name: "prepend and line-1 hunks both shown at line 1 sort by shown end, given line-1 hunk first",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 1, Count: 3}, {Start: 0, Count: 0}}}},
			findings:   "## Blocking\n- run.go:100 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:1", "run.go:1-3"},
		},
		{
			name: "prepend and line-1 hunks both shown at line 1 sort by shown end, given prepend first",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 0, Count: 0}, {Start: 1, Count: 3}}}},
			findings:   "## Blocking\n- run.go:100 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:1", "run.go:1-3"},
		},
		{
			name: "prepend still fires when a finding cites line 3",
			delta: landdelta.Delta{Known: true, Files: 1, Paths: []string{"run.go"},
				Ranges: map[string][]landdelta.Range{"run.go": {{Start: 0, Count: 0}}}},
			findings:   "## Blocking\n- run.go:3 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"run.go:1"},
		},
		{
			name: "multiple paths follow delta.Paths order",
			delta: landdelta.Delta{Known: true, Files: 2, Paths: []string{"a.go", "b.go"},
				Ranges: map[string][]landdelta.Range{
					"a.go": {{Start: 100, Count: 1}},
					"b.go": {{Start: 20, Count: 1}},
				}},
			findings:   "## Blocking\n- a.go:1 — bug\n- b.go:1 — bug\n",
			wantFire:   true,
			wantBeyond: []string{"a.go:100", "b.go:20"},
			wantReason: beyondReasonPrefix + "a.go:100, b.go:20",
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
			if c.wantReason != "" && got.Reason != c.wantReason {
				t.Errorf("Decide(...).Reason = %q, want %q", got.Reason, c.wantReason)
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
			locs: []Location{{Path: "a", Line: 10, End: 10}},
			want: []window{{start: 8, end: 12}},
		},
		{
			name: "adjacent windows merge into one",
			locs: []Location{{Path: "a", Line: 10, End: 10}, {Path: "a", Line: 14, End: 14}},
			want: []window{{start: 8, end: 16}},
		},
		{
			name: "far-apart windows stay disjoint",
			locs: []Location{{Path: "a", Line: 10, End: 10}, {Path: "a", Line: 100, End: 100}},
			want: []window{{start: 8, end: 12}, {start: 98, end: 102}},
		},
		{
			name: "range widens from its start to its end",
			locs: []Location{{Path: "a", Line: 10, End: 20}},
			want: []window{{start: 8, end: 22}},
		},
		{
			name: "range swallows a nested single line",
			locs: []Location{{Path: "a", Line: 12, End: 12}, {Path: "a", Line: 10, End: 20}},
			want: []window{{start: 8, end: 22}},
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
			if got := coveredBy(window{start: c.start, end: c.end}, windows); got != c.want {
				t.Errorf("coveredBy(%d, %d, %v) = %v, want %v", c.start, c.end, windows, got, c.want)
			}
		})
	}
}
