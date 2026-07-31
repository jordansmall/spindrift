package dispatch

import "testing"

// TestBuildBoxEnvForwardsSchemaVars verifies that buildBoxEnv picks up env
// var names listed in Config.BoxEnvVars and that per-issue vars are always
// present.
func TestBuildBoxEnvForwardsSchemaVars(t *testing.T) {
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "tok")

	cfg := Config{BoxEnvVars: "REPO_SLUG GH_TOKEN"}
	env := buildBoxEnv(cfg, "7", "Test issue", 0, "", "")

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

// TestBuildBoxEnvUsesResolveEnv proves buildBoxEnv resolves each
// BoxEnvVars name through Config.ResolveEnv when set, instead of a raw
// os.Getenv — the seam that lets a boxEnv knob's document-baked value (ADR
// 0020: no per-var env export from the wrapper any more) still reach the
// Box even when the operator never set it as an ambient env var.
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
	env := buildBoxEnv(cfg, "7", "Test issue", 0, "", "")
	if env["MODEL"] != "from-resolver" {
		t.Errorf("MODEL: got %q, want from-resolver", env["MODEL"])
	}
}

// TestBuildBoxEnvResolveEnvReceivesIssueNumber verifies ResolveEnv is called
// with the dispatched issue's own number (issue #1734) — CODE_FORGE=local's
// per-seam BASE_BRANCH resolution needs to know which issue it's resolving
// for, since each seam may key its Integration branch off a different
// parent.
func TestBuildBoxEnvResolveEnvReceivesIssueNumber(t *testing.T) {
	var gotNum string
	cfg := Config{
		BoxEnvVars: "BASE_BRANCH",
		ResolveEnv: func(num, name string) string {
			gotNum = num
			return ""
		},
	}
	buildBoxEnv(cfg, "1734", "Test issue", 0, "", "")
	if gotNum != "1734" {
		t.Errorf("ResolveEnv num: got %q, want %q", gotNum, "1734")
	}
}

// TestBuildBoxEnvSetsFixPassAndSummary verifies FIX_PASS and
// CI_FAILURE_SUMMARY are present when fixPass>0 and summary is non-empty.
func TestBuildBoxEnvSetsFixPassAndSummary(t *testing.T) {
	env := buildBoxEnv(Config{}, "3", "T", 2, "lint failed", "")
	if env["FIX_PASS"] != "2" {
		t.Errorf("FIX_PASS: got %q, want %q", env["FIX_PASS"], "2")
	}
	if env["CI_FAILURE_SUMMARY"] != "lint failed" {
		t.Errorf("CI_FAILURE_SUMMARY: got %q, want %q", env["CI_FAILURE_SUMMARY"], "lint failed")
	}
}

// TestBuildBoxEnvSetsDispatchKind verifies DISPATCH_KIND is forwarded into
// every Box (the kind env plumbing seam, ADR 0022), defaulting to "work"
// when Config.Kind is unset so every pre-existing (kind-unaware) caller
// keeps behaving the same way.
func TestBuildBoxEnvSetsDispatchKind(t *testing.T) {
	if got := buildBoxEnv(Config{}, "3", "T", 0, "", "")["DISPATCH_KIND"]; got != "work" {
		t.Errorf("DISPATCH_KIND with unset Config.Kind: got %q, want %q", got, "work")
	}
	if got := buildBoxEnv(Config{Kind: "research"}, "3", "T", 0, "", "")["DISPATCH_KIND"]; got != "research" {
		t.Errorf("DISPATCH_KIND with Config.Kind=research: got %q, want %q", got, "research")
	}
}

// TestBuildBoxEnv_SelfContainedSetsMarker verifies buildBoxEnv forwards
// Config.SelfContained into the Box as SELF_CONTAINED=1 (issue #2202), so the
// entrypoint can skip clone_repo and select the self-contained research
// prompt.
func TestBuildBoxEnv_SelfContainedSetsMarker(t *testing.T) {
	if got := buildBoxEnv(Config{SelfContained: true}, "3", "T", 0, "", "")["SELF_CONTAINED"]; got != "1" {
		t.Errorf("SELF_CONTAINED with Config.SelfContained=true: got %q, want %q", got, "1")
	}
}

// TestBuildBoxEnv_SelfContainedAbsentByDefault verifies buildBoxEnv leaves
// SELF_CONTAINED unset (not "0", not "") when Config.SelfContained is false,
// matching every pre-#2202 construction site.
func TestBuildBoxEnv_SelfContainedAbsentByDefault(t *testing.T) {
	if _, ok := buildBoxEnv(Config{}, "3", "T", 0, "", "")["SELF_CONTAINED"]; ok {
		t.Error("SELF_CONTAINED should be absent when Config.SelfContained is false")
	}
}

// TestBuildBoxEnvSetsRunNonce verifies buildBoxEnv forwards the Dispatch's
// per-run nonce (issue #1937) into the Box as RUN_NONCE.
func TestBuildBoxEnvSetsRunNonce(t *testing.T) {
	env := buildBoxEnv(Config{}, "3", "T", 0, "", "the-nonce")
	if env["RUN_NONCE"] != "the-nonce" {
		t.Errorf("RUN_NONCE: got %q, want %q", env["RUN_NONCE"], "the-nonce")
	}
}

// TestBuildBoxEnvSetsWriteEnabledSignal verifies buildBoxEnv resolves the
// write-enabled-vs-not decision once, host-side, and forwards it as a single
// explicit positive signal (BOX_WRITE_ENABLED, issue #1951): present under
// read-write, absent under read-only, so the Box never has to re-derive it
// from a defaultable BOX_FORGE_AND_ISSUE_ACCESS string.
func TestBuildBoxEnvSetsWriteEnabledSignal(t *testing.T) {
	if _, ok := buildBoxEnv(Config{BoxForgeAndIssueAccess: "read-write"}, "3", "T", 0, "", "")["BOX_WRITE_ENABLED"]; !ok {
		t.Error("BOX_WRITE_ENABLED should be set when BoxForgeAndIssueAccess=read-write")
	}
	if _, ok := buildBoxEnv(Config{BoxForgeAndIssueAccess: "read-only"}, "3", "T", 0, "", "")["BOX_WRITE_ENABLED"]; ok {
		t.Error("BOX_WRITE_ENABLED should be absent when BoxForgeAndIssueAccess=read-only")
	}
	// The fail-closed property this signal exists for: an empty/malformed
	// Config.BoxForgeAndIssueAccess (unreachable in production once
	// validate() rejects it, but the property must hold in this function
	// itself, not depend on an upstream caller) renders no-write too.
	if _, ok := buildBoxEnv(Config{}, "3", "T", 0, "", "")["BOX_WRITE_ENABLED"]; ok {
		t.Error("BOX_WRITE_ENABLED should be absent when BoxForgeAndIssueAccess is empty/malformed")
	}
}
