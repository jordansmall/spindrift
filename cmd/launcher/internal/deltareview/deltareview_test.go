package deltareview

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/landdelta"
)

func TestFindingPaths(t *testing.T) {
	cases := []struct {
		name     string
		findings string
		want     []string
	}{
		{
			name: "both sections with distinct paths",
			findings: "VERDICT: BLOCK\n\n" +
				"## Blocking\n" +
				"- run.go:120 — wrong outcome\n\n" +
				"## Non-blocking\n" +
				"- other/file.go:5 — nit\n",
			want: []string{"other/file.go", "run.go"},
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
			want: []string{"cmd/launcher/other.go", "cmd/launcher/run.go"},
		},
		{
			name: "path with line and column suffix",
			findings: "## Blocking\n" +
				"- cmd/launcher/run.go:42:7 — bug\n",
			want: []string{"cmd/launcher/run.go"},
		},
		{
			name: "backticked and emphasized locations",
			findings: "## Blocking\n" +
				"- `cmd/launcher/run.go:42` — bug\n" +
				"## Non-blocking\n" +
				"- **cmd/launcher/other.go** — nit\n",
			want: []string{"cmd/launcher/other.go", "cmd/launcher/run.go"},
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
			want: []string{"nested/dir/file.go"},
		},
		{
			name: "duplicate paths collapse",
			findings: "## Blocking\n" +
				"- run.go:1 — bug one\n" +
				"- run.go:2 — bug two\n",
			want: []string{"run.go"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FindingPaths(c.findings)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("FindingPaths(%q) = %#v, want %#v", c.findings, got, c.want)
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
