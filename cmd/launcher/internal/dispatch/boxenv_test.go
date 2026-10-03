package dispatch

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/signalsocket"
)

// Issue #3445 made an IssueTextFor error fatal to the dispatch. Callers of
// this helper pass configs that cannot produce that error, so a non-nil error
// here means buildBoxEnv itself broke, not the thing under test.
func mustBuildBoxEnv(t *testing.T, cfg Config, number, title string, fixPass int, ciFailureSummary, nonce string) map[string]string {
	t.Helper()
	env, err := buildBoxEnv(cfg, issueSubject(number, title), fixPass, ciFailureSummary, nonce)
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

// Issue #3996: the Box reads its axes as separate facts, never
// DISPATCH_KIND (display-only there).
func TestBuildBoxEnvSetsDispatchAxes(t *testing.T) {
	for _, d := range dispatchkind.All {
		env := mustBuildBoxEnv(t, Config{Kind: d.Name}, "3", "T", 0, "", "")
		if got := env["DISPATCH_KEYING"]; got != d.Keying.String() {
			t.Errorf("%s: DISPATCH_KEYING = %q, want %q", d.Name, got, d.Keying.String())
		}
		if got := env["DISPATCH_ANNOUNCE_VERB"]; got != d.AnnounceVerb {
			t.Errorf("%s: DISPATCH_ANNOUNCE_VERB = %q, want %q", d.Name, got, d.AnnounceVerb)
		}
		if got := env["DISPATCH_KEY"]; got != "3" {
			t.Errorf("%s: DISPATCH_KEY = %q, want %q", d.Name, got, "3")
		}
	}
}

// DISPATCH_KEY carries the same bare issue key an issue Dispatch's outcome
// line uses (dispatchkey.Key.String()).
func TestBuildBoxEnvDispatchKeyForIssue(t *testing.T) {
	if got := mustBuildBoxEnv(t, Config{}, "42", "T", 0, "", "")["DISPATCH_KEY"]; got != "42" {
		t.Errorf("DISPATCH_KEY: got %q, want %q", got, "42")
	}
}

// DISPATCH_KEY carries the byte-identical "butler-<chore>" outcome-line key
// (dispatchkey.Chore(name).String()) for a butler Dispatch.
func TestBuildBoxEnvDispatchKeyForChore(t *testing.T) {
	env, err := buildBoxEnv(Config{Kind: dispatchkind.Butler.Name}, choreSubject(Chore{Name: "lint-sweep"}), 0, "", "")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	if got := env["DISPATCH_KEY"]; got != "butler-lint-sweep" {
		t.Errorf("DISPATCH_KEY: got %q, want %q", got, "butler-lint-sweep")
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

// Issue #3901: the Box resolves its advise-only posture from DISPATCH_KIND
// itself, so a second ADVISE_ONLY input must never come back to disagree.
func TestBuildBoxEnv_AdviseOnlyNeverForwarded(t *testing.T) {
	for _, d := range dispatchkind.All {
		env := mustBuildBoxEnv(t, Config{Kind: d.Name}, "3", "T", 0, "", "")
		if _, ok := env["ADVISE_ONLY"]; ok {
			t.Errorf("ADVISE_ONLY should never be forwarded (kind=%s)", d.Name)
		}
		if got := env["DISPATCH_KIND"]; got != d.Name {
			t.Errorf("DISPATCH_KIND with Config.Kind=%s: got %q, want %q", d.Name, got, d.Name)
		}
	}
	if _, ok := mustBuildBoxEnv(t, Config{Kind: "bogus-kind"}, "3", "T", 0, "", "")["ADVISE_ONLY"]; ok {
		t.Error("ADVISE_ONLY should be absent for an unrecognized Config.Kind")
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
	// Issue #3906: a ReadOnlyBox kind (butler) must never see
	// BOX_WRITE_ENABLED, even given a raw read-write value directly on
	// Config — buildBoxEnv's own fail-closed check, independent of main.go's
	// upstream forcing.
	if _, ok := mustBuildBoxEnv(t, Config{Kind: "butler", BoxForgeAndIssueAccess: "read-write"}, "3", "T", 0, "", "")["BOX_WRITE_ENABLED"]; ok {
		t.Error("BOX_WRITE_ENABLED should be absent for a butler Config even when BoxForgeAndIssueAccess=read-write")
	}
	// A non-ReadOnlyBox kind is unaffected by the #3906 check.
	if _, ok := mustBuildBoxEnv(t, Config{Kind: "work", BoxForgeAndIssueAccess: "read-write"}, "3", "T", 0, "", "")["BOX_WRITE_ENABLED"]; !ok {
		t.Error("BOX_WRITE_ENABLED should still be set for a work Config with BoxForgeAndIssueAccess=read-write")
	}
}

// Issue #3906: BOX_FORGE_AND_ISSUE_ACCESS is boxEnv=true (lib/env-schema.nix),
// so it reaches the Box through the BoxEnvVars forwarding loop as a raw
// value. A butler Config must forward the forced "read-only" there too, not
// the raw knob, or the Box sees a write-capable BOX_FORGE_AND_ISSUE_ACCESS
// alongside an absent BOX_WRITE_ENABLED.
func TestBuildBoxEnvForcesForgeAndIssueAccessForReadOnlyBoxKind(t *testing.T) {
	cfg := Config{
		Kind:                   "butler",
		BoxEnvVars:             "BOX_FORGE_AND_ISSUE_ACCESS",
		BoxForgeAndIssueAccess: "read-write",
		ResolveEnv: func(_, name string) string {
			if name == "BOX_FORGE_AND_ISSUE_ACCESS" {
				return "read-write"
			}
			return ""
		},
	}
	if got := mustBuildBoxEnv(t, cfg, "3", "T", 0, "", "")["BOX_FORGE_AND_ISSUE_ACCESS"]; got != "read-only" {
		t.Errorf("BOX_FORGE_AND_ISSUE_ACCESS: got %q, want %q", got, "read-only")
	}

	// Any other kind forwards the resolver's value untouched.
	cfg.Kind = "research"
	cfg.BoxForgeAndIssueAccess = ""
	if got := mustBuildBoxEnv(t, cfg, "3", "T", 0, "", "")["BOX_FORGE_AND_ISSUE_ACCESS"]; got != "read-write" {
		t.Errorf("BOX_FORGE_AND_ISSUE_ACCESS for research: got %q, want %q", got, "read-write")
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
func TestBuildBoxEnvForwardsFilerEnabledWorkerProvisioned(t *testing.T) {
	env := mustBuildBoxEnv(t, Config{
		FilerEnabled:      true,
		WorkerProvisioned: true,
	}, "3", "T", 0, "", "")
	for _, name := range []string{"BOX_FILER_ENABLED", "BOX_WORKER_PROVISIONED"} {
		if got := env[name]; got != "1" {
			t.Errorf("%s: got %q, want %q", name, got, "1")
		}
	}

	env = mustBuildBoxEnv(t, Config{}, "3", "T", 0, "", "")
	for _, name := range []string{"BOX_FILER_ENABLED", "BOX_WORKER_PROVISIONED"} {
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
	env, err := buildBoxEnv(Config{}, issueSubject("3", "T"), 0, "", "")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	if _, ok := env["ISSUE_TEXT"]; ok {
		t.Error("ISSUE_TEXT should be absent when Config.IssueTextFor is nil")
	}

	env, err = buildBoxEnv(Config{
		IssueTextFor: func(number string) (string, error) { return "the issue body", nil },
	}, issueSubject("3", "T"), 0, "", "")
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
	}, issueSubject("3", "T"), 0, "", "")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	if _, ok := env["ISSUE_TEXT"]; ok {
		t.Errorf("ISSUE_TEXT should be absent when Config.IssueTextFor resolves to an empty string, got %q", env["ISSUE_TEXT"])
	}

	_, err = buildBoxEnv(Config{
		IssueTextFor: func(number string) (string, error) { return "", errors.New("boom") },
	}, issueSubject("3", "T"), 0, "", "")
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

// Issue #3875 (ADR 0056): a chore Dispatch forwards CHORE_* and BASE_BRANCH
// straight from the Chore, and never the issue-keyed trio, since a chore key
// names no tracker issue.
func TestBuildBoxEnv_ChoreForwardsChoreVarsNotIssueVars(t *testing.T) {
	var resolveEnvCalls, gotKeys []string
	cfg := Config{
		BoxEnvVars: "BASE_BRANCH MODEL",
		ResolveEnv: func(num, name string) string {
			resolveEnvCalls = append(resolveEnvCalls, name)
			gotKeys = append(gotKeys, num)
			if name == "MODEL" {
				return "from-resolver"
			}
			return ""
		},
		IssueTextFor: func(number string) (string, error) {
			t.Fatalf("IssueTextFor must not be called for a chore Dispatch, got number=%q", number)
			return "", nil
		},
	}
	c := Chore{
		Name:   "lint-sweep",
		Branch: "butler/lint-sweep",
		Scope: chore.Scope{
			Head:      "deadbeef",
			DiffRange: "cafe..deadbeef",
			Slice:     []string{"a.go", "b.go"},
		},
		Classes:      []string{"flaky-test", "dead-code"},
		PatchClasses: []string{"docs-drift"},
	}
	env, err := buildBoxEnv(cfg, choreSubject(c), 0, "", "the-nonce")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}

	if got := env["CHORE_NAME"]; got != "lint-sweep" {
		t.Errorf("CHORE_NAME: got %q, want %q", got, "lint-sweep")
	}
	if got := env["CHORE_HEAD"]; got != "deadbeef" {
		t.Errorf("CHORE_HEAD: got %q, want %q", got, "deadbeef")
	}
	if got := env["CHORE_DIFF_RANGE"]; got != "cafe..deadbeef" {
		t.Errorf("CHORE_DIFF_RANGE: got %q, want %q", got, "cafe..deadbeef")
	}
	if got := env["CHORE_SLICE"]; got != "a.go\nb.go" {
		t.Errorf("CHORE_SLICE: got %q, want %q", got, "a.go\nb.go")
	}
	if got := env["CHORE_CLASSES"]; got != "flaky-test dead-code" {
		t.Errorf("CHORE_CLASSES: got %q, want %q", got, "flaky-test dead-code")
	}
	if got := env["CHORE_PATCH_CLASSES"]; got != "docs-drift" {
		t.Errorf("CHORE_PATCH_CLASSES: got %q, want %q", got, "docs-drift")
	}
	if got := env["BASE_BRANCH"]; got != "butler/lint-sweep" {
		t.Errorf("BASE_BRANCH: got %q, want %q", got, "butler/lint-sweep")
	}
	for _, unwanted := range []string{"ISSUE_NUMBER", "ISSUE_TITLE", "ISSUE_TEXT"} {
		if v, ok := env[unwanted]; ok {
			t.Errorf("%s should be absent for a chore Dispatch, got %q", unwanted, v)
		}
	}

	for _, name := range resolveEnvCalls {
		if name == "BASE_BRANCH" {
			t.Error("ResolveEnv must not be called with BASE_BRANCH for a chore Dispatch")
		}
	}
	if got := env["MODEL"]; got != "from-resolver" {
		t.Errorf("MODEL: got %q, want %q (other BoxEnvVars still resolve normally)", got, "from-resolver")
	}
	for _, got := range gotKeys {
		if want := dispatchkey.Chore("lint-sweep").String(); got != want {
			t.Errorf("ResolveEnv key: got %q, want %q (subj.key.String(), issue #3954)", got, want)
		}
	}
}

// A chore whose Scope carries no Head/DiffRange/Slice (the first run over an
// empty tree, in principle) forwards none of CHORE_HEAD/CHORE_DIFF_RANGE/
// CHORE_SLICE, matching every other optional field's absent-when-empty shape.
func TestBuildBoxEnv_ChoreOmitsEmptyScopeFields(t *testing.T) {
	env, err := buildBoxEnv(Config{}, choreSubject(Chore{Name: "empty", Branch: "b"}), 0, "", "")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	for _, name := range []string{"CHORE_HEAD", "CHORE_DIFF_RANGE", "CHORE_SLICE"} {
		if v, ok := env[name]; ok {
			t.Errorf("%s should be absent for an empty Scope, got %q", name, v)
		}
	}
}

// A chore whose Classes is nil/empty (promotion off, or no allow-list --
// butler.go's job to decide, this package just forwards whatever it is
// given) omits CHORE_CLASSES entirely, the same absent-when-empty shape as
// every other optional Chore field.
func TestBuildBoxEnv_ChoreOmitsEmptyClasses(t *testing.T) {
	env, err := buildBoxEnv(Config{}, choreSubject(Chore{Name: "empty", Branch: "b"}), 0, "", "")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	if v, ok := env["CHORE_CLASSES"]; ok {
		t.Errorf("CHORE_CLASSES should be absent when Classes is empty, got %q", v)
	}
}

// The patch rung's CHORE_PATCH_CLASSES sibling (issue #4072, ADR 0057) gets
// the same absent-when-empty treatment: butler.go's job to leave
// PatchClasses nil whenever the rung is off or today's patch room is spent,
// this package just forwards whatever it is given.
func TestBuildBoxEnv_ChoreOmitsEmptyPatchClasses(t *testing.T) {
	env, err := buildBoxEnv(Config{}, choreSubject(Chore{Name: "empty", Branch: "b"}), 0, "", "")
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	if v, ok := env["CHORE_PATCH_CLASSES"]; ok {
		t.Errorf("CHORE_PATCH_CLASSES should be absent when PatchClasses is empty, got %q", v)
	}
}

// CHORE_MAX_FINDINGS forwards the sweep's own room (chore.Room.Findings,
// issue #3994) when the host set one, and falls back to
// signalsocket.DefaultMaxIssueIntents (8) when it left MaxFindings zero --
// the same fallback startSignalSocket applies to the socket carrier's cap,
// so a log-carrier Box is told the same number a socket-carrier one's
// listener actually enforces.
func TestBuildBoxEnv_ChoreForwardsMaxFindings(t *testing.T) {
	cases := []struct {
		name        string
		maxFindings int
		want        string
	}{
		{"host cap set", 12, "12"},
		{"host cap unset", 0, strconv.Itoa(signalsocket.DefaultMaxIssueIntents)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := buildBoxEnv(Config{}, choreSubject(Chore{Name: "empty", Branch: "b", MaxFindings: tc.maxFindings}), 0, "", "")
			if err != nil {
				t.Fatalf("buildBoxEnv: unexpected error: %v", err)
			}
			if got := env["CHORE_MAX_FINDINGS"]; got != tc.want {
				t.Errorf("CHORE_MAX_FINDINGS: got %q, want %q", got, tc.want)
			}
		})
	}
}

// subject is the single source of a Dispatch's key and title (issue #3954,
// #3988): newDispatch sets d.number from subj.key.String(), and announce
// prints subj.title.
func TestSubjectKeyAndTitle(t *testing.T) {
	cases := []struct {
		name      string
		subj      subject
		wantKey   string
		wantTitle string
	}{
		{
			name:      "issue",
			subj:      issueSubject("42", "Fix the thing"),
			wantKey:   "42",
			wantTitle: "Fix the thing",
		},
		{
			name:      "chore",
			subj:      choreSubject(Chore{Name: "lint-sweep"}),
			wantKey:   dispatchkey.Chore("lint-sweep").String(),
			wantTitle: "butler: lint-sweep",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.subj.key.String(); got != c.wantKey {
				t.Errorf("key.String(): got %q, want %q", got, c.wantKey)
			}
			if got := c.subj.title; got != c.wantTitle {
				t.Errorf("title: got %q, want %q", got, c.wantTitle)
			}
		})
	}
}

// RESEARCH_VERDICTS is JSON; the Box derives the research prompt's status enum
// from it (issue #4159), so quotes and spaces must reach the Box unmangled.
func TestBuildBoxEnvForwardsResearchVerdictsJSONVerbatim(t *testing.T) {
	const verdicts = `[{"verdict":"accept","label":"l-accept","description":"a b"}]`
	t.Setenv("RESEARCH_VERDICTS", verdicts)

	cfg := Config{BoxEnvVars: "RESEARCH_VERDICTS"}
	env := mustBuildBoxEnv(t, cfg, "7", "Test issue", 0, "", "")

	if env["RESEARCH_VERDICTS"] != verdicts {
		t.Errorf("RESEARCH_VERDICTS: got %q, want %q", env["RESEARCH_VERDICTS"], verdicts)
	}
}
