package dispatch

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
)

// Issue #3445 made an IssueTextFor error fatal to the dispatch. Callers of
// this helper pass configs that cannot produce that error, so a non-nil error
// here means buildBoxEnv itself broke, not the thing under test.
func mustBuildBoxEnv(t *testing.T, cfg Config, number, title string, fixPass int, ciFailureSummary, nonce string) map[string]string {
	t.Helper()
	env, err := buildBoxEnv(cfg, number, title, fixPass, ciFailureSummary, nonce)
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	return env
}

func TestBuildBoxEnvForwardsSchemaVars(t *testing.T) {
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "tok")

	cfg := Config{BoxEnvVars: "REPO_SLUG GH_TOKEN"}
	env := mustBuildBoxEnv(t, cfg, "7", "Test issue", 0, "", "")

	if env["REPO_SLUG"] != "owner/repo" {
		t.Errorf("REPO_SLUG: got %q, want %q", env["REPO_SLUG"], "owner/repo")
	}
	if env["GH_TOKEN"] != "tok" {
		t.Errorf("GH_TOKEN: got %q, want %q", env["GH_TOKEN"], "tok")
	}
	if env["ISSUE_NUMBER"] != "7" {
		t.Errorf("ISSUE_NUMBER: got %q, want %q", env["ISSUE_NUMBER"], "7")
	}
	if env["ISSUE_TITLE"] != "Test issue" {
		t.Errorf("ISSUE_TITLE: got %q, want %q", env["ISSUE_TITLE"], "Test issue")
	}
	if _, ok := env["FIX_PASS"]; ok {
		t.Error("FIX_PASS should not be set for fixPass=0")
	}
	if _, ok := env["CI_FAILURE_SUMMARY"]; ok {
		t.Error("CI_FAILURE_SUMMARY should not be set when empty")
	}
}

// buildBoxEnv resolves each BoxEnvVars name through Config.ResolveEnv rather
// than a raw os.Getenv, so a boxEnv knob's document-baked value still reaches
// the Box when the operator never set it as an ambient env var. ADR 0020
// dropped the wrapper's per-var env export.
func TestBuildBoxEnvUsesResolveEnv(t *testing.T) {
	cfg := Config{
		BoxEnvVars: "MODEL",
		ResolveEnv: func(num, name string) string {
			if name == "MODEL" {
				return "from-resolver"
			}
			return ""
		},
	}
	env := mustBuildBoxEnv(t, cfg, "7", "Test issue", 0, "", "")
	if env["MODEL"] != "from-resolver" {
		t.Errorf("MODEL: got %q, want from-resolver", env["MODEL"])
	}
}

// Issue #1734: CODE_FORGE=local resolves BASE_BRANCH per seam, so ResolveEnv
// has to know which issue it is resolving for. Each seam may key its
// Integration branch off a different parent.
func TestBuildBoxEnvResolveEnvReceivesIssueNumber(t *testing.T) {
	var gotNum string
	cfg := Config{
		BoxEnvVars: "BASE_BRANCH",
		ResolveEnv: func(num, name string) string {
			gotNum = num
			return ""
		},
	}
	mustBuildBoxEnv(t, cfg, "1734", "Test issue", 0, "", "")
	if gotNum != "1734" {
		t.Errorf("ResolveEnv num: got %q, want %q", gotNum, "1734")
	}
}

func TestBuildBoxEnvSetsFixPassAndSummary(t *testing.T) {
	env := mustBuildBoxEnv(t, Config{}, "3", "T", 2, "lint failed", "")
	if env["FIX_PASS"] != "2" {
		t.Errorf("FIX_PASS: got %q, want %q", env["FIX_PASS"], "2")
	}
	if env["CI_FAILURE_SUMMARY"] != "lint failed" {
		t.Errorf("CI_FAILURE_SUMMARY: got %q, want %q", env["CI_FAILURE_SUMMARY"], "lint failed")
	}
}

// ADR 0022 added DISPATCH_KIND. It defaults to "work" when Config.Kind is
// unset so every pre-existing, kind-unaware caller keeps behaving the same
// way.
func TestBuildBoxEnvSetsDispatchKind(t *testing.T) {
	if got := mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "")["DISPATCH_KIND"]; got != "work" {
		t.Errorf("DISPATCH_KIND with unset Config.Kind: got %q, want %q", got, "work")
	}
	if got := mustBuildBoxEnv(t, Config{Kind: "research"}, "3", "T", 0, "", "")["DISPATCH_KIND"]; got != "research" {
		t.Errorf("DISPATCH_KIND with Config.Kind=research: got %q, want %q", got, "research")
	}
}

// Issue #2202: the entrypoint reads SELF_CONTAINED to skip clone_repo and
// select the self-contained research prompt.
func TestBuildBoxEnv_SelfContainedSetsMarker(t *testing.T) {
	if got := mustBuildBoxEnv(t, Config{SelfContained: true}, "3", "T", 0, "", "")["SELF_CONTAINED"]; got != "1" {
		t.Errorf("SELF_CONTAINED with Config.SelfContained=true: got %q, want %q", got, "1")
	}
}

// SELF_CONTAINED stays unset, not "0" and not "", matching every pre-#2202
// construction site.
func TestBuildBoxEnv_SelfContainedAbsentByDefault(t *testing.T) {
	if _, ok := mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "")["SELF_CONTAINED"]; ok {
		t.Error("SELF_CONTAINED should be absent when Config.SelfContained is false")
	}
}

// Issue #1937: the Box reads the Dispatch's per-run nonce as RUN_NONCE.
func TestBuildBoxEnvSetsRunNonce(t *testing.T) {
	env := mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "the-nonce")
	if env["RUN_NONCE"] != "the-nonce" {
		t.Errorf("RUN_NONCE: got %q, want %q", env["RUN_NONCE"], "the-nonce")
	}
}

// Issue #1951: the host decides write-enabled once and forwards one positive
// signal, so the Box never re-derives it from the defaultable
// BOX_FORGE_AND_ISSUE_ACCESS string.
func TestBuildBoxEnvSetsWriteEnabledSignal(t *testing.T) {
	if _, ok := mustBuildBoxEnv(t, Config{BoxForgeAndIssueAccess: "read-write"}, "3", "T", 0, "", "")["BOX_WRITE_ENABLED"]; !ok {
		t.Error("BOX_WRITE_ENABLED should be set when BoxForgeAndIssueAccess=read-write")
	}
	if _, ok := mustBuildBoxEnv(t, Config{BoxForgeAndIssueAccess: "read-only"}, "3", "T", 0, "", "")["BOX_WRITE_ENABLED"]; ok {
		t.Error("BOX_WRITE_ENABLED should be absent when BoxForgeAndIssueAccess=read-only")
	}
	// This case is the fail-closed property the signal exists for. validate()
	// rejects an empty or malformed BoxForgeAndIssueAccess upstream, but the
	// property has to hold in buildBoxEnv itself rather than depend on the
	// caller.
	if _, ok := mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "")["BOX_WRITE_ENABLED"]; ok {
		t.Error("BOX_WRITE_ENABLED should be absent when BoxForgeAndIssueAccess is empty/malformed")
	}
}

// Issue #3063: Config carries the two resolved backend.Descriptor rows
// directly instead of the wider forge.Capabilities value. Each var is present
// only as "1" when true, and absent rather than "0" when false.
func TestBuildBoxEnvForwardsDescriptors(t *testing.T) {
	env := mustBuildBoxEnv(t, Config{
		ForgeDescriptor:   backend.Descriptor{HostMediatedRemote: true, OutboxRelayCapable: true},
		TrackerDescriptor: backend.Descriptor{InBoxUnreachableTracker: true},
	}, "3", "T", 0, "", "")
	if got := env["BOX_HOST_MEDIATED_REMOTE"]; got != "1" {
		t.Errorf("BOX_HOST_MEDIATED_REMOTE with ForgeDescriptor.HostMediatedRemote=true: got %q, want %q", got, "1")
	}
	if got := env["BOX_OUTBOX_RELAY_CAPABLE"]; got != "1" {
		t.Errorf("BOX_OUTBOX_RELAY_CAPABLE with ForgeDescriptor.OutboxRelayCapable=true: got %q, want %q", got, "1")
	}
	// FullyLocal is HostMediatedRemote && InBoxUnreachableTracker, both true here.
	if got := env["BOX_FULLY_LOCAL"]; got != "1" {
		t.Errorf("BOX_FULLY_LOCAL with both seams local: got %q, want %q", got, "1")
	}
	if got := env["BOX_IN_BOX_UNREACHABLE_TRACKER"]; got != "1" {
		t.Errorf("BOX_IN_BOX_UNREACHABLE_TRACKER with TrackerDescriptor.InBoxUnreachableTracker=true: got %q, want %q", got, "1")
	}

	env = mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "")
	if _, ok := env["BOX_HOST_MEDIATED_REMOTE"]; ok {
		t.Error("BOX_HOST_MEDIATED_REMOTE should be absent when ForgeDescriptor.HostMediatedRemote is false")
	}
	if _, ok := env["BOX_OUTBOX_RELAY_CAPABLE"]; ok {
		t.Error("BOX_OUTBOX_RELAY_CAPABLE should be absent when ForgeDescriptor.OutboxRelayCapable is false")
	}
	if _, ok := env["BOX_FULLY_LOCAL"]; ok {
		t.Error("BOX_FULLY_LOCAL should be absent when ForgeDescriptor/TrackerDescriptor are zero-value")
	}
	if _, ok := env["BOX_IN_BOX_UNREACHABLE_TRACKER"]; ok {
		t.Error("BOX_IN_BOX_UNREACHABLE_TRACKER should be absent when TrackerDescriptor.InBoxUnreachableTracker is false")
	}
}

// Issue #2533: each var is absent when its Config field is empty, including
// TrackerAxisWrite's legitimate empty-for-local-tracker case.
func TestBuildBoxEnvForwardsTrackerAxisAndForgeBackend(t *testing.T) {
	env := mustBuildBoxEnv(t, Config{
		TrackerAxisRead:  "GITHUB",
		TrackerAxisWrite: "GITHUB",
		TrackerAxisFiler: "GH",
		ForgeBackend:     "GH",
	}, "3", "T", 0, "", "")
	if got := env["BOX_TRACKER_AXIS_READ"]; got != "GITHUB" {
		t.Errorf("BOX_TRACKER_AXIS_READ: got %q, want %q", got, "GITHUB")
	}
	if got := env["BOX_TRACKER_AXIS_WRITE"]; got != "GITHUB" {
		t.Errorf("BOX_TRACKER_AXIS_WRITE: got %q, want %q", got, "GITHUB")
	}
	if got := env["BOX_TRACKER_AXIS_FILER"]; got != "GH" {
		t.Errorf("BOX_TRACKER_AXIS_FILER: got %q, want %q", got, "GH")
	}
	if got := env["BOX_FORGE_BACKEND"]; got != "GH" {
		t.Errorf("BOX_FORGE_BACKEND: got %q, want %q", got, "GH")
	}

	env = mustBuildBoxEnv(t, Config{TrackerAxisRead: "LOCAL", TrackerAxisWrite: ""}, "3", "T", 0, "", "")
	if got := env["BOX_TRACKER_AXIS_READ"]; got != "LOCAL" {
		t.Errorf("BOX_TRACKER_AXIS_READ: got %q, want %q", got, "LOCAL")
	}
	if _, ok := env["BOX_TRACKER_AXIS_WRITE"]; ok {
		t.Error("BOX_TRACKER_AXIS_WRITE should be absent when Config.TrackerAxisWrite is empty (local tracker)")
	}

	env = mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "")
	for _, name := range []string{"BOX_TRACKER_AXIS_READ", "BOX_TRACKER_AXIS_WRITE", "BOX_TRACKER_AXIS_FILER", "BOX_FORGE_BACKEND"} {
		if _, ok := env[name]; ok {
			t.Errorf("%s should be absent when Config is zero-valued", name)
		}
	}
}

// Issue #2533: each var is present only as "1" when true and absent rather
// than "0" when false, matching BOX_FULLY_LOCAL's forwarding shape.
func TestBuildBoxEnvForwardsFilerEnabledWorkerProvisionedReviewLoop(t *testing.T) {
	env := mustBuildBoxEnv(t, Config{
		FilerEnabled:           true,
		WorkerProvisioned:      true,
		ReviewLoopInline:       true,
		ReviewLoopOrchestrator: true,
	}, "3", "T", 0, "", "")
	for _, name := range []string{"BOX_FILER_ENABLED", "BOX_WORKER_PROVISIONED", "BOX_REVIEW_LOOP_INLINE", "BOX_REVIEW_LOOP_ORCHESTRATOR"} {
		if got := env[name]; got != "1" {
			t.Errorf("%s: got %q, want %q", name, got, "1")
		}
	}

	env = mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "")
	for _, name := range []string{"BOX_FILER_ENABLED", "BOX_WORKER_PROVISIONED", "BOX_REVIEW_LOOP_INLINE", "BOX_REVIEW_LOOP_ORCHESTRATOR"} {
		if _, ok := env[name]; ok {
			t.Errorf("%s should be absent when the Config field is false", name)
		}
	}
}

// Issue #3171: absence is the load-bearing half, because these vars carry
// only an operator's explicit dispatch-time REVIEW_MODEL/REVIEW_EFFORT, so a
// forwarded schema default or document value would silently override the
// baked roster on every dispatch.
func TestBuildBoxEnvForwardsReviewOverrides(t *testing.T) {
	env := mustBuildBoxEnv(t, Config{
		ReviewModelOverride:  "claude-sonnet-5",
		ReviewEffortOverride: "high",
	}, "3", "T", 0, "", "")
	if got := env["BOX_REVIEW_MODEL_OVERRIDE"]; got != "claude-sonnet-5" {
		t.Errorf("BOX_REVIEW_MODEL_OVERRIDE: got %q, want %q", got, "claude-sonnet-5")
	}
	if got := env["BOX_REVIEW_EFFORT_OVERRIDE"]; got != "high" {
		t.Errorf("BOX_REVIEW_EFFORT_OVERRIDE: got %q, want %q", got, "high")
	}

	env = mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "")
	for _, name := range []string{"BOX_REVIEW_MODEL_OVERRIDE", "BOX_REVIEW_EFFORT_OVERRIDE"} {
		if _, ok := env[name]; ok {
			t.Errorf("%s should be absent when the Config field is empty", name)
		}
	}
}

// Issue #3445: a failing closure errors rather than silently dropping the
// var: every issue-prompt.md-family prompt now tells the Box its body lives
// in the injected ISSUE_TEXT section and not to fetch it from the tracker, so
// a Box launched without it has no recourse. An unreadable subject issue
// fails the dispatch outright, with no retry (see buildBoxEnv's doc).
func TestBuildBoxEnvForwardsIssueText(t *testing.T) {
	env, err := buildBoxEnv(Config{}, "3", "T", 0, "", "")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	if _, ok := env["ISSUE_TEXT"]; ok {
		t.Error("ISSUE_TEXT should be absent when Config.IssueTextFor is nil")
	}

	env, err = buildBoxEnv(Config{
		IssueTextFor: func(number string) (string, error) { return "the issue body", nil },
	}, "3", "T", 0, "", "")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	if got := env["ISSUE_TEXT"]; got != "the issue body" {
		t.Errorf("ISSUE_TEXT: got %q, want %q", got, "the issue body")
	}

	// Issue #3470: an empty issue text never becomes a box.Env entry at all,
	// so the runner tests that pass ISSUE_TEXT="" exercise a state real
	// dispatch never produces.
	env, err = buildBoxEnv(Config{
		IssueTextFor: func(number string) (string, error) { return "", nil },
	}, "3", "T", 0, "", "")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	if _, ok := env["ISSUE_TEXT"]; ok {
		t.Errorf("ISSUE_TEXT should be absent when Config.IssueTextFor resolves to an empty string, got %q", env["ISSUE_TEXT"])
	}

	_, err = buildBoxEnv(Config{
		IssueTextFor: func(number string) (string, error) { return "", errors.New("boom") },
	}, "3", "T", 0, "", "")
	if err == nil {
		t.Fatal("buildBoxEnv: want a non-nil error when Config.IssueTextFor errors")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("buildBoxEnv error = %v, want it to mention the issue number", err)
	}
}

// Issue #3157: BOX_SCOUT_PROVISIONED mirrors WorkerProvisioned's forwarding
// shape exactly, present only as "1" when true and absent rather than "0"
// when false.
func TestBuildBoxEnvForwardsScoutProvisioned(t *testing.T) {
	env := mustBuildBoxEnv(t, Config{ScoutProvisioned: true}, "3", "T", 0, "", "")
	if got := env["BOX_SCOUT_PROVISIONED"]; got != "1" {
		t.Errorf("BOX_SCOUT_PROVISIONED: got %q, want %q", got, "1")
	}

	env = mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "")
	if _, ok := env["BOX_SCOUT_PROVISIONED"]; ok {
		t.Error("BOX_SCOUT_PROVISIONED should be absent when Config.ScoutProvisioned is false")
	}
}
