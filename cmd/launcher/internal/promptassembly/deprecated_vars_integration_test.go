//go:build integration

package promptassembly

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/seamtest"
)

// These tests are guards over the production rows and shipped prompts, so
// they read the Nix-rendered registry and prompts tree from the seam fixtures
// rather than a committed copy (a synthetic registry would make them vacuous).

// seamPrompts returns the Nix-rendered prompts dir and fragment registry,
// skipping the test when the seam fixtures are absent.
func seamPrompts(t *testing.T) (string, Registry) {
	t.Helper()
	reg, err := LoadRegistryFile(seamtest.Path(t, "fragments-registry.json"))
	if err != nil {
		t.Fatalf("LoadRegistryFile: %v", err)
	}
	return seamtest.Path(t, "prompts"), reg
}

func seamEnv(prompts string) Env {
	env := coveredEnv()
	env.PromptsDir = prompts
	return env
}

// copyPrompts copies prompts into a temp dir so a test can rewrite one file.
func copyPrompts(t *testing.T, prompts string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(prompts)); err != nil {
		t.Fatalf("CopyFS: %v", err)
	}
	return dir
}

// replaceInFile rewrites path with every old replaced by new, failing the test
// if the file does not contain old.
func replaceInFile(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s does not contain %s", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), old, new)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertOverrideRendersAsSlot assembles env against the shipped prompts, then
// against a copy whose file swaps slot for the deprecated vars old, and
// requires the prompt and the agents JSON to match.
func assertOverrideRendersAsSlot(t *testing.T, reg Registry, prompts string, env Env, file, slot, old string) {
	t.Helper()
	want, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	env.PromptsDir = copyPrompts(t, prompts)
	replaceInFile(t, filepath.Join(env.PromptsDir, file), slot, old)
	got, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble (override): %v", err)
	}
	if got.Prompt != want.Prompt {
		t.Errorf("override with the deprecated vars renders a different prompt from %s", slot)
	}
	if got.AgentsJSON != want.AgentsJSON {
		t.Errorf("override with the deprecated vars renders different agents JSON from %s", slot)
	}
}

func TestAssembleNoDeprecationWarningFromShippedPrompts(t *testing.T) {
	prompts, reg := seamPrompts(t)
	env := seamEnv(prompts)
	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	warnings, err := Validate(env, result, nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none for the shipped prompts", warnings)
	}
}

// An override still naming the per-backend vars the slot-collapse (issue
// #5647) deprecated renders exactly what the shipped slot prompt does, on
// every tracker.
func TestAssembleDeprecatedSlotVarsRenderAsBefore(t *testing.T) {
	prompts, reg := seamPrompts(t)
	envFor := func(tracker, axis string) Env {
		e := seamEnv(prompts)
		e.IssueTracker = tracker
		e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler = axis, axis, axis
		if tracker == "local" {
			e.TrackerAxisFiler = "GH" // a local tracker's Filer rides github's arm
		}
		e.FilerEnabled = true
		e.AgentsJSONTemplate = `{"scout":{"model":"x"},"filer":{"model":"x"}}`
		e.AgentsPromptFiles = `{"scout":"scout-prompt.md","filer":"filer-prompt.md"}`
		return e
	}
	slots := []struct {
		name, kind, file, slot, old string
	}{
		{"research", "research", "research-prompt.md", "${RESEARCH_ISSUE_READ_STEP}",
			"${RESEARCH_ISSUE_READ_GITHUB_STEP}${RESEARCH_ISSUE_READ_LOCAL_STEP}${RESEARCH_ISSUE_READ_FORGEJO_STEP}"},
		{"scout", "work", "scout-prompt.md", "${SCOUT_ISSUE_READ_STEP}",
			"${SCOUT_ISSUE_READ_GITHUB_STEP}${SCOUT_ISSUE_READ_FORGEJO_STEP}"},
		{"filer", "work", "filer-prompt.md", "${FILER_DEDUP_STEP}",
			"${FILER_DEDUP_SEARCH_STEP}${FILER_DEDUP_SEARCH_FORGEJO_STEP}${FILER_DEDUP_SEARCH_LOCAL_STEP}"},
		{"filer file", "work", "filer-prompt.md", "${FILER_DIRECT_FILE_STEP}",
			"${FILER_FILE_DIRECT_STEP}${FILER_FILE_DIRECT_FORGEJO_STEP}"},
		{"filer label", "work", "filer-prompt.md", "${FILER_DIRECT_LABEL_STEP}",
			"${FILER_LABEL_DIRECT_STEP}${FILER_LABEL_DIRECT_FORGEJO_STEP}"},
	}
	trackers := []struct{ tracker, axis string }{{"github", "GITHUB"}, {"forgejo", "FORGEJO"}, {"local", "LOCAL"}, {"jira", "GITHUB"}}
	for _, s := range slots {
		for _, tr := range trackers {
			t.Run(s.name+"/"+tr.tracker, func(t *testing.T) {
				env := envFor(tr.tracker, tr.axis)
				env.DispatchKind = s.kind
				assertOverrideRendersAsSlot(t, reg, prompts, env, s.file, s.slot, s.old)
			})
		}
	}
}

// The same guarantee for the verdict, landing and blocked-comment slots
// (issue #5647), which fork on write access and signal carrier as well as on
// the tracker: every combination must render as the per-backend vars did.
func TestAssembleDeprecatedAccessSlotVarsRenderAsBefore(t *testing.T) {
	prompts, reg := seamPrompts(t)
	slots := []struct {
		name, kind, file, slot, old string
	}{
		{"verdict", "research", "research-prompt.md",
			"${RESEARCH_VERDICT_STEP}${RESEARCH_VERDICT_RELAY_STEP}${RESEARCH_VERDICT_RELAY_SOCKET_STEP}",
			"${RESEARCH_VERDICT_GITHUB_STEP}${RESEARCH_VERDICT_GITHUB_READONLY_STEP}${RESEARCH_VERDICT_GITHUB_READONLY_SOCKET_STEP}" +
				"${RESEARCH_VERDICT_LOCAL_STEP}${RESEARCH_VERDICT_LOCAL_SOCKET_STEP}${RESEARCH_VERDICT_FORGEJO_STEP}" +
				"${RESEARCH_VERDICT_FORGEJO_READONLY_STEP}${RESEARCH_VERDICT_FORGEJO_READONLY_SOCKET_STEP}"},
		{"landing", "research", "research-prompt.md", "${RESEARCH_OUTCOME_LANDING_STEP}",
			"${RESEARCH_OUTCOME_LANDING_GITHUB_STEP}${RESEARCH_OUTCOME_LANDING_FORGEJO_STEP}"},
		{"blocked comment", "work", "issue-prompt.md",
			"${ISSUE_BLOCKED_COMMENT_STEP}${ISSUE_BLOCKED_COMMENT_RELAY_STEP}",
			"${ISSUE_BLOCKED_COMMENT_GITHUB_STEP}${ISSUE_BLOCKED_COMMENT_GITHUB_READONLY_STEP}${ISSUE_BLOCKED_COMMENT_LOCAL_STEP}" +
				"${ISSUE_BLOCKED_COMMENT_FORGEJO_STEP}${ISSUE_BLOCKED_COMMENT_FORGEJO_READONLY_STEP}"},
	}
	trackers := []struct{ tracker, axis string }{{"github", "GITHUB"}, {"forgejo", "FORGEJO"}, {"local", "LOCAL"}, {"jira", "GITHUB"}}
	for _, s := range slots {
		for _, tr := range trackers {
			for _, boxWrite := range []bool{true, false} {
				for _, carrier := range []string{"log", "socket"} {
					name := fmt.Sprintf("%s/%s/write=%v/%s", s.name, tr.tracker, boxWrite, carrier)
					t.Run(name, func(t *testing.T) {
						env := seamEnv(prompts)
						env.DispatchKind = s.kind
						env.IssueTracker = tr.tracker
						env.TrackerAxisRead, env.TrackerAxisWrite, env.TrackerAxisFiler = tr.axis, tr.axis, tr.axis
						if tr.tracker == "local" {
							env.TrackerAxisWrite = ""
						}
						env.BoxWriteEnabled = boxWrite
						env.SignalCarrier = carrier
						assertOverrideRendersAsSlot(t, reg, prompts, env, s.file, s.slot, s.old)
					})
				}
			}
		}
	}
}

// The forge-axis, host-mediated and PR-body slots (issue #5647) fork on the
// code forge, on write access and on the local PR-body opt-in rather than on
// the tracker, so each combination must render as the per-backend vars did.
func TestAssembleDeprecatedForgeSlotVarsRenderAsBefore(t *testing.T) {
	prompts, reg := seamPrompts(t)
	slots := []struct {
		name, kind, file, slot, old string
		fixPass                     int
	}{
		{"fix ci read", "work", "fix-prompt.md", "${FIX_CI_READ_STEP}",
			"${FIX_CI_READ_GITHUB_STEP}${FIX_CI_READ_FORGEJO_STEP}", 1},
		{"open pr create", "work", "issue-prompt.md", "${OPEN_PR_CREATE_DIRECT_STEP}",
			"${OPEN_PR_CREATE_READ_WRITE_STEP}${OPEN_PR_CREATE_READ_WRITE_FORGEJO_STEP}", 0},
		{"if blocked pr", "work", "issue-prompt.md", "${IF_BLOCKED_PR_DIRECT_STEP}",
			"${IF_BLOCKED_PR_READ_WRITE_STEP}${IF_BLOCKED_PR_READ_WRITE_FORGEJO_STEP}", 0},
		{"pr body", "work", "issue-prompt.md", "${PR_BODY_ISSUE_REF_STEP}${PR_BODY_NO_ISSUE_REF_STEP}",
			"${PR_BODY_LOCAL_REF_STEP}${PR_BODY_LOCAL_NOREF_STEP}", 0},
		{"commit push", "work", "issue-prompt.md", "${COMMIT_PUSH_HOST_MEDIATED_STEP}",
			"${COMMIT_PUSH_LOCAL_STEP}", 0},
		{"if blocked triage", "work", "issue-prompt.md", "${IF_BLOCKED_TRIAGE_HOST_MEDIATED_STEP}",
			"${IF_BLOCKED_TRIAGE_LOCAL_STEP}", 0},
		{"if blocked push", "work", "issue-prompt.md", "${IF_BLOCKED_PUSH_HOST_MEDIATED_STEP}",
			"${IF_BLOCKED_PUSH_LOCAL_STEP}", 0},
	}
	forges := []struct {
		name, backend            string
		outboxCapable, hostMedia bool
	}{
		{"github", "GH", true, false},
		{"forgejo", "FORGEJO", true, false},
		{"git", "GH", false, false},
		{"local", "GH", false, true},
	}
	trackers := []struct{ tracker, axis string }{{"github", "GITHUB"}, {"local", "LOCAL"}}
	for _, s := range slots {
		for _, f := range forges {
			for _, tr := range trackers {
				for _, boxWrite := range []bool{true, false} {
					for _, ref := range []bool{true, false} {
						name := fmt.Sprintf("%s/%s/%s/write=%v/ref=%v", s.name, f.name, tr.tracker, boxWrite, ref)
						t.Run(name, func(t *testing.T) {
							env := seamEnv(prompts)
							env.FixPass = s.fixPass
							env.DispatchKind = s.kind
							env.IssueTracker = tr.tracker
							env.TrackerAxisRead, env.TrackerAxisWrite, env.TrackerAxisFiler = tr.axis, tr.axis, tr.axis
							if tr.tracker == "local" {
								env.TrackerAxisWrite = ""
							}
							env.CodeForge = f.name
							env.ForgeBackend = f.backend
							env.OutboxRelayCapable = f.outboxCapable
							env.HostMediatedRemote = f.hostMedia
							env.BoxWriteEnabled = boxWrite
							env.LocalIssueReference = ref
							assertOverrideRendersAsSlot(t, reg, prompts, env, s.file, s.slot, s.old)
						})
					}
				}
			}
		}
	}
}
