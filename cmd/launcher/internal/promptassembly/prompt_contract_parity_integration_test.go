//go:build integration

package promptassembly_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/seamtest"
)

// parityFixture is one row of lib/prompt-contract.nix's parityFixtures: a
// validateMarkers row crossed with its gate and whether the marker is present,
// with Nix's own verdict for that combination.
type parityFixture struct {
	ID            string `json:"id"`
	Gate          bool   `json:"gate"`
	MarkerPresent bool   `json:"markerPresent"`
	Verdict       string `json:"verdict"`
}

const (
	parityFiler     = `{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}`
	parityPromptMap = `{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md"}`
)

// parityPrompts is a prompts dir of stub templates, so each fixture's marker
// set is exactly what the test writes and no real fragment supplies one.
// review-prompt.md carries a VERDICT: line unless the fixture overrides it,
// so a row that does not target reviewer-verdict never trips it.
func parityPrompts(t *testing.T, override map[string]string) string {
	t.Helper()
	files := map[string]string{
		"issue-prompt.md":  "issue stub\n",
		"scout-prompt.md":  "scout stub\n",
		"review-prompt.md": "reviewer stub\n\nVERDICT: APPROVE or BLOCK\n",
		"worker-prompt.md": "worker stub\n",
		"fix-prompt.md":    "fix stub\n",
	}
	for name, body := range override {
		files[name] = body
	}
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func withMarker(present bool, marker, withText, withoutText string) string {
	if present {
		return withText + marker + "\n"
	}
	return withoutText + "\n"
}

// parityCell is the Env and stub templates that drive one fixture's row to its
// gate state, mirroring how each row's gate is derived in gates.go.
func parityCell(t *testing.T, f parityFixture) promptassembly.Env {
	t.Helper()
	env := promptassembly.Env{
		IssueTracker: "github", TrackerAxisRead: "GITHUB", TrackerAxisWrite: "GITHUB", TrackerAxisFiler: "GH",
		CodeForge: "github", ForgeBackend: "GH", BoxWriteEnabled: true, DispatchKind: "work",
		IssueNumber: "7", IssueTitle: "Do the thing", Branch: "agent/issue-7", BaseBranch: "main",
		InProgressLabel: "agent-in-progress", CompleteLabel: "agent-complete", RunNonce: "n0nce",
		AgentsPromptFiles: parityPromptMap,
	}
	override := map[string]string{}
	switch f.ID {
	case "verdict-comment-relay":
		override["research-prompt.md"] = withMarker(f.MarkerPresent, "SPINDRIFT_COMMENT",
			"research stub\n\nPost your verdict with ", "research stub, no verdict-comment marker here")
		if f.Gate {
			env.DispatchKind, env.BoxWriteEnabled = "research", false
		}
	case "reviewer-verdict":
		override["review-prompt.md"] = withMarker(f.MarkerPresent, "VERDICT: APPROVE or BLOCK",
			"reviewer stub\n\n", "reviewer stub, no verdict line here")
		// The row gates on a rendered review prompt, which Assemble emits only
		// for a fresh-work dispatch; a fix pass closes the gate.
		if !f.Gate {
			env.FixPass = 1
		}
	case "pr-intent":
		override["issue-prompt.md"] = withMarker(f.MarkerPresent, "SPINDRIFT_PR_INTENT",
			"issue stub with ", "issue stub, no PR-intent marker here")
		env.BoxWriteEnabled = !f.Gate
	case "issue-intent":
		override["filer-prompt.md"] = withMarker(f.MarkerPresent, "SPINDRIFT_ISSUE_INTENT",
			"filer stub with ", "filer stub, no issue-intent marker here")
		// FILER_FILE_RELAY needs the filer and a read-only Box at once, so
		// turning writes on alone closes the gate while the filer's prompt
		// stays populated and markerPresent still matters.
		env.AgentsJSONTemplate, env.FilerEnabled, env.BoxWriteEnabled = parityFiler, true, !f.Gate
	case "research-issue-intent":
		// The research case of FILER_FILE_RELAY (ADR 0041) also arms the
		// verdict-comment-relay row, which scans this same research prompt
		// for SPINDRIFT_COMMENT: carry it so that row never fires here.
		comment := ""
		if f.Gate {
			comment = "\n\nPost your verdict with SPINDRIFT_COMMENT here"
		}
		body := "research stub, no issue-intent marker here"
		if f.MarkerPresent {
			body = "research stub with SPINDRIFT_ISSUE_INTENT here"
		}
		override["research-prompt.md"] = body + comment + "\n"
		if f.Gate {
			env.DispatchKind, env.FilerEnabled = "research", true
		}
	default:
		t.Fatalf("fixture id %q has no cell: extend parityCell to cover it", f.ID)
	}
	env.PromptsDir = parityPrompts(t, override)
	return env
}

// TestValidatorMatchesParityFold drives the real validator, over the real
// prompt-contract registry, through every fixture lib/prompt-contract.nix
// resolved and asserts it blocks exactly when parityFold(verdict) says it must:
// only "reject" blocks, "ok" and "advise" never do. This is the cross-language
// proof that the pure-Nix fold (nix/checks/prompt-contract-parity.nix) matches
// the runtime validator.
func TestValidatorMatchesParityFold(t *testing.T) {
	var fixtures []parityFixture
	raw, err := os.ReadFile(seamtest.Path(t, "prompt-contract-parity-fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	rows, err := promptassembly.LoadValidateMarkersFile(seamtest.Path(t, "prompt-contract-registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := promptassembly.LoadRegistryFile(seamtest.Path(t, "fragments-registry.json"))
	if err != nil {
		t.Fatal(err)
	}

	if len(fixtures) == 0 {
		t.Fatal("no parity fixtures")
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ID] = false
	}
	for _, f := range fixtures {
		seen[f.ID] = true
		name := f.ID + "/gate=" + boolWord(f.Gate) + "/marker=" + boolWord(f.MarkerPresent)
		t.Run(name, func(t *testing.T) {
			env := parityCell(t, f)
			result, err := promptassembly.Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			_, err = promptassembly.Validate(env, result, rows)
			blocked := err != nil
			if want := f.Verdict == "reject"; blocked != want {
				t.Errorf("verdict %q: validator blocked = %v (err %v), want %v", f.Verdict, blocked, err, want)
			}
		})
	}
	for id, covered := range seen {
		if !covered {
			t.Errorf("validateMarkers row %q has no parity fixture", id)
		}
	}
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
