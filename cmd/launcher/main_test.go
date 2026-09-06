package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/outcome"
)

// TestMainRun_NoArgs_PrintsHelpAndDoesNotDispatch verifies a bare `spindrift`
// (no subcommand) prints the concise help to stdout and exits 0, instead of
// falling through to the dispatch default (issue #555).
func TestMainRun_NoArgs_PrintsHelpAndDoesNotDispatch(t *testing.T) {
	t.Setenv("SOME_KEY", "")
	os.Unsetenv("SOME_KEY")
	withSchemaFlags(t, []flagEntry{{env: "SOME_KEY", dflt: "10"}})

	var stdout, stderr bytes.Buffer
	code := mainRun(nil, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage: spindrift [flags] <subcommand>") {
		t.Errorf("stdout missing help usage line, got:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestMainRun_UnknownSubcommand_PrintsHelpToStderrAndExits1 verifies an
// unrecognized subcommand prints help to stderr and exits 1, instead of
// falling through to the dispatch default (issue #555).
func TestMainRun_UnknownSubcommand_PrintsHelpToStderrAndExits1(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"frobnicate"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Usage: spindrift [flags] <subcommand>") {
		t.Errorf("stderr missing help usage line, got:\n%s", stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

// TestMainRun_Research_RoutesThroughBootstrap verifies the `research`
// subcommand parses like `dispatch` (bare, `<nums>`, `--no-build`, `--yes`)
// and reaches the same bootstrap/validate prologue — proven here by a
// missing REPO_SLUG surfacing the same validation error dispatch would hit,
// without needing a real runner or gh.
func TestMainRun_Research_RoutesThroughBootstrap(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	cases := [][]string{
		{"research"},
		{"research", "42"},
		{"research", "--no-build", "42"},
		{"research", "--yes", "42"},
		{"research", "--continuous", "42"},
	}
	for _, argv := range cases {
		var stdout, stderr bytes.Buffer
		code := mainRun(argv, &stdout, &stderr)
		if code != 1 {
			t.Errorf("mainRun(%v) code = %d, want 1", argv, code)
		}
		if !strings.Contains(stderr.String(), "REPO_SLUG") {
			t.Errorf("mainRun(%v) stderr = %q, want a REPO_SLUG validation error", argv, stderr.String())
		}
	}
}

// TestMainRun_Dispatch_ContinuousSetsEnv verifies the bare `--continuous`
// flag (issue #2033) sets CONTINUOUS_DISPATCH the same way
// `--continuous-dispatch 1` does, reaching loadConfig via bootstrap before
// validate fails fast on the missing REPO_SLUG. The `dispatch` verb now
// routes a config-invalid bootstrap error through bootstrapExitCode (issue
// #2568 slice 2), so the expected code is exitConfigInvalid rather than the
// generic 1.
func TestMainRun_Dispatch_ContinuousSetsEnv(t *testing.T) {
	t.Setenv("REPO_SLUG", "")
	t.Setenv("CONTINUOUS_DISPATCH", "")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"dispatch", "--continuous"}, &stdout, &stderr)
	if code != exitConfigInvalid {
		t.Errorf("mainRun(dispatch --continuous) code = %d, want %d", code, exitConfigInvalid)
	}
	if got := os.Getenv("CONTINUOUS_DISPATCH"); got != "1" {
		t.Errorf("CONTINUOUS_DISPATCH = %q, want %q", got, "1")
	}
}

// TestMainRun_Dispatch_MissingRepoSlugUnderLocalForge_ExitsConfigInvalid
// verifies the #2032 repro: CODE_FORGE=local with ISSUE_TRACKER left at its
// github default is not the fully-local exemption (repoRequirementExempt,
// internal/launcherchecks) -- that only exempts REPO_SLUG when both
// CODE_FORGE and ISSUE_TRACKER are local (or a self-contained research
// run). So REPO_SLUG stays required, validate() fails on it, and the
// `dispatch` verb (issue
// #2568 slice 2) now surfaces that as exitConfigInvalid instead of a bare 1
// -- a typo'd/missing REPO_SLUG under a local Code Forge is a config error,
// not the generic failure every other bootstrap problem produces.
func TestMainRun_Dispatch_MissingRepoSlugUnderLocalForge_ExitsConfigInvalid(t *testing.T) {
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("ISSUE_TRACKER", "github")
	t.Setenv("REPO_SLUG", "")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"dispatch"}, &stdout, &stderr)
	if code != exitConfigInvalid {
		t.Errorf("mainRun(dispatch) code = %d, want %d; stderr=%s", code, exitConfigInvalid, stderr.String())
	}
	if !strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("mainRun(dispatch) stderr = %q, want a REPO_SLUG validation error", stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (bootstrap fails before any dispatch work runs)", stdout.String())
	}
}

// TestMainRun_Research_ContinuousSetsEnv verifies `--continuous` on the
// `research` verb also sets CONTINUOUS_DISPATCH (issue #2033), mirroring
// TestMainRun_Dispatch_ContinuousSetsEnv.
func TestMainRun_Research_ContinuousSetsEnv(t *testing.T) {
	t.Setenv("REPO_SLUG", "")
	t.Setenv("CONTINUOUS_DISPATCH", "")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"research", "--continuous"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("mainRun(research --continuous) code = %d, want 1", code)
	}
	if got := os.Getenv("CONTINUOUS_DISPATCH"); got != "1" {
		t.Errorf("CONTINUOUS_DISPATCH = %q, want %q", got, "1")
	}
}

// TestDispatch_RejectsSelfContained verifies the `dispatch` verb rejects
// --self-contained (issue #2202) before reaching bootstrap — the flag is
// research-only. Asserted via the returned error text rather than a
// REPO_SLUG validation error, proving the guard fires ahead of bootstrap.
func TestDispatch_RejectsSelfContained(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"dispatch", "--self-contained"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("mainRun(dispatch --self-contained) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "self-contained") {
		t.Errorf("mainRun(dispatch --self-contained) stderr = %q, want it to mention self-contained", stderr.String())
	}
}

// TestRegistry_MissingOrUnknownSubcommand_UsageError verifies the `registry`
// verb handler's own usage branch (main.go): with no subcommand, or an
// unrecognized one, it prints the discover usage line to stderr and exits
// nonzero, never reaching cmdRegistryDiscover's own arg handling.
func TestRegistry_MissingOrUnknownSubcommand_UsageError(t *testing.T) {
	for _, args := range [][]string{
		{"registry"},
		{"registry", "bogus"},
	} {
		var stdout, stderr bytes.Buffer
		code := mainRun(args, &stdout, &stderr)
		if code != 1 {
			t.Errorf("mainRun(%v) code = %d, want 1", args, code)
		}
		if !strings.Contains(stderr.String(), "usage: spindrift registry discover <repo-dir> <routes-file> [--force]") {
			t.Errorf("mainRun(%v) stderr = %q, want the usage message", args, stderr.String())
		}
	}
}

// TestRecover_RejectsSelfContained verifies the `recover` verb rejects
// --self-contained (issue #2202) the same way dispatch does — it is
// research-only.
func TestRecover_RejectsSelfContained(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"recover", "--self-contained", "42"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("mainRun(recover --self-contained 42) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "self-contained") {
		t.Errorf("mainRun(recover --self-contained 42) stderr = %q, want it to mention self-contained", stderr.String())
	}
}

// TestMainRun_Recover_StripsFlagsBeforeIssueID verifies the `recover` verb
// routes through parseIssuePositionals (issue #3054) rather than reading
// args[0] raw. Before the fix, "recover --yes" (no numeric arg) treated the
// unstripped "--yes" itself as the issue number, so len(args) was 1 and the
// usage check never fired -- the bad ID sailed on into bootstrap and surfaced
// as a REPO_SLUG error instead. "recover --yes 42" is the mirror case: with
// stripping, the lone numeric "42" clears the usage check and reaches
// bootstrap, proving "--yes" never displaces it as the resolved issue ID.
func TestMainRun_Recover_StripsFlagsBeforeIssueID(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"recover", "--yes"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("mainRun(recover --yes) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "usage: spindrift recover <issue-number>") {
		t.Errorf("mainRun(recover --yes) stderr = %q, want the usage message (no numeric issue ID present)", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = mainRun([]string{"recover", "--yes", "42"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("mainRun(recover --yes 42) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("mainRun(recover --yes 42) stderr = %q, want a REPO_SLUG validation error (issue ID resolved as 42, past the usage check)", stderr.String())
	}
}

// TestMainRun_Recover_AcceptsNonNumericIssueID verifies recover's positional
// is never run through any numeric-only filter (issue #3054) — see
// parseIssuePositionals's doc comment (flags.go) for why. A non-numeric
// ID must clear the usage check and reach bootstrap exactly like a numeric
// one, not get silently filtered out.
func TestMainRun_Recover_AcceptsNonNumericIssueID(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"recover", "SPIN-42"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("mainRun(recover SPIN-42) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("mainRun(recover SPIN-42) stderr = %q, want a REPO_SLUG validation error (non-numeric ID must still reach bootstrap)", stderr.String())
	}
}

// TestMainRun_Preview_StripsFlagsBeforeIssueID is a basic smoke/regression
// check that `preview` reaches the same downstream REPO_SLUG bootstrap error
// regardless of where "--no-build" sits relative to the issue ID. It does
// NOT, on its own, pin down which call is doing the stripping:
// parseIssuePositionals strips "--no-build" exactly once, upstream, and its
// returned remaining is used directly as the issue-ID list with no further
// filtering (issue #3055). It also cannot prove an ID resolved at all —
// mainRun([]string{"preview"}) with zero args produces the same REPO_SLUG
// stderr error asserted on here. The strip mechanism itself is unit-tested
// directly by the TestParseIssuePositionals_* tests in flags_test.go.
//
// cmdPreview's error path writes through fmt.Fprintf(os.Stderr, ...) rather
// than the io.Writer mainRun hands its verb handlers (unlike recover's
// bootstrap path), so the assertion reads real os.Stderr via a redirected
// temp file instead of the buffer passed to mainRun.
func TestMainRun_Preview_StripsFlagsBeforeIssueID(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	cases := [][]string{
		{"preview", "42"},
		{"preview", "42", "--no-build"},
		{"preview", "--no-build", "42"},
	}
	for _, argv := range cases {
		var code int
		var stdout, stderr bytes.Buffer
		out := captureStderrFile(t, func() {
			code = mainRun(argv, &stdout, &stderr)
		})

		if code != 1 {
			t.Errorf("mainRun(%v) code = %d, want 1", argv, code)
		}
		if !strings.Contains(out, "REPO_SLUG") {
			t.Errorf("mainRun(%v) real stderr = %q, want a REPO_SLUG validation error", argv, out)
		}
	}
}

// setBootstrapReadyLocalEnv sets up a fully-local (CODE_FORGE=local,
// ISSUE_TRACKER=local) environment that clears bootstrap() end to end,
// mirroring TestBootstrap_Success_HoldsAccumLockUntilCleanup's fixture
// (bootstrap_test.go) -- needed because dispatch/research's selective-vs-
// queue routing decision (main.go) runs only after bootstrap succeeds, so a
// REPO_SLUG-style validate() failure (the shortcut every other mainRun test
// in this file uses) would short-circuit before that decision is ever
// reached and prove nothing about which path was taken. Returns the local
// issues dir so callers can seed issue files by ID via
// writeLocalReadyIssue (selective_test.go).
func setBootstrapReadyLocalEnv(t *testing.T) (issuesDir string) {
	t.Helper()
	stubExecutableOnPath(t, "pasta")
	checkout := mustSeedableCheckout(t)
	issuesDir = t.TempDir()

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", filepath.Join(t.TempDir(), "accum.git"))
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Chdir(checkout)
	return issuesDir
}

// TestMainRun_NonNumericAndMixedIssueIDs_HitSelectivePath verifies dispatch,
// research, and preview all route non-numeric IDs through their selective
// path (issue #3055 slice 2) now that no filtering of non-numeric args
// happens anywhere (slice 1):
//
//   - dispatch/research: `<nums>` routing (main.go) sends a non-empty nums
//     list to cmdDispatchSelective, research differing only in
//     dispatchKindResearch threaded through bootstrap.
//   - preview: the same shape reaches previewSelectiveList, needing only
//     newGatedContext (no runner readiness check), so it clears end-to-end
//     with the lighter fully-local fixture (setFullyLocalEnv,
//     testhelpers_test.go) plus RUNTIME=echo, as
//     TestNewGatedContext_CleanConfig_SucceedsAndPopulatesFields does.
//
// In all three, fetchSelectiveIssues (selective.go) fails fast on an unknown
// issue -- before ever touching the runner/dispatch factory -- and wraps the
// literal ID into its error text ("issue <id>: ..."), so the unresolved ID
// naming itself in stderr proves the selective path (not a full-queue drain,
// which never names a specific issue) was taken with exactly the given
// ID(s). It does not prove ordering was preserved -- fetchSelectiveIssues
// fails on the first unresolvable ID regardless of position -- ordering is
// proven separately by
// TestFetchSelectiveIssues_MixedNumericAndSlugIDs_PreservesOrder
// (selective_test.go).
func TestMainRun_NonNumericAndMixedIssueIDs_HitSelectivePath(t *testing.T) {
	verbs := []struct {
		verb          string
		setup         func(t *testing.T) (issuesDir string)
		wantMsgSuffix string
	}{
		{
			verb:          "dispatch",
			setup:         setBootstrapReadyLocalEnv,
			wantMsgSuffix: "proof the selective path was taken with this exact ID",
		},
		{
			verb:          "research",
			setup:         setBootstrapReadyLocalEnv,
			wantMsgSuffix: "proof the selective path was taken with this exact ID",
		},
		{
			verb: "preview",
			setup: func(t *testing.T) string {
				setFullyLocalEnv(t)
				t.Setenv("RUNTIME", "echo")
				return os.Getenv("LOCAL_ISSUES_DIR")
			},
			wantMsgSuffix: "proof the selective-list preview path was taken with this exact ID",
		},
	}

	cases := []struct {
		name   string
		ids    []string
		seed42 bool
	}{
		{"slug alone", []string{"SPIN-99"}, false},
		{"numeric then slug", []string{"42", "SPIN-99"}, true},
	}

	for _, v := range verbs {
		t.Run(v.verb, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					issuesDir := v.setup(t)
					if tc.seed42 {
						writeLocalReadyIssue(t, issuesDir, "42", "untriaged")
					}
					argv := append([]string{v.verb}, tc.ids...)

					var code int
					var stdout, stderr bytes.Buffer
					out := captureStderrFile(t, func() {
						code = mainRun(argv, &stdout, &stderr)
					})

					if code != 1 {
						t.Errorf("mainRun(%v) code = %d, want 1 (SPIN-99 unknown)", argv, code)
					}
					if !strings.Contains(out, "issue SPIN-99") {
						t.Errorf("mainRun(%v) stderr = %q, want it to name the unresolved slug SPIN-99 (%s)", argv, out, v.wantMsgSuffix)
					}
				})
			}
		})
	}
}

// captureStderrFile redirects os.Stderr to a fresh temp file for the
// duration of fn and returns its contents. Needed for code paths that
// write through the real os.Stderr rather than an injected io.Writer.
func captureStderrFile(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = f
	fn()
	os.Stderr = orig
	f.Close()
	out, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestMainRun_Console_RoutesThroughBootstrap verifies the `console`
// subcommand reaches the same bootstrap/validate prologue as the other
// subcommands — proven here by a missing REPO_SLUG surfacing the same
// validation error, without needing a real terminal or launcher (issue #694).
func TestMainRun_Console_RoutesThroughBootstrap(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"console"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("mainRun([console]) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("mainRun([console]) stderr = %q, want a REPO_SLUG validation error", stderr.String())
	}
}

// TestMainRun_AmbientKnobEnv_WarnsAndStillHonored is the verb-level proof of
// ADR 0020's staged deprecation: mainRun on a real subcommand (research,
// which reaches bootstrap/validate without touching a real runner or gh —
// see TestMainRun_Research_RoutesThroughBootstrap) both prints the
// provenance warning for an ambient knob env var and still resolves it into
// config, exercising the actual wiring (snapshot before parseFlags, flush
// after the bare-invocation check) rather than warnAmbientKnobEnv in
// isolation.
func TestMainRun_AmbientKnobEnv_WarnsAndStillHonored(t *testing.T) {
	t.Setenv("REPO_SLUG", "")
	t.Setenv("MAX_JOBS", "5")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"research"}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("mainRun code = %d, want 1; stderr=%s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "MAX_JOBS=5 set in environment") {
		t.Errorf("stderr = %q, want a MAX_JOBS provenance warning", out)
	}
	if !strings.Contains(out, "--max-jobs") || !strings.Contains(out, "dispatch.maxJobs") {
		t.Errorf("stderr = %q, want both the flag and domain-path migration targets named", out)
	}

	// The value is still honored this release: loadConfig() (called inside
	// bootstrap, after the warning fires) resolves MAX_JOBS=5 from the same
	// ambient env the warning just reported on.
	c := loadConfig()
	if c.maxJobs != 5 {
		t.Errorf("maxJobs = %d, want 5 (ambient env still honored)", c.maxJobs)
	}
}

// TestMainRun_NoArgs_AmbientKnobEnv_WarnsBeforeHelp verifies a bare
// `spindrift` still surfaces the ADR 0020 provenance warning when an ambient
// knob env var is set, instead of silently dropping it because the
// len(args)==0 branch (issue #555) returns before the flush (issue #814).
func TestMainRun_NoArgs_AmbientKnobEnv_WarnsBeforeHelp(t *testing.T) {
	t.Setenv("MAX_JOBS", "5")

	var stdout, stderr bytes.Buffer
	code := mainRun(nil, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage: spindrift [flags] <subcommand>") {
		t.Errorf("stdout missing help usage line, got:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "MAX_JOBS=5 set in environment") {
		t.Errorf("stderr = %q, want a MAX_JOBS provenance warning", stderr.String())
	}
}

// TestMainRun_HelpFlag_AmbientKnobEnv_WarnsBeforeHelp verifies `--help`
// (and `--help --all`) still surface the ADR 0020 provenance warning when an
// ambient knob env var is set, instead of the help branch's early return
// (main.go, before warnAmbientKnobEnv is even called) silently dropping it
// (issue #814).
func TestMainRun_HelpFlag_AmbientKnobEnv_WarnsBeforeHelp(t *testing.T) {
	t.Setenv("MAX_JOBS", "5")

	cases := [][]string{
		{"--help"},
		{"--help", "--all"},
	}
	for _, argv := range cases {
		var stdout, stderr bytes.Buffer
		code := mainRun(argv, &stdout, &stderr)
		if code != 0 {
			t.Errorf("mainRun(%v) code = %d, want 0", argv, code)
		}
		if !strings.Contains(stdout.String(), "Usage: spindrift [flags] <subcommand>") {
			t.Errorf("mainRun(%v) stdout missing help usage line, got:\n%s", argv, stdout.String())
		}
		if !strings.Contains(stderr.String(), "MAX_JOBS=5 set in environment") {
			t.Errorf("mainRun(%v) stderr = %q, want a MAX_JOBS provenance warning", argv, stderr.String())
		}
	}
}

// TestMainRun_ExtractInputFlagError_AmbientKnobEnv_StillWarns verifies a
// malformed --input flag (no value) still surfaces the ADR 0020 provenance
// warning when an ambient knob env var is set, instead of extractInputFlag's
// error return dropping it silently (issue #1191).
func TestMainRun_ExtractInputFlagError_AmbientKnobEnv_StillWarns(t *testing.T) {
	t.Setenv("MAX_JOBS", "5")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"--input"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("mainRun code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag --input requires a value") {
		t.Errorf("stderr = %q, want the extractInputFlag error", stderr.String())
	}
	if !strings.Contains(stderr.String(), "MAX_JOBS=5 set in environment") {
		t.Errorf("stderr = %q, want a MAX_JOBS provenance warning", stderr.String())
	}
}

// TestMainRun_ParseFlagsError_AmbientKnobEnv_StillWarns verifies an
// unrecognized flag still surfaces the ADR 0020 provenance warning when an
// ambient knob env var is set, instead of parseFlags's error return
// dropping it silently (issue #1191).
func TestMainRun_ParseFlagsError_AmbientKnobEnv_StillWarns(t *testing.T) {
	t.Setenv("MAX_JOBS", "5")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"--not-a-real-flag"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("mainRun code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--not-a-real-flag") {
		t.Errorf("stderr = %q, want the parseFlags error", stderr.String())
	}
	if !strings.Contains(stderr.String(), "MAX_JOBS=5 set in environment") {
		t.Errorf("stderr = %q, want a MAX_JOBS provenance warning", stderr.String())
	}
}

// TestMainRun_LoadInputDocumentError_AmbientKnobEnv_StillWarns verifies a
// --input path that fails to load still surfaces the ADR 0020 provenance
// warning when an ambient knob env var is set, instead of
// loadInputDocument's error return dropping it silently (issue #1191).
func TestMainRun_LoadInputDocumentError_AmbientKnobEnv_StillWarns(t *testing.T) {
	t.Setenv("MAX_JOBS", "5")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"--input", filepath.Join(t.TempDir(), "missing.json"), "dispatch"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("mainRun code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "MAX_JOBS=5 set in environment") {
		t.Errorf("stderr = %q, want a MAX_JOBS provenance warning", stderr.String())
	}
}

// TestMainRun_InputDocument_SeedsConfig_FlagOverridesDocument is the
// verb-level proof of ADR 0020's precedence chain: a --input document
// resolves REPO_SLUG (no env, no flag set), and an explicit --repo-slug flag
// on top of that same document wins. Both cases are observed the same way
// TestMainRun_Research_RoutesThroughBootstrap does — validate() fails on the
// *next* required field (GIT_USER_NAME) once REPO_SLUG is satisfied, proving
// resolution happened before any real gh/network call.
func TestMainRun_InputDocument_SeedsConfig_FlagOverridesDocument(t *testing.T) {
	for _, key := range []string{"REPO_SLUG", "GIT_USER_NAME", "GIT_USER_EMAIL", "GH_TOKEN"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}

	dir := t.TempDir()
	docPath := filepath.Join(dir, "input.json")
	body := `{"settings":{"REPO_SLUG":"doc-org/doc-repo"},"artifacts":{}}`
	if err := os.WriteFile(docPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loadedDoc = nil })

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"--input", docPath, "research"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("mainRun code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("stderr = %q, want REPO_SLUG resolved from the document (no REPO_SLUG complaint)", stderr.String())
	}
	if !strings.Contains(stderr.String(), "set ") {
		t.Errorf("stderr = %q, want validate() to fail on some later required field", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = mainRun([]string{"--input", docPath, "--repo-slug", "flag-org/flag-repo", "research"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("mainRun code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("stderr = %q, want REPO_SLUG resolved (flag overrides document)", stderr.String())
	}
}

// TestVerbHandlers_CoversExactlyNineRealVerbs proves the verb dispatch
// table is the single source of truth for "what subcommands actually
// exist" (issue #1574): it enumerates verbHandlers' keys and asserts they
// are exactly the nine documented subcommands, no more, no fewer. The
// hidden __complete-issues shell-completion verb is deliberately excluded
// from this table (main.go dispatches it separately, before the table
// lookup), so it must not appear here either.
func TestVerbHandlers_CoversExactlyNineRealVerbs(t *testing.T) {
	want := []string{"build", "console", "dispatch", "doctor", "preview", "reconcile", "recover", "registry", "research"}

	got := make([]string, 0, len(verbHandlers))
	for verb := range verbHandlers {
		got = append(got, verb)
	}
	sort.Strings(got)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("verbHandlers keys = %v, want %v", got, want)
	}
}

// TestSubcommandRegistry_MatchesVerbHandlers proves the generated
// subcommandRegistry (lib/subcommands.nix, issue #1575) names exactly the
// same set as verbHandlers: a verb added to one table without the other
// fails here, before it can silently drift the way console/doctor already
// had across the hand-written completion/man-page listings.
func TestSubcommandRegistry_MatchesVerbHandlers(t *testing.T) {
	verbSet := make(map[string]bool, len(verbHandlers))
	for verb := range verbHandlers {
		verbSet[verb] = true
	}

	regSet := make(map[string]bool, len(subcommandRegistry))
	for _, e := range subcommandRegistry {
		regSet[e.name] = true
	}

	for verb := range verbSet {
		if !regSet[verb] {
			t.Errorf("verbHandlers verb %q has no matching subcommandRegistry entry", verb)
		}
	}
	for name := range regSet {
		if !verbSet[name] {
			t.Errorf("subcommandRegistry entry %q has no matching verbHandlers verb", name)
		}
	}
}

// TestConfigHasNoModelFields enforces that scoutModel/reviewModel stay out of
// the config struct; those models forward via BOX_ENV_VARS instead. model
// itself is the one exception (ADR 0009 amendment, #260): validate() reads it
// to detect the opencode Driver's github-copilot Provider prefix, but it
// still must not be threaded any further than that gate.
func TestConfigHasNoModelFields(t *testing.T) {
	ct := reflect.TypeOf(config{})
	for _, name := range []string{"scoutModel", "reviewModel"} {
		if _, ok := ct.FieldByName(name); ok {
			t.Errorf("config has field %q; remove it — models forward via BOX_ENV_VARS", name)
		}
	}
}

// TestRunnerConfig_DriverMountTargets verifies DRIVER_SESSION_CACHE_DIR
// (nix-baked from the Driver declaration, ADR 0009) reaches runner.Config, so
// the OCI/bwrap adapters mount the Driver's session-cache dir at its declared
// path instead of a hardcoded ".claude" literal (issue #448).
// DRIVER_SKILLS_DIR is no longer part of this Go-side plumbing (issue
// #2489): the operator-override skills mount now always lands at the fixed
// /operator-skills staging path (see operatorSkillsDir in mount.go), and
// DRIVER_SKILLS_DIR itself is read only by entrypoint.sh's own bash-level
// copy step at box startup, not by the launcher.
func TestRunnerConfig_DriverMountTargets(t *testing.T) {
	t.Setenv("DRIVER_SESSION_CACHE_DIR", "/home/agent/.claude/projects")

	c := loadConfig()
	rc := runnerConfig(c)

	if rc.DriverSessionCacheDir != "/home/agent/.claude/projects" {
		t.Errorf("DriverSessionCacheDir = %q, want /home/agent/.claude/projects", rc.DriverSessionCacheDir)
	}
}

// TestRunnerConfig_PasswdGroupFiles verifies PASSWD_FILE/GROUP_FILE and their
// .drv companions (nix-sourced account files, issue #2663) reach
// runner.Config so the bwrap adapter can bind them instead of runner-written
// copies.
func TestRunnerConfig_PasswdGroupFiles(t *testing.T) {
	// PASSWD_FILE/GROUP_FILE are bare store-path files (pkgs.writeText
	// output), not directories containing a nested passwd/group file --
	// matches the shape nix/checks/preambles.nix asserts against.
	t.Setenv("PASSWD_FILE", "/nix/store/abc-passwd")
	t.Setenv("GROUP_FILE", "/nix/store/def-group")
	t.Setenv("PASSWD_FILE_DRV", "/nix/store/abc-passwd.drv")
	t.Setenv("GROUP_FILE_DRV", "/nix/store/def-group.drv")

	c := loadConfig()
	rc := runnerConfig(c)

	if rc.PasswdFile != "/nix/store/abc-passwd" {
		t.Errorf("PasswdFile = %q, want /nix/store/abc-passwd", rc.PasswdFile)
	}
	if rc.GroupFile != "/nix/store/def-group" {
		t.Errorf("GroupFile = %q, want /nix/store/def-group", rc.GroupFile)
	}
	if rc.PasswdFileDrv != "/nix/store/abc-passwd.drv" {
		t.Errorf("PasswdFileDrv = %q, want /nix/store/abc-passwd.drv", rc.PasswdFileDrv)
	}
	if rc.GroupFileDrv != "/nix/store/def-group.drv" {
		t.Errorf("GroupFileDrv = %q, want /nix/store/def-group.drv", rc.GroupFileDrv)
	}
}

// TestRunnerConfig_NixConfigFile verifies NIX_CONFIG_FILE/NIX_CONFIG_FILE_DRV
// (nix-in-a-Box config + store-DB snapshot plumbing, issue #2664) reach
// runner.Config so the bwrap adapter can mount the config and realize its
// snapshot closure.
func TestRunnerConfig_NixConfigFile(t *testing.T) {
	t.Setenv("NIX_CONFIG_FILE", "/nix/store/abc-nix-conf")
	t.Setenv("NIX_CONFIG_FILE_DRV", "/nix/store/abc-nix-conf.drv")

	c := loadConfig()
	rc := runnerConfig(c)

	if rc.NixConfigFile != "/nix/store/abc-nix-conf" {
		t.Errorf("NixConfigFile = %q, want /nix/store/abc-nix-conf", rc.NixConfigFile)
	}
	if rc.NixConfigFileDrv != "/nix/store/abc-nix-conf.drv" {
		t.Errorf("NixConfigFileDrv = %q, want /nix/store/abc-nix-conf.drv", rc.NixConfigFileDrv)
	}
}

// TestLoadConfig_NixStoreWritable_ReadsArtifact verifies loadConfig() reads
// the NIX_STORE_WRITABLE artifact into config.nixStoreWritable (issue #2665):
// the bwrap adapter's read-write /nix/store overlay gate.
func TestLoadConfig_NixStoreWritable_ReadsArtifact(t *testing.T) {
	t.Setenv("NIX_STORE_WRITABLE", "true")

	c := loadConfig()

	if !c.nixStoreWritable {
		t.Errorf("loadConfig().nixStoreWritable = false, want true")
	}
}

// TestLoadConfig_NixStoreWritable_DefaultsFalse verifies an unset
// NIX_STORE_WRITABLE leaves config.nixStoreWritable false, matching every
// other artifact-backed bool default in this file.
func TestLoadConfig_NixStoreWritable_DefaultsFalse(t *testing.T) {
	t.Setenv("NIX_STORE_WRITABLE", "")

	c := loadConfig()

	if c.nixStoreWritable {
		t.Errorf("loadConfig().nixStoreWritable = true, want false when NIX_STORE_WRITABLE is unset")
	}
}

// TestRunnerConfig_NixStoreWritable verifies NIX_STORE_WRITABLE reaches
// runner.Config so the bwrap adapter can decide whether to overlay
// /nix/store as a writable tmpfs layer (issue #2665, ADR 0042).
func TestRunnerConfig_NixStoreWritable(t *testing.T) {
	t.Setenv("NIX_STORE_WRITABLE", "true")

	c := loadConfig()
	rc := runnerConfig(c)

	if !rc.NixStoreWritable {
		t.Errorf("rc.NixStoreWritable = false, want true")
	}
}

// TestRunnerConfig_NixStoreWritable_DefaultsFalse verifies an unset
// NIX_STORE_WRITABLE reaches runner.Config as false, not a stray default.
func TestRunnerConfig_NixStoreWritable_DefaultsFalse(t *testing.T) {
	t.Setenv("NIX_STORE_WRITABLE", "")

	c := loadConfig()
	rc := runnerConfig(c)

	if rc.NixStoreWritable {
		t.Errorf("rc.NixStoreWritable = true, want false when NIX_STORE_WRITABLE is unset")
	}
}

// TestRunnerConfig_DriverSessionCacheDirUnset verifies that an unset
// DRIVER_SESSION_CACHE_DIR (a Driver declaring no session-state dir) reaches
// runner.Config as empty, not a fallback literal.
func TestRunnerConfig_DriverSessionCacheDirUnset(t *testing.T) {
	t.Setenv("DRIVER_SESSION_CACHE_DIR", "")

	c := loadConfig()
	rc := runnerConfig(c)

	if rc.DriverSessionCacheDir != "" {
		t.Errorf("DriverSessionCacheDir = %q, want empty when DRIVER_SESSION_CACHE_DIR is unset", rc.DriverSessionCacheDir)
	}
}

// TestRunnerConfig_IssueTrackerAndLocalIssuesDir verifies that ISSUE_TRACKER=
// local reaches runner.Config as HostMediatedIssueTracker=true and
// LOCAL_ISSUES_DIR reaches it resolved to an absolute path (issue #1691,
// ADR 0032; issue #2267): the runners render the /issues mount's Source
// directly into their bind syntax, and a relative host path there is a
// footgun the Launcher must not hand off.
func TestRunnerConfig_IssueTrackerAndLocalIssuesDir(t *testing.T) {
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", "relative-issues-dir")

	c := loadConfig()
	rc := runnerConfig(c)

	if !rc.HostMediatedIssueTracker {
		t.Errorf("HostMediatedIssueTracker = %v, want true for ISSUE_TRACKER=local", rc.HostMediatedIssueTracker)
	}
	if !filepath.IsAbs(rc.LocalIssuesDir) {
		t.Errorf("LocalIssuesDir = %q, want an absolute path", rc.LocalIssuesDir)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wd, "relative-issues-dir")
	if rc.LocalIssuesDir != want {
		t.Errorf("LocalIssuesDir = %q, want %q", rc.LocalIssuesDir, want)
	}
}

// TestLoadConfig_CodeForgeLocal_DefaultsAccumulationRepoDir verifies that
// loadConfig() itself applies absCodeForgeAccumulationRepoDir when
// CODE_FORGE=local, so every downstream reader of
// c.codeForgeAccumulationRepoDir (newCodeForge's host-side landing forge and
// runnerConfig's /repo mount source) agrees on the same resolved absolute
// path (issue #1726).
func TestLoadConfig_CodeForgeLocal_DefaultsAccumulationRepoDir(t *testing.T) {
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", "")

	c := loadConfig()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wd, ".spindrift", "accum.git")
	if c.codeForgeAccumulationRepoDir != want {
		t.Errorf("loadConfig().codeForgeAccumulationRepoDir = %q, want %q", c.codeForgeAccumulationRepoDir, want)
	}
}

// TestLoadConfig_CodeForgeGithub_AccumulationRepoDirStaysEmpty verifies the
// default is local-only: github/git forges get no Accumulation repo default
// (issue #1726 acceptance criterion: "the field stays empty/unused").
func TestLoadConfig_CodeForgeGithub_AccumulationRepoDirStaysEmpty(t *testing.T) {
	t.Setenv("CODE_FORGE", "github")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", "")

	c := loadConfig()

	if c.codeForgeAccumulationRepoDir != "" {
		t.Errorf("loadConfig().codeForgeAccumulationRepoDir = %q, want empty for CODE_FORGE=github", c.codeForgeAccumulationRepoDir)
	}
}

// TestRunnerConfig_CodeForgeLocal_MatchesNewCodeForgeAccumulationRepoDir
// verifies the /repo mount source (runnerConfig) and the host-side landing
// forge (newCodeForge) resolve to the exact same absolute Accumulation repo
// path when CODE_FORGE=local and the knob is left to default (issue #1726
// acceptance criterion: "the read-only /repo mount and the host-side
// landing forge use the same resolved path").
func TestRunnerConfig_CodeForgeLocal_MatchesNewCodeForgeAccumulationRepoDir(t *testing.T) {
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", "")

	c := loadConfig()
	rc := runnerConfig(c)
	if cf := newCodeForge(c, local.SanitizedParent{}, nil); cf == nil {
		t.Fatal("newCodeForge(CODE_FORGE=local) = nil")
	}

	if !filepath.IsAbs(rc.AccumulationRepoDir) {
		t.Fatalf("runnerConfig().AccumulationRepoDir = %q, want an absolute path", rc.AccumulationRepoDir)
	}
	if rc.AccumulationRepoDir != c.codeForgeAccumulationRepoDir {
		t.Errorf("runnerConfig().AccumulationRepoDir = %q, want %q (loadConfig's resolved value)", rc.AccumulationRepoDir, c.codeForgeAccumulationRepoDir)
	}
}

// TestAbsLocalIssuesDir_EmptyStaysEmpty verifies the empty-string guard: an
// unset LOCAL_ISSUES_DIR must reach runner.Config as "", not filepath.Abs("")
// (which resolves to the process cwd and would silently mount cwd at
// /issues once a caller ever sets ISSUE_TRACKER=local with the dir unset).
func TestAbsLocalIssuesDir_EmptyStaysEmpty(t *testing.T) {
	if got := absLocalIssuesDir(""); got != "" {
		t.Errorf("absLocalIssuesDir(\"\") = %q, want \"\"", got)
	}
}

// TestAbsCodeForgeAccumulationRepoDir_DefaultsWhenLocalAndUnset verifies that
// CODE_FORGE=local with the knob unset defaults to .spindrift/accum.git
// under the process cwd, resolved to an absolute path (issue #1726) — the
// same absolute-path requirement absLocalIssuesDir enforces for the /issues
// mount, here so the /repo mount and the host-side landing forge agree.
func TestAbsCodeForgeAccumulationRepoDir_DefaultsWhenLocalAndUnset(t *testing.T) {
	got := absCodeForgeAccumulationRepoDir("local", "")

	if !filepath.IsAbs(got) {
		t.Fatalf("absCodeForgeAccumulationRepoDir(local, \"\") = %q, want an absolute path", got)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wd, ".spindrift", "accum.git")
	if got != want {
		t.Errorf("absCodeForgeAccumulationRepoDir(local, \"\") = %q, want %q", got, want)
	}
}

// TestAbsCodeForgeAccumulationRepoDir_ExplicitOverrideResolvedAbsolute
// verifies an operator-supplied relative override still wins over the
// default and still gets resolved to an absolute path (issue #1726
// acceptance criterion: "an explicitly set value still overrides the
// default and is resolved to an absolute path").
func TestAbsCodeForgeAccumulationRepoDir_ExplicitOverrideResolvedAbsolute(t *testing.T) {
	got := absCodeForgeAccumulationRepoDir("local", "custom-accum-dir")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wd, "custom-accum-dir")
	if got != want {
		t.Errorf("absCodeForgeAccumulationRepoDir(local, %q) = %q, want %q", "custom-accum-dir", got, want)
	}
}

// TestAbsCodeForgeAccumulationRepoDir_NonLocalLeavesDirUntouched verifies
// github/git forges get no default and no resolution — the field stays
// empty/unused there (issue #1726 acceptance criterion).
func TestAbsCodeForgeAccumulationRepoDir_NonLocalLeavesDirUntouched(t *testing.T) {
	for _, cf := range []string{"github", "git", ""} {
		if got := absCodeForgeAccumulationRepoDir(cf, ""); got != "" {
			t.Errorf("absCodeForgeAccumulationRepoDir(%q, \"\") = %q, want \"\"", cf, got)
		}
	}
}

// --- newIssueTracker tests ---

// TestNewIssueTracker_Jira verifies that ISSUE_TRACKER=jira selects a tracker
// backed by the Jira REST API instead of the GitHub gh-exec adapter.
func TestNewIssueTracker_Jira(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"accountId":"abc"}`))
	}))
	defer srv.Close()

	c := minimalValidConfig()
	c.issueTracker = "jira"
	c.jiraBaseURL = srv.URL
	c.jiraProjectKey = "PROJ"
	c.jiraToken = "tok"

	it := newIssueTracker(c)
	slug, err := it.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if slug != "PROJ" {
		t.Errorf("Probe() = %q, want the Jira adapter (PROJ)", slug)
	}
}

// TestNewIssueTracker_Forgejo verifies that ISSUE_TRACKER=forgejo selects a
// tracker backed by the Forgejo/Gitea REST API instead of the GitHub
// gh-exec adapter.
func TestNewIssueTracker_Forgejo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"full_name":"owner/repo"}`))
	}))
	defer srv.Close()

	c := minimalValidConfig()
	c.issueTracker = "forgejo"
	c.forgejoBaseURL = srv.URL
	c.forgejoToken = "tok"
	c.repoSlug = "owner/repo"

	it := newIssueTracker(c)
	slug, err := it.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if slug != "owner/repo" {
		t.Errorf("Probe() = %q, want the Forgejo adapter (owner/repo)", slug)
	}
}

// --- dispatch kind tests (ADR 0022) ---

// TestApplyDispatchKind_Research_SetsResearchLabelFamily verifies that the
// research kind overrides the four lifecycle label fields to the fixed
// research family, leaving completeLabel blank since research's Complete
// transition carries a verdict instead of a single label.
func TestApplyDispatchKind_Research_SetsResearchLabelFamily(t *testing.T) {
	c := applyDispatchKind(minimalValidConfig(), dispatchKindResearch)
	rl := forge.ResearchDispatchLabels()

	if c.dispatchKind != dispatchKindResearch {
		t.Errorf("dispatchKind = %q, want %q", c.dispatchKind, dispatchKindResearch)
	}
	if c.label != rl.Dispatchable {
		t.Errorf("label = %q, want %q", c.label, rl.Dispatchable)
	}
	if c.inProgressLabel != rl.InProgress {
		t.Errorf("inProgressLabel = %q, want %q", c.inProgressLabel, rl.InProgress)
	}
	if c.failedLabel != rl.Failed {
		t.Errorf("failedLabel = %q, want %q", c.failedLabel, rl.Failed)
	}
	if c.completeLabel != "" {
		t.Errorf("completeLabel = %q, want empty (verdict carries Complete instead)", c.completeLabel)
	}
}

// TestApplyDispatchKind_Work_LeavesConfiguredLabelsAlone verifies the work
// kind is a no-op on the label fields: the operator-configurable
// LABEL/*_LABEL knobs are untouched.
func TestApplyDispatchKind_Work_LeavesConfiguredLabelsAlone(t *testing.T) {
	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.completeLabel, c.failedLabel = "custom-ready", "custom-wip", "custom-done", "custom-broken"

	got := applyDispatchKind(c, dispatchKindWork)

	if got.dispatchKind != dispatchKindWork {
		t.Errorf("dispatchKind = %q, want %q", got.dispatchKind, dispatchKindWork)
	}
	if got.label != "custom-ready" || got.inProgressLabel != "custom-wip" || got.completeLabel != "custom-done" || got.failedLabel != "custom-broken" {
		t.Errorf("applyDispatchKind(work) mutated configured labels: %+v", got)
	}
}

// TestApplyDispatchKind_ValueEmbed_DoesNotAliasOriginal verifies that
// config's by-value embed of schemaConfig means applyDispatchKind's
// copy-and-mutate (main.go) mutates only the returned copy — the caller's
// original config, including its nested schemaConfig, is left untouched.
// A pointer embed would let the label swap alias and corrupt the caller's
// struct; this test pins the value-embed guarantee explicitly.
func TestApplyDispatchKind_ValueEmbed_DoesNotAliasOriginal(t *testing.T) {
	orig := config{schemaConfig: schemaConfig{
		label:           "orig-label",
		inProgressLabel: "orig-in-progress",
		completeLabel:   "orig-complete",
		failedLabel:     "orig-failed",
	}}
	origCopy := orig

	got := applyDispatchKind(orig, dispatchKindResearch)
	rl := forge.ResearchDispatchLabels()

	if !reflect.DeepEqual(orig, origCopy) {
		t.Errorf("applyDispatchKind mutated the caller's original config: got %+v, want unchanged %+v", orig, origCopy)
	}
	if got.label != rl.Dispatchable || got.inProgressLabel != rl.InProgress || got.failedLabel != rl.Failed || got.completeLabel != "" {
		t.Errorf("returned copy does not carry the research label family: %+v", got)
	}
}

// TestNewIssueTracker_ResearchKind_WiresVerdictLabels verifies that a
// research-kind config's IssueTracker actually resolves verdict labels
// (CompleteVerdict), while a work-kind config's does not — the kind-aware
// seam ADR 0022 describes, exercised end-to-end through the local adapter
// since its state field is trivially observable from disk.
func TestNewIssueTracker_ResearchKind_WiresVerdictLabels(t *testing.T) {
	dir := t.TempDir()
	issueFile := `---
title: Some issue
state: agent-research-in-progress
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(dir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	c := minimalValidConfig()
	c.issueTracker = "local"
	c.localIssuesDir = dir
	c = applyDispatchKind(c, dispatchKindResearch)

	it := newIssueTracker(c)
	if err := it.CompleteVerdict("42", forge.Recommend); err != nil {
		t.Fatalf("CompleteVerdict: %v", err)
	}
	iss, err := it.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !containsLabel(iss.Labels, "agent-research-recommend") {
		t.Errorf("issue labels = %v, want agent-research-recommend", iss.Labels)
	}
}

// TestNewIssueTracker_ResearchKind_WiresCustomVerdictLabels mirrors
// TestNewIssueTracker_ResearchKind_WiresVerdictLabels but sets a custom
// RESEARCH_VERDICTS override (c.researchVerdicts, the field
// researchVerdictLabels(c) parses) end-to-end through newIssueTracker: a
// custom "approve" verdict applies the configured "agent-research-approve"
// label instead of any compiled-default label (ADR 0022, issue #2201).
func TestNewIssueTracker_ResearchKind_WiresCustomVerdictLabels(t *testing.T) {
	dir := t.TempDir()
	issueFile := `---
title: Some issue
state: agent-research-in-progress
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(dir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	c := minimalValidConfig()
	c.issueTracker = "local"
	c.localIssuesDir = dir
	c = applyDispatchKind(c, dispatchKindResearch)
	c.researchVerdicts = `[{"verdict":"approve","label":"agent-research-approve","description":"looks good"}]`

	it := newIssueTracker(c)
	if err := it.CompleteVerdict("42", forge.Verdict("approve")); err != nil {
		t.Fatalf("CompleteVerdict: %v", err)
	}
	iss, err := it.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !containsLabel(iss.Labels, "agent-research-approve") {
		t.Errorf("issue labels = %v, want agent-research-approve", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-research-recommend") {
		t.Errorf("issue labels = %v, must not contain the compiled-default label", iss.Labels)
	}
}

// --- integer-knob parsing tests ---

// TestMaxParallelEdgeCases covers the atoi() fallback for values where zero
// would deadlock the semaphore: 0, negative, and non-numeric all fall back to
// the compiled default (3).
func TestMaxParallelEdgeCases(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"0", 3},   // zero → deadlock guard, must fall back
		{"-1", 3},  // negative → fall back
		{"-99", 3}, // large negative → fall back
		{"abc", 3}, // non-numeric → fall back
		{"", 3},    // unset → fall back to default
		{"1", 1},   // valid positive → use as-is
		{"10", 10}, // larger valid value → use as-is
	}
	for _, tc := range cases {
		t.Setenv("MAX_PARALLEL", tc.env)
		c := loadConfig()
		if c.maxParallel != tc.want {
			t.Errorf("MAX_PARALLEL=%q: got %d, want %d", tc.env, c.maxParallel, tc.want)
		}
	}
}

// TestMaxJobsEdgeCases covers the atoiNonneg() fallback: zero is valid
// (meaning unlimited), negatives fall back to default (0).
func TestMaxJobsEdgeCases(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"0", 0},   // zero is valid (unlimited)
		{"-1", 0},  // negative → fall back to default
		{"abc", 0}, // non-numeric → fall back to default
		{"", 0},    // unset → fall back to default
		{"5", 5},   // valid positive → use as-is
	}
	for _, tc := range cases {
		t.Setenv("MAX_JOBS", tc.env)
		c := loadConfig()
		if c.maxJobs != tc.want {
			t.Errorf("MAX_JOBS=%q: got %d, want %d", tc.env, c.maxJobs, tc.want)
		}
	}
}

// TestLoadConfig_LabelDefaultComesFromSchemaTable proves loadConfig() sources
// LABEL's default from the generated schemaFlags table (issue #670 consolidates
// the former separate schemaDefaults table into it) rather than a hand-written
// literal: swapping the table's entry changes what an unset LABEL resolves to.
func TestLoadConfig_LabelDefaultComesFromSchemaTable(t *testing.T) {
	// Force LABEL absent for the test but restore its pre-test value
	// (including "was unset") on cleanup.
	t.Setenv("LABEL", "")
	os.Unsetenv("LABEL")

	patched := append([]flagEntry(nil), schemaFlags...)
	for i := range patched {
		if patched[i].env == "LABEL" {
			patched[i].dflt = "custom-default-from-table"
			break
		}
	}
	withSchemaFlags(t, patched)

	c := loadConfig()
	if c.label != "custom-default-from-table" {
		t.Errorf("label should come from schemaFlags table, got %q", c.label)
	}
}

// TestLoadConfig_SpindriftDirsDefaultComesFromSchemaTable proves loadConfig()
// sources spindriftPromptDir/spindriftSkillsDir defaults from the generated
// schemaFlags table (issue #812) rather than raw os.Getenv, matching every
// other flakeOption-adjacent knob in loadConfig().
func TestLoadConfig_SpindriftDirsDefaultComesFromSchemaTable(t *testing.T) {
	// Force each key absent for the test but restore its pre-test value
	// (including "was unset") on cleanup.
	t.Setenv("SPINDRIFT_PROMPT_DIR", "")
	os.Unsetenv("SPINDRIFT_PROMPT_DIR")
	t.Setenv("SPINDRIFT_SKILLS_DIR", "")
	os.Unsetenv("SPINDRIFT_SKILLS_DIR")

	withSchemaFlags(t, []flagEntry{
		{env: "SPINDRIFT_PROMPT_DIR", dflt: "custom-prompt-default"},
		{env: "SPINDRIFT_SKILLS_DIR", dflt: "custom-skills-default"},
	})

	c := loadConfig()
	if c.spindriftPromptDir != "custom-prompt-default" {
		t.Errorf("spindriftPromptDir should come from schemaFlags table, got %q", c.spindriftPromptDir)
	}
	if c.spindriftSkillsDir != "custom-skills-default" {
		t.Errorf("spindriftSkillsDir should come from schemaFlags table, got %q", c.spindriftSkillsDir)
	}
}

// TestLoadConfig_SpindriftDirsEnvBeatsSchemaTable proves a set
// SPINDRIFT_PROMPT_DIR/SPINDRIFT_SKILLS_DIR env var still wins over the
// schemaFlags table default, completing the precedence coverage the sibling
// default-only test above leaves unexercised (issue #1180).
func TestLoadConfig_SpindriftDirsEnvBeatsSchemaTable(t *testing.T) {
	t.Setenv("SPINDRIFT_PROMPT_DIR", "from-env-prompt")
	t.Setenv("SPINDRIFT_SKILLS_DIR", "from-env-skills")

	withSchemaFlags(t, []flagEntry{
		{env: "SPINDRIFT_PROMPT_DIR", dflt: "custom-prompt-default"},
		{env: "SPINDRIFT_SKILLS_DIR", dflt: "custom-skills-default"},
	})

	c := loadConfig()
	if c.spindriftPromptDir != "from-env-prompt" {
		t.Errorf("spindriftPromptDir = %q, want from-env-prompt", c.spindriftPromptDir)
	}
	if c.spindriftSkillsDir != "from-env-skills" {
		t.Errorf("spindriftSkillsDir = %q, want from-env-skills", c.spindriftSkillsDir)
	}
}

// TestLoadConfig_SpindriftDirsEnvBeatsSchemaTable_Mixed proves the two knobs
// resolve independently: setting only SPINDRIFT_PROMPT_DIR still lets
// SPINDRIFT_SKILLS_DIR fall back to its schema default, and vice versa.
func TestLoadConfig_SpindriftDirsEnvBeatsSchemaTable_Mixed(t *testing.T) {
	t.Setenv("SPINDRIFT_PROMPT_DIR", "from-env-prompt")
	t.Setenv("SPINDRIFT_SKILLS_DIR", "")
	os.Unsetenv("SPINDRIFT_SKILLS_DIR")

	withSchemaFlags(t, []flagEntry{
		{env: "SPINDRIFT_PROMPT_DIR", dflt: "custom-prompt-default"},
		{env: "SPINDRIFT_SKILLS_DIR", dflt: "custom-skills-default"},
	})

	c := loadConfig()
	if c.spindriftPromptDir != "from-env-prompt" {
		t.Errorf("spindriftPromptDir = %q, want from-env-prompt", c.spindriftPromptDir)
	}
	if c.spindriftSkillsDir != "custom-skills-default" {
		t.Errorf("spindriftSkillsDir = %q, want custom-skills-default", c.spindriftSkillsDir)
	}
}

// TestLoadConfig_EmptyDisablesLimit proves loadConfig() routes PIDS_LIMIT
// and MEMORY_LIMIT through the schema's emptyDisables loader
// (getenvSchemaPreserveEmpty, not getenvSchema): an unset env var still
// falls back to the schema default, but an explicit KEY="" override
// resolves to "" instead of collapsing into that default -- the contract
// getenvSchema deliberately lacks (issue #3048).
func TestLoadConfig_EmptyDisablesLimit(t *testing.T) {
	cases := []struct {
		name string
		env  string
		// useRealTable true means leave the ambient package-level
		// schemaFlags (the real generated flagtable_gen.go table) in
		// place instead of stubbing it via withSchemaFlags -- want below
		// is still the acceptance criterion's literal (issue #3048: "512"
		// / "5g"), but now checked against what loadConfig() actually
		// resolves from the live schema, so a drifted flagtable_gen.go
		// default fails this test instead of silently matching a
		// disconnected stub. The other cases assert override behavior
		// that's independent of the actual default value, so stubbing is
		// fine there and keeps them decoupled from the real table's
		// contents.
		useRealTable bool
		setEnv       bool
		envVal       string
		dflt         string // only used when useRealTable is false
		want         string
		getGot       func(c config) string
	}{
		{name: "pidsLimit unset falls back to schema default", env: "PIDS_LIMIT", useRealTable: true, setEnv: false, want: "512", getGot: func(c config) string { return c.pidsLimit }},
		{name: "pidsLimit empty override disables the limit", env: "PIDS_LIMIT", setEnv: true, envVal: "", dflt: "512", want: "", getGot: func(c config) string { return c.pidsLimit }},
		{name: "pidsLimit non-empty override passes through verbatim", env: "PIDS_LIMIT", setEnv: true, envVal: "4096", dflt: "512", want: "4096", getGot: func(c config) string { return c.pidsLimit }},
		{name: "memoryLimit unset falls back to schema default", env: "MEMORY_LIMIT", useRealTable: true, setEnv: false, want: "5g", getGot: func(c config) string { return c.memoryLimit }},
		{name: "memoryLimit empty override disables the limit", env: "MEMORY_LIMIT", setEnv: true, envVal: "", dflt: "5g", want: "", getGot: func(c config) string { return c.memoryLimit }},
		{name: "memoryLimit non-empty override passes through verbatim", env: "MEMORY_LIMIT", setEnv: true, envVal: "8g", dflt: "5g", want: "8g", getGot: func(c config) string { return c.memoryLimit }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv(tc.env, tc.envVal)
			} else {
				t.Setenv(tc.env, "")
				os.Unsetenv(tc.env)
			}

			if !tc.useRealTable {
				withSchemaFlags(t, []flagEntry{{env: tc.env, dflt: tc.dflt}})
			}
			// useRealTable cases call no withSchemaFlags at all: the
			// package-level schemaFlags is whatever the real
			// flagtable_gen.go loaded it as (previous subtests' stubs are
			// already restored by their own t.Cleanup, per
			// TestWithSchemaFlags_SwapsAndRestores above).

			got := tc.getGot(loadConfig())
			if got != tc.want {
				t.Errorf("%s = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

// TestIntSchemaDefault covers intSchemaDefault directly: a numeric schema
// default parses, a non-numeric one falls back to 0, and an absent key falls
// back to 0 too (issue #672).
func TestIntSchemaDefault(t *testing.T) {
	// nil is a placeholder: every case below reassigns schemaFlags before
	// reading it, so the initial value here is never observed.
	withSchemaFlags(t, nil)

	cases := []struct {
		name string
		dflt string
		want int
	}{
		{"numeric default", "42", 42},
		{"non-numeric default", "abc", 0},
	}
	for _, tc := range cases {
		schemaFlags = []flagEntry{{env: "SOME_KEY", dflt: tc.dflt}}
		if got := intSchemaDefault("SOME_KEY"); got != tc.want {
			t.Errorf("%s: intSchemaDefault(SOME_KEY) = %d, want %d", tc.name, got, tc.want)
		}
	}

	schemaFlags = []flagEntry{}
	if got := intSchemaDefault("ABSENT_KEY"); got != 0 {
		t.Errorf("absent key: intSchemaDefault(ABSENT_KEY) = %d, want 0", got)
	}
}

// TestGetenvSchemaPreserveEmpty covers getenvSchemaPreserveEmpty directly: a
// genuinely unset env var falls back to the schema default, same as
// getenvSchema; an env var explicitly set to the empty string returns ""
// verbatim rather than falling back to the schema default -- the contract
// getenvSchema deliberately lacks; and an env var set to a non-empty value
// passes through verbatim (issue #3048).
func TestGetenvSchemaPreserveEmpty(t *testing.T) {
	cases := []struct {
		name   string
		setEnv bool // false: leave env genuinely unset
		envVal string
		want   string
	}{
		{name: "unset falls back to schema default", setEnv: false, want: "2048"},
		{name: "set empty returns empty string verbatim", setEnv: true, envVal: "", want: ""},
		{name: "set non-empty passes through verbatim", setEnv: true, envVal: "4096", want: "4096"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv("PIDS_LIMIT", tc.envVal)
			} else {
				t.Setenv("PIDS_LIMIT", "")
				os.Unsetenv("PIDS_LIMIT")
			}

			withSchemaFlags(t, []flagEntry{{env: "PIDS_LIMIT", dflt: "2048"}})

			if got := getenvSchemaPreserveEmpty("PIDS_LIMIT"); got != tc.want {
				t.Errorf("getenvSchemaPreserveEmpty(PIDS_LIMIT) = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAtoiSchema covers atoiSchema directly: a valid positive env value wins
// over the schema default; zero, negative, non-numeric, and unset env all
// fall back to the schema default (issue #672).
func TestAtoiSchema(t *testing.T) {
	withSchemaFlags(t, []flagEntry{{env: "SOME_KEY", dflt: "10"}})

	cases := []struct {
		env  string
		want int
	}{
		{"5", 5},
		{"0", 10},
		{"-1", 10},
		{"abc", 10},
		{"", 10},
	}
	for _, tc := range cases {
		t.Setenv("SOME_KEY", tc.env)
		if got := atoiSchema("SOME_KEY"); got != tc.want {
			t.Errorf("SOME_KEY=%q: atoiSchema(SOME_KEY) = %d, want %d", tc.env, got, tc.want)
		}
	}
}

// TestAtoiNonnegSchema covers atoiNonnegSchema directly: zero and positive env
// values win over the schema default; negative, non-numeric, and unset env
// all fall back to the schema default (issue #672).
func TestAtoiNonnegSchema(t *testing.T) {
	withSchemaFlags(t, []flagEntry{{env: "SOME_KEY", dflt: "0"}})

	cases := []struct {
		env  string
		want int
	}{
		{"0", 0},
		{"5", 5},
		{"-1", 0},
		{"abc", 0},
		{"", 0},
	}
	for _, tc := range cases {
		t.Setenv("SOME_KEY", tc.env)
		if got := atoiNonnegSchema("SOME_KEY"); got != tc.want {
			t.Errorf("SOME_KEY=%q: atoiNonnegSchema(SOME_KEY) = %d, want %d", tc.env, got, tc.want)
		}
	}
}

// TestGitIdentityField_FallsBackToHostGitConfig proves GIT_USER_NAME/
// GIT_USER_EMAIL fall back to the host git config when the document/flag/env
// chain supplies nothing — the in-process replacement for the wrapper's
// retired `${VAR:-$(git config ...)}` bash fallback (ADR 0020).
func TestGitIdentityField_FallsBackToHostGitConfig(t *testing.T) {
	t.Setenv("GIT_USER_NAME", "")
	os.Unsetenv("GIT_USER_NAME")
	orig := gitConfigLookup
	t.Cleanup(func() { gitConfigLookup = orig })
	gitConfigLookup = func(key string) string {
		if key == "user.name" {
			return "Host Git User"
		}
		return ""
	}

	if got := gitIdentityField("GIT_USER_NAME", "user.name"); got != "Host Git User" {
		t.Errorf("gitIdentityField = %q, want Host Git User", got)
	}
}

// TestGitIdentityField_ExplicitValueSkipsGitConfig proves an explicit
// value (document/flag/env) wins over the host git config fallback.
func TestGitIdentityField_ExplicitValueSkipsGitConfig(t *testing.T) {
	t.Setenv("GIT_USER_NAME", "Explicit Name")
	orig := gitConfigLookup
	t.Cleanup(func() { gitConfigLookup = orig })
	gitConfigLookup = func(string) string {
		t.Fatal("gitConfigLookup should not be called when an explicit value is set")
		return ""
	}

	if got := gitIdentityField("GIT_USER_NAME", "user.name"); got != "Explicit Name" {
		t.Errorf("gitIdentityField = %q, want Explicit Name", got)
	}
}

// TestLoadConfig_DocumentSettingBeatsSchemaDefault proves the Launcher input
// document's settings value (ADR 0020: schema default < flake settings)
// backs a knob ahead of the generated schemaFlags table when neither an
// explicit flag nor ambient env supplies one.
func TestLoadConfig_DocumentSettingBeatsSchemaDefault(t *testing.T) {
	t.Setenv("BASE_BRANCH", "")
	os.Unsetenv("BASE_BRANCH")
	t.Cleanup(func() { loadedDoc = nil })

	loadedDoc = &inputDocument{Settings: map[string]string{"BASE_BRANCH": "from-document"}}

	c := loadConfig()
	if c.baseBranch != "from-document" {
		t.Errorf("baseBranch = %q, want from-document", c.baseBranch)
	}
}

// TestLoadConfig_EnvBeatsDocument proves env (ambient or flag-set — the two
// are indistinguishable at loadConfig()'s layer, ADR 0020 stage 1: an
// ambient knob env var still wins this release, just with a deprecation
// warning printed elsewhere) still overrides the document's settings value.
func TestLoadConfig_EnvBeatsDocument(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })

	loadedDoc = &inputDocument{Settings: map[string]string{"BASE_BRANCH": "from-document"}}
	t.Setenv("BASE_BRANCH", "from-env")

	c := loadConfig()
	if c.baseBranch != "from-env" {
		t.Errorf("baseBranch = %q, want from-env", c.baseBranch)
	}
}

// TestLoadConfig_PromptDirDocumentSettingBeatsSchemaDefault proves the
// Launcher input document's settings value backs spindriftPromptDir ahead of
// the generated schemaFlags table when no ambient env var supplies one —
// the prompt-dir specialization of TestLoadConfig_DocumentSettingBeatsSchemaDefault
// (issue #2200).
func TestLoadConfig_PromptDirDocumentSettingBeatsSchemaDefault(t *testing.T) {
	t.Setenv("SPINDRIFT_PROMPT_DIR", "")
	os.Unsetenv("SPINDRIFT_PROMPT_DIR")
	t.Cleanup(func() { loadedDoc = nil })

	loadedDoc = &inputDocument{Settings: map[string]string{"SPINDRIFT_PROMPT_DIR": "from-document-prompt"}}

	c := loadConfig()
	if c.spindriftPromptDir != "from-document-prompt" {
		t.Errorf("spindriftPromptDir = %q, want from-document-prompt", c.spindriftPromptDir)
	}
}

// TestLoadConfig_PromptDirEnvBeatsDocument proves an ambient
// SPINDRIFT_PROMPT_DIR env var still overrides the document's settings
// value — the prompt-dir specialization of TestLoadConfig_EnvBeatsDocument
// (issue #2200).
func TestLoadConfig_PromptDirEnvBeatsDocument(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })

	loadedDoc = &inputDocument{Settings: map[string]string{"SPINDRIFT_PROMPT_DIR": "from-document-prompt"}}
	t.Setenv("SPINDRIFT_PROMPT_DIR", "from-env-prompt")

	c := loadConfig()
	if c.spindriftPromptDir != "from-env-prompt" {
		t.Errorf("spindriftPromptDir = %q, want from-env-prompt", c.spindriftPromptDir)
	}
}

// TestLoadConfig_ArtifactsFromDocument proves the nix-computed artifact
// fields (image refs, driver name, ...) resolve from the loaded document's
// artifacts section when no env var supplies them — the replacement for the
// retired goRunPreamble/goBuildPreamble env exports (ADR 0020).
func TestLoadConfig_ArtifactsFromDocument(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	// Force each key absent for the test but restore its pre-test value
	// (including "was unset") on cleanup.
	for _, k := range []string{"IMAGE_ARCHIVE", "RUNTIME", "DRIVER", "BOX_ENV_VARS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	loadedDoc = &inputDocument{Artifacts: map[string]string{
		"IMAGE_ARCHIVE": "/nix/store/doc-image",
		"RUNTIME":       "podman",
		"DRIVER":        "claude",
		"BOX_ENV_VARS":  "MODEL BASE_BRANCH",
	}}

	c := loadConfig()
	if c.imageArchive != "/nix/store/doc-image" {
		t.Errorf("imageArchive = %q, want /nix/store/doc-image", c.imageArchive)
	}
	if c.runtime != "podman" {
		t.Errorf("runtime = %q, want podman", c.runtime)
	}
	if c.driver != "claude" {
		t.Errorf("driver = %q, want claude", c.driver)
	}
	if c.boxEnvVars != "MODEL BASE_BRANCH" {
		t.Errorf("boxEnvVars = %q, want %q", c.boxEnvVars, "MODEL BASE_BRANCH")
	}
}

// TestValidate_RepoSlugRequired verifies that validate() fails when REPO_SLUG
// is empty, confirming the required-validation contract is not masked by any
// settings-baked preamble default (which bakes an empty ${REPO_SLUG:-}).
func TestValidate_RepoSlugRequired(t *testing.T) {
	c := minimalValidConfig()
	c.repoSlug = ""
	err := validate(c)
	if err == nil {
		t.Fatal("validate() must require REPO_SLUG when empty")
	}
	if !strings.Contains(err.Error(), "REPO_SLUG") {
		t.Errorf("error should mention REPO_SLUG, got: %v", err)
	}
}

// TestResolveCapabilitySignals_NoDocumentFallsBackToRegistry verifies that
// with no loaded document (direct binary invocation — tests, manual
// debugging), resolveCapabilitySignals always derives the signals fresh
// from the backend registry rather than trusting a forwarded artifact that
// was never populated (issue #2527 review: getenvArtifact("FULLY_LOCAL", "")
// is always "" with no document, never reflecting a true fully-local pairing).
func TestResolveCapabilitySignals_NoDocumentFallsBackToRegistry(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil

	sig := resolveCapabilitySignals("local", "local")
	if !sig.fullyLocal {
		t.Errorf("fullyLocal = false, want true for local/local with no document")
	}
	if !sig.hostMediatedRemote {
		t.Errorf("hostMediatedRemote = false, want true for codeForge=local")
	}
	if !sig.inBoxUnreachableTracker {
		t.Errorf("inBoxUnreachableTracker = false, want true for issueTracker=local")
	}
}

// TestResolveCapabilitySignals_MatchingDocumentTrustsForwardedArtifact
// verifies that when the resolved CODE_FORGE/ISSUE_TRACKER pairing matches
// what was baked into the document's settings section (no override
// happened), resolveCapabilitySignals trusts the nix-forwarded artifact
// bools directly instead of re-deriving them.
func TestResolveCapabilitySignals_MatchingDocumentTrustsForwardedArtifact(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "github"},
		Artifacts: map[string]string{
			"HOST_MEDIATED_REMOTE":       "false",
			"OUTBOX_RELAY_CAPABLE":       "true",
			"IN_BOX_UNREACHABLE_TRACKER": "false",
			"FULLY_LOCAL":                "false",
		},
	}

	sig := resolveCapabilitySignals("github", "github")
	if sig.hostMediatedRemote {
		t.Errorf("hostMediatedRemote = true, want false (forwarded artifact)")
	}
	if !sig.outboxRelayCapable {
		t.Errorf("outboxRelayCapable = false, want true (forwarded artifact)")
	}
	if sig.inBoxUnreachableTracker {
		t.Errorf("inBoxUnreachableTracker = true, want false (forwarded artifact)")
	}
	if sig.fullyLocal {
		t.Errorf("fullyLocal = true, want false (forwarded artifact)")
	}
}

// TestResolveCapabilitySignals_OverrideAwayFromBakedDocumentFallsBack
// verifies that when the pairing actually in effect this run diverges from
// what was baked into the document (a CLI flag or env override), the
// forwarded artifact is NOT trusted -- resolveCapabilitySignals falls back
// to a fresh registry lookup on the resolved names instead (issue #2527
// review: a github-baked document run with --forge-backend local
// --tracker local must not keep reading the baked FULLY_LOCAL=false).
func TestResolveCapabilitySignals_OverrideAwayFromBakedDocumentFallsBack(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings:  map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "github"},
		Artifacts: map[string]string{"FULLY_LOCAL": "false"},
	}

	sig := resolveCapabilitySignals("local", "local")
	if !sig.fullyLocal {
		t.Errorf("fullyLocal = false, want true (override to local/local ignores stale github-baked artifact)")
	}
}

// TestResolveCapabilitySignals_MatchingDocumentIgnoresAmbientEnvOverride
// verifies that in the matching-document branch, the four capability-signal
// keys are read strictly from the document's Artifacts section, never from
// os.Getenv -- unlike getenvArtifact's other callers, these four are
// nix-resolved policy, not operator knobs, so a stray ambient env var (e.g.
// FULLY_LOCAL=true left over in a shell or CI environment) must not override
// what nix actually baked into the document (issue #2527 review).
func TestResolveCapabilitySignals_MatchingDocumentIgnoresAmbientEnvOverride(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "github"},
		Artifacts: map[string]string{
			"HOST_MEDIATED_REMOTE":       "false",
			"OUTBOX_RELAY_CAPABLE":       "false",
			"IN_BOX_UNREACHABLE_TRACKER": "false",
			"FULLY_LOCAL":                "false",
		},
	}
	t.Setenv("HOST_MEDIATED_REMOTE", "true")
	t.Setenv("OUTBOX_RELAY_CAPABLE", "true")
	t.Setenv("IN_BOX_UNREACHABLE_TRACKER", "true")
	t.Setenv("FULLY_LOCAL", "true")

	sig := resolveCapabilitySignals("github", "github")
	if sig.hostMediatedRemote {
		t.Errorf("hostMediatedRemote = true, want false (ambient env must not override the document)")
	}
	if sig.outboxRelayCapable {
		t.Errorf("outboxRelayCapable = true, want false (ambient env must not override the document)")
	}
	if sig.inBoxUnreachableTracker {
		t.Errorf("inBoxUnreachableTracker = true, want false (ambient env must not override the document)")
	}
	if sig.fullyLocal {
		t.Errorf("fullyLocal = true, want false (ambient env must not override the document)")
	}
}

// TestResolveCapabilitySignals_MatchingDocumentMissingArtifactKeysFallsBack
// verifies that when the matching document's Artifacts section carries none
// of the four capability-signal keys at all (an old/malformed document that
// predates this feature, or a nix rendering bug), resolveCapabilitySignals
// does not trust an all-false answer -- it falls through to the
// registry-derived fallback instead. A local/local document missing these
// keys must still resolve fullyLocal=true (both registry rows are true for
// local), never the wrong all-false reading validate() would otherwise use
// to wrongly demand REPO_SLUG/GH_TOKEN (issue #2527 review).
func TestResolveCapabilitySignals_MatchingDocumentMissingArtifactKeysFallsBack(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings:  map[string]string{"CODE_FORGE": "local", "ISSUE_TRACKER": "local"},
		Artifacts: map[string]string{"RUNTIME": "podman"},
	}

	sig := resolveCapabilitySignals("local", "local")
	if !sig.fullyLocal {
		t.Errorf("fullyLocal = false, want true (missing artifact keys must fall back to registry derivation)")
	}
	if !sig.hostMediatedRemote {
		t.Errorf("hostMediatedRemote = false, want true (missing artifact keys must fall back to registry derivation)")
	}
	if !sig.inBoxUnreachableTracker {
		t.Errorf("inBoxUnreachableTracker = false, want true (missing artifact keys must fall back to registry derivation)")
	}
}

// TestResolveCapabilitySignals_MatchingDocumentPartialArtifactKeysFallsBack
// verifies that when the matching document's Artifacts section carries only
// SOME of the four capability-signal keys (a partial/malformed render, e.g.
// 3 of 4), resolveCapabilitySignals does not trust the partial set -- it
// falls through to the registry-derived fallback for ALL FOUR signals, the
// same as the zero-keys-present case
// (TestResolveCapabilitySignals_MatchingDocumentMissingArtifactKeysFallsBack
// above). Presence must be checked with AND across all four keys, not OR:
// an OR lets a document missing even one key into the trust branch, where
// docArtifact(missingKey) == "true" silently reads the absent key as false
// rather than falling back (issue #2527 review, partial-key finding).
func TestResolveCapabilitySignals_MatchingDocumentPartialArtifactKeysFallsBack(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{"CODE_FORGE": "local", "ISSUE_TRACKER": "local"},
		Artifacts: map[string]string{
			"HOST_MEDIATED_REMOTE":       "false",
			"IN_BOX_UNREACHABLE_TRACKER": "false",
			"FULLY_LOCAL":                "false",
			// OUTBOX_RELAY_CAPABLE deliberately absent -- 3 of 4 keys present.
		},
	}

	sig := resolveCapabilitySignals("local", "local")
	// hostMediatedRemote/inBoxUnreachableTracker/fullyLocal are all true in
	// the local/local registry derivation -- deliberately the opposite of
	// what the (present-but-wrong) document artifacts above say, so a wrong
	// trust-branch read shows up as false here instead of silently matching
	// by coincidence.
	if !sig.fullyLocal {
		t.Errorf("fullyLocal = false, want true (partial artifact keys must fall back to registry derivation)")
	}
	if !sig.hostMediatedRemote {
		t.Errorf("hostMediatedRemote = false, want true (partial artifact keys must fall back to registry derivation)")
	}
	if !sig.inBoxUnreachableTracker {
		t.Errorf("inBoxUnreachableTracker = false, want true (partial artifact keys must fall back to registry derivation)")
	}
	// outboxRelayCapable is false in the local/local registry derivation
	// (local.OutboxRelayCapable is unset) -- asserted too, so all four
	// signals are covered even though this one doesn't itself discriminate
	// bug from fix (the missing key reads as false either way here).
	if sig.outboxRelayCapable {
		t.Errorf("outboxRelayCapable = true, want false (registry-derived value for local/local)")
	}
}

// TestTrackerAxisSignalsAndForgeBackendSignal_UnregisteredNameFallsBack
// verifies that trackerAxisSignals/forgeBackendSignal fall back to the
// default arm (GITHUB/GITHUB/GH, GH) for a name with no backendRows entry
// at all, exercised directly rather than only through the registered
// names (github/local/forgejo/jira) resolveTrackerAndForgeSignals's other
// tests cover -- the registry-driven bodies (issue #2533 review) resolve
// this case via `backendByName` returning ok=false / a zero-value
// Descriptor (TrackerAxisRead=="" / ForgeBackend==""), the same sentinel
// an unregistered CODE_FORGE/ISSUE_TRACKER name has always produced, but
// unlike the deleted hand-written switch statements' `default:` case, this
// is a genuinely distinct code path (the "not found" branch) worth its own
// coverage rather than an assumed side effect of the known-name tests.
func TestTrackerAxisSignalsAndForgeBackendSignal_UnregisteredNameFallsBack(t *testing.T) {
	read, write, filer := trackerAxisSignals("not-a-real-backend")
	if read != "GITHUB" || write != "GITHUB" || filer != "GH" {
		t.Errorf("trackerAxisSignals(unregistered) = (%q,%q,%q), want (GITHUB,GITHUB,GH)", read, write, filer)
	}

	if got := forgeBackendSignal("not-a-real-backend"); got != "GH" {
		t.Errorf("forgeBackendSignal(unregistered) = %q, want GH", got)
	}
}

// TestResolveTrackerAndForgeSignals_NoDocumentFallsBackToComputation verifies
// that with no loaded document, resolveTrackerAndForgeSignals always derives
// the tracker-axis/forge-backend strings fresh from the pure mirror of
// lib/mkHarness.nix's trackerAxisRead/Write/Filer/forgeBackend computation
// rather than reading an unpopulated docArtifact as "" (issue #2533 review).
func TestResolveTrackerAndForgeSignals_NoDocumentFallsBackToComputation(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil

	read, write, filer, forge := resolveTrackerAndForgeSignals("forgejo", "forgejo")
	if read != "FORGEJO" || write != "FORGEJO" || filer != "FORGEJO" || forge != "FORGEJO" {
		t.Errorf("forgejo/forgejo = (%q,%q,%q,%q), want all FORGEJO", read, write, filer, forge)
	}

	read, write, filer, forge = resolveTrackerAndForgeSignals("github", "local")
	if read != "LOCAL" || write != "" || filer != "GH" || forge != "GH" {
		t.Errorf("github/local = (%q,%q,%q,%q), want (LOCAL,\"\",GH,GH)", read, write, filer, forge)
	}

	read, write, filer, forge = resolveTrackerAndForgeSignals("github", "github")
	if read != "GITHUB" || write != "GITHUB" || filer != "GH" || forge != "GH" {
		t.Errorf("github/github = (%q,%q,%q,%q), want (GITHUB,GITHUB,GH,GH)", read, write, filer, forge)
	}
}

// TestResolveTrackerAndForgeSignals_MatchingDocumentTrustsForwardedArtifact
// verifies that when the resolved CODE_FORGE/ISSUE_TRACKER pairing matches
// what was baked into the document's settings section, the forwarded
// artifact strings are trusted directly instead of being recomputed --
// asserted against values a fresh github/github computation would never
// produce, so the test can't pass by coincidence.
func TestResolveTrackerAndForgeSignals_MatchingDocumentTrustsForwardedArtifact(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "github"},
		Artifacts: map[string]string{
			"TRACKER_AXIS_READ":  "WEIRD_READ",
			"TRACKER_AXIS_WRITE": "WEIRD_WRITE",
			"TRACKER_AXIS_FILER": "WEIRD_FILER",
			"FORGE_BACKEND":      "WEIRD_FORGE",
		},
	}

	read, write, filer, forge := resolveTrackerAndForgeSignals("github", "github")
	if read != "WEIRD_READ" || write != "WEIRD_WRITE" || filer != "WEIRD_FILER" || forge != "WEIRD_FORGE" {
		t.Errorf("got (%q,%q,%q,%q), want forwarded artifact values", read, write, filer, forge)
	}
}

// TestResolveTrackerAndForgeSignals_OverrideAwayFromBakedDocumentFallsBack
// verifies that when the pairing actually in effect diverges from what was
// baked into the document (a dispatch-time --tracker/--forge-backend
// override), the stale forwarded artifact is NOT trusted -- a
// github-baked document overridden to forgejo/forgejo must not keep
// reading the baked BOX_TRACKER_AXIS_READ=GITHUB (issue #2533 review).
func TestResolveTrackerAndForgeSignals_OverrideAwayFromBakedDocumentFallsBack(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "github"},
		Artifacts: map[string]string{
			"TRACKER_AXIS_READ":  "GITHUB",
			"TRACKER_AXIS_WRITE": "GITHUB",
			"TRACKER_AXIS_FILER": "GH",
			"FORGE_BACKEND":      "GH",
		},
	}

	read, write, filer, forge := resolveTrackerAndForgeSignals("forgejo", "forgejo")
	if read != "FORGEJO" || write != "FORGEJO" || filer != "FORGEJO" || forge != "FORGEJO" {
		t.Errorf("override to forgejo/forgejo = (%q,%q,%q,%q), want all FORGEJO (ignores stale github-baked artifact)", read, write, filer, forge)
	}
}

// TestResolveTrackerAndForgeSignals_PartialArtifactKeysFallsBack verifies
// that when the matching document's Artifacts section carries only some of
// the four tracker/forge keys (e.g. 3 of 4), resolveTrackerAndForgeSignals
// falls back to the fresh computation for all four, not just the missing
// one (issue #2533 review, mirrors the capability-signals partial-key
// guard).
func TestResolveTrackerAndForgeSignals_PartialArtifactKeysFallsBack(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{"CODE_FORGE": "forgejo", "ISSUE_TRACKER": "forgejo"},
		Artifacts: map[string]string{
			"TRACKER_AXIS_READ":  "GITHUB",
			"TRACKER_AXIS_WRITE": "GITHUB",
			"TRACKER_AXIS_FILER": "GH",
			// FORGE_BACKEND deliberately absent -- 3 of 4 keys present.
		},
	}

	read, write, filer, forge := resolveTrackerAndForgeSignals("forgejo", "forgejo")
	if read != "FORGEJO" || write != "FORGEJO" || filer != "FORGEJO" || forge != "FORGEJO" {
		t.Errorf("partial keys = (%q,%q,%q,%q), want all FORGEJO (falls back to fresh computation for all four)", read, write, filer, forge)
	}
}

// TestResolveAgentPresenceSignals_NoDocumentFallsBackToSchemaDefaults
// verifies that with no loaded document, resolveAgentPresenceSignals
// returns the schema-default-derived values instead of unconditionally
// false: WORKER_MODEL's schema default is non-empty ("claude-sonnet-5"),
// so WorkerProvisioned must default true, and ORCHESTRATOR_ENABLED's
// schema default is false, so ReviewLoopInline/ReviewLoopOrchestrator must
// default (true, false) -- exactly one true, never both false (issue #2533
// review).
func TestResolveAgentPresenceSignals_NoDocumentFallsBackToSchemaDefaults(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	// Isolate from whatever this test process's own ambient environment
	// happens to carry (e.g. a dispatched Box's own ORCHESTRATOR_ENABLED),
	// so the schema-default fallback this test pins is deterministic
	// regardless of host.
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("")
	if presence.filerEnabled {
		t.Errorf("filerEnabled = true, want false (FILER_MODEL schema default is empty)")
	}
	if !presence.workerProvisioned {
		t.Errorf("workerProvisioned = false, want true (WORKER_MODEL schema default is non-empty)")
	}
	if !presence.reviewLoopInline {
		t.Errorf("reviewLoopInline = false, want true (ORCHESTRATOR_ENABLED schema default is false)")
	}
	if presence.reviewLoopOrchestrator {
		t.Errorf("reviewLoopOrchestrator = true, want false (ORCHESTRATOR_ENABLED schema default is false)")
	}
}

// TestResolveAgentPresenceSignals_MatchingDocumentTrustsForwardedArtifact
// verifies that when all four artifact keys are present and the live
// FILER_MODEL/WORKER_MODEL/ORCHESTRATOR_ENABLED values match what the
// document baked into its Settings section, the forwarded artifact values
// are trusted directly -- asserted against values the schema-default
// fallback would never produce, so the test can't pass by coincidence.
func TestResolveAgentPresenceSignals_MatchingDocumentTrustsForwardedArtifact(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{
			"FILER_MODEL":          "",
			"WORKER_MODEL":         "claude-sonnet-5",
			"ORCHESTRATOR_ENABLED": "",
		},
		Artifacts: map[string]string{
			"FILER_ENABLED":            "true",
			"WORKER_PROVISIONED":       "false",
			"REVIEW_LOOP_INLINE":       "false",
			"REVIEW_LOOP_ORCHESTRATOR": "true",
		},
	}
	// Live values must equal what the document baked in for the trust
	// branch to activate -- pinned explicitly rather than left to whatever
	// this test process's own ambient environment happens to carry.
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "claude-sonnet-5")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("")
	if !presence.filerEnabled {
		t.Errorf("filerEnabled = false, want true (forwarded artifact)")
	}
	if presence.workerProvisioned {
		t.Errorf("workerProvisioned = true, want false (forwarded artifact)")
	}
	if presence.reviewLoopInline {
		t.Errorf("reviewLoopInline = true, want false (forwarded artifact)")
	}
	if !presence.reviewLoopOrchestrator {
		t.Errorf("reviewLoopOrchestrator = false, want true (forwarded artifact)")
	}
}

// TestResolveAgentPresenceSignals_OverrideAwayFromBakedDocumentFallsBack
// verifies that a dispatch-time ORCHESTRATOR_ENABLED override away from what
// the document baked into its Settings section is NOT trusted for the
// review-loop pair -- an orchestrator-off-baked document overridden to
// orchestrator-on must not keep reading the stale baked
// REVIEW_LOOP_INLINE=true/REVIEW_LOOP_ORCHESTRATOR=false artifacts (issue
// #2533 review): unlike FILER_MODEL/WORKER_MODEL, ORCHESTRATOR_ENABLED is
// boxEnv=true (lib/env-schema.nix), so buildBoxEnv/resolveBoxEnvVar forward
// whatever it resolves to in the ambient environment at dispatch time into
// the Box regardless of what was baked into the document at image-build
// time -- trusting the stale artifact here would hand the Box off to the
// orchestrator ($ORCHESTRATOR, sourced from that same ambient
// ORCHESTRATOR_ENABLED) while still rendering the inline review-loop
// section. FILER_MODEL/WORKER_MODEL stay matched to the document here, so
// this also pins that the roster pair's trust gate is unaffected by the
// review-loop pair's fallback -- the two gates are independent.
func TestResolveAgentPresenceSignals_OverrideAwayFromBakedDocumentFallsBack(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{
			"FILER_MODEL":          "",
			"WORKER_MODEL":         "claude-sonnet-5",
			"ORCHESTRATOR_ENABLED": "",
		},
		Artifacts: map[string]string{
			"FILER_ENABLED":            "false",
			"WORKER_PROVISIONED":       "true",
			"REVIEW_LOOP_INLINE":       "true",
			"REVIEW_LOOP_ORCHESTRATOR": "false",
		},
	}
	// FILER_MODEL/WORKER_MODEL stay matched to the baked document -- only
	// ORCHESTRATOR_ENABLED is overridden, isolating the divergence this
	// test exercises. "1" is the bool-kind schema knob's live-value
	// convention (parseFlags's byBool handling / Nix's toString-of-bool),
	// not the literal string "true".
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "claude-sonnet-5")
	t.Setenv("ORCHESTRATOR_ENABLED", "1")

	presence := resolveAgentPresenceSignals("")
	if presence.filerEnabled {
		t.Errorf("filerEnabled = true, want false (trusted straight from the document's FILER_ENABLED artifact -- the roster pair's trust gate is independent of the ORCHESTRATOR_ENABLED override this test exercises)")
	}
	if !presence.workerProvisioned {
		t.Errorf("workerProvisioned = false, want true (trusted straight from the document's WORKER_PROVISIONED artifact -- the roster pair's trust gate is independent of the ORCHESTRATOR_ENABLED override this test exercises)")
	}
	if presence.reviewLoopInline {
		t.Errorf("reviewLoopInline = true, want false (override to orchestrator-on ignores stale baked REVIEW_LOOP_INLINE=true)")
	}
	if !presence.reviewLoopOrchestrator {
		t.Errorf("reviewLoopOrchestrator = false, want true (override to orchestrator-on ignores stale baked REVIEW_LOOP_ORCHESTRATOR=false)")
	}
}

// TestResolveAgentPresenceSignals_FilerModelOverride_DocumentArtifactStillTrusted
// verifies that a dispatch-time FILER_MODEL override away from what the
// document baked into its Settings section does NOT defeat trust in the
// document's FILER_ENABLED artifact (issue #2533 review): unlike
// ORCHESTRATOR_ENABLED, FILER_MODEL/WORKER_MODEL never reach the box at
// runtime -- lib/image.nix bakes AGENTS_JSON_TEMPLATE as a FIXED value at
// build time from the *configured* models, and lib/mkHarness.nix computes
// filerEnabled/workerProvisioned purely from that baked template's own
// parsed keys -- so a live FILER_MODEL override has zero effect on the
// box's real --agents roster. A box built with filerModel="" (no filer in
// the roster) baking FILER_ENABLED=false into the document must still
// report filerEnabled=false even when the live environment carries a
// non-empty FILER_MODEL override, rather than recomputing
// filerModel != "" = true from the live override the way the old
// all-four-or-nothing trust gate did.
func TestResolveAgentPresenceSignals_FilerModelOverride_DocumentArtifactStillTrusted(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{
			"FILER_MODEL":          "",
			"WORKER_MODEL":         "claude-sonnet-5",
			"ORCHESTRATOR_ENABLED": "",
		},
		Artifacts: map[string]string{
			"FILER_ENABLED":            "false",
			"WORKER_PROVISIONED":       "true",
			"REVIEW_LOOP_INLINE":       "true",
			"REVIEW_LOOP_ORCHESTRATOR": "false",
		},
	}
	// FILER_MODEL is overridden away from the document's baked "" to a
	// non-empty model -- the scenario that trips the old code's
	// all-three-must-match trust gate. WORKER_MODEL/ORCHESTRATOR_ENABLED
	// stay matched to the baked document, isolating the divergence this
	// test exercises to FILER_MODEL alone.
	t.Setenv("FILER_MODEL", "haiku")
	t.Setenv("WORKER_MODEL", "claude-sonnet-5")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("")
	if presence.filerEnabled {
		t.Errorf("filerEnabled = true, want false (document's baked FILER_ENABLED artifact must be trusted regardless of a live FILER_MODEL override -- AGENTS_JSON_TEMPLATE is a fixed, non-overridable bake)")
	}
	if !presence.workerProvisioned {
		t.Errorf("workerProvisioned = false, want true (forwarded artifact)")
	}
	if !presence.reviewLoopInline {
		t.Errorf("reviewLoopInline = false, want true (forwarded artifact)")
	}
	if presence.reviewLoopOrchestrator {
		t.Errorf("reviewLoopOrchestrator = true, want false (forwarded artifact)")
	}
}

// TestResolveAgentPresenceSignals_WorkerModelOverride_DocumentArtifactStillTrusted
// is the WORKER_MODEL mirror of the FILER_MODEL case above: a document
// baked with WORKER_MODEL="claude-sonnet-5" (worker provisioned,
// WORKER_PROVISIONED=true) must still report workerProvisioned=true even
// when the live WORKER_MODEL is overridden away to empty -- the fallback
// computation (workerModel != "") would otherwise say false, diverging from
// what lib/mkHarness.nix actually baked into the box's --agents roster.
func TestResolveAgentPresenceSignals_WorkerModelOverride_DocumentArtifactStillTrusted(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{
			"FILER_MODEL":          "",
			"WORKER_MODEL":         "claude-sonnet-5",
			"ORCHESTRATOR_ENABLED": "",
		},
		Artifacts: map[string]string{
			"FILER_ENABLED":            "false",
			"WORKER_PROVISIONED":       "true",
			"REVIEW_LOOP_INLINE":       "true",
			"REVIEW_LOOP_ORCHESTRATOR": "false",
		},
	}
	// WORKER_MODEL is overridden away from the document's baked
	// "claude-sonnet-5" to empty. FILER_MODEL/ORCHESTRATOR_ENABLED stay
	// matched to the baked document, isolating the divergence to
	// WORKER_MODEL alone.
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("")
	if presence.filerEnabled {
		t.Errorf("filerEnabled = true, want false (forwarded artifact)")
	}
	if !presence.workerProvisioned {
		t.Errorf("workerProvisioned = false, want true (document's baked WORKER_PROVISIONED artifact must be trusted regardless of a live WORKER_MODEL override -- AGENTS_JSON_TEMPLATE is a fixed, non-overridable bake)")
	}
	if !presence.reviewLoopInline {
		t.Errorf("reviewLoopInline = false, want true (forwarded artifact)")
	}
	if presence.reviewLoopOrchestrator {
		t.Errorf("reviewLoopOrchestrator = true, want false (forwarded artifact)")
	}
}

// TestResolveAgentPresenceSignals_OrchestratorOverride_ReviewLoopStaysLiveDerived
// verifies that decoupling FILER_ENABLED/WORKER_PROVISIONED trust from the
// live-vs-doc match requirement leaves REVIEW_LOOP_INLINE/
// REVIEW_LOOP_ORCHESTRATOR's trust condition unchanged: ORCHESTRATOR_ENABLED
// is boxEnv=true (lib/env-schema.nix) and entrypoint.sh reads it live at
// runtime, so a dispatch-time override away from what the document baked in
// must still fall through to the live-value-derived reviewLoopInline/
// reviewLoopOrchestrator, not the document's now-independently-trusted (but
// still stale for this axis) REVIEW_LOOP_* artifacts -- even though
// FILER_ENABLED/WORKER_PROVISIONED are trusted straight from the document
// in this same call, since FILER_MODEL/WORKER_MODEL stay matched here.
func TestResolveAgentPresenceSignals_OrchestratorOverride_ReviewLoopStaysLiveDerived(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{
			"FILER_MODEL":          "",
			"WORKER_MODEL":         "claude-sonnet-5",
			"ORCHESTRATOR_ENABLED": "",
		},
		Artifacts: map[string]string{
			"FILER_ENABLED":            "false",
			"WORKER_PROVISIONED":       "true",
			"REVIEW_LOOP_INLINE":       "true",
			"REVIEW_LOOP_ORCHESTRATOR": "false",
		},
	}
	// FILER_MODEL/WORKER_MODEL stay matched to the baked document -- only
	// ORCHESTRATOR_ENABLED is overridden, isolating the divergence this
	// test exercises to the review-loop pair.
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "claude-sonnet-5")
	t.Setenv("ORCHESTRATOR_ENABLED", "1")

	presence := resolveAgentPresenceSignals("")
	if presence.filerEnabled {
		t.Errorf("filerEnabled = true, want false (document's FILER_ENABLED artifact is trusted independent of the review-loop axis)")
	}
	if !presence.workerProvisioned {
		t.Errorf("workerProvisioned = false, want true (document's WORKER_PROVISIONED artifact is trusted independent of the review-loop axis)")
	}
	if presence.reviewLoopInline {
		t.Errorf("reviewLoopInline = true, want false (override to orchestrator-on ignores stale baked REVIEW_LOOP_INLINE=true, falls back to live-derived value)")
	}
	if !presence.reviewLoopOrchestrator {
		t.Errorf("reviewLoopOrchestrator = false, want true (override to orchestrator-on ignores stale baked REVIEW_LOOP_ORCHESTRATOR=false, falls back to live-derived value)")
	}
}

// TestResolveAgentPresenceSignals_NoDocumentOpencodeDriverFallsBackFalse
// verifies that the version-skew fallback (no document at all, so neither
// FILER_ENABLED nor WORKER_PROVISIONED keys exist) is driver-aware (issue
// #2533 review): lib/drivers/opencode.nix's agentsJsonTemplate always
// renders "" regardless of roster contents (it provisions subagents via
// on-disk agents/*.md files instead), so nix always bakes
// FILER_ENABLED=WORKER_PROVISIONED=false for the opencode Driver
// (mkharness-filer-worker-false-for-opencode-driver pins this on the nix
// side) even with a non-empty FILER_MODEL/WORKER_MODEL configured. A
// driver-blind fallback of filerModel != ""/workerModel != "" would report
// both true here, diverging from what nix would have baked.
func TestResolveAgentPresenceSignals_NoDocumentOpencodeDriverFallsBackFalse(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	t.Setenv("FILER_MODEL", "haiku")
	t.Setenv("WORKER_MODEL", "claude-sonnet-5")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("opencode")
	if presence.filerEnabled {
		t.Errorf("filerEnabled = true, want false (opencode Driver always bakes FILER_ENABLED=false regardless of FILER_MODEL)")
	}
	if presence.workerProvisioned {
		t.Errorf("workerProvisioned = true, want false (opencode Driver always bakes WORKER_PROVISIONED=false regardless of WORKER_MODEL)")
	}
}

// TestResolveAgentPresenceSignals_PartialArtifactKeysFallsBack verifies
// that the FILER_ENABLED/WORKER_PROVISIONED and REVIEW_LOOP_INLINE/
// REVIEW_LOOP_ORCHESTRATOR trust gates are independent (issue #2533
// review): with REVIEW_LOOP_ORCHESTRATOR deliberately absent (3 of 4
// artifact keys present) but both FILER_ENABLED/WORKER_PROVISIONED keys
// present, the roster pair is still trusted straight from the document
// while the review-loop pair -- missing one of its own two keys -- falls
// back to the schema-default-derived live value for BOTH of its members,
// not just the missing one.
func TestResolveAgentPresenceSignals_PartialArtifactKeysFallsBack(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{
			"FILER_MODEL":          "",
			"WORKER_MODEL":         "claude-sonnet-5",
			"ORCHESTRATOR_ENABLED": "",
		},
		Artifacts: map[string]string{
			"FILER_ENABLED":      "true",
			"WORKER_PROVISIONED": "false",
			"REVIEW_LOOP_INLINE": "false",
			// REVIEW_LOOP_ORCHESTRATOR deliberately absent -- 3 of 4 keys present.
		},
	}
	// Live values pinned to match the baked document, so only the partial
	// artifact keys drive the fallback below, not an incidental live/baked
	// mismatch this test isn't exercising.
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "claude-sonnet-5")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("")
	if !presence.filerEnabled {
		t.Errorf("filerEnabled = false, want true (both roster-pair keys present, trusted from document despite the review-loop pair's missing key)")
	}
	if presence.workerProvisioned {
		t.Errorf("workerProvisioned = true, want false (both roster-pair keys present, trusted from document despite the review-loop pair's missing key)")
	}
	if !presence.reviewLoopInline {
		t.Errorf("reviewLoopInline = false, want true (review-loop pair missing REVIEW_LOOP_ORCHESTRATOR falls back to schema default for both members)")
	}
	if presence.reviewLoopOrchestrator {
		t.Errorf("reviewLoopOrchestrator = true, want false (review-loop pair missing REVIEW_LOOP_ORCHESTRATOR falls back to schema default for both members)")
	}
}

// TestResolveAgentPresenceSignals_ScoutNoDocumentFallsBackToSchemaDefault
// verifies that with no loaded document, scoutProvisioned falls back to
// SCOUT_MODEL's own schema default (non-empty, "claude-haiku-4-5-20251001"),
// mirroring workerProvisioned's schema-default fallback (issue #3157):
// getenv treats a KEY="" override as unset (main.go's getenv), so a schema
// key whose default is non-empty can't be driven false via t.Setenv through
// this no-document fallback path -- the false case is covered only via
// TestResolveAgentPresenceSignals_ScoutDocumentArtifactTrustedRegardlessOfLiveOverride
// below, which forces scoutProvisioned=false through a loaded document's
// SCOUT_PROVISIONED artifact rather than through the live SCOUT_MODEL
// fallback exercised here.
func TestResolveAgentPresenceSignals_ScoutNoDocumentFallsBackToSchemaDefault(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "")
	t.Setenv("SCOUT_MODEL", "")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("")
	if !presence.scoutProvisioned {
		t.Errorf("scoutProvisioned = false, want true (SCOUT_MODEL schema default is non-empty)")
	}
}

// TestResolveAgentPresenceSignals_ScoutDocumentArtifactTrustedRegardlessOfLiveOverride
// verifies scoutProvisioned mirrors filerEnabled/workerProvisioned's
// fixed-bake trust shape: once SCOUT_PROVISIONED is present in the loaded
// document's Artifacts, it is trusted regardless of whether the live
// SCOUT_MODEL matches what the document baked in (issue #3157) -- unlike
// the ambient-env-forwarded review-loop pair, AGENTS_JSON_TEMPLATE is a
// fixed, non-overridable bake.
func TestResolveAgentPresenceSignals_ScoutDocumentArtifactTrustedRegardlessOfLiveOverride(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{
			"FILER_MODEL":          "",
			"WORKER_MODEL":         "claude-sonnet-5",
			"ORCHESTRATOR_ENABLED": "",
		},
		Artifacts: map[string]string{
			"FILER_ENABLED":            "false",
			"WORKER_PROVISIONED":       "true",
			"SCOUT_PROVISIONED":        "false",
			"REVIEW_LOOP_INLINE":       "true",
			"REVIEW_LOOP_ORCHESTRATOR": "false",
		},
	}
	// Live SCOUT_MODEL deliberately overridden non-empty, away from what a
	// scoutModel="" bake would have produced -- the document's baked
	// SCOUT_PROVISIONED=false must still win.
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "claude-sonnet-5")
	t.Setenv("SCOUT_MODEL", "claude-haiku-4-5-20251001")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("")
	if presence.scoutProvisioned {
		t.Errorf("scoutProvisioned = true, want false (document's baked SCOUT_PROVISIONED artifact must be trusted regardless of a live SCOUT_MODEL override)")
	}
}

// TestResolveAgentPresenceSignals_ScoutMissingArtifactKeyFallsBackIndependently
// verifies scoutProvisioned's trust gate is independent of the roster
// pair's (issue #3157, version-skew case): a document baked before this
// slice landed carries FILER_ENABLED/WORKER_PROVISIONED but no
// SCOUT_PROVISIONED key at all, so scoutProvisioned alone must fall back
// to the live SCOUT_MODEL-derived value while filerEnabled/workerProvisioned
// stay trusted from the document, exactly mirroring how the review-loop
// pair's own missing-key fallback (PartialArtifactKeysFallsBack) never
// disturbs the roster pair.
func TestResolveAgentPresenceSignals_ScoutMissingArtifactKeyFallsBackIndependently(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{
			"FILER_MODEL":          "",
			"WORKER_MODEL":         "claude-sonnet-5",
			"ORCHESTRATOR_ENABLED": "",
		},
		Artifacts: map[string]string{
			"FILER_ENABLED":            "true",
			"WORKER_PROVISIONED":       "false",
			"REVIEW_LOOP_INLINE":       "true",
			"REVIEW_LOOP_ORCHESTRATOR": "false",
			// SCOUT_PROVISIONED deliberately absent -- pre-#3157 document.
		},
	}
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "claude-sonnet-5")
	t.Setenv("SCOUT_MODEL", "claude-haiku-4-5-20251001")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("")
	if !presence.filerEnabled {
		t.Errorf("filerEnabled = false, want true (trusted from document despite SCOUT_PROVISIONED's absence)")
	}
	if presence.workerProvisioned {
		t.Errorf("workerProvisioned = true, want false (trusted from document despite SCOUT_PROVISIONED's absence)")
	}
	if !presence.scoutProvisioned {
		t.Errorf("scoutProvisioned = false, want true (missing artifact key falls back to live SCOUT_MODEL-derived value)")
	}
}

// TestResolveAgentPresenceSignals_ScoutNoDocumentOpencodeDriverFallsBackToScoutModel
// pins that scout is decoupled from the filer/worker opencode=false rule:
// unlike agentsJsonTemplate, lib/drivers/opencode.nix provisions scout via
// agentFilesTemplate (writes .config/opencode/agents/scout.md) keyed off
// finalRoster, so opencode's fallback must track SCOUT_MODEL like every
// other driver even while filerEnabled/workerProvisioned stay false.
func TestResolveAgentPresenceSignals_ScoutNoDocumentOpencodeDriverFallsBackToScoutModel(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	t.Setenv("FILER_MODEL", "haiku")
	t.Setenv("WORKER_MODEL", "claude-sonnet-5")
	t.Setenv("SCOUT_MODEL", "claude-haiku-4-5-20251001")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	presence := resolveAgentPresenceSignals("opencode")
	if !presence.scoutProvisioned {
		t.Errorf("scoutProvisioned = false, want true (opencode provisions scout via agentFilesTemplate regardless of agentsJsonTemplate)")
	}
	if presence.filerEnabled || presence.workerProvisioned {
		t.Errorf("filerEnabled=%v workerProvisioned=%v, want both false (opencode Driver always bakes these false)", presence.filerEnabled, presence.workerProvisioned)
	}
}

// TestValidate_FullyLocalExemptsRepoSlugAndGhToken verifies that validate()
// does not require REPO_SLUG or GH_TOKEN when both CODE_FORGE and
// ISSUE_TRACKER are local (issue #1895): the github gh-exec client that
// reads them is never constructed under that combination. c.fullyLocal/
// c.inBoxUnreachableTracker are deliberately left at their zero value and
// loadedDoc left nil, so this exercises resolveCapabilitySignals's
// registry-fallback derivation from c.codeForge/c.issueTracker rather than
// a directly-set (and tautological) config field.
func TestValidate_FullyLocalExemptsRepoSlugAndGhToken(t *testing.T) {
	c := minimalValidLocalConfig()
	c.issueTracker = "local"
	c.repoSlug = ""
	c.ghToken = ""
	if err := validate(c); err != nil {
		t.Errorf("validate() should exempt REPO_SLUG/GH_TOKEN when CODE_FORGE and ISSUE_TRACKER are both local: %v", err)
	}
}

// TestValidate_OverrideAwayFromBakedGithubDocumentExemptsRepoSlugAndGhToken
// reproduces the issue #2527 review finding: a github-baked input document
// (nix built with default CODE_FORGE=github) run with an override to
// --forge-backend local --tracker local must still exempt REPO_SLUG/
// GH_TOKEN -- validate() must not keep trusting the stale
// FULLY_LOCAL=false baked for the pre-override pairing.
func TestValidate_OverrideAwayFromBakedGithubDocumentExemptsRepoSlugAndGhToken(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "github"},
		Artifacts: map[string]string{
			"HOST_MEDIATED_REMOTE":       "true",
			"IN_BOX_UNREACHABLE_TRACKER": "false",
			"FULLY_LOCAL":                "false",
		},
	}

	c := minimalValidLocalConfig()
	c.issueTracker = "local"
	c.repoSlug = ""
	c.ghToken = ""
	if err := validate(c); err != nil {
		t.Errorf("validate() should exempt REPO_SLUG/GH_TOKEN on an override away from a github-baked document: %v", err)
	}
}

// TestValidate_OverrideBackToGithubFromFullyLocalDocumentRequiresRepoSlugAndGhToken
// reproduces the inverse of the issue #2527 review finding: a fully-local
// baked document (Settings local/local, Artifacts FULLY_LOCAL=true)
// overridden back to CODE_FORGE=github/ISSUE_TRACKER=github at runtime must
// require REPO_SLUG/GH_TOKEN again -- validate() must not keep trusting the
// stale FULLY_LOCAL=true baked for the pre-override pairing.
func TestValidate_OverrideBackToGithubFromFullyLocalDocumentRequiresRepoSlugAndGhToken(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{"CODE_FORGE": "local", "ISSUE_TRACKER": "local"},
		Artifacts: map[string]string{
			"HOST_MEDIATED_REMOTE":       "true",
			"IN_BOX_UNREACHABLE_TRACKER": "true",
			"FULLY_LOCAL":                "true",
		},
	}

	c := minimalValidConfig()
	c.repoSlug = ""
	c.ghToken = ""
	if err := validate(c); err == nil {
		t.Error("validate() must require REPO_SLUG/GH_TOKEN on an override back to github from a fully-local-baked document")
	}
}

// TestValidate_MixedLocalStillRequiresRepoSlugAndGhToken verifies the
// fully-local exemption does not leak into a mixed configuration where only
// one of CODE_FORGE/ISSUE_TRACKER is local — both fields stay required.
func TestValidate_MixedLocalStillRequiresRepoSlugAndGhToken(t *testing.T) {
	// CODE_FORGE=local, ISSUE_TRACKER=github (default).
	c := minimalValidLocalConfig()
	c.repoSlug = ""
	if err := validate(c); err == nil {
		t.Error("validate() must still require REPO_SLUG when only CODE_FORGE is local")
	}
	c = minimalValidLocalConfig()
	c.ghToken = ""
	if err := validate(c); err == nil {
		t.Error("validate() must still require GH_TOKEN when only CODE_FORGE is local")
	}

	// CODE_FORGE=github (default), ISSUE_TRACKER=local.
	c = minimalValidConfig()
	c.issueTracker = "local"
	c.repoSlug = ""
	if err := validate(c); err == nil {
		t.Error("validate() must still require REPO_SLUG when only ISSUE_TRACKER is local")
	}
	c = minimalValidConfig()
	c.issueTracker = "local"
	c.ghToken = ""
	if err := validate(c); err == nil {
		t.Error("validate() must still require GH_TOKEN when only ISSUE_TRACKER is local")
	}
}

// TestValidate_ChoiceErrorsPrecedeCrossKnobErrors pins the origin/main
// ordering restored by issue #2559: validate()'s validateChoice(MERGE_MODE)
// call must run — and its enum-choice error must win — before the
// CODE_FORGE=local cross-knob check that requires MERGE_MODE=immediate. A
// prior refactor accidentally moved the cross-knob checks ahead of the
// validateChoice calls, so an invalid MERGE_MODE under CODE_FORGE=local
// surfaced the cross-knob error ("requires MERGE_MODE=immediate") instead of
// the enum-choice error listing valid MERGE_MODE choices.
func TestValidate_ChoiceErrorsPrecedeCrossKnobErrors(t *testing.T) {
	c := minimalValidLocalConfig()
	c.issueTracker = "local"
	c.mergeMode = "bogus"

	err := validate(c)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), "requires MERGE_MODE") {
		t.Fatalf("got cross-knob error (wrong precedence), want MERGE_MODE enum-choice error: %v", err)
	}
	if !strings.Contains(err.Error(), "MERGE_MODE") {
		t.Fatalf("want error to mention MERGE_MODE, got: %v", err)
	}
}

// TestValidate_ChoiceErrorsPrecedeRegistryProxyRoutesRetirementError pins the
// same ordering as TestValidate_ChoiceErrorsPrecedeCrossKnobErrors, but for
// the registry-proxy-routes row folded into launcherCrossKnobChecks: an
// invalid MERGE_MODE must surface validateChoice's enum-choice error, not
// validateRetiredRegistryProxyKnobs' retirement error (issue #3145), even
// though both are broken.
func TestValidate_ChoiceErrorsPrecedeRegistryProxyRoutesRetirementError(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_UPSTREAM_URL", "https://registry.example.com")
	c := minimalValidConfig()
	c.mergeMode = "bogus"

	err := validate(c)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), "REGISTRY_PROXY_UPSTREAM_URL") {
		t.Fatalf("got retirement error (wrong precedence), want MERGE_MODE enum-choice error: %v", err)
	}
	if !strings.Contains(err.Error(), "MERGE_MODE") {
		t.Fatalf("want error to mention MERGE_MODE, got: %v", err)
	}
}

// TestValidate_RegistryProxyRoutesRetirementErrorPrecedesBoxForgeAndIssueAccessChoiceError
// pins the inverse of TestValidate_ChoiceErrorsPrecedeRegistryProxyRoutesRetirementError:
// BOX_FORGE_AND_ISSUE_ACCESS is the one choiceKnobRegistry row marked
// AfterCrossKnobChecks (choiceknobs.go), so when both it and a cross-knob
// check are simultaneously broken, the registry-proxy-routes row's
// retirement error must win, not the BOX_FORGE_AND_ISSUE_ACCESS enum-choice
// error. This defends against choiceKnobRow.AfterCrossKnobChecks being
// silently flipped to false, or the registry being reordered so that
// BOX_FORGE_AND_ISSUE_ACCESS's validateChoice call runs ahead of
// launcherCrossKnobChecks in validate() again -- either of which would flip
// this precedence without failing any other test.
func TestValidate_RegistryProxyRoutesRetirementErrorPrecedesBoxForgeAndIssueAccessChoiceError(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_UPSTREAM_URL", "https://registry.example.com")
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only-ish"

	err := validate(c)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), "BOX_FORGE_AND_ISSUE_ACCESS") {
		t.Fatalf("got BOX_FORGE_AND_ISSUE_ACCESS enum-choice error (wrong precedence), want registry-proxy-routes retirement error: %v", err)
	}
	if !strings.Contains(err.Error(), "REGISTRY_PROXY_UPSTREAM_URL") {
		t.Fatalf("want error to mention REGISTRY_PROXY_UPSTREAM_URL, got: %v", err)
	}
}

// TestValidate_ResearchSelfContainedExemptsRepoSlugAndGhToken verifies that
// validate() does not require REPO_SLUG or GH_TOKEN for a research dispatch
// with selfContained set and a local issue tracker (issue #2202,
// --self-contained): the Box clones no repo and explores none, and the local
// tracker supplies the issue content directly, so neither field is
// meaningful.
func TestValidate_ResearchSelfContainedExemptsRepoSlugAndGhToken(t *testing.T) {
	c := applyDispatchKind(minimalValidConfig(), dispatchKindResearch)
	c.selfContained = true
	c.issueTracker = "local"
	c.repoSlug = ""
	c.ghToken = ""
	if err := validate(c); err != nil {
		t.Errorf("validate() should exempt REPO_SLUG/GH_TOKEN for self-contained research with a local issue tracker: %v", err)
	}
}

// TestValidate_ResearchSelfContainedGithubTrackerStillRequiresRepoSlug
// verifies that the self-contained REPO_SLUG/GH_TOKEN relaxation does not
// fire for a github issue tracker (issue #2202): a self-contained research
// run against a github-hosted issue still needs REPO_SLUG/GH_TOKEN to read
// the issue and post the verdict, so validate() must keep requiring it.
func TestValidate_ResearchSelfContainedGithubTrackerStillRequiresRepoSlug(t *testing.T) {
	c := applyDispatchKind(minimalValidConfig(), dispatchKindResearch)
	c.selfContained = true
	c.repoSlug = ""
	err := validate(c)
	if err == nil {
		t.Fatal("validate() must still require REPO_SLUG for self-contained research with a github issue tracker")
	}
	if !strings.Contains(err.Error(), "REPO_SLUG") {
		t.Errorf("error should mention REPO_SLUG, got: %v", err)
	}
}

// TestValidate_SelfContainedRejectedOutsideResearch verifies that validate()
// rejects selfContained set on any dispatch kind other than research (issue
// #2202) — the flag is research-only.
func TestValidate_SelfContainedRejectedOutsideResearch(t *testing.T) {
	c := minimalValidConfig()
	c.selfContained = true
	err := validate(c)
	if err == nil {
		t.Fatal("validate() must reject selfContained outside the research dispatch kind")
	}
	if !strings.Contains(err.Error(), "self-contained") {
		t.Errorf("error should mention self-contained, got: %v", err)
	}
}

// TestValidate_ResearchWithoutSelfContainedStillRequiresRepoSlug guards
// against over-relaxing the REPO_SLUG gate: a research dispatch without
// --self-contained still requires REPO_SLUG like any other kind.
func TestValidate_ResearchWithoutSelfContainedStillRequiresRepoSlug(t *testing.T) {
	c := applyDispatchKind(minimalValidConfig(), dispatchKindResearch)
	c.repoSlug = ""
	if err := validate(c); err == nil {
		t.Error("validate() must still require REPO_SLUG for research without --self-contained")
	}
}

// TestValidateMergeMode_RejectsUnknown verifies that validate() fails fast when
// MERGE_MODE is set to an unrecognised value.
func TestValidateMergeMode_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.mergeMode = "turbo"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised MERGE_MODE")
	}
}

// TestValidate_JiraRequiresBaseURLProjectKeyToken verifies validate() fails
// fast when ISSUE_TRACKER=jira but the Jira connection fields are missing,
// rather than deferring to a runtime Jira API error.
func TestValidate_JiraRequiresBaseURLProjectKeyToken(t *testing.T) {
	base := minimalValidConfig()
	base.issueTracker = "jira"
	base.jiraBaseURL = "https://example.atlassian.net"
	base.jiraProjectKey = "PROJ"
	base.jiraToken = "tok"

	if err := validate(base); err != nil {
		t.Fatalf("fully configured jira config should validate: %v", err)
	}

	for _, field := range []string{"jiraBaseURL", "jiraProjectKey", "jiraToken"} {
		c := base
		switch field {
		case "jiraBaseURL":
			c.jiraBaseURL = ""
		case "jiraProjectKey":
			c.jiraProjectKey = ""
		case "jiraToken":
			c.jiraToken = ""
		}
		if err := validate(c); err == nil {
			t.Errorf("validate() must require %s when ISSUE_TRACKER=jira", field)
		}
	}
}

// TestValidate_JiraFieldsOptionalForGitHub verifies validate() does not
// require Jira fields when ISSUE_TRACKER is unset/github.
func TestValidate_JiraFieldsOptionalForGitHub(t *testing.T) {
	c := minimalValidConfig()
	if err := validate(c); err != nil {
		t.Fatalf("github default must not require jira fields: %v", err)
	}
}

// TestValidate_ForgejoRequiresBaseURLAndToken verifies validate() requires
// FORGEJO_BASE_URL and FORGEJO_TOKEN when ISSUE_TRACKER=forgejo, and accepts
// a fully configured forgejo config.
func TestValidate_ForgejoRequiresBaseURLAndToken(t *testing.T) {
	base := minimalValidConfig()
	base.issueTracker = "forgejo"
	base.forgejoBaseURL = "https://codeberg.org"
	base.forgejoToken = "tok"

	if err := validate(base); err != nil {
		t.Fatalf("fully configured forgejo config should validate: %v", err)
	}

	c := base
	c.forgejoToken = ""
	err := validate(c)
	if err == nil {
		t.Fatalf("validate() must require FORGEJO_TOKEN when ISSUE_TRACKER=forgejo")
	}
	if !strings.Contains(err.Error(), "FORGEJO_TOKEN") {
		t.Errorf("validate() error = %q, want it to name FORGEJO_TOKEN", err.Error())
	}

	c = base
	c.forgejoBaseURL = ""
	if err := validate(c); err == nil {
		t.Errorf("validate() must require FORGEJO_BASE_URL when ISSUE_TRACKER=forgejo")
	}
}

// TestValidate_ForgejoCodeForge verifies validate() requires
// FORGEJO_BASE_URL and FORGEJO_TOKEN when CODE_FORGE=forgejo, and accepts a
// fully configured forgejo code-forge config.
func TestValidate_ForgejoCodeForge(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.forgejoToken = "tok"

	if err := validate(c); err != nil {
		t.Fatalf("fully configured forgejo code-forge config should validate: %v", err)
	}

	c.forgejoToken = ""
	err := validate(c)
	if err == nil {
		t.Fatalf("validate() must require FORGEJO_TOKEN when CODE_FORGE=forgejo")
	}
	if !strings.Contains(err.Error(), "FORGEJO_TOKEN") {
		t.Errorf("validate() error = %q, want it to name FORGEJO_TOKEN", err.Error())
	}
}

// TestValidate_OpencodeCopilotCredential verifies that validate() gates
// credential required-ness on the Driver: the opencode Driver's
// github-copilot Provider is OAuth-only and needs OPENCODE_AUTH_CONTENT (not
// the claude credentials), other opencode Providers need neither, and the
// default claude Driver still requires a claude credential (ADR 0009
// amendment, #260).
func TestValidate_OpencodeCopilotCredential(t *testing.T) {
	c := minimalValidConfig()
	c.driver = "opencode"
	c.model = "github-copilot/claude-opus-4-8"
	c.opencodeAuthContent = ""
	if err := validate(c); err == nil {
		t.Fatal("validate() should require OPENCODE_AUTH_CONTENT for the github-copilot Provider")
	}

	c = minimalValidConfig()
	c.driver = "opencode"
	c.model = "github-copilot/claude-opus-4-8"
	c.opencodeAuthContent = "gho_test"
	c.claudeOAuthToken = ""
	c.anthropicAPIKey = ""
	if err := validate(c); err != nil {
		t.Errorf("validate() should not require claude credentials under the opencode Driver: %v", err)
	}

	c = minimalValidConfig()
	c.driver = "opencode"
	c.model = "anthropic/claude-opus-4-8"
	c.opencodeAuthContent = ""
	c.claudeOAuthToken = ""
	c.anthropicAPIKey = ""
	if err := validate(c); err != nil {
		t.Errorf("validate() should only require OPENCODE_AUTH_CONTENT for the github-copilot Provider: %v", err)
	}

	c = minimalValidConfig()
	c.claudeOAuthToken = ""
	c.anthropicAPIKey = ""
	if err := validate(c); err == nil {
		t.Fatal("validate() should still require a claude credential under the default (claude) Driver")
	}
}

// TestValidateMergeMode_AcceptsKnown verifies that validate() accepts the three
// documented MERGE_MODE values.
func TestValidateMergeMode_AcceptsKnown(t *testing.T) {
	for _, mode := range []string{"immediate", "auto", "manual"} {
		c := minimalValidConfig()
		c.mergeMode = mode
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid MERGE_MODE %q: %v", mode, err)
		}
	}
}

// TestValidateMergeMethod_RejectsUnknown verifies that validate() fails fast
// when MERGE_METHOD is set to an unrecognised value.
func TestValidateMergeMethod_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.mergeMethod = "fast-forward"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised MERGE_METHOD")
	}
}

// TestValidateMergeMethod_AcceptsKnown verifies that validate() accepts the
// three documented MERGE_METHOD values.
func TestValidateMergeMethod_AcceptsKnown(t *testing.T) {
	for _, method := range []string{"merge", "squash", "rebase"} {
		c := minimalValidConfig()
		c.mergeMethod = method
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid MERGE_METHOD %q: %v", method, err)
		}
	}
}

// TestValidateSyncMethod_RejectsUnknown verifies that validate() fails fast
// when SYNC_METHOD is set to an unrecognised value.
func TestValidateSyncMethod_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.syncMethod = "fast-forward"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised SYNC_METHOD")
	}
}

// TestValidateSyncMethod_AcceptsKnown verifies that validate() accepts the
// documented SYNC_METHOD values.
func TestValidateSyncMethod_AcceptsKnown(t *testing.T) {
	for _, method := range []string{"rebase", "merge"} {
		c := minimalValidConfig()
		c.syncMethod = method
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid SYNC_METHOD %q: %v", method, err)
		}
	}
}

// TestValidateOverlapGate_RejectsUnknown verifies that validate() fails fast
// when OVERLAP_GATE is set to an unrecognised value.
func TestValidateOverlapGate_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.overlapGate = "yolo"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised OVERLAP_GATE")
	}
}

// TestValidateOverlapGate_AcceptsKnown verifies that validate() accepts the
// two documented OVERLAP_GATE values.
func TestValidateOverlapGate_AcceptsKnown(t *testing.T) {
	for _, mode := range []string{"defer", "off"} {
		c := minimalValidConfig()
		c.overlapGate = mode
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid OVERLAP_GATE %q: %v", mode, err)
		}
	}
}

// TestValidateDriver_RejectsUnknown verifies that validate() fails fast when
// DRIVER is set to an unregistered Driver name.
func TestValidateDriver_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.driver = "bogus"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised DRIVER")
	}
}

// TestValidateDriver_AcceptsKnownAndEmpty verifies that validate() accepts
// the registered "claude" Driver as well as an empty DRIVER (which defaults
// to "claude").
func TestValidateDriver_AcceptsKnownAndEmpty(t *testing.T) {
	for _, d := range []string{"claude", ""} {
		c := minimalValidConfig()
		c.driver = d
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid DRIVER %q: %v", d, err)
		}
	}
}

// TestValidateIssueTracker_RejectsUnknown verifies that validate() fails fast
// when ISSUE_TRACKER is set to an unrecognised value.
func TestValidateIssueTracker_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.issueTracker = "jira"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised ISSUE_TRACKER")
	}
}

// TestValidateIssueTracker_AcceptsKnown verifies that validate() accepts the
// two documented ISSUE_TRACKER values.
func TestValidateIssueTracker_AcceptsKnown(t *testing.T) {
	for _, tracker := range []string{"github", "local"} {
		c := minimalValidConfig()
		c.issueTracker = tracker
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid ISSUE_TRACKER %q: %v", tracker, err)
		}
	}
}

// TestNewIssueTracker_Local verifies that ISSUE_TRACKER=local selects a
// tracker reading from localIssuesDir instead of the GitHub gh-exec adapter.
func TestNewIssueTracker_Local(t *testing.T) {
	dir := t.TempDir()
	issueFile := `---
title: Fix the thing
state: ready-for-agent
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(dir, "fix-thing.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	c := minimalValidConfig()
	c.issueTracker = "local"
	c.localIssuesDir = dir
	c.label = "ready-for-agent"

	it := newIssueTracker(c)
	issues, err := it.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "fix-thing" {
		t.Errorf("ListIssues = %+v, want [fix-thing]", issues)
	}
}

// TestValidateCodeForge_RejectsUnknown verifies that validate() fails fast when
// CODE_FORGE is set to an unrecognised value.
func TestValidateCodeForge_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "gitlab"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised CODE_FORGE")
	}
}

// TestValidateCodeForge_RejectsUnknown_ExactMessage verifies validate()'s
// exact CODE_FORGE-invalid error string, so a registry-driven rewrite of the
// CODE_FORGE switch (issue #2267) can't silently drift the message text. The
// "must be ..." list is rendered from validCodeForgeNames() (issue #2520
// slice 4), so its word order tracks backendRows' declaration order
// (github, forgejo, jira, local, git -- jira excluded, ValidAsCodeForge is
// false) rather than a hand-typed literal. want now also pins the remedy
// line, because the row's Remedy now reaches validate()'s error text (issue
// #2886).
func TestValidateCodeForge_RejectsUnknown_ExactMessage(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "gitlab"
	err := validate(c)
	if err == nil {
		t.Fatal("validate() should reject unrecognised CODE_FORGE")
	}
	want := "CODE_FORGE=\"gitlab\" is not valid; must be github, forgejo, local, or git" +
		"\nremedy: set CODE_FORGE to a supported value and fill in any forge-specific fields it requires"
	if err.Error() != want {
		t.Errorf("validate() error = %q, want %q", err.Error(), want)
	}
}

// TestValidateCodeForge_RejectsUnknown_RecoversRemedyErrorViaErrorsAs proves
// the doctor.RemedyError seam validate() returns for the launcherCrossKnobChecks
// call site (main.go's second doctor.RunRequiredFailFast call) survives as a
// structured value, not just as text a caller has to re-parse: errors.As
// recovers the *doctor.RemedyError, and its Check.Name identifies exactly
// which row failed (issue #2886).
func TestValidateCodeForge_RejectsUnknown_RecoversRemedyErrorViaErrorsAs(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "gitlab"
	err := validate(c)

	var remedyErr *doctor.RemedyError
	if !errors.As(err, &remedyErr) {
		t.Fatalf("validate() error = %v, want errors.As to recover a *doctor.RemedyError", err)
	}
	if remedyErr.Check.Name != "code-forge-config" {
		t.Errorf("remedyErr.Check.Name = %q, want %q", remedyErr.Check.Name, "code-forge-config")
	}
}

// TestValidate_RequiredKnobFailure_IncludesRemedy covers the OTHER
// doctor.RunRequiredFailFast call site (main.go's launcherRequiredKnobChecks
// group) than the CODE_FORGE tests above. It uses the driver-credentials row
// because its Remedy text differs from its Probe error text, so the
// repeats-the-error-text suppression rule doesn't eat the remedy line.
func TestValidate_RequiredKnobFailure_IncludesRemedy(t *testing.T) {
	c := minimalValidConfig()
	c.claudeOAuthToken = ""
	c.anthropicAPIKey = ""

	err := validate(c)
	if err == nil {
		t.Fatal("validate() should reject a claude DRIVER with no credential set")
	}
	wantRemedy := checkByName(t, launcherRequiredKnobChecks(c), "driver-credentials").Remedy
	if !strings.Contains(err.Error(), "\nremedy: "+wantRemedy) {
		t.Errorf("validate() error = %q, want it to contain remedy line %q", err.Error(), wantRemedy)
	}
}

// TestValidate_RegistryProxyRoutesFailure_IncludesRemedy covers the third
// cross-knob row -- the one issue #2886 was raised about -- reaching
// validate()'s error text with its Remedy: unlike issue-tracker-config and
// code-forge-config, registry-proxy-routes is wired in from cmd/launcher
// itself (launcherCrossKnobDeps), so its Remedy travels a path
// internal/launcherchecks' rows don't.
func TestValidate_RegistryProxyRoutesFailure_IncludesRemedy(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = filepath.Join(t.TempDir(), "absent-routes.toml")

	err := validate(c)
	if err == nil {
		t.Fatal("validate() should reject an unreadable REGISTRY_PROXY_ROUTES_FILE")
	}
	wantRemedy := checkByName(t, launcherCrossKnobChecks(c), registryProxyRoutesCheckName).Remedy
	if !strings.Contains(err.Error(), "\nremedy: "+wantRemedy) {
		t.Errorf("validate() error = %q, want it to contain remedy line %q", err.Error(), wantRemedy)
	}
}

// TestValidateCodeForge_Git_RequiresRemoteURL verifies that validate() fails
// fast when CODE_FORGE=git but no remote URL is configured — the git Code
// Forge has nothing to clone from or push to without one.
func TestValidateCodeForge_Git_RequiresRemoteURL(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = ""
	err := validate(c)
	if err == nil {
		t.Fatal("validate() should require CODE_FORGE_REMOTE_URL when CODE_FORGE=git")
	}
	if !strings.Contains(err.Error(), "CODE_FORGE_REMOTE_URL") {
		t.Errorf("error should mention CODE_FORGE_REMOTE_URL, got: %v", err)
	}
}

// TestValidateCodeForge_AcceptsKnown verifies that validate() accepts both
// documented CODE_FORGE values.
func TestValidateCodeForge_AcceptsKnown(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	if err := validate(c); err != nil {
		t.Errorf("validate() rejected CODE_FORGE=github: %v", err)
	}

	c = minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/owner/repo.git"
	if err := validate(c); err != nil {
		t.Errorf("validate() rejected valid CODE_FORGE=git config: %v", err)
	}

	c = minimalValidLocalConfig()
	if err := validate(c); err != nil {
		t.Errorf("validate() rejected valid CODE_FORGE=local config: %v", err)
	}
}

// TestValidateCodeForge_Local_AcceptsUnsetAccumulationRepoDir verifies that
// validate() no longer requires CODE_FORGE_ACCUMULATION_REPO_DIR under
// CODE_FORGE=local (issue #1726): loadConfig() now defaults it to
// .spindrift/accum.git, so an empty value here (as a hand-built config, or a
// validate() call before loadConfig()'s default runs) must not fail fast.
func TestValidateCodeForge_Local_AcceptsUnsetAccumulationRepoDir(t *testing.T) {
	c := minimalValidLocalConfig()
	c.codeForgeAccumulationRepoDir = ""
	if err := validate(c); err != nil {
		t.Errorf("validate() rejected CODE_FORGE=local with CODE_FORGE_ACCUMULATION_REPO_DIR unset: %v", err)
	}
}

// TestValidateCodeForge_Local_RequiresImmediateMergeMode verifies that
// validate() fails fast when CODE_FORGE=local is paired with any MERGE_MODE
// other than immediate — only immediate relays the seam bundle into the
// Accumulation repo; manual and auto strand it in the outbox (issue #1725).
func TestValidateCodeForge_Local_RequiresImmediateMergeMode(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		c := minimalValidLocalConfig()
		c.mergeMode = mode
		if err := validate(c); err == nil {
			t.Errorf("validate() should reject CODE_FORGE=local with MERGE_MODE=%s", mode)
		}
	}

	c := minimalValidLocalConfig()
	if err := validate(c); err != nil {
		t.Errorf("validate() rejected CODE_FORGE=local with MERGE_MODE=immediate: %v", err)
	}
}

// TestValidateBoxForgeAndIssueAccess_RejectsUnknown verifies that validate()
// fails fast when BOX_FORGE_AND_ISSUE_ACCESS is set to an unrecognised value.
func TestValidateBoxForgeAndIssueAccess_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only-ish"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised BOX_FORGE_AND_ISSUE_ACCESS")
	}
}

// TestValidateBoxForgeAndIssueAccess_AcceptsKnown verifies that validate()
// accepts the two documented BOX_FORGE_AND_ISSUE_ACCESS values.
func TestValidateBoxForgeAndIssueAccess_AcceptsKnown(t *testing.T) {
	for _, mode := range []string{"read-write", "read-only"} {
		c := minimalValidLocalConfig()
		c.boxForgeAndIssueAccess = mode
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid BOX_FORGE_AND_ISSUE_ACCESS %q: %v", mode, err)
		}
	}
}

// TestNewCodeForge_Git_ReturnsPushOnlyAdapter verifies that CODE_FORGE=git
// wires newCodeForge to the push-only git adapter — one with no PRForge
// surface at all — instead of the github gh-exec adapter.
func TestNewCodeForge_Git_ReturnsPushOnlyAdapter(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/owner/repo.git"

	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	if _, ok := cf.(forge.PRForge); ok {
		t.Error("newCodeForge(CODE_FORGE=git) satisfies PRForge, want the push-only git adapter to implement CodeForge only")
	}
}

// TestNewCodeForge_Forgejo_IsPRForge verifies that CODE_FORGE=forgejo wires
// newCodeForge to the Forgejo adapter, which satisfies forge.PRForge — the
// second full-parity PRForge backend beside github (issue #1961): it opens
// PRs, watches CI, and drives merge/auto-merge/draft-ready through the same
// seam.
func TestNewCodeForge_Forgejo_IsPRForge(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.forgejoToken = "tok"

	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	if cf == nil {
		t.Fatal("newCodeForge(CODE_FORGE=forgejo) returned nil")
	}
	if _, ok := cf.(forge.PRForge); !ok {
		t.Error("newCodeForge(CODE_FORGE=forgejo) does not satisfy PRForge, want the full-parity PRForge adapter")
	}
}

// TestNewCodeForge_Local_ReturnsBundleRelayAdapter verifies that
// CODE_FORGE=local wires newCodeForge to an adapter that is push-only (no
// PRForge) but does implement the BundleRelay/LandingRef hooks the local
// landing path needs (ADR 0033) — neither the git nor the github adapter do.
func TestNewCodeForge_Local_ReturnsBundleRelayAdapter(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = filepath.Join(t.TempDir(), "repo.git")

	cf := newCodeForge(c, local.ResolveParent("1694", ""), nil)

	if _, ok := cf.(forge.PRForge); ok {
		t.Error("newCodeForge(CODE_FORGE=local) satisfies PRForge, want a push-only adapter")
	}
	if _, ok := cf.(forge.BundleRelay); !ok {
		t.Error("newCodeForge(CODE_FORGE=local) does not satisfy forge.BundleRelay")
	}
	if _, ok := cf.(forge.LandingRef); !ok {
		t.Error("newCodeForge(CODE_FORGE=local) does not satisfy forge.LandingRef")
	}
}

// TestNewCodeForge_Github_ImplementsPRForge verifies that CODE_FORGE=github
// (the default) wires newCodeForge to an adapter satisfying PRForge.
func TestNewCodeForge_Github_ImplementsPRForge(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"

	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	if _, ok := cf.(forge.PRForge); !ok {
		t.Error("newCodeForge(CODE_FORGE=github) does not satisfy PRForge")
	}
}

// TestNewCodeForge_GithubReadOnly_ImplementsBundleRelay verifies that
// CODE_FORGE=github with BOX_FORGE_AND_ISSUE_ACCESS=read-only wires
// newCodeForge to an adapter satisfying forge.BundleRelay in addition to
// PRForge (issue #1918) -- the Box no longer pushes in-box, so the launcher
// needs the bundle-relay hand-off settle's merge gate already knows to look
// for via type assertion.
func TestNewCodeForge_GithubReadOnly_ImplementsBundleRelay(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	c.boxForgeAndIssueAccess = "read-only"

	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	if _, ok := cf.(forge.BundleRelay); !ok {
		t.Error("newCodeForge(CODE_FORGE=github, BOX_FORGE_AND_ISSUE_ACCESS=read-only) does not satisfy forge.BundleRelay")
	}
	if _, ok := cf.(forge.PRForge); !ok {
		t.Error("newCodeForge(CODE_FORGE=github, BOX_FORGE_AND_ISSUE_ACCESS=read-only) does not satisfy forge.PRForge")
	}
}

// TestNewCodeForge_GithubReadWrite_DoesNotImplementBundleRelay verifies the
// default BOX_FORGE_AND_ISSUE_ACCESS=read-write keeps today's github adapter
// byte-for-byte: it must never satisfy forge.BundleRelay, or settle's
// generic relay-before-merge (ready.go) would try to relay a bundle a
// read-write Box never wrote.
func TestNewCodeForge_GithubReadWrite_DoesNotImplementBundleRelay(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	c.boxForgeAndIssueAccess = "read-write"

	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	if _, ok := cf.(forge.BundleRelay); ok {
		t.Error("newCodeForge(CODE_FORGE=github, BOX_FORGE_AND_ISSUE_ACCESS=read-write) satisfies forge.BundleRelay, want it hidden")
	}
}

// TestNewCodeForge_GithubReadOnly_SatisfiesCapabilityGate verifies that
// CODE_FORGE=github + ISSUE_TRACKER=github (the default) +
// BOX_FORGE_AND_ISSUE_ACCESS=read-only passes checkReadOnlyCapabilityGate:
// github's backend registry row (issue #2526) carries RelayCapable and
// HostPostingCapable, so the gate — now a registry lookup by name rather
// than a live interface assertion against a constructed cf/it (issue #2526
// slice 3) — accepts it. newCodeForge's production wiring is exercised here
// too (proving it constructs without error for this config), even though
// the gate itself no longer inspects the value it returns.
func TestNewCodeForge_GithubReadOnly_SatisfiesCapabilityGate(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	c.boxForgeAndIssueAccess = "read-only"
	_ = newCodeForge(c, local.SanitizedParent{}, nil)

	if err := checkReadOnlyCapabilityGate(c); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() with CODE_FORGE=github/ISSUE_TRACKER=github, read-only = %v, want nil", err)
	}
}

// TestNewCodeForge_ForgejoReadOnly_SatisfiesBundleRelayAndDraftPRCreator
// verifies that CODE_FORGE=forgejo with BOX_FORGE_AND_ISSUE_ACCESS=read-only
// wires newCodeForge to the read-only Forgejo wrapper
// (forgejo.NewReadOnlyForgejoCodeForge), which satisfies both
// forge.BundleRelay and forge.DraftPRCreator -- the same host-mediation
// seams github's read-only wrapper already provides, mirrored for the
// second full-parity PRForge backend (issue #1964).
func TestNewCodeForge_ForgejoReadOnly_SatisfiesBundleRelayAndDraftPRCreator(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.forgejoToken = "tok"
	c.boxForgeAndIssueAccess = "read-only"

	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	if _, ok := cf.(forge.BundleRelay); !ok {
		t.Error("newCodeForge(CODE_FORGE=forgejo, BOX_FORGE_AND_ISSUE_ACCESS=read-only) does not satisfy forge.BundleRelay")
	}
	if _, ok := cf.(forge.DraftPRCreator); !ok {
		t.Error("newCodeForge(CODE_FORGE=forgejo, BOX_FORGE_AND_ISSUE_ACCESS=read-only) does not satisfy forge.DraftPRCreator")
	}
}

// TestNewCodeForge_ForgejoReadWrite_DoesNotImplementBundleRelayOrDraftPRCreator
// verifies the default BOX_FORGE_AND_ISSUE_ACCESS=read-write keeps today's
// plain Forgejo adapter byte-for-byte: it must satisfy neither
// forge.BundleRelay nor forge.DraftPRCreator, or settle's generic
// relay-before-merge (ready.go) would try to relay a bundle a read-write Box
// never wrote.
func TestNewCodeForge_ForgejoReadWrite_DoesNotImplementBundleRelayOrDraftPRCreator(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.forgejoToken = "tok"
	c.boxForgeAndIssueAccess = "read-write"

	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	if _, ok := cf.(forge.BundleRelay); ok {
		t.Error("newCodeForge(CODE_FORGE=forgejo, BOX_FORGE_AND_ISSUE_ACCESS=read-write) satisfies forge.BundleRelay, want it hidden")
	}
	if _, ok := cf.(forge.DraftPRCreator); ok {
		t.Error("newCodeForge(CODE_FORGE=forgejo, BOX_FORGE_AND_ISSUE_ACCESS=read-write) satisfies forge.DraftPRCreator, want it hidden")
	}
}

// TestNewCodeForge_LocalReadOnly_ReturnsPlainAdapter verifies that
// CODE_FORGE=local ignores BOX_FORGE_AND_ISSUE_ACCESS=read-only entirely:
// local never had a distinct read-only CodeForge constructor, so read-only
// falls through to the same plain adapter as read-write (unlike github and
// forgejo, which swap in a dedicated read-only wrapper).
func TestNewCodeForge_LocalReadOnly_ReturnsPlainAdapter(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = filepath.Join(t.TempDir(), "repo.git")
	c.boxForgeAndIssueAccess = "read-only"

	cf := newCodeForge(c, local.ResolveParent("1694", ""), nil)

	if cf == nil {
		t.Fatal("newCodeForge(CODE_FORGE=local, BOX_FORGE_AND_ISSUE_ACCESS=read-only) returned nil")
	}
	if _, ok := cf.(forge.BundleRelay); !ok {
		t.Error("newCodeForge(CODE_FORGE=local, BOX_FORGE_AND_ISSUE_ACCESS=read-only) does not satisfy forge.BundleRelay")
	}
	if _, ok := cf.(forge.PRForge); ok {
		t.Error("newCodeForge(CODE_FORGE=local, BOX_FORGE_AND_ISSUE_ACCESS=read-only) satisfies PRForge, want a push-only adapter")
	}
}

// TestNewCodeForge_GitReadOnly_ReturnsPlainAdapter mirrors
// TestNewCodeForge_LocalReadOnly_ReturnsPlainAdapter for CODE_FORGE=git:
// git also has no distinct read-only CodeForge constructor, so read-only
// falls through to the same push-only adapter as read-write.
func TestNewCodeForge_GitReadOnly_ReturnsPlainAdapter(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/owner/repo.git"
	c.boxForgeAndIssueAccess = "read-only"

	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	if cf == nil {
		t.Fatal("newCodeForge(CODE_FORGE=git, BOX_FORGE_AND_ISSUE_ACCESS=read-only) returned nil")
	}
	if _, ok := cf.(forge.PRForge); ok {
		t.Error("newCodeForge(CODE_FORGE=git, BOX_FORGE_AND_ISSUE_ACCESS=read-only) satisfies PRForge, want a push-only adapter")
	}
}

// TestDispatchCompletionBanner_Github verifies that CODE_FORGE=github keeps
// the "branches pushed and PRs opened" wording, since it's the only forge
// that opens PRs (issue #1733).
func TestDispatchCompletionBanner_Github(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	c.repoSlug = "owner/repo"

	got := dispatchCompletionBanner(c)

	want := "==> all agents finished — branches pushed and PRs opened on owner/repo.\n"
	if got != want {
		t.Errorf("dispatchCompletionBanner(github) = %q, want %q", got, want)
	}
}

// TestDispatchCompletionBanner_Git verifies that CODE_FORGE=git reports
// branches pushed but drops the PR claim — the git adapter is push-only and
// never opens a PR (issue #1733).
func TestDispatchCompletionBanner_Git(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.repoSlug = "owner/repo"
	c.codeForgeRemoteURL = "https://git.example.com/owner/repo.git"

	got := dispatchCompletionBanner(c)

	want := "==> all agents finished — branches pushed on owner/repo.\n"
	if got != want {
		t.Errorf("dispatchCompletionBanner(git) = %q, want %q", got, want)
	}
}

// TestDispatchCompletionBanner_Local verifies that CODE_FORGE=local claims
// neither a push nor a PR — the launcher lands seams host-side onto the
// Accumulation repo's Integration branch instead (ADR 0033, issue #1733).
// It names no single branch: each seam resolves its own Integration branch
// from its own parent: frontmatter (issue #1734), so a mixed-parent batch
// may land onto several in the same run.
func TestDispatchCompletionBanner_Local(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.mergeMode = "immediate"

	got := dispatchCompletionBanner(c)

	want := "==> all agents finished — seams landed host-side into their own Integration branches in the Accumulation repo.\n"
	if got != want {
		t.Errorf("dispatchCompletionBanner(local) = %q, want %q", got, want)
	}
}

// TestDispatchConfig_NoDocument_UsesGuardedResolvers verifies dispatchConfig
// wires resolveTrackerAndForgeSignals/resolveAgentPresenceSignals rather
// than the old unguarded docArtifact(...) reads: with no loaded document,
// WorkerProvisioned must come back true (WORKER_MODEL's schema default is
// non-empty), not the old bug's unconditional false, and the tracker-axis/
// forge-backend strings must come back the fresh github/github computation,
// not empty (issue #2533 review).
func TestDispatchConfig_NoDocument_UsesGuardedResolvers(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	// Isolate from whatever this test process's own ambient environment
	// happens to carry (e.g. a dispatched Box's own ORCHESTRATOR_ENABLED),
	// so the schema-default fallback this test pins is deterministic
	// regardless of host.
	t.Setenv("FILER_MODEL", "")
	t.Setenv("WORKER_MODEL", "")
	t.Setenv("ORCHESTRATOR_ENABLED", "")

	cf := forge.NewFake()
	it := forge.NewFake()
	cfg := dispatchConfig(minimalValidConfig(), it, testWired(it), cf, forge.Capabilities{})

	if !cfg.WorkerProvisioned {
		t.Error("WorkerProvisioned = false, want true (WORKER_MODEL schema default is non-empty)")
	}
	if !cfg.ReviewLoopInline || cfg.ReviewLoopOrchestrator {
		t.Errorf("ReviewLoopInline=%v ReviewLoopOrchestrator=%v, want (true, false) (ORCHESTRATOR_ENABLED schema default is false)", cfg.ReviewLoopInline, cfg.ReviewLoopOrchestrator)
	}
	if cfg.TrackerAxisRead != "GITHUB" || cfg.TrackerAxisWrite != "GITHUB" || cfg.TrackerAxisFiler != "GH" {
		t.Errorf("TrackerAxis = (%q,%q,%q), want (GITHUB,GITHUB,GH) for github/github", cfg.TrackerAxisRead, cfg.TrackerAxisWrite, cfg.TrackerAxisFiler)
	}
	if cfg.ForgeBackend != "GH" {
		t.Errorf("ForgeBackend = %q, want GH for codeForge=github", cfg.ForgeBackend)
	}
}

// TestDispatchConfig_ReviewOverridesExplicitEnvOnly verifies dispatchConfig
// fills ReviewModelOverride/ReviewEffortOverride from the ambient
// environment alone (issue #3171): a loaded document's settings and the
// schema defaults (REVIEW_MODEL's is non-empty, "claude-opus-5") must never
// leak in — those values already reached the baked roster at eval time, and
// forwarding them as an override would beat that roster on every dispatch.
func TestDispatchConfig_ReviewOverridesExplicitEnvOnly(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{Settings: map[string]string{
		"REVIEW_MODEL":  "doc-model",
		"REVIEW_EFFORT": "doc-effort",
	}}
	t.Setenv("REVIEW_MODEL", "")
	t.Setenv("REVIEW_EFFORT", "")

	cf := forge.NewFake()
	it := forge.NewFake()
	cfg := dispatchConfig(minimalValidConfig(), it, testWired(it), cf, forge.Capabilities{})
	if cfg.ReviewModelOverride != "" || cfg.ReviewEffortOverride != "" {
		t.Errorf("overrides = (%q,%q), want empty when the operator set nothing at dispatch time", cfg.ReviewModelOverride, cfg.ReviewEffortOverride)
	}

	t.Setenv("REVIEW_MODEL", "env-model")
	t.Setenv("REVIEW_EFFORT", "env-effort")
	cfg = dispatchConfig(minimalValidConfig(), it, testWired(it), cf, forge.Capabilities{})
	if cfg.ReviewModelOverride != "env-model" || cfg.ReviewEffortOverride != "env-effort" {
		t.Errorf("overrides = (%q,%q), want (env-model,env-effort) from the ambient env", cfg.ReviewModelOverride, cfg.ReviewEffortOverride)
	}
}

// TestDispatchConfig_CopiesDescriptorRowsThrough proves dispatchConfig
// copies its caps forge.Capabilities argument's ForgeDescriptor/
// TrackerDescriptor rows straight into the returned dispatch.Config's own
// ForgeDescriptor/TrackerDescriptor fields, rather than dropping one or
// swapping them. dispatch.go's own buildBoxEnv/box.go's needsOutbox read
// cfg.ForgeDescriptor/TrackerDescriptor to decide outbox-relay routing
// (issue #2533 area), so a caller that built a caps value upstream (e.g.
// from resolveCapabilities) must see that exact pair survive the
// dispatchConfig hand-off untouched (issue #3063). ForgeDescriptor and
// TrackerDescriptor are set to distinct backend.Descriptor names here
// specifically so a passthrough bug that swaps the two fields, or drops
// one, is visible via reflect.DeepEqual rather than accidentally still
// comparing equal.
func TestDispatchConfig_CopiesDescriptorRowsThrough(t *testing.T) {
	caps := forge.Capabilities{
		ForgeDescriptor:   backend.Descriptor{Name: "test-forge", HostMediatedRemote: true},
		TrackerDescriptor: backend.Descriptor{Name: "test-tracker", InBoxUnreachableTracker: true},
	}

	cf := forge.NewFake()
	it := forge.NewFake()
	cfg := dispatchConfig(minimalValidConfig(), it, testWired(it), cf, caps)

	if !reflect.DeepEqual(cfg.ForgeDescriptor, caps.ForgeDescriptor) {
		t.Errorf("dispatchConfig() ForgeDescriptor = %+v, want %+v (the caps argument copied through unchanged)", cfg.ForgeDescriptor, caps.ForgeDescriptor)
	}
	if !reflect.DeepEqual(cfg.TrackerDescriptor, caps.TrackerDescriptor) {
		t.Errorf("dispatchConfig() TrackerDescriptor = %+v, want %+v (the caps argument copied through unchanged)", cfg.TrackerDescriptor, caps.TrackerDescriptor)
	}
}

// TestDispatchConfig_DivergentDocumentArtifactsDoNotReachCapabilities pins
// issue #3062: a loaded document whose Settings match the resolved
// CODE_FORGE/ISSUE_TRACKER names but whose Artifacts contradict the caps
// argument must not reach dispatchConfig's Capabilities --
// resolveCapabilitySignals trusts that document, dispatchConfig passes caps
// through unchanged. See dispatchConfig's doc comment (main.go) for why the
// two paths read capabilities differently.
func TestDispatchConfig_DivergentDocumentArtifactsDoNotReachCapabilities(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputDocument{
		Settings: map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "github"},
		Artifacts: map[string]string{
			"HOST_MEDIATED_REMOTE":       "true",
			"OUTBOX_RELAY_CAPABLE":       "false",
			"IN_BOX_UNREACHABLE_TRACKER": "true",
			"FULLY_LOCAL":                "true",
		},
	}

	sig := resolveCapabilitySignals("github", "github")
	// Fixture sanity check, not behaviour this issue changes: it confirms the
	// document really fires the fast path, so the dispatchConfig assertion
	// below is meaningful.
	if !sig.hostMediatedRemote || sig.outboxRelayCapable || !sig.inBoxUnreachableTracker || !sig.fullyLocal {
		t.Fatalf("resolveCapabilitySignals() = %+v, want the document's forwarded artifacts (true,false,true,true)", sig)
	}

	caps := forge.Capabilities{
		ForgeDescriptor:   backend.Descriptor{Name: "github", HostMediatedRemote: false, OutboxRelayCapable: true},
		TrackerDescriptor: backend.Descriptor{Name: "github", InBoxUnreachableTracker: false},
	}
	cf := forge.NewFake()
	it := forge.NewFake()
	cfg := dispatchConfig(minimalValidConfig(), it, testWired(it), cf, caps)

	if !reflect.DeepEqual(cfg.Capabilities, caps) {
		t.Errorf("dispatchConfig() Capabilities = %+v, want %+v (the registry-sourced caps argument, unaffected by the contradicting document)", cfg.Capabilities, caps)
	}
}

// TestDispatchConfig_PRForge_WiresOpenPRForIssue verifies issue #565's
// wiring: when cf implements forge.PRForge, dispatchConfig sets
// OpenPRForIssue to a closure that resolves the issue's agent branch and
// reports whether it already has an open PR.
func TestDispatchConfig_PRForge_WiresOpenPRForIssue(t *testing.T) {
	cf := forge.NewFake()
	cf.SetPR(cf.AgentBranch("42"), forge.PR{URL: "https://github.com/o/r/pull/1"})

	it := forge.NewFake()
	cfg := dispatchConfig(minimalValidConfig(), it, testWired(it), cf, forge.Capabilities{})

	if cfg.OpenPRForIssue == nil {
		t.Fatal("want OpenPRForIssue set for a PRForge-implementing Code Forge")
	}
	found, err := cfg.OpenPRForIssue("42")
	if err != nil {
		t.Fatalf("OpenPRForIssue: unexpected error: %v", err)
	}
	if !found {
		t.Error("want found=true for an issue with an open PR")
	}
	found, err = cfg.OpenPRForIssue("99")
	if err != nil {
		t.Fatalf("OpenPRForIssue: unexpected error: %v", err)
	}
	if found {
		t.Error("want found=false for an issue with no PR")
	}
}

// TestDispatchConfig_NonPRForge_OpenPRForIssueAlwaysReportsNotFound verifies
// that a push-only Code Forge (no PR lookup) still gets a non-nil
// OpenPRForIssue closure, which always reports found=false via
// forge.ResolveOpenPR's own PRForge fallback -- so a zero-exit
// rate-limited retry proceeds unguarded rather than erroring.
func TestDispatchConfig_NonPRForge_OpenPRForIssueAlwaysReportsNotFound(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://example.com/repo.git"
	cf := newCodeForge(c, local.SanitizedParent{}, nil)
	if _, ok := cf.(forge.PRForge); ok {
		t.Fatal("test setup: expected a non-PRForge Code Forge")
	}

	it := forge.NewFake()
	cfg := dispatchConfig(c, it, testWired(it), cf, forge.Capabilities{})

	if cfg.OpenPRForIssue == nil {
		t.Fatal("want OpenPRForIssue set for a non-PRForge Code Forge")
	}
	found, err := cfg.OpenPRForIssue("42")
	if err != nil {
		t.Fatalf("OpenPRForIssue: unexpected error: %v", err)
	}
	if found {
		t.Error("want found=false for a non-PRForge Code Forge")
	}
}

// TestDispatchConfig_Local_ResolveEnv_ForwardsIntegrationBranchAsBaseBranch
// verifies that under CODE_FORGE=local, dispatchConfig's ResolveEnv resolves
// BASE_BRANCH to the dispatched issue's own Integration branch
// (integration/<parent>, ADR 0033, issue #1734) once that branch exists --
// so a dependent seam's Box clones a branch that already contains its
// blocker's landed code (issue #1700). The issue has no parent: frontmatter
// set, so its resolved parent falls back to its own number.
func TestDispatchConfig_Local_ResolveEnv_ForwardsIntegrationBranchAsBaseBranch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.baseBranch = "main"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42"})
	fc.SetBranchExists("integration/42", true)
	cf := fc.AsLocal()

	cfg := dispatchConfig(c, fc, testWired(fc), cf, forge.Capabilities{ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true}})

	if got := cfg.ResolveEnv("42", "BASE_BRANCH"); got != "integration/42" {
		t.Errorf("ResolveEnv(42, BASE_BRANCH) = %q, want %q", got, "integration/42")
	}
}

// TestDispatchConfig_Local_ResolveEnv_UsesEachIssuesOwnParent verifies two
// issues dispatched in the same run resolve BASE_BRANCH from their own
// distinct parent: frontmatter (issue #1734) -- a mixed-parent batch must
// never collapse onto a single Integration branch.
func TestDispatchConfig_Local_ResolveEnv_UsesEachIssuesOwnParent(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.baseBranch = "main"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Parent: "Calc Engine"})
	fc.SetIssue(forge.Issue{Number: "11", Parent: "Render Pipeline"})
	fc.SetBranchExists("integration/calc-engine", true)
	fc.SetBranchExists("integration/render-pipeline", true)
	cf := fc.AsLocal()

	cfg := dispatchConfig(c, fc, testWired(fc), cf, forge.Capabilities{ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true}})

	if got := cfg.ResolveEnv("10", "BASE_BRANCH"); got != "integration/calc-engine" {
		t.Errorf("ResolveEnv(10, BASE_BRANCH) = %q, want %q", got, "integration/calc-engine")
	}
	if got := cfg.ResolveEnv("11", "BASE_BRANCH"); got != "integration/render-pipeline" {
		t.Errorf("ResolveEnv(11, BASE_BRANCH) = %q, want %q", got, "integration/render-pipeline")
	}
}

// TestDispatchConfig_Local_ResolveEnv_FallsBackToBaseBranchBeforeFirstLand
// verifies the other half of the same seam: a broad ticket's first (or
// wholly independent) seam dispatches before any blocker has ever landed,
// so integration/<parent> does not exist yet on the Accumulation repo --
// ensureIntegrationBranch only ever creates it host-side, from inside
// RelayBundle, once some seam actually lands. Forwarding BASE_BRANCH as
// that not-yet-existing ref would make the Box's `git checkout -b $BRANCH
// origin/$BASE_BRANCH` fail outright, so ResolveEnv must fall back to the
// operator's real base branch until BranchExists confirms the Integration
// branch is there.
func TestDispatchConfig_Local_ResolveEnv_FallsBackToBaseBranchBeforeFirstLand(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.baseBranch = "main"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42"})
	fc.SetBranchExists("integration/42", false)
	cf := fc.AsLocal()

	cfg := dispatchConfig(c, fc, testWired(fc), cf, forge.Capabilities{ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true}})

	if got := cfg.ResolveEnv("42", "BASE_BRANCH"); got != "main" {
		t.Errorf("ResolveEnv(42, BASE_BRANCH) = %q, want %q", got, "main")
	}
}

// TestDispatchConfig_Local_ResolveEnv_LoudlyFallsBackWhenBlockedSeamMissesIntegrationBranch
// verifies the complementary hardening half of issue #2130: a seam that
// DepsOf reports has blockers should never reach the resolver with its own
// Integration branch still missing -- the #2130 readiness gate is supposed
// to hold it until its blocker's work lands onto that very branch. If one
// slips through anyway, ResolveEnv still falls back to the operator's real
// base branch (the Box must clone something), but it must say so loudly on
// stdout rather than silently seeding bare base the way a genuinely
// blocker-free seam does.
func TestDispatchConfig_Local_ResolveEnv_LoudlyFallsBackWhenBlockedSeamMissesIntegrationBranch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.baseBranch = "main"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42"})
	fc.SetBranchExists("integration/42", false)
	fc.NativeDeps = map[string][]string{"42": {"41"}}
	cf := fc.AsLocal()

	cfg := dispatchConfig(c, fc, testWired(fc), cf, forge.Capabilities{ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true}})

	var got string
	out := captureStdout(t, func() {
		got = cfg.ResolveEnv("42", "BASE_BRANCH")
	})

	if got != "main" {
		t.Errorf("ResolveEnv(42, BASE_BRANCH) = %q, want %q", got, "main")
	}
	if !strings.Contains(out, "blocker(s)") {
		t.Errorf("stdout = %q, want a loud blocker diagnostic mentioning %q", out, "blocker(s)")
	}
}

// TestDispatchConfig_Local_ResolveEnv_SilentlyFallsBackWhenBlockerFreeSeamMissesIntegrationBranch
// verifies the other half stays exactly as before this hardening: a
// blocker-free (or wholly independent) seam's first dispatch, before its own
// Integration branch has ever been created, still seeds silently from the
// operator's real base branch -- no loud diagnostic, since there is nothing
// wrong to report.
func TestDispatchConfig_Local_ResolveEnv_SilentlyFallsBackWhenBlockerFreeSeamMissesIntegrationBranch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.baseBranch = "main"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42"})
	fc.SetBranchExists("integration/42", false)
	cf := fc.AsLocal()

	cfg := dispatchConfig(c, fc, testWired(fc), cf, forge.Capabilities{ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true}})

	var got string
	out := captureStdout(t, func() {
		got = cfg.ResolveEnv("42", "BASE_BRANCH")
	})

	if got != "main" {
		t.Errorf("ResolveEnv(42, BASE_BRANCH) = %q, want %q", got, "main")
	}
	if out != "" {
		t.Errorf("stdout = %q, want no diagnostic for a blocker-free seam", out)
	}
}

// TestDispatchConfig_Local_ResolveEnv_LoudlyFallsBackWhenBlockerLookupErrors
// covers the residual path AC5's blocker-count diagnostic left silent: when
// the Integration branch is missing AND DepsOf itself errors, the resolver
// cannot confirm whether the seam was blocked, so it must still fall back to
// the operator base branch but say so loudly rather than seeding bare base in
// silence -- an unknown blocker status is not the same as a known
// blocker-free one.
func TestDispatchConfig_Local_ResolveEnv_LoudlyFallsBackWhenBlockerLookupErrors(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.baseBranch = "main"
	fc := forge.NewFake()
	// Deliberately do NOT SetIssue("42"): DepsOf then returns an
	// "issue 42 not found" error, while ResolveParent falls back to the
	// seam's own slug (integration/42), which BranchExists reports absent.
	fc.SetBranchExists("integration/42", false)
	cf := fc.AsLocal()

	cfg := dispatchConfig(c, fc, testWired(fc), cf, forge.Capabilities{ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true}})

	var got string
	out := captureStdout(t, func() {
		got = cfg.ResolveEnv("42", "BASE_BRANCH")
	})

	if got != "main" {
		t.Errorf("ResolveEnv(42, BASE_BRANCH) = %q, want %q", got, "main")
	}
	if !strings.Contains(out, "checking blockers") {
		t.Errorf("stdout = %q, want a loud diagnostic mentioning %q", out, "checking blockers")
	}
}

// TestDispatchConfig_NonLocal_ResolveEnv_PassesThroughUnchanged verifies
// that localBaseBranchResolver's non-local branch forwards BASE_BRANCH
// exactly as resolveBoxEnvVar would -- CODE_FORGE=github/git never consult
// cf.BranchExists at all, unlike the CODE_FORGE=local cases above.
func TestDispatchConfig_NonLocal_ResolveEnv_PassesThroughUnchanged(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	cf := forge.NewFake()

	t.Setenv("BASE_BRANCH", "release")
	cfg := dispatchConfig(c, cf, testWired(forge.NewFake()), cf, forge.Capabilities{})

	if got := cfg.ResolveEnv("42", "BASE_BRANCH"); got != "release" {
		t.Errorf("ResolveEnv(42, BASE_BRANCH) = %q, want %q", got, "release")
	}
}

// TestDispatchConfig_ResolveEnv_BoxGHTokenOverridesGHToken verifies opt-in
// two-actor separation (ADR 0016, issue #380): when BOX_GH_TOKEN is set,
// dispatchConfig's ResolveEnv resolves the Box's GH_TOKEN to that value
// instead of the launcher's own, while the launcher's ambient GH_TOKEN stays
// untouched for its own host-side forge calls.
func TestDispatchConfig_ResolveEnv_BoxGHTokenOverridesGHToken(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	t.Setenv("GH_TOKEN", "launcher-token")
	t.Setenv("BOX_GH_TOKEN", "box-token")
	cf := forge.NewFake()

	cfg := dispatchConfig(c, cf, testWired(forge.NewFake()), cf, forge.Capabilities{})

	if got := cfg.ResolveEnv("42", "GH_TOKEN"); got != "box-token" {
		t.Errorf("ResolveEnv(42, GH_TOKEN) = %q, want %q", got, "box-token")
	}
	if got := os.Getenv("GH_TOKEN"); got != "launcher-token" {
		t.Errorf("launcher's own GH_TOKEN mutated: got %q, want %q", got, "launcher-token")
	}
}

// TestDispatchConfig_ResolveEnv_GHTokenPassthroughWhenBoxGHTokenUnset
// verifies the single-token default: with BOX_GH_TOKEN unset, ResolveEnv
// resolves GH_TOKEN exactly as before this issue -- the launcher's own
// ambient value, forwarded unchanged.
func TestDispatchConfig_ResolveEnv_GHTokenPassthroughWhenBoxGHTokenUnset(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	t.Setenv("GH_TOKEN", "launcher-token")
	cf := forge.NewFake()

	cfg := dispatchConfig(c, cf, testWired(forge.NewFake()), cf, forge.Capabilities{})

	if got := cfg.ResolveEnv("42", "GH_TOKEN"); got != "launcher-token" {
		t.Errorf("ResolveEnv(42, GH_TOKEN) = %q, want %q", got, "launcher-token")
	}
}

// TestDispatchConfig_ResolveEnv_BoxForgejoTokenOverridesForgejoToken verifies
// the Forgejo analog of BOX_GH_TOKEN (ADR 0016): when BOX_FORGEJO_TOKEN is
// set, dispatchConfig's ResolveEnv resolves the Box's FORGEJO_TOKEN to that
// value instead of the launcher's own -- the credential-withholding
// mechanism for "no forgejo write credential in the Box under read-only".
func TestDispatchConfig_ResolveEnv_BoxForgejoTokenOverridesForgejoToken(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.forgejoToken = "launcher-fj-tok"
	t.Setenv("FORGEJO_TOKEN", "launcher-fj-tok")
	t.Setenv("BOX_FORGEJO_TOKEN", "box-fj-tok")
	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	cfg := dispatchConfig(c, forge.NewFake(), testWired(forge.NewFake()), cf, forge.Capabilities{})

	if got := cfg.ResolveEnv("1", "FORGEJO_TOKEN"); got != "box-fj-tok" {
		t.Errorf("ResolveEnv(1, FORGEJO_TOKEN) = %q, want %q", got, "box-fj-tok")
	}
}

// TestDispatchConfig_ResolveEnv_BoxForgejoTokenUnsetFallsThrough verifies the
// single-token default: with BOX_FORGEJO_TOKEN unset, ResolveEnv resolves
// FORGEJO_TOKEN exactly as before this issue -- the launcher's own ambient
// value, forwarded unchanged.
func TestDispatchConfig_ResolveEnv_BoxForgejoTokenUnsetFallsThrough(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.forgejoToken = "launcher-fj-tok"
	t.Setenv("FORGEJO_TOKEN", "launcher-fj-tok")
	t.Setenv("BOX_FORGEJO_TOKEN", "")
	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	cfg := dispatchConfig(c, forge.NewFake(), testWired(forge.NewFake()), cf, forge.Capabilities{})

	if got := cfg.ResolveEnv("1", "FORGEJO_TOKEN"); got != "launcher-fj-tok" {
		t.Errorf("ResolveEnv(1, FORGEJO_TOKEN) = %q, want %q", got, "launcher-fj-tok")
	}
}

// TestDispatchConfig_ResolveEnv_BoxForgejoTokenDoesNotAffectOtherNames
// verifies the override is scoped to the FORGEJO_TOKEN name only -- a
// BOX_FORGEJO_TOKEN set in the environment must not leak into GH_TOKEN or
// any other resolved name.
func TestDispatchConfig_ResolveEnv_BoxForgejoTokenDoesNotAffectOtherNames(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.forgejoToken = "launcher-fj-tok"
	t.Setenv("GH_TOKEN", "launcher-gh-tok")
	t.Setenv("BOX_FORGEJO_TOKEN", "box-fj-tok")
	cf := newCodeForge(c, local.SanitizedParent{}, nil)

	cfg := dispatchConfig(c, forge.NewFake(), testWired(forge.NewFake()), cf, forge.Capabilities{})

	if got := cfg.ResolveEnv("1", "GH_TOKEN"); got != "launcher-gh-tok" {
		t.Errorf("ResolveEnv(1, GH_TOKEN) = %q, want %q", got, "launcher-gh-tok")
	}
}

// TestDispatchConfig_ResolveEnv_JiraTokenFallsThroughUntouched verifies
// boxTokenResolver's registry walk (issue #2267) leaves a token name with no
// registered boxTokenEnvVar -- jira's row carries a tokenEnvVar but no
// boxTokenEnvVar, since jira is tracker-only and has no Box-side override
// knob -- to fall straight through to next unchanged, exactly like any other
// non-overridden name.
func TestDispatchConfig_ResolveEnv_JiraTokenFallsThroughUntouched(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	t.Setenv("JIRA_TOKEN", "launcher-jira-tok")
	cf := forge.NewFake()

	cfg := dispatchConfig(c, cf, testWired(forge.NewFake()), cf, forge.Capabilities{})

	if got := cfg.ResolveEnv("1", "JIRA_TOKEN"); got != "launcher-jira-tok" {
		t.Errorf("ResolveEnv(1, JIRA_TOKEN) = %q, want %q", got, "launcher-jira-tok")
	}
}

// TestDispatchConfig_Local_ResolveEnv_BoxGHTokenOverridesGHToken verifies
// the BOX_GH_TOKEN override applies under CODE_FORGE=local too -- it is a
// host-side control signal independent of Code Forge, unlike BASE_BRANCH's
// local-only Integration-branch substitution.
func TestDispatchConfig_Local_ResolveEnv_BoxGHTokenOverridesGHToken(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.baseBranch = "main"
	t.Setenv("GH_TOKEN", "launcher-token")
	t.Setenv("BOX_GH_TOKEN", "box-token")
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42"})
	cf := fc.AsLocal()

	cfg := dispatchConfig(c, fc, testWired(fc), cf, forge.Capabilities{ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true}})

	if got := cfg.ResolveEnv("42", "GH_TOKEN"); got != "box-token" {
		t.Errorf("ResolveEnv(42, GH_TOKEN) = %q, want %q", got, "box-token")
	}
}

// TestDispatchConfig_Local_ResolveEnv_FallsBackToBaseBranchOnBranchExistsError
// verifies that a BranchExists failure (e.g. the Accumulation repo path is
// unreadable) falls back to the operator's real base branch rather than
// forwarding a ref that was never confirmed to exist -- the same safe
// posture as "not found", just reached through the error return instead of
// exists=false.
func TestDispatchConfig_Local_ResolveEnv_FallsBackToBaseBranchOnBranchExistsError(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.baseBranch = "main"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42"})
	fc.BranchExistsErr = errors.New("repo path unreadable")
	cf := fc.AsLocal()

	cfg := dispatchConfig(c, fc, testWired(fc), cf, forge.Capabilities{ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true}})

	if got := cfg.ResolveEnv("42", "BASE_BRANCH"); got != "main" {
		t.Errorf("ResolveEnv(42, BASE_BRANCH) = %q, want %q", got, "main")
	}
}

// createIntegrationBranchForTest points newBranch at fromBranch's current
// tip inside the bare repo at bare, standing in for an earlier seam already
// having landed — settleConfig's CodeForgeForIssue only needs a real ref to
// resolve against, not an actual Merge. Returns the resolved sha.
func createIntegrationBranchForTest(t *testing.T, bare, fromBranch, newBranch string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", bare, "rev-parse", "refs/heads/"+fromBranch).CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse %s: %v: %s", fromBranch, err, out)
	}
	sha := strings.TrimSpace(string(out))
	if out, err := exec.Command("git", "-C", bare, "update-ref", "refs/heads/"+newBranch, sha).CombinedOutput(); err != nil {
		t.Fatalf("update-ref %s: %v: %s", newBranch, err, out)
	}
	return sha
}

// TestSettleConfig_Local_CodeForgeForIssueResolvesEachIssuesOwnParent
// verifies settleConfig wires Config.CodeForgeForIssue so that mergeImmediate
// lands each dispatched issue through ITS OWN resolved parent's CodeForge
// instance (ADR 0033, issue #1734) — a mixed-parent batch must never
// collapse onto a single Integration branch the way the removed
// CODE_FORGE_INTEGRATION_PARENT knob did.
// TestSettleConfig_ReadOnlyThreadsFromBoxForgeAndIssueAccess verifies
// settleConfig's ReadOnly field mirrors c.boxForgeAndIssueAccess (issue
// #1917): "read-only" threads true, and the "read-write" default (every
// pre-existing config) threads false, so settle.Settle's blocked-note relay
// gate sees the mode directly.
func TestSettleConfig_ReadOnlyThreadsFromBoxForgeAndIssueAccess(t *testing.T) {
	fc := forge.NewFake()

	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	sc := settleConfig(c, localloop.Wire(localloopConfig(c), fc), fc, forge.Capabilities{})
	if !sc.ReadOnly {
		t.Error("settleConfig(read-only).ReadOnly = false, want true")
	}

	cRW := minimalValidConfig()
	scRW := settleConfig(cRW, localloop.Wire(localloopConfig(cRW), fc), fc, forge.Capabilities{})
	if scRW.ReadOnly {
		t.Error("settleConfig(read-write default).ReadOnly = true, want false")
	}
}

// TestSettleConfig_BaseBranchThreadsFromConfig verifies settleConfig's
// BaseBranch field mirrors c.baseBranch (issue #1919): settle's
// hostMediateDraftPR needs the target branch to open a read-only Box's
// host-mediated draft PR against, the same base the Box's own in-box
// `gh pr create` would otherwise have passed as --base.
func TestSettleConfig_BaseBranchThreadsFromConfig(t *testing.T) {
	fc := forge.NewFake()

	c := minimalValidConfig()
	c.baseBranch = "main"
	sc := settleConfig(c, localloop.Wire(localloopConfig(c), fc), fc, forge.Capabilities{})
	if sc.BaseBranch != "main" {
		t.Errorf("settleConfig(baseBranch=main).BaseBranch = %q, want %q", sc.BaseBranch, "main")
	}
}

// TestWavesConfig_WiresTransientRetryKnobs verifies wavesConfig threads
// c.transientRetryMax and c.transientBackoffSecs into waves.Config's Policy
// field (issue #2866, #2928) — the same launcher-wide
// TRANSIENT_RETRY_MAX/TRANSIENT_BACKOFF_SECS knob dispatch's exit-retry path
// and settleConfig already thread, now reaching RunContinuous's
// rate-limited re-discover retry loop too.
func TestWavesConfig_WiresTransientRetryKnobs(t *testing.T) {
	c := minimalValidConfig()
	c.transientRetryMax = 5
	c.transientBackoffSecs = 10

	wc := wavesConfig(c)
	if wc.Policy.Max != 5 {
		t.Errorf("wavesConfig(c).Policy.Max = %d, want 5", wc.Policy.Max)
	}
	if wc.Policy.Unit != 10*time.Second {
		t.Errorf("wavesConfig(c).Policy.Unit = %v, want %v", wc.Policy.Unit, 10*time.Second)
	}
}

// TestRetryPolicy_ConvertsSecondsToDuration verifies retryPolicy converts all
// three transient-retry knobs into a retry.Policy, in particular that
// transientBackoffSecs and holdJitterSecs are each scaled by time.Second
// rather than passed through as raw seconds-as-nanoseconds (issue #2928).
func TestRetryPolicy_ConvertsSecondsToDuration(t *testing.T) {
	c := minimalValidConfig()
	c.transientRetryMax = 7
	c.transientBackoffSecs = 11
	c.holdJitterSecs = 13

	p := retryPolicy(c)
	if p.Max != 7 {
		t.Errorf("retryPolicy(c).Max = %d, want 7", p.Max)
	}
	if p.Unit != 11*time.Second {
		t.Errorf("retryPolicy(c).Unit = %v, want %v", p.Unit, 11*time.Second)
	}
	if p.Jitter != 13*time.Second {
		t.Errorf("retryPolicy(c).Jitter = %v, want %v", p.Jitter, 13*time.Second)
	}
}

// TestNewSettle_ResearchReadOnly_RelaysVerdictComment verifies that newSettle,
// for the research dispatch kind under BOX_FORGE_AND_ISSUE_ACCESS=read-only,
// wires a ResearchSettle that relays the SPINDRIFT_COMMENT verdict via
// it.Comment for a github-shaped tracker (AsNoLandingRecorder) — the same
// host-mediated posting local always got, now driven by the read-only mode
// rather than the tracker's shape (issue #1917).
func TestNewSettle_ResearchReadOnly_RelaysVerdictComment(t *testing.T) {
	c := minimalValidConfig()
	c.dispatchKind = dispatchKindResearch
	c.boxForgeAndIssueAccess = "read-only"

	fc := forge.NewFake(forge.ResearchDispatchLabels())
	fc.VerdictLabels = forge.ResearchVerdictLabels()
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{"agent-research-in-progress"}})
	ghLike := fc.AsNoLandingRecorder()

	s := newSettle(c, ghLike, nil, nil, forge.Capabilities{})
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded in code"},
		},
		Comment:      "**Verdict** — recommend",
		CommentFound: true,
	}
	s.Settle(dispatch.NewFake(), "42", 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment relayed for a github-shaped tracker under read-only, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
	if fc.CommentCalls[0].Body != result.Comment {
		t.Errorf("comment body: got %q, want %q", fc.CommentCalls[0].Body, result.Comment)
	}
}

func TestSettleConfig_Local_CodeForgeForIssueResolvesEachIssuesOwnParent(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
	repo := forgetest.NewGitRepoFixture(t, "main")
	c := minimalValidLocalConfig()
	c.codeForgeAccumulationRepoDir = repo.Bare
	c.baseBranch = "main"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Parent: "Calc Engine"})
	fc.SetIssue(forge.Issue{Number: "11", Parent: "Render Pipeline"})

	sc := settleConfig(c, localloop.Wire(localloopConfig(c), fc), fc.AsLocal(), forge.Capabilities{ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true}})
	if sc.CodeForgeForIssue == nil {
		t.Fatal("settleConfig(CODE_FORGE=local).CodeForgeForIssue is nil")
	}

	lr10, ok := sc.CodeForgeForIssue("10").(forge.LandingRef)
	if !ok {
		t.Fatal("issue 10's resolved CodeForge does not implement forge.LandingRef")
	}
	lr11, ok := sc.CodeForgeForIssue("11").(forge.LandingRef)
	if !ok {
		t.Fatal("issue 11's resolved CodeForge does not implement forge.LandingRef")
	}

	sha10 := createIntegrationBranchForTest(t, repo.Bare, "main", "integration/calc-engine")
	sha11 := createIntegrationBranchForTest(t, repo.Bare, "main", "integration/render-pipeline")

	landing10, err := lr10.LandingRef()
	if err != nil {
		t.Fatalf("issue 10 LandingRef: %v", err)
	}
	if want := "integration/calc-engine@" + sha10; landing10 != want {
		t.Errorf("issue 10 LandingRef = %q, want %q", landing10, want)
	}
	landing11, err := lr11.LandingRef()
	if err != nil {
		t.Fatalf("issue 11 LandingRef: %v", err)
	}
	if want := "integration/render-pipeline@" + sha11; landing11 != want {
		t.Errorf("issue 11 LandingRef = %q, want %q", landing11, want)
	}
}

// TestSettleConfig_CapabilitiesThreadsFromReadContext proves the read-tier
// Capabilities value (resolved once by newReadContext, issue #2944/#2945)
// reaches settleConfig's Config.Capabilities unchanged, rather than
// settleConfig silently leaving Config.Capabilities at its zero value —
// which would leave settle.New's pr/landing nil even though the read tier
// resolved real handles (the regression this slice closes). Exercises a
// fully-local fixture (ISSUE_TRACKER=local, CODE_FORGE=local), the same
// pairing TestNewReadContext_FullyLocal_ResolvesCapabilities already proves
// resolves a non-nil LandingRecorder — the same real handle must survive
// the trip through settleConfig.
func TestSettleConfig_CapabilitiesThreadsFromReadContext(t *testing.T) {
	setFullyLocalEnv(t)

	rc := newReadContext(dispatchKindWork, false)
	if rc.capabilities.LandingRecorder == nil {
		t.Fatal("rc.capabilities.LandingRecorder = nil, want non-nil for a local IssueTracker (precondition)")
	}

	lw := localloop.Wire(localloopConfig(rc.config), rc.issueTracker)
	sc := settleConfig(rc.config, lw, rc.codeForge, rc.capabilities)

	if sc.Capabilities.LandingRecorder == nil {
		t.Error("settleConfig(...).Capabilities.LandingRecorder = nil, want the same non-nil handle newReadContext resolved")
	}
	if !reflect.DeepEqual(sc.Capabilities, rc.capabilities) {
		t.Errorf("settleConfig(...).Capabilities = %+v, want rc.capabilities unchanged = %+v", sc.Capabilities, rc.capabilities)
	}
}

// minimalValidConfig returns a config that passes validate() so tests can
// mutate exactly one field at a time.
// minimalValidLocalConfig returns a minimalValidConfig() wired for a valid
// CODE_FORGE=local run (accumulation dir and the only merge mode local
// accepts), so local-specific tests only need to override the one field
// under test. validate() derives its capability signals fresh via
// resolveCapabilitySignals(c.codeForge, c.issueTracker) (issue #2527
// review), never reading c.hostMediatedRemote/c.inBoxUnreachableTracker/
// c.fullyLocal directly, so callers don't need to set those fields
// themselves — setting c.codeForge/c.issueTracker to "local" is enough.
func minimalValidLocalConfig() config {
	c := minimalValidConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = ".spindrift/accum.git"
	c.mergeMode = "immediate"
	return c
}

func minimalValidConfig() config {
	return config{
		runtime: "echo", // echo is always on PATH
		schemaConfig: schemaConfig{
			repoSlug:               "owner/repo",
			gitUserName:            "bot",
			gitUserEmail:           "bot@example.com",
			ghToken:                "ghp_test",
			claudeOAuthToken:       "tok",
			mergeMode:              "manual",
			mergeMethod:            "rebase",
			syncMethod:             "rebase",
			issueTracker:           "github",
			codeForge:              "github",
			overlapGate:            "defer",
			boxForgeAndIssueAccess: "read-write",
			networkMode:            "open",
		},
	}
}

// --- runDoctor tests ---

// TestRunDoctor_WiresLauncherChecksIntoOutput verifies runDoctor passes
// launcherChecks(c) — not nil — as doctor.Run's extraChecks argument (AC2):
// a launcherChecks row failure must be visible in doctor's own output. c is
// minimalValidConfig() (passes validate(), so every launcherChecks row but
// the one under test succeeds) plus the four work-tier labels
// defaultLabelConfig() sets (minimalValidConfig() leaves them unset, which
// would otherwise make doctor.Run's own label check fail on the empty-string
// label and mask the launcherChecks wiring this test targets), with
// gitUserName cleared to fail exactly the "git-user-name" row and no other.
func TestRunDoctor_WiresLauncherChecksIntoOutput(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.gitUserName = "" // fails exactly the launcherChecks "git-user-name" row

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
		t.Fatalf("unexpected error: %v", err) // extraChecks are informational-only, never fail Run
	}
	out := buf.String()
	if !strings.Contains(out, "MISSING: git-user-name") {
		t.Errorf("want runDoctor's output to report launcherChecks(c)'s failing git-user-name row (proves it wired launcherChecks(c), not nil), got:\n%s", out)
	}
}

// TestRunDoctor_WiresBwrapCapabilityChecksIntoOutput verifies runDoctor
// passes doctorReportChecks(c) -- not doctorExtraChecks(c) -- as doctor.Run's
// extraChecks argument: doctorReportChecks(c) appends
// bwrapCapabilityChecks(c)'s three bwrap-capability rows on top of
// doctorExtraChecks(c), so an operator running `spindrift doctor` on a
// bwrap-runner config sees the host's bwrap capability posture. Swapping
// runDoctor's call site back to doctorExtraChecks(c) would drop these rows
// silently -- the full suite would still pass, since
// TestRunDoctor_WiresLauncherChecksIntoOutput above only proves
// doctorExtraChecks(c) itself is wired, not the bwrap rows appended on top
// (issue #2671 round-3 review finding). c.runnerKind = freshness.KindBwrap
// is required for bwrapCapabilityChecks to return any rows at all
// (self-gated, see bwrap_doctor_checks.go); validateCgroupDelegationFn is
// swapped to fail so the always-Advisory bwrap-cgroup-delegation row renders
// deterministically as "advisory:" rather than depending on this host's
// real cgroup delegation state.
func TestRunDoctor_WiresBwrapCapabilityChecksIntoOutput(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.runnerKind = freshness.KindBwrap

	origCgroup := validateCgroupDelegationFn
	t.Cleanup(func() { validateCgroupDelegationFn = origCgroup })
	validateCgroupDelegationFn = func([]string) error { return errors.New("distinguishable cgroup delegation sentinel") }

	// Deterministic stand-ins so this test never spawns a real bwrap
	// subprocess (validateOverlayFn) or does a real PATH lookup
	// (validatePastaFn) -- their return value doesn't matter to the
	// assertion below, which targets bwrap-cgroup-delegation only.
	origOverlay := validateOverlayFn
	t.Cleanup(func() { validateOverlayFn = origOverlay })
	validateOverlayFn = func() error { return nil }

	origPasta := validatePastaFn
	t.Cleanup(func() { validatePastaFn = origPasta })
	validatePastaFn = func() error { return nil }

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
		t.Fatalf("unexpected error: %v", err) // extraChecks are informational-only, never fail Run
	}
	out := buf.String()
	if !strings.Contains(out, "advisory: bwrap-cgroup-delegation") {
		t.Errorf("want runDoctor's output to report bwrapCapabilityChecks(c)'s bwrap-cgroup-delegation row (proves it wired doctorReportChecks(c), not doctorExtraChecks(c)), got:\n%s", out)
	}
}

func TestDoctor_Success(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "owner/repo") {
		t.Errorf("want output to contain resolved repo, got %q", buf.String())
	}
}

// TestDoctor_ReportsEachSeamsOwnSlug verifies runDoctor prints each seam's own
// Probe() result — not the IssueTracker's slug reused for the CodeForge line
// — since under ISSUE_TRACKER=jira the two seams resolve to different
// identities (a Jira project key vs a GitHub repo slug).
func TestDoctor_ReportsEachSeamsOwnSlug(t *testing.T) {
	it := forge.NewFake()
	it.ProbeRepo = "PROJ"
	it.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	var buf bytes.Buffer
	if err := runDoctor(it, cf, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "issue tracker confirmed — PROJ") {
		t.Errorf("want issue tracker line to report PROJ, got %q", out)
	}
	if !strings.Contains(out, "code forge confirmed — owner/repo") {
		t.Errorf("want code forge line to report owner/repo, got %q", out)
	}
}

func TestDoctor_AuthFailure(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, f, config{}, &buf, strings.NewReader(""), false, doctorReportChecks(config{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, forge.ErrAuthFailure) {
		t.Errorf("want ErrAuthFailure, got %v", err)
	}
}

// TestDoctor_AuthFailure_NotDoublyReported verifies a failing built-in
// Required check is reported exactly once: via the returned error, not also
// as a "MISSING: ..." row written to w. The caller (cmdDoctor in main.go)
// already prints the returned error to stderr, so Run writing the same
// failure to w too would double-report it — origin/main's pre-refactor Run
// never wrote anything to w on this failure path.
func TestDoctor_AuthFailure_NotDoublyReported(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, f, config{}, &buf, strings.NewReader(""), false, doctorReportChecks(config{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(buf.String(), "MISSING: issue-tracker") {
		t.Errorf("want w to not contain the failing built-in row, got: %s", buf.String())
	}
}

// TestDoctor_AuthFailure_Jira verifies the auth-failure remediation text
// names JIRA_TOKEN, not GH_TOKEN, when the issue tracker is jira — the
// generic message would misdirect an operator debugging a Jira probe.
func TestDoctor_AuthFailure_Jira(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, f, config{schemaConfig: schemaConfig{issueTracker: "jira"}}, &buf, strings.NewReader(""), false, doctorReportChecks(config{schemaConfig: schemaConfig{issueTracker: "jira"}}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "JIRA_TOKEN") {
		t.Errorf("want error to mention JIRA_TOKEN, got: %v", err)
	}
}

func TestDoctor_RepoNotFound(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrRepoNotFound

	var buf bytes.Buffer
	err := runDoctor(f, f, config{}, &buf, strings.NewReader(""), false, doctorReportChecks(config{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, forge.ErrRepoNotFound) {
		t.Errorf("want ErrRepoNotFound, got %v", err)
	}
}

// TestDoctor_AuthFailure_Forgejo verifies the auth-failure remediation text
// names FORGEJO_TOKEN, not GH_TOKEN, when the issue tracker is forgejo — the
// generic message would misdirect an operator debugging a Forgejo probe.
func TestDoctor_AuthFailure_Forgejo(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, f, config{schemaConfig: schemaConfig{issueTracker: "forgejo"}}, &buf, strings.NewReader(""), false, doctorReportChecks(config{schemaConfig: schemaConfig{issueTracker: "forgejo"}}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "FORGEJO_TOKEN") {
		t.Errorf("want error to mention FORGEJO_TOKEN, got: %v", err)
	}
	if strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("want error to not mention GH_TOKEN, got: %v", err)
	}
}

// TestDoctor_RepoNotFound_Forgejo verifies the repo-not-found remediation
// text names FORGEJO_BASE_URL, not REPO_SLUG, when the issue tracker is
// forgejo.
func TestDoctor_RepoNotFound_Forgejo(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrRepoNotFound

	var buf bytes.Buffer
	err := runDoctor(f, f, config{schemaConfig: schemaConfig{issueTracker: "forgejo"}}, &buf, strings.NewReader(""), false, doctorReportChecks(config{schemaConfig: schemaConfig{issueTracker: "forgejo"}}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "FORGEJO_BASE_URL") {
		t.Errorf("want error to mention FORGEJO_BASE_URL, got: %v", err)
	}
	if strings.Contains(err.Error(), "REPO_SLUG") {
		t.Errorf("want error to not mention REPO_SLUG, got: %v", err)
	}
}

// TestDoctor_AuthFailure_Local pins today's behavior for ISSUE_TRACKER=local:
// the backend registry carries no doctor hint override for "local" (it falls
// through to the github-shaped default), so the auth-failure remediation
// text still names GH_TOKEN / --repo-slug, not a local-specific hint.
func TestDoctor_AuthFailure_Local(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, f, config{schemaConfig: schemaConfig{issueTracker: "local"}}, &buf, strings.NewReader(""), false, doctorReportChecks(config{schemaConfig: schemaConfig{issueTracker: "local"}}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("want error to mention GH_TOKEN, got: %v", err)
	}
}

func defaultLabelConfig() config {
	return config{
		schemaConfig: schemaConfig{
			label:           "ready-for-agent",
			inProgressLabel: "agent-in-progress",
			failedLabel:     "agent-failed",
			completeLabel:   "agent-complete",
			codeForge:       "github",
			issueTracker:    "github",
			baseBranch:      "main",
			// mergeMode "manual" keeps the branch-protection row (issue
			// #2570) Advisory rather than Required here — these tests exist
			// to exercise label/runtime/token-gate behavior, not branch
			// protection, and the forge.Fake instances they build generally
			// don't script SetBranchProtected, so an unset "main" would
			// otherwise report a spurious Required failure unrelated to what
			// each test actually verifies.
			mergeMode: "manual",
		},
		// "echo" is always on PATH, so it's a safe non-empty default that
		// makes the new doctor runtime row print "ok" without dragging in a
		// real container runtime — unrelated tests shouldn't trip the check.
		runtime: "echo",
	}
}

// TestDoctor_RuntimeRow_OnPath_PrintsOk verifies doctor prints an "ok" line
// naming the configured runtime when it resolves to a binary on PATH — using
// defaultLabelConfig()'s "echo" runtime, which the runner package can
// genuinely LookPath since it's always available.
func TestDoctor_RuntimeRow_OnPath_PrintsOk(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `ok: runtime "echo" found on PATH`) {
		t.Errorf("want output to contain an ok line naming the echo runtime, got:\n%s", out)
	}
}

// TestDoctor_RuntimeRow_AbsentFromPATH_PrintsAdvisoryNotFatal verifies a
// runtime that resolves to no binary on PATH is reported as an advisory —
// never a fatal error — mirroring the research/priority/ambiguous-spec label
// rows; rationale on doctor.Config.Runtime.
func TestDoctor_RuntimeRow_AbsentFromPATH_PrintsAdvisoryNotFatal(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := defaultLabelConfig()
	c.runtime = "definitely-not-a-real-binary-xyz"

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `advisory: runtime "definitely-not-a-real-binary-xyz" not ready`) {
		t.Errorf("want output to contain an advisory line naming the missing runtime, got:\n%s", out)
	}
	if !strings.Contains(out, "does not fail this check") {
		t.Errorf("want output to note the runtime row does not fail this check, got:\n%s", out)
	}
}

// TestDoctor_RuntimeRow_Unset_PrintsAdvisorySkipped verifies an empty
// RUNTIME is reported as a skipped-check advisory — never a fatal error —
// mirroring the on-PATH and absent-from-PATH runtime row tests above.
func TestDoctor_RuntimeRow_Unset_PrintsAdvisorySkipped(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := defaultLabelConfig()
	c.runtime = ""

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "advisory: RUNTIME not set — skipping runtime check") {
		t.Errorf("want output to contain the RUNTIME-unset advisory line, got:\n%s", out)
	}
}

// TestDoctor_RuntimeRow_ReportedExactlyOnce guards against the runtime
// check regressing back to two competing implementations (extraChecks row
// + a separate hand-rolled doctor.Config.Runtime block) — issue #2559 AC2.
func TestDoctor_RuntimeRow_ReportedExactlyOnce(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := defaultLabelConfig()
	c.runtime = ""

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	// Counts the exact advisory line doctor.Run prints for an unset RUNTIME,
	// not a bare "RUNTIME" substring: since issue #2942, doctor also reports
	// the "network-mode-runtime" gate row (launchgates.go), whose name would
	// otherwise collide with a looser substring count.
	want := "advisory: RUNTIME not set — skipping runtime check"
	if n := strings.Count(out, want); n != 1 {
		t.Errorf("want exactly one %q line, got %d occurrences in:\n%s", want, n, out)
	}
}

func TestDoctor_LabelsAllPresent(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, label := range []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"} {
		if !strings.Contains(out, label) {
			t.Errorf("want output to contain label %q, got:\n%s", label, out)
		}
	}
	if !strings.Contains(out, "present") {
		t.Errorf("want output to mention 'present', got:\n%s", out)
	}
}

// TestDoctor_ReportsRecoverableCount verifies doctor prints a count of
// issues in the Recoverable dispatch state (ADR 0039 slice S4, #2255) as its
// own line, counting only issues carrying the Recoverable label and not
// issues in other states.
func TestDoctor_ReportsRecoverableCount(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
		Recoverable:  "agent-recoverable",
	})
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	f.SetIssue(forge.Issue{Number: "5", State: forge.IssueOpen, Labels: []string{"agent-recoverable"}})
	f.SetIssue(forge.Issue{Number: "6", State: forge.IssueOpen, Labels: []string{"agent-recoverable"}})
	f.SetIssue(forge.Issue{Number: "7", State: forge.IssueOpen, Labels: []string{"agent-failed"}})

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "2 recoverable issue(s)") {
		t.Errorf("want output to report 2 recoverable issue(s), got:\n%s", out)
	}
}

// TestDoctor_RecoverableCount_ZeroWhenLabelUnmapped verifies doctor reports
// zero recoverable issues — not the full open-issue count — against a
// tracker whose label family leaves Recoverable unmapped, mirroring GitHub
// and Forgejo in production (#2255): both ignore an empty label filter and
// return every open issue rather than erroring, so a naive unconditional
// ListIssues(Recoverable) call would misreport every open issue as
// recoverable instead of zero.
func TestDoctor_RecoverableCount_ZeroWhenLabelUnmapped(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
		// Recoverable left empty — never a real label on this tracker.
	})
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	f.SetIssue(forge.Issue{Number: "5", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "6", State: forge.IssueOpen, Labels: []string{"agent-failed"}})

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "0 recoverable issue(s)") {
		t.Errorf("want output to report 0 recoverable issue(s), got:\n%s", out)
	}
}

// TestDoctor_AllLabelsPresent_PrintsSuccess verifies the early-return path
// taken when both work and research labels are already present prints an
// explicit success confirmation, mirroring the post-creation success line
// (#1170).
func TestDoctor_AllLabelsPresent_PrintsSuccess(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	research := doctor.ResearchLabelNames()
	priority := doctor.PriorityLabelNames()
	ambiguous := doctor.AmbiguousLabelNames()
	f.Labels = append(append(append([]string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}, research...), priority...), ambiguous...)

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ok: all triage, research, priority, and ambiguous-spec labels present") {
		t.Errorf("want success confirmation, got:\n%s", out)
	}
}

func TestDoctor_LabelsSomeMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress"}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig()))
	if err == nil {
		t.Fatal("expected non-zero exit for missing labels, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "missing") {
		t.Errorf("want output to mention 'missing', got:\n%s", out)
	}
}

func TestDoctor_LabelsAllMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig()))
	if err == nil {
		t.Fatal("expected non-zero exit for all-missing labels, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "missing") {
		t.Errorf("want output to mention 'missing', got:\n%s", out)
	}
}

// TestDoctor_NoTTY_ResearchLabelsMissing_ExitZero verifies missing research
// labels (ADR 0022) are advisory only: doctor prefixes each row "advisory:"
// and exits zero as long as the fatal work labels are all present (#796).
func TestDoctor_NoTTY_ResearchLabelsMissing_ExitZero(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("missing research labels must not fail doctor, got: %v", err)
	}
	out := buf.String()
	for _, label := range []string{
		"agent-research", "agent-research-in-progress", "agent-research-failed",
		"agent-research-recommend", "agent-research-reject", "agent-research-unclear",
		"agent-research-finding",
	} {
		if !strings.Contains(out, "advisory: label \""+label+"\" missing") {
			t.Errorf("want advisory line for research label %q, got:\n%s", label, out)
		}
		if strings.Contains(out, "MISSING: label \""+label+"\"") {
			t.Errorf("research label %q must not render with the fatal MISSING prefix, got:\n%s", label, out)
		}
	}
}

func TestDoctor_NoTTY_NoPrompt(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three missing

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig()))
	if err == nil {
		t.Fatal("expected non-zero exit for missing labels, got nil")
	}
	if strings.Contains(buf.String(), "[y/N]") {
		t.Errorf("no-TTY path must not show a prompt, got:\n%s", buf.String())
	}
	if len(f.CreateLabelCalls) != 0 {
		t.Errorf("no-TTY path must not create labels, got %d calls", len(f.CreateLabelCalls))
	}
}

func TestDoctor_TTY_Decline(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three missing

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("n\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err == nil {
		t.Fatal("expected non-zero exit on decline, got nil")
	}
	if !strings.Contains(buf.String(), "[y/N]") {
		t.Errorf("TTY path must show the prompt, got:\n%s", buf.String())
	}
	if len(f.CreateLabelCalls) != 0 {
		t.Errorf("decline must not create labels, got %d calls", len(f.CreateLabelCalls))
	}
}

// TestDoctor_TTY_Decline_PromptShowsTierBreakdown verifies issue #2569's
// tiered-label-prompt AC: when both the required (work) tier and an advisory
// tier (research, here) have missing labels, the single interactive prompt
// names the required-tier count and that declining it fails the check, and
// the advisory-tier count and that declining it is safe — all in the same
// [y/N] prompt/scan, with no extra round-trip.
func TestDoctor_TTY_Decline_PromptShowsTierBreakdown(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	priority := doctor.PriorityLabelNames()
	ambiguous := doctor.AmbiguousLabelNames()
	// Two work labels missing (agent-failed, agent-complete) and all seven
	// research labels missing; priority and ambiguous-spec present so the
	// advisory count is scoped to research alone.
	f.Labels = append(append([]string{"ready-for-agent", "agent-in-progress"}, priority...), ambiguous...)

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("n\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err == nil {
		t.Fatal("expected non-zero exit on decline, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "[y/N]") {
		t.Errorf("prompt must still end in [y/N], got:\n%s", out)
	}
	if !strings.Contains(out, "2 required") {
		t.Errorf("prompt must name the required-tier count (2), got:\n%s", out)
	}
	if !strings.Contains(out, "7 advisory") {
		t.Errorf("prompt must name the advisory-tier count (7), got:\n%s", out)
	}
	if !strings.Contains(out, "declining leaves this check failing") {
		t.Errorf("prompt must state declining the required tier fails the check, got:\n%s", out)
	}
	if !strings.Contains(out, "declining is safe") {
		t.Errorf("prompt must state declining the advisory tier is safe, got:\n%s", out)
	}
	if len(f.CreateLabelCalls) != 0 {
		t.Errorf("decline must not create labels, got %d calls", len(f.CreateLabelCalls))
	}
}

// TestDoctor_TTY_Decline_PromptOmitsConsequenceWhenNoRequiredMissing verifies
// that when every work-tier label is present and only an advisory tier
// (research, here) is missing, the prompt names "0 required" without the
// "(declining leaves this check failing)" consequence clause — that clause
// describes a tier that, with zero missing labels in it, has no consequence
// to state.
func TestDoctor_TTY_Decline_PromptOmitsConsequenceWhenNoRequiredMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	priority := doctor.PriorityLabelNames()
	ambiguous := doctor.AmbiguousLabelNames()
	f.Labels = append(append([]string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}, priority...), ambiguous...)

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("n\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("missing advisory labels alone must not fail doctor, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "0 required") {
		t.Errorf("prompt must name the required-tier count (0), got:\n%s", out)
	}
	if strings.Contains(out, "0 required (declining leaves this check failing)") {
		t.Errorf("prompt must not attach a consequence clause to an empty required tier, got:\n%s", out)
	}
}

func TestDoctor_TTY_Confirm(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	research := doctor.ResearchLabelNames()
	priority := doctor.PriorityLabelNames()
	ambiguous := doctor.AmbiguousLabelNames()
	// Two work labels missing: agent-failed and agent-complete. Research,
	// priority, and ambiguous-spec labels are all present throughout, so
	// this test stays scoped to work label creation.
	f.Labels = append(append(append([]string{"ready-for-agent", "agent-in-progress"}, research...), priority...), ambiguous...)
	// After creation the fake doesn't auto-add to Labels, so script the
	// second ListLabels call (re-verify) to return all four work labels.
	f.LabelsSeq = [][]string{
		append(append(append([]string{"ready-for-agent", "agent-in-progress"}, research...), priority...), ambiguous...),                                   // first check
		append(append(append([]string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}, research...), priority...), ambiguous...), // re-verify
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("unexpected error after confirm: %v", err)
	}
	if len(f.CreateLabelCalls) != 2 {
		t.Fatalf("want 2 CreateLabel calls, got %d", len(f.CreateLabelCalls))
	}
	names := []string{f.CreateLabelCalls[0].Name, f.CreateLabelCalls[1].Name}
	if !contains(names, "agent-failed") || !contains(names, "agent-complete") {
		t.Errorf("want agent-failed and agent-complete created, got %v", names)
	}
	// Verify default colors are from doctor.TriageLabelMeta
	for _, call := range f.CreateLabelCalls {
		if call.Color == "" || call.Color == "ededed" {
			t.Errorf("label %q should use a named color, got %q", call.Name, call.Color)
		}
	}
	out := buf.String()
	if !strings.Contains(out, "ok: all triage, research, priority, and ambiguous-spec labels present") {
		t.Errorf("want success message after creation, got:\n%s", out)
	}
}

// TestDoctor_TTY_Confirm_ResearchLabels verifies interactive doctor also
// offers to create missing research labels (advisory tier, ADR 0022 / ADR
// 0041) alongside work labels, and creates them with real
// colors/descriptions — never the "ededed" gray fallback (#796). It also
// pins that the pre-creation row uses the same advisory wording as the
// no-TTY path.
func TestDoctor_TTY_Confirm_ResearchLabels(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	work := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	research := doctor.ResearchLabelNames()
	priority := doctor.PriorityLabelNames()
	ambiguous := doctor.AmbiguousLabelNames()
	// All work, priority, and ambiguous-spec labels present; all seven
	// research labels missing, so this test stays scoped to research label
	// creation.
	f.Labels = append(append(append([]string{}, work...), priority...), ambiguous...)
	f.LabelsSeq = [][]string{
		append(append(append([]string{}, work...), priority...), ambiguous...),
		append(append(append(append([]string{}, work...), priority...), ambiguous...), research...), // re-verify: research now created too
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("unexpected error after confirm: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "advisory: label \""+research[0]+"\" missing") {
		t.Errorf("want advisory line for research label %q before creation, got:\n%s", research[0], out)
	}
	if len(f.CreateLabelCalls) != len(research) {
		t.Fatalf("want %d CreateLabel calls, got %d", len(research), len(f.CreateLabelCalls))
	}
	for _, call := range f.CreateLabelCalls {
		if call.Color == "" || call.Color == "ededed" {
			t.Errorf("research label %q should use a named color, got %q", call.Name, call.Color)
		}
		if call.Description == "" {
			t.Errorf("research label %q should have a description", call.Name)
		}
	}
}

// TestDoctor_TTY_Confirm_RenamedLifecycleLabel_UsesCorrectMeta verifies that
// when an operator renames a work-tier label away from its default (e.g.
// LABEL=custom-ready-for-agent), doctor still resolves that label's real
// color/description by role (MetaDispatchable etc.), not by a literal
// TriageLabelMeta[name] lookup keyed on the default name — which would miss
// and fall back to the gray "ededed" no-description default (#2528 AC2).
// Table-driven across all four work-tier roles, not just Dispatchable: the
// metaFor switch in doctor.go resolves each renamed field
// (c.Label/c.InProgressLabel/c.FailedLabel/c.CompleteLabel) against its own
// Meta<Role> var, and a copy-paste mistake swapping which field maps to
// which role would only show up on the roles this test actually renames.
func TestDoctor_TTY_Confirm_RenamedLifecycleLabel_UsesCorrectMeta(t *testing.T) {
	tests := []struct {
		role      string
		renameCfg func(cfg *config, renamed string)
		otherLive []string // the other three work-tier labels, already at default
		wantMeta  doctor.LabelMeta
	}{
		{
			role:      "Dispatchable",
			renameCfg: func(cfg *config, renamed string) { cfg.label = renamed },
			otherLive: []string{"agent-in-progress", "agent-failed", "agent-complete"},
			wantMeta:  doctor.MetaDispatchable,
		},
		{
			role:      "InProgress",
			renameCfg: func(cfg *config, renamed string) { cfg.inProgressLabel = renamed },
			otherLive: []string{"ready-for-agent", "agent-failed", "agent-complete"},
			wantMeta:  doctor.MetaInProgress,
		},
		{
			role:      "Failed",
			renameCfg: func(cfg *config, renamed string) { cfg.failedLabel = renamed },
			otherLive: []string{"ready-for-agent", "agent-in-progress", "agent-complete"},
			wantMeta:  doctor.MetaFailed,
		},
		{
			role:      "Complete",
			renameCfg: func(cfg *config, renamed string) { cfg.completeLabel = renamed },
			otherLive: []string{"ready-for-agent", "agent-in-progress", "agent-failed"},
			wantMeta:  doctor.MetaComplete,
		},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			f := forge.NewFake()
			f.ProbeRepo = "owner/repo"

			cfg := defaultLabelConfig()
			renamed := "custom-" + tt.role
			tt.renameCfg(&cfg, renamed)

			research := doctor.ResearchLabelNames()
			priority := doctor.PriorityLabelNames()
			ambiguous := doctor.AmbiguousLabelNames()
			present := append(append(append(append([]string{}, tt.otherLive...), research...), priority...), ambiguous...)
			f.Labels = present
			f.LabelsSeq = [][]string{
				present,
				append(append([]string{}, present...), renamed), // re-verify: renamed label now created too
			}

			var buf bytes.Buffer
			err := runDoctor(f, f, cfg, &buf, strings.NewReader("y\n"), true, doctorReportChecks(cfg))
			if err != nil {
				t.Fatalf("unexpected error after confirm: %v", err)
			}

			if len(f.CreateLabelCalls) != 1 {
				t.Fatalf("want 1 CreateLabel call, got %d: %+v", len(f.CreateLabelCalls), f.CreateLabelCalls)
			}
			call := f.CreateLabelCalls[0]
			if call.Name != renamed {
				t.Fatalf("want CreateLabel call for %q, got %q", renamed, call.Name)
			}
			if call.Color != tt.wantMeta.Color {
				t.Errorf("want color %q (Meta%s), got %q", tt.wantMeta.Color, tt.role, call.Color)
			}
			if call.Description != tt.wantMeta.Description {
				t.Errorf("want description %q (Meta%s), got %q", tt.wantMeta.Description, tt.role, call.Description)
			}
		})
	}
}

// TestDoctor_TTY_Confirm_ResearchStillMissing_Advisory verifies that when a
// create run's re-verify still finds research labels missing (e.g. eventual
// consistency on the forge side), doctor prints a non-fatal advisory summary
// instead of silently returning nil — mirroring the work tier's explicit
// "still missing after creation" message but never failing the check (#800).
func TestDoctor_TTY_Confirm_ResearchStillMissing_Advisory(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	work := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	f.Labels = work // all work labels present, all seven research labels missing
	f.LabelsSeq = [][]string{
		work,
		work, // re-verify: research labels still missing despite CreateLabel "succeeding"
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("research labels still missing after creation must not fail doctor, got: %v", err)
	}
	out := buf.String()
	var advisoryLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "advisory: 7 research label(s) still missing after creation") {
			advisoryLine = line
			break
		}
	}
	if advisoryLine == "" {
		t.Fatalf("want advisory summary after incomplete research creation, got:\n%s", out)
	}
	for _, name := range doctor.ResearchLabelNames() {
		if !strings.Contains(advisoryLine, name) {
			t.Errorf("want advisory line to name missing label %q, got:\n%s", name, advisoryLine)
		}
	}
	if strings.Contains(out, "ok: all triage, research, priority, and ambiguous-spec labels present") {
		t.Errorf("must not print success message when research labels are still missing, got:\n%s", out)
	}
}

// TestDoctor_NoTTY_PriorityLabelsMissing_ExitZero verifies missing priority
// labels (ADR 0040) are advisory only: doctor prefixes each row "advisory:"
// and exits zero as long as the fatal work labels are all present,
// mirroring the research tier's non-fatal treatment (#2282).
func TestDoctor_NoTTY_PriorityLabelsMissing_ExitZero(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("missing priority labels must not fail doctor, got: %v", err)
	}
	out := buf.String()
	for _, label := range doctor.PriorityLabelNames() {
		if !strings.Contains(out, "advisory: label \""+label+"\" missing") {
			t.Errorf("want advisory line for priority label %q, got:\n%s", label, out)
		}
		if strings.Contains(out, "MISSING: label \""+label+"\"") {
			t.Errorf("priority label %q must not render with the fatal MISSING prefix, got:\n%s", label, out)
		}
	}
	wantAdvisory := "advisory: " + strconv.Itoa(len(doctor.PriorityLabelNames())) + " priority label(s) missing (ADR 0040)"
	if !strings.Contains(out, wantAdvisory) {
		t.Errorf("want advisory line %q, got:\n%s", wantAdvisory, out)
	}
}

// TestDoctor_TTY_Confirm_PriorityLabels verifies interactive doctor also
// offers to create missing priority labels (advisory tier, ADR 0040)
// alongside work labels, and creates them with real colors/descriptions —
// never the "ededed" gray fallback (#2282). It also pins that the
// pre-creation row uses the same advisory wording as the no-TTY path.
func TestDoctor_TTY_Confirm_PriorityLabels(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	work := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	research := doctor.ResearchLabelNames()
	priority := doctor.PriorityLabelNames()
	ambiguous := doctor.AmbiguousLabelNames()
	// All work, research, and ambiguous-spec labels present; all three
	// priority labels missing, so this test stays scoped to priority label
	// creation.
	f.Labels = append(append(append([]string{}, work...), research...), ambiguous...)
	f.LabelsSeq = [][]string{
		append(append(append([]string{}, work...), research...), ambiguous...),
		append(append(append(append([]string{}, work...), research...), ambiguous...), priority...), // re-verify: priority now created too
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("unexpected error after confirm: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "advisory: label \""+priority[0]+"\" missing") {
		t.Errorf("want advisory line for priority label %q before creation, got:\n%s", priority[0], out)
	}
	if len(f.CreateLabelCalls) != len(priority) {
		t.Fatalf("want %d CreateLabel calls, got %d", len(priority), len(f.CreateLabelCalls))
	}
	for _, call := range f.CreateLabelCalls {
		if call.Color == "" || call.Color == "ededed" {
			t.Errorf("priority label %q should use a named color, got %q", call.Name, call.Color)
		}
		if call.Description == "" {
			t.Errorf("priority label %q should have a description", call.Name)
		}
	}
}

// TestDoctor_TTY_Confirm_PriorityStillMissing_Advisory verifies that when a
// create run's re-verify still finds priority labels missing (e.g. eventual
// consistency on the forge side), doctor prints a non-fatal advisory summary
// instead of silently returning nil — mirroring the research tier's
// analogous message but never failing the check (#2282).
func TestDoctor_TTY_Confirm_PriorityStillMissing_Advisory(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	work := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	research := doctor.ResearchLabelNames()
	f.Labels = append(append([]string{}, work...), research...) // all work and research labels present, all three priority labels missing
	f.LabelsSeq = [][]string{
		append(append([]string{}, work...), research...),
		append(append([]string{}, work...), research...), // re-verify: priority labels still missing despite CreateLabel "succeeding"
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("priority labels still missing after creation must not fail doctor, got: %v", err)
	}
	out := buf.String()
	var advisoryLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "advisory: 3 priority label(s) still missing after creation") {
			advisoryLine = line
			break
		}
	}
	if advisoryLine == "" {
		t.Fatalf("want advisory summary after incomplete priority creation, got:\n%s", out)
	}
	for _, name := range doctor.PriorityLabelNames() {
		if !strings.Contains(advisoryLine, name) {
			t.Errorf("want advisory line to name missing label %q, got:\n%s", name, advisoryLine)
		}
	}
	if strings.Contains(out, "ok: all triage, research, priority, and ambiguous-spec labels present") {
		t.Errorf("must not print success message when priority labels are still missing, got:\n%s", out)
	}
}

// TestDoctor_NoTTY_AmbiguousLabelMissing_ExitZero verifies the missing
// agent-ambiguous-spec label (issue #2275) is advisory only: doctor prefixes
// its row "advisory:" and exits zero as long as the fatal work labels are
// present, mirroring the research/priority tiers' non-fatal treatment.
func TestDoctor_NoTTY_AmbiguousLabelMissing_ExitZero(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("missing ambiguous-spec label must not fail doctor, got: %v", err)
	}
	out := buf.String()
	for _, label := range doctor.AmbiguousLabelNames() {
		if !strings.Contains(out, "advisory: label \""+label+"\" missing") {
			t.Errorf("want advisory line for ambiguous-spec label %q, got:\n%s", label, out)
		}
		if strings.Contains(out, "MISSING: label \""+label+"\"") {
			t.Errorf("ambiguous-spec label %q must not render with the fatal MISSING prefix, got:\n%s", label, out)
		}
	}
	wantAdvisory := "advisory: " + strconv.Itoa(len(doctor.AmbiguousLabelNames())) + " ambiguous-spec label(s) missing"
	if !strings.Contains(out, wantAdvisory) {
		t.Errorf("want advisory line %q, got:\n%s", wantAdvisory, out)
	}
}

// TestDoctor_TTY_Confirm_AmbiguousLabel verifies interactive doctor also
// offers to create the missing agent-ambiguous-spec label (advisory tier,
// issue #2275) alongside work/research/priority labels, and creates it with
// a real color/description — never the "ededed" gray fallback. It also pins
// that the pre-creation row uses the same advisory wording as the no-TTY
// path.
func TestDoctor_TTY_Confirm_AmbiguousLabel(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	work := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	research := doctor.ResearchLabelNames()
	priority := doctor.PriorityLabelNames()
	ambiguous := doctor.AmbiguousLabelNames()
	// All work, research, and priority labels present; the ambiguous-spec
	// label missing, so this test stays scoped to ambiguous-spec label
	// creation.
	f.Labels = append(append(append([]string{}, work...), research...), priority...)
	f.LabelsSeq = [][]string{
		append(append(append([]string{}, work...), research...), priority...),
		append(append(append(append([]string{}, work...), research...), priority...), ambiguous...), // re-verify: ambiguous-spec now created too
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("unexpected error after confirm: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "advisory: label \""+ambiguous[0]+"\" missing") {
		t.Errorf("want advisory line for ambiguous-spec label %q before creation, got:\n%s", ambiguous[0], out)
	}
	if len(f.CreateLabelCalls) != len(ambiguous) {
		t.Fatalf("want %d CreateLabel calls, got %d", len(ambiguous), len(f.CreateLabelCalls))
	}
	for _, call := range f.CreateLabelCalls {
		if call.Color == "" || call.Color == "ededed" {
			t.Errorf("ambiguous-spec label %q should use a named color, got %q", call.Name, call.Color)
		}
		if call.Description == "" {
			t.Errorf("ambiguous-spec label %q should have a description", call.Name)
		}
	}
}

// TestDoctor_TTY_Confirm_AmbiguousStillMissing_Advisory verifies that when a
// create run's re-verify still finds the ambiguous-spec label missing (e.g.
// eventual consistency on the forge side), doctor prints a non-fatal
// advisory summary instead of silently returning nil — mirroring the
// research/priority tiers' analogous message but never failing the check.
func TestDoctor_TTY_Confirm_AmbiguousStillMissing_Advisory(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	work := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	research := doctor.ResearchLabelNames()
	priority := doctor.PriorityLabelNames()
	// All work, research, and priority labels present, the ambiguous-spec
	// label missing.
	f.Labels = append(append(append([]string{}, work...), research...), priority...)
	f.LabelsSeq = [][]string{
		append(append(append([]string{}, work...), research...), priority...),
		append(append(append([]string{}, work...), research...), priority...), // re-verify: ambiguous-spec still missing despite CreateLabel "succeeding"
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true, doctorReportChecks(defaultLabelConfig()))
	if err != nil {
		t.Fatalf("ambiguous-spec label still missing after creation must not fail doctor, got: %v", err)
	}
	out := buf.String()
	var advisoryLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "advisory: 1 ambiguous-spec label(s) still missing after creation") {
			advisoryLine = line
			break
		}
	}
	if advisoryLine == "" {
		t.Fatalf("want advisory summary after incomplete ambiguous-spec creation, got:\n%s", out)
	}
	for _, name := range doctor.AmbiguousLabelNames() {
		if !strings.Contains(advisoryLine, name) {
			t.Errorf("want advisory line to name missing label %q, got:\n%s", name, advisoryLine)
		}
	}
	if strings.Contains(out, "ok: all triage, research, priority, and ambiguous-spec labels present") {
		t.Errorf("must not print success message when ambiguous-spec label is still missing, got:\n%s", out)
	}
}

// TestReferenceDocLabelSnippetMatchesTriageDefaults guards against the docs'
// manual `gh label create` fallback commands (for consumers who skip
// `spindrift doctor`) drifting from doctor.TriageLabelMeta, the single source of
// truth for those defaults — work and research tiers alike (#611, #641, #796).
func TestReferenceDocLabelSnippetMatchesTriageDefaults(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference.md"))
	if err != nil {
		t.Fatalf("read docs/reference.md: %v", err)
	}
	line := regexp.MustCompile(`gh label create (\S+)\s+--repo owner/repo --color (\S+) --description "([^"]*)"`)
	matches := line.FindAllStringSubmatch(string(raw), -1)
	seen := map[string]int{}
	for _, m := range matches {
		name, color, description := m[1], m[2], m[3]
		seen[name]++
		want, ok := doctor.TriageLabelMeta[name]
		if !ok {
			t.Errorf("docs/reference.md snippet creates unknown label %q", name)
			continue
		}
		if color != want.Color {
			t.Errorf("label %q: docs color = %q, want %q (doctor default)", name, color, want.Color)
		}
		if description != want.Description {
			t.Errorf("label %q: docs description = %q, want %q (doctor default)", name, description, want.Description)
		}
	}

	for name := range doctor.TriageLabelMeta {
		switch seen[name] {
		case 0:
			t.Errorf("docs/reference.md is missing a `gh label create` line for %q", name)
		case 1:
			// exactly once, as expected
		default:
			t.Errorf("docs/reference.md has %d `gh label create` lines for %q, want exactly 1", seen[name], name)
		}
	}
}

// TestReferenceDocSystemRowDoesNotDuplicateIntro guards against the `system`
// option table row restating the auto-supplied/passed-through mechanism
// already explained by the intro paragraph above the option table (#880) —
// commit 5a5993f (#660) added that intro paragraph but left the table row's
// existing prose intact, so the same two facts ended up asserted twice.
func TestReferenceDocSystemRowDoesNotDuplicateIntro(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference.md"))
	if err != nil {
		t.Fatalf("read docs/reference.md: %v", err)
	}
	row := regexp.MustCompile("(?m)^\\| `system`.*$").FindString(string(raw))
	if row == "" {
		t.Fatalf("docs/reference.md is missing the `system` option table row")
	}
	if strings.Contains(row, "flake-parts passes its own") {
		t.Errorf("system table row restates the flake-parts pass-through mechanism already covered by the intro paragraph above the table; row: %s", row)
	}
}

// TestReferenceDocHasLocalCodeForgeSection guards against the
// `CODE_FORGE=local` host-mediated loop (ADR 0033) staying discoverable only
// as scattered knob-table rows: it must have its own section, parallel to
// the `ISSUE_TRACKER=local` section, cross-linking both ADR 0033 and ADR
// 0032, and it must never reintroduce the removed `CODE_FORGE_INTEGRATION_PARENT`
// env var (#1877).
func TestReferenceDocHasLocalCodeForgeSection(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference.md"))
	if err != nil {
		t.Fatalf("read docs/reference.md: %v", err)
	}
	doc := string(raw)

	heading := "### Local code forge (`CODE_FORGE=local`)"
	idx := strings.Index(doc, heading)
	if idx == -1 {
		t.Fatalf("docs/reference.md is missing the %q section heading", heading)
	}
	section := doc[idx:]

	if !strings.Contains(section, "adr/0033-host-mediated-local-code-forge.md") {
		t.Errorf("Local code forge section does not link ADR 0033")
	}
	if !strings.Contains(section, "adr/0032-host-mediated-local-issue-content.md") {
		t.Errorf("Local code forge section does not link ADR 0032")
	}
	if strings.Contains(doc, "CODE_FORGE_INTEGRATION_PARENT") {
		t.Errorf("docs/reference.md must not reintroduce the removed CODE_FORGE_INTEGRATION_PARENT env var")
	}
}

// parseLegacySettingsSectionNames reads lib/legacy-settings-section.nix and
// returns the distinct set of section names (the string values in each
// `knob = "section";` row), sorted. Test-only: production code never parses
// this file directly (lib/flakeModule.nix consumes it as Nix data), so this
// helper has no non-test counterpart.
func parseLegacySettingsSectionNames(t *testing.T) []string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("..", "..", "lib", "legacy-settings-section.nix"))
	if err != nil {
		t.Fatalf("read lib/legacy-settings-section.nix: %v", err)
	}

	return parseLegacySettingsSectionNamesContent(t, string(content))
}

// parseLegacySettingsSectionNamesContent is parseLegacySettingsSectionNames'
// content-parsing core, split out so tests can exercise it directly against
// synthetic content (e.g. to prove it ignores Nix line comments) without
// round-tripping through the real lib/legacy-settings-section.nix file.
func parseLegacySettingsSectionNamesContent(t fataler, content string) []string {
	t.Helper()

	content = stripNixLineComments(content)

	rowRe := regexp.MustCompile(`\w+\s*=\s*"([^"]+)";`)
	seen := map[string]bool{}
	for _, match := range rowRe.FindAllStringSubmatch(content, -1) {
		seen[match[1]] = true
	}
	if len(seen) == 0 {
		t.Fatalf("parsed zero section names from lib/legacy-settings-section.nix; regex out of sync with file format?")
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestParseLegacySettingsSectionNames_ParsesRealFile is the canary for
// parseLegacySettingsSectionNames: it proves the regex parses the real
// lib/legacy-settings-section.nix rather than pinning that file's full
// output, so a row added there needs no edit here. The helper dedupes the
// knob rows down to the distinct section names, which is why this canary
// asserts the helper's own contract (sorted, duplicate-free, non-empty) plus
// spot checks rather than a count cross-check: dedupe leaves no per-row
// quantity to cross-check against. A rowRe regression that still matches at
// least one row of every spot-checked section therefore passes here and is
// caught instead by
// TestDeprecatedDocSpellings_SectionMarkersMatchLegacySettingsSection below,
// which is the drift guard for deprecatedDocSpellings and compares the
// parsed set exactly.
func TestParseLegacySettingsSectionNames_ParsesRealFile(t *testing.T) {
	got := parseLegacySettingsSectionNames(t)

	seen := map[string]bool{}
	for i, name := range got {
		if name == "" {
			t.Errorf("parseLegacySettingsSectionNames()[%d] is empty", i)
		}
		if seen[name] {
			t.Errorf("parseLegacySettingsSectionNames() contains duplicate %q", name)
		}
		seen[name] = true
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("parseLegacySettingsSectionNames() = %v, want sorted", got)
	}

	for _, want := range []string{"repository", "models", "sandbox"} {
		if !seen[want] {
			t.Errorf("parseLegacySettingsSectionNames() = %v, want it to contain %q", got, want)
		}
	}
}

// TestParseLegacySettingsSectionNamesContent_IgnoresNixComments guards
// against the regex matching inside Nix line comments: a `#`-prefixed
// comment line that merely looks like a real `knob = "section";` row (e.g.
// "# historical note: phantomKnob = \"phantomSection\";") must never
// contribute a phantom section name to the parsed set.
func TestParseLegacySettingsSectionNamesContent_IgnoresNixComments(t *testing.T) {
	const synthetic = `{
  # historical note: phantomKnob = "phantomSection";
  repoSlug = "repository";
}
`

	got := parseLegacySettingsSectionNamesContent(t, synthetic)
	want := []string{"repository"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseLegacySettingsSectionNamesContent(t, synthetic) = %v, want %v (a '#'-comment row must not be picked up as a real section name)", got, want)
	}
}

// deprecatedDocSpellings are the old settings.<section>.<knob> shim path
// spellings findDeprecatedDocSpellings denylists in doc prose (README.md,
// docs/**/*.md). Mirrors quickstart/quickstart_test.go's
// deprecatedPathSpellings, but for docs prose rather than generated
// flake.nix output. Each entry is
// either a section-level marker ("settings.<section>", covering every knob
// under that section — lib/legacy-settings-section.nix has 9 unique section
// names, all denylisted here: repository, lifecycleLabels, issueDiscovery,
// branches, sandbox, models, concurrency, selfHealing,
// promptSkillIteration) or one of two hybrid markers
// ("settings.issues.forgejo", "settings.issues.research.verdicts") that
// never had ANY valid settings.* form at all (lib/env-schema.nix marks their
// knobs legacySettingsExempt = true) so they stay their own never-valid
// paths — there's no legacy "settings.issues" section in
// lib/legacy-settings-section.nix to generalize to in the first place. The
// bare flat structural-shim spellings
// (e.g. "runtime = " with no leading "infra.") are checked separately by
// findDeprecatedDocSpellings via flatShimGeneralizedMarkers below, since,
// unlike these, they can't be told apart from their canonical dotted form
// by substring alone.
var deprecatedDocSpellings = []string{
	"settings.repository",
	"settings.lifecycleLabels",
	"settings.issueDiscovery",
	"settings.branches",
	"settings.sandbox",
	"settings.models",
	"settings.concurrency",
	"settings.selfHealing",
	"settings.promptSkillIteration",
	"settings.issues.forgejo",
	"settings.issues.research.verdicts",
}

// TestDeprecatedDocSpellings_SectionMarkersMatchLegacySettingsSection is the
// drift guard for deprecatedDocSpellings' 9 section-level markers: it
// derives "settings.<section>" for every distinct section name
// parseLegacySettingsSectionNames finds in lib/legacy-settings-section.nix
// and asserts deprecatedDocSpellings contains exactly those, order
// independent. Maintenance strategy going forward: if
// lib/legacy-settings-section.nix ever gains or loses a distinct section
// name (e.g. a new legacy section is frozen in, or ADR 0037's removal at
// 1.0 deletes the file), this test fails until deprecatedDocSpellings is
// hand-updated to match — so the two lists can't silently drift apart. The
// two hybrid markers ("settings.issues.forgejo",
// "settings.issues.research.verdicts") are a deliberate, permanent
// carve-out from that derivation: their knobs are legacySettingsExempt in
// lib/env-schema.nix, meaning they never had a row in
// lib/legacy-settings-section.nix to begin with, so there is nothing to
// derive them from — this test asserts they're still present rather than
// trying to generate them.
func TestDeprecatedDocSpellings_SectionMarkersMatchLegacySettingsSection(t *testing.T) {
	sectionNames := parseLegacySettingsSectionNames(t)

	wantSectionMarkers := make([]string, 0, len(sectionNames))
	for _, name := range sectionNames {
		wantSectionMarkers = append(wantSectionMarkers, "settings."+name)
	}
	sort.Strings(wantSectionMarkers)

	const hybridForgejo = "settings.issues.forgejo"
	const hybridResearchVerdicts = "settings.issues.research.verdicts"

	var gotSectionMarkers []string
	hybridSeen := map[string]bool{}
	for _, spelling := range deprecatedDocSpellings {
		switch spelling {
		case hybridForgejo, hybridResearchVerdicts:
			hybridSeen[spelling] = true
		default:
			gotSectionMarkers = append(gotSectionMarkers, spelling)
		}
	}
	sort.Strings(gotSectionMarkers)

	if !reflect.DeepEqual(gotSectionMarkers, wantSectionMarkers) {
		t.Errorf("deprecatedDocSpellings section-level markers = %v, want %v (derived from lib/legacy-settings-section.nix)", gotSectionMarkers, wantSectionMarkers)
	}

	for _, hybrid := range []string{hybridForgejo, hybridResearchVerdicts} {
		if !hybridSeen[hybrid] {
			t.Errorf("deprecatedDocSpellings missing hand-maintained hybrid marker %q (legacySettingsExempt in lib/env-schema.nix, no legacy section to derive it from)", hybrid)
		}
	}
}

// flatShimGeneralizedMarkers generalizes the bare-flat-shim detection to
// every flake-module Consumer structural shim from lib/structural-paths.nix
// that has zero collision with a legitimate, non-deprecated doc usage
// today. canonicalPrefix is the domain-tree path (from
// lib/structural-paths.nix, joined by "." with a trailing ".") that must
// immediately precede a "<name> = " occurrence for it to be the canonical,
// non-deprecated spelling; an empty canonicalPrefix means the flat name is
// never even a suffix of its canonical dotted form in the first place —
// lib/structural-paths.nix renames the leaf itself for nixInBox and
// nixStoreWritable (to infra.nix.inBox and infra.nix.storeWritable), so no
// prefix could ever make the bare spelling canonical, and the check treats
// every bare occurrence as deprecated unconditionally.
//
// packages, roster, and nixpkgs are deliberately EXCLUDED from this list —
// do not "fix" that by adding them. This exclusion is contingent, not
// principled: every other marker below is *equally* a legitimate bare
// lib/mkHarness.nix parameter name — the three excluded here aren't
// uniquely "ambiguous" in some deeper sense. They're excluded only because,
// as the docs stand today, they're the ones that actually appear bare in
// doc prose and would false-positive if generalized: docs/reference.md's
// "Calling mkHarness directly" section uses `packages = p: [ p.go ];` and
// `nixpkgs = inputs.nixpkgs;` as literal, still-canonical bare mkHarness
// arguments; docs/reference.md also describes nix/dogfood-defaults.nix's
// own internal `roster = rosterLib.defaultRoster {...}` field; and
// README.md's `packages = [ config.packages.spindrift ];` is nixpkgs' own
// unrelated `mkShell` argument. Nothing here rules out a future doc example
// introducing a bare `driver = `, `overlays = `, or any other marker in
// this list in an equally legitimate way — when that happens, the fix is:
// (1) remove that marker from flatShimGeneralizedMarkers, (2) add it to
// flatShimDeliberateCollisions below, and (3) update wantCollisions inside
// TestFlatShimGeneralizedMarkers_ExcludesDeliberateCollisions to match —
// that test pins flatShimDeliberateCollisions's exact contents, so step (2)
// alone still fails the pin.
// TestFlatShimGeneralizedMarkers_ExcludesDeliberateCollisions enforces the
// two lists stay disjoint.
var flatShimGeneralizedMarkers = []struct {
	name            string
	canonicalPrefix string
}{
	{"runtime", "infra."},
	{"driver", "agents."},
	{"prompt", "agents."},
	{"skills", "agents."},
	{"prefetch", "infra.image."},
	{"extraClosures", "infra.image."},
	{"overlays", "infra."},
	{"config", "infra."},
	{"nixInBox", ""},
	{"nixStoreWritable", ""},
}

// flatShimDeliberateCollisions lists flat marker names deliberately excluded
// from flatShimGeneralizedMarkers because each collides with a live bare doc
// usage today; see the doc comment on flatShimGeneralizedMarkers for the
// full rationale and
// TestFlatShimGeneralizedMarkers_ExcludesDeliberateCollisions for
// enforcement.
var flatShimDeliberateCollisions = []string{"packages", "roster", "nixpkgs"}

// findDeprecatedDocSpellings scans content for any deprecatedDocSpellings
// substring, plus any occurrence anywhere in the content (not just at a
// line start — a bare spelling can appear mid-sentence in doc prose, e.g.
// README.md's "(or set `runtime = \"docker\"`; ...)") of a bare flat
// structural-shim spelling from flatShimGeneralizedMarkers, distinguished
// from its canonical dotted spelling by checking the immediately preceding
// characters aren't that marker's canonicalPrefix. Reports a single finding
// per marker if any bare occurrence is found, consistent with how the
// deprecatedDocSpellings markers report once per marker regardless of
// occurrence count.
func findDeprecatedDocSpellings(content string) []string {
	var found []string
	for _, deprecated := range deprecatedDocSpellings {
		if strings.Contains(content, deprecated) {
			found = append(found, deprecated)
		}
	}

	for _, shim := range flatShimGeneralizedMarkers {
		marker := shim.name + " = "
		searchFrom := 0
		for {
			idx := strings.Index(content[searchFrom:], marker)
			if idx < 0 {
				break
			}
			matchStart := searchFrom + idx
			if shim.canonicalPrefix == "" {
				found = append(found, marker)
				break
			}
			precedingStart := matchStart - len(shim.canonicalPrefix)
			if precedingStart < 0 || content[precedingStart:matchStart] != shim.canonicalPrefix {
				found = append(found, marker)
				break
			}
			searchFrom = matchStart + len(marker)
		}
	}

	return found
}

// bareOccurrenceExists reports whether name appears in content as a bare
// "<name> = " assignment — one not immediately preceded by "." — as
// opposed to only ever appearing as the tail of a longer dotted attribute
// path (e.g. "infra.image.packages = "). Unlike findDeprecatedDocSpellings'
// canonicalPrefix check, which validates against one specific known-dotted
// spelling per marker, this rejects any dotted prefix at all, since a
// flatShimDeliberateCollisions name (packages, roster, nixpkgs) has no
// single canonical dotted form to compare against — it can legitimately
// appear dotted under several unrelated attribute paths.
func bareOccurrenceExists(content, name string) bool {
	marker := name + " = "
	searchFrom := 0
	for {
		idx := strings.Index(content[searchFrom:], marker)
		if idx < 0 {
			return false
		}
		matchStart := searchFrom + idx
		if matchStart == 0 || content[matchStart-1] != '.' {
			return true
		}
		searchFrom = matchStart + len(marker)
	}
}

// TestFindDeprecatedDocSpellings_DetectsReintroducedSpelling is the
// guard-demo acceptance criterion from issue #2566: it proves the lint
// helper actually fails on a reintroduced deprecated spelling, covering a
// deprecatedDocSpellings marker, the bare runtime = "..." structural shim,
// and clean content (including the canonical infra.runtime spelling) that
// reports nothing. The bare form (runtime = ) reports without its trailing
// quote, matching every other flatShimGeneralizedMarkers entry.
func TestFindDeprecatedDocSpellings_DetectsReintroducedSpelling(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "deprecated marker present",
			content: "Configure it via `settings.branches.mergeGuardPaths` (baked).",
			want:    []string{"settings.branches"},
		},
		{
			name:    "bare runtime structural shim",
			content: "  runtime = \"docker\";\n",
			want:    []string{"runtime = "},
		},
		{
			name:    "clean content with canonical spelling",
			content: "  infra.runtime = \"docker\";\n",
			want:    nil,
		},
		{
			name: "mid-sentence bare runtime structural shim (README shape)",
			content: "- **podman** (or set `runtime = \"docker\"`; `runtime = \"rancher\"` for Rancher\n" +
				"  Desktop in containerd mode, driven via `nerdctl`; or `runtime = \"bwrap\"` for\n" +
				"  the daemonless bubblewrap sandbox on Linux, which needs no container runtime).",
			want: []string{"runtime = "},
		},
		{
			name:    "mid-sentence canonical spelling not reported",
			content: "- **podman** (or set `infra.runtime = \"docker\"` for the shim).",
			want:    nil,
		},
		{
			name:    "widened settings.branches section marker (not just mergeGuardPaths)",
			content: "Set it via `settings.branches.baseBranch` (deprecated).",
			want:    []string{"settings.branches"},
		},
		{
			name:    "newly added settings.sandbox section marker",
			content: "Configure `settings.sandbox.devShellName` (deprecated).",
			want:    []string{"settings.sandbox"},
		},
		{
			name:    "newly added settings.models section marker",
			content: "Configure `settings.models.filerModel` (deprecated).",
			want:    []string{"settings.models"},
		},
		{
			name:    "newly added settings.concurrency section marker",
			content: "Configure `settings.concurrency.maxParallel` (deprecated).",
			want:    []string{"settings.concurrency"},
		},
		{
			name:    "newly added settings.selfHealing section marker",
			content: "Configure `settings.selfHealing.maxFixAttempts` (deprecated).",
			want:    []string{"settings.selfHealing"},
		},
		{
			name:    "newly added settings.promptSkillIteration section marker",
			content: "Configure `settings.promptSkillIteration.autoFormat` (deprecated).",
			want:    []string{"settings.promptSkillIteration"},
		},
		{
			name:    "bare nixInBox flat structural shim (empty canonicalPrefix — no dotted prefix could make it canonical)",
			content: "  nixInBox = false;\n",
			want:    []string{"nixInBox = "},
		},
		{
			name:    "bare nixStoreWritable flat structural shim (empty canonicalPrefix — no dotted prefix could make it canonical)",
			content: "  nixStoreWritable = true;\n",
			want:    []string{"nixStoreWritable = "},
		},
		{
			name:    "bare prefetch flat structural shim",
			content: "  prefetch = [ ];\n",
			want:    []string{"prefetch = "},
		},
		{
			name:    "canonical agents.prompt spelling not reported",
			content: "  agents.prompt = \"...\";\n",
			want:    nil,
		},
		{
			name:    "canonical infra.image.prefetch spelling not reported",
			content: "  infra.image.prefetch = [ ];\n",
			want:    nil,
		},
		{
			// packages/roster/nixpkgs are deliberately NOT generalized: each
			// collides with a legitimate, non-deprecated doc usage today —
			// docs/reference.md's "Calling mkHarness directly" section uses
			// `packages = p: [ p.go ];` and `nixpkgs = inputs.nixpkgs;` as
			// literal, still-canonical bare mkHarness arguments;
			// docs/reference.md also documents nix/dogfood-defaults.nix's
			// own `roster = rosterLib.defaultRoster {...}` field; and
			// README.md's `packages = [ config.packages.spindrift ];` is
			// nixpkgs' own unrelated mkShell argument. Do not add these
			// three to flatShimGeneralizedMarkers — it would turn this
			// check into a false-positive generator.
			name:    "packages/roster/nixpkgs deliberately not generalized",
			content: "  packages = p: [ p.go ];\n  roster = rosterLib.defaultRoster {};\n  nixpkgs = inputs.nixpkgs;\n",
			want:    nil,
		},
		{
			name: "multiple markers in one input accumulate in declaration order",
			content: "Configure `settings.sandbox.devShellName` and `settings.models.filerModel`.\n" +
				"  runtime = \"docker\";\n" +
				"  nixInBox = false;\n",
			want: []string{"settings.sandbox", "settings.models", "runtime = ", "nixInBox = "},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findDeprecatedDocSpellings(tc.content)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findDeprecatedDocSpellings(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// TestFlatShimGeneralizedMarkers_ExcludesDeliberateCollisions enforces, at
// the Go level, the prose rule documented above flatShimGeneralizedMarkers:
// packages, roster, and nixpkgs must never be (re-)added to it, because each
// collides with a legitimate, non-deprecated bare doc usage today. This
// turns "do not fix that by adding them" from a comment a human might miss
// into a test that fails the moment someone does.
//
// It first pins flatShimDeliberateCollisions to its exact expected
// membership (compared order-insensitively — this test cares which names
// are excluded, not what order they're declared in) — otherwise shrinking
// or emptying that list (e.g. moving "packages" out of it and into
// flatShimGeneralizedMarkers) would make the exclusion loop below assert
// nothing and stay green through exactly the regression this test exists
// to catch. Deliberately re-pinned on every edit to flatShimDeliberateCollisions,
// including a pure reorder: that friction is the point, not an oversight to
// simplify away. It then confirms each collision name still has a bare
// occurrence (not a suffix of some longer dotted attribute path, e.g.
// infra.image.packages) somewhere across README.md and docs/, tying the
// carve-out to the actual doc contingency it claims to rest on: if those
// doc examples are ever rewritten to the dotted canonical form, this test
// flags that the carve-out is no longer justified.
func TestFlatShimGeneralizedMarkers_ExcludesDeliberateCollisions(t *testing.T) {
	wantCollisions := []string{"packages", "roster", "nixpkgs"}
	gotCollisions := append([]string(nil), flatShimDeliberateCollisions...)
	sort.Strings(gotCollisions)
	sortedWant := append([]string(nil), wantCollisions...)
	sort.Strings(sortedWant)
	if !reflect.DeepEqual(gotCollisions, sortedWant) {
		t.Fatalf("flatShimDeliberateCollisions = %v, want (any order) %v", flatShimDeliberateCollisions, wantCollisions)
	}

	for _, shim := range flatShimGeneralizedMarkers {
		for _, collision := range flatShimDeliberateCollisions {
			if shim.name == collision {
				t.Errorf("flatShimGeneralizedMarkers contains %q, which is a deliberate collision that must stay excluded", shim.name)
			}
		}
	}

	docs := collectMarkdownDocs(t)
	for _, collision := range flatShimDeliberateCollisions {
		found := false
		for _, doc := range docs {
			if bareOccurrenceExists(doc.content, collision) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("deliberate collision %q no longer appears bare in README.md or docs/ — the carve-out may no longer be justified; if so, move it into flatShimGeneralizedMarkers and update wantCollisions in this test to match", collision)
		}
	}
}

// parseStructuralPaths reads lib/structural-paths.nix and returns the full
// map of flat structural knob name to its ordered domain-tree path segments
// (e.g. "driver" -> ["agents", "driver"]). Test-only: production code never
// parses this file directly (lib/flakeModule.nix and
// nix/checks/schema-drift.nix consume it as Nix data), so this helper has no
// non-test counterpart. Mirrors parseLegacySettingsSectionNames above in
// shape and style.
func parseStructuralPaths(t *testing.T) map[string][]string {
	t.Helper()

	return parseStructuralPathsContent(t, readStructuralPathsFile(t))
}

// readStructuralPathsFile returns the raw text of lib/structural-paths.nix.
// Split out of parseStructuralPaths so the cross-check in
// TestParseStructuralPaths_ParsesRealFile, which needs the same content to
// scan a second way, does not repeat the path and its read-failure message.
func readStructuralPathsFile(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("..", "..", "lib", "structural-paths.nix"))
	if err != nil {
		t.Fatalf("read lib/structural-paths.nix: %v", err)
	}
	return string(content)
}

// fataler is the minimal slice of *testing.T's failure-reporting surface
// parseLegacySettingsSectionNamesContent and parseStructuralPathsContent
// need. Accepting this interface instead of the concrete *testing.T lets a
// test substitute a fake that records a Fatalf call instead of tearing down
// the calling goroutine via runtime.Goexit -- necessary to assert "did it
// fail cleanly" from the very test goroutine making that assertion.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// stripNixLineComments strips Nix line comments (an unescaped '#' to end of
// line) from content before regex-matching, so a commented-out row that
// merely looks like a real data row (e.g. `# historical note: repoSlug =
// "sandbox";`) is never picked up as one. Sufficient for the flat, plain
// attrset fixture files this package parses (lib/legacy-settings-section.nix,
// lib/structural-paths.nix), which contain no string literals with '#' in
// them; deliberately not a general Nix tokenizer.
func stripNixLineComments(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "#"); idx != -1 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// parseStructuralPathsContent is parseStructuralPaths' content-parsing core,
// split out so tests can exercise it directly against synthetic content
// (e.g. to prove it handles comment lines and empty segment lists correctly)
// without round-tripping through the real lib/structural-paths.nix file.
func parseStructuralPathsContent(t fataler, content string) map[string][]string {
	t.Helper()

	content = stripNixLineComments(content)

	entryRe := regexp.MustCompile(`(\w+)\s*=\s*\[([^\]]*)\]`)
	segmentRe := regexp.MustCompile(`"([^"]+)"`)

	paths := map[string][]string{}
	for _, entry := range entryRe.FindAllStringSubmatch(content, -1) {
		name := entry[1]
		var segments []string
		for _, segment := range segmentRe.FindAllStringSubmatch(entry[2], -1) {
			segments = append(segments, segment[1])
		}
		if len(segments) == 0 {
			t.Fatalf("lib/structural-paths.nix entry %q has no segments; regex out of sync with file format?", name)
		}
		paths[name] = segments
	}
	if len(paths) == 0 {
		t.Fatalf("parsed zero entries from lib/structural-paths.nix; regex out of sync with file format?")
	}

	return paths
}

// fatalRecorder is a fake fataler that records whether Fatalf was called
// instead of tearing down the calling goroutine, so a test can observe "did
// the code under test fail cleanly" from its own goroutine.
type fatalRecorder struct {
	called  bool
	message string
}

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.called = true
	r.message = fmt.Sprintf(format, args...)
}

// TestParseStructuralPathsContent_EmptySegmentListFailsCleanly guards against
// a silent slice-bounds panic: if lib/structural-paths.nix ever contains an
// entry whose list has no string segments (e.g. "emptyThing = [ ];"),
// parseStructuralPathsContent must fail cleanly via Fatalf, naming the
// offending entry, rather than silently storing an empty segments slice for
// TestFlatShimGeneralizedMarkers_MatchesStructuralPaths's
// `segments[len(segments)-1]` lookup to panic on later.
func TestParseStructuralPathsContent_EmptySegmentListFailsCleanly(t *testing.T) {
	const synthetic = `{
  emptyThing = [ ];
  driver = [
    "agents"
    "driver"
  ];
}
`

	rec := &fatalRecorder{}
	var panicked any
	var paths map[string][]string
	func() {
		defer func() { panicked = recover() }()
		paths = parseStructuralPathsContent(rec, synthetic)
	}()

	if panicked != nil {
		t.Fatalf("parseStructuralPathsContent panicked instead of failing cleanly via Fatalf: %v", panicked)
	}
	if rec.called {
		if !strings.Contains(rec.message, "emptyThing") {
			t.Errorf("parseStructuralPathsContent's Fatalf message = %q, want it to name the offending entry %q", rec.message, "emptyThing")
		}
		return
	}

	// Pre-fix, parseStructuralPathsContent never calls Fatalf and silently
	// stores an empty segments slice for "emptyThing"; replicate the exact
	// downstream indexing TestFlatShimGeneralizedMarkers_MatchesStructuralPaths
	// performs on the result, which is what actually panics, so this failure
	// reproduces the real bug precisely rather than merely asserting "Fatalf
	// was never called".
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseStructuralPathsContent silently stored an empty segments slice for an entry, which panics downstream consumers (e.g. TestFlatShimGeneralizedMarkers_MatchesStructuralPaths) instead of failing cleanly: %v", r)
			}
		}()
		for name, segments := range paths {
			_ = segments[len(segments)-1] != name
		}
	}()
	t.Fatalf("parseStructuralPathsContent(rec, synthetic) with an empty-segment entry neither called Fatalf nor panicked; want a clean failure naming the offending entry")
}

// TestParseStructuralPathsContent_IgnoresNixComments guards against the
// regex matching inside Nix line comments: a `#`-prefixed comment line that
// merely looks like a real `name = [ ... ];` row (e.g. "# historical note:
// phantomThing = [ \"phantom\" ];") must never contribute a phantom entry to
// the parsed map.
func TestParseStructuralPathsContent_IgnoresNixComments(t *testing.T) {
	const synthetic = `{
  # historical note: phantomThing = [ "phantom" ];
  driver = [
    "agents"
    "driver"
  ];
}
`

	got := parseStructuralPathsContent(t, synthetic)
	want := map[string][]string{
		"driver": {"agents", "driver"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseStructuralPathsContent(t, synthetic) = %v, want %v (a '#'-comment row must not be picked up as a real entry)", got, want)
	}
}

// TestParseStructuralPaths_ParsesRealFile is the canary for
// parseStructuralPaths: it proves the regexes parse the real
// lib/structural-paths.nix rather than pinning that file's full output, so a
// new entry there needs no edit here. It spot-checks three entries unlikely
// to churn (driver: 2 flat segments; roster: 3, nested; nixStoreWritable:
// leaf name != knob name, the case
// TestFlatShimGeneralizedMarkers_MatchesStructuralPaths depends on), then
// cross-checks the parsed entry and segment counts against
// countStructuralPathShapes. TestFlatShimGeneralizedMarkers_MatchesStructuralPaths
// below is the actual drift guard for flatShimGeneralizedMarkers.
func TestParseStructuralPaths_ParsesRealFile(t *testing.T) {
	got := parseStructuralPaths(t)

	wantEntries := map[string][]string{
		"driver":           {"agents", "driver"},
		"roster":           {"agents", "models", "roster"},
		"nixStoreWritable": {"infra", "nix", "storeWritable"},
	}
	for name, want := range wantEntries {
		if segments, ok := got[name]; !ok || !reflect.DeepEqual(segments, want) {
			t.Errorf("parseStructuralPaths()[%q] = %v, want %v", name, segments, want)
		}
	}

	wantEntryCount, wantSegmentCount := countStructuralPathShapes(t, readStructuralPathsFile(t))

	if len(got) != wantEntryCount {
		t.Errorf("parseStructuralPaths() has %d entries, crude line scan of lib/structural-paths.nix counted %d entry headers", len(got), wantEntryCount)
	}
	gotSegmentCount := 0
	for _, segments := range got {
		gotSegmentCount += len(segments)
	}
	if gotSegmentCount != wantSegmentCount {
		t.Errorf("parseStructuralPaths() has %d total segments, crude line scan of lib/structural-paths.nix counted %d segment lines", gotSegmentCount, wantSegmentCount)
	}
}

// countStructuralPathShapes counts entry-header lines and segment lines in
// structural-paths.nix content by trimmed-line shape alone, never by
// entryRe/segmentRe — the regexes parseStructuralPathsContent itself uses.
// Counting the file a second, independent way is what gives
// TestParseStructuralPaths_ParsesRealFile's cross-check its teeth: a regex
// regression moves one count and not the other, so the two disagree.
//
// Reading shape alone means this scan assumes the nixfmt layout the file has
// today, one segment string per line with the list brackets on their own
// lines. A hand-written one-line entry (`config = [ "infra" "config" ];`)
// counts as an entry header with no segments, so the cross-check fails even
// though parseStructuralPathsContent read it correctly. That is the intended
// trade-off: a reformat of this file is exactly the change that could
// silently hollow out the drift guards, so it should stop the build and be
// looked at rather than pass quietly.
//
// Deliberately a package-private near-duplicate of quickstart_test.go's
// countLegacySettingsRows — see the doc comment on that package's
// parseLegacySettingsSections for why these test-only Nix scanners are
// duplicated across the package boundary rather than shared.
func countStructuralPathShapes(t *testing.T, content string) (entryCount, segmentCount int) {
	t.Helper()

	for _, line := range strings.Split(stripNixLineComments(content), "\n") {
		switch trimmed := strings.TrimSpace(line); {
		case trimmed == "", trimmed == "{", trimmed == "}", trimmed == "];":
			// Not a line entryRe/segmentRe would ever match.
		case strings.HasPrefix(trimmed, `"`):
			segmentCount++
		default:
			entryCount++
		}
	}
	return entryCount, segmentCount
}

// TestFlatShimGeneralizedMarkers_MatchesStructuralPaths is the drift guard
// for flatShimGeneralizedMarkers' canonicalPrefix values: for every entry in
// lib/structural-paths.nix that isn't in flatShimDeliberateCollisions (see
// the doc comment on flatShimGeneralizedMarkers for why those three are
// excluded — that carve-out is enforced separately by
// TestFlatShimGeneralizedMarkers_ExcludesDeliberateCollisions, not here), it
// derives the expected canonicalPrefix as the entry's segments minus their
// final (leaf) element, joined by "." with a trailing "." — the flat name
// itself stands in for that final segment when findDeprecatedDocSpellings
// checks for the canonical "<canonicalPrefix><name> = " form — and asserts
// flatShimGeneralizedMarkers contains exactly that (name, canonicalPrefix)
// pair — no more, no fewer. Maintenance strategy going forward: if
// lib/structural-paths.nix ever gains, loses, or renames an entry, this test
// fails until flatShimGeneralizedMarkers is hand-updated to match, so the
// two can't silently drift apart.
//
// The general rule this derivation applies: canonicalPrefix can only be
// derived as "segments-minus-leaf, dotted" when the entry's flat name is
// itself the leaf (final) segment — that's what lets the flat name stand in
// for the leaf when reconstructing the canonical dotted form. Whenever
// lib/structural-paths.nix renames the leaf segment away from the flat name,
// the flat name is never even a suffix of its canonical dotted form in the
// first place, so no derived prefix could ever be correct, and the expected
// canonicalPrefix must be "" instead. nixInBox and nixStoreWritable are the
// two current instances of this (leaf segments inBox, storeWritable — not
// nixInBox, nixStoreWritable), but the check below is keyed on the general
// leaf-segment-equals-flat-name condition, not on those two literal names,
// so it stays correct if lib/structural-paths.nix ever gains another
// renamed-leaf entry.
func TestFlatShimGeneralizedMarkers_MatchesStructuralPaths(t *testing.T) {
	structuralPaths := parseStructuralPaths(t)

	collisions := map[string]bool{}
	for _, name := range flatShimDeliberateCollisions {
		collisions[name] = true
	}

	want := map[string]string{}
	for name, segments := range structuralPaths {
		if collisions[name] {
			continue
		}
		if segments[len(segments)-1] != name {
			want[name] = ""
			continue
		}
		want[name] = strings.Join(segments[:len(segments)-1], ".") + "."
	}

	got := map[string]string{}
	for _, shim := range flatShimGeneralizedMarkers {
		got[shim.name] = shim.canonicalPrefix
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("flatShimGeneralizedMarkers (name -> canonicalPrefix) = %v, want %v (derived from lib/structural-paths.nix, excluding flatShimDeliberateCollisions)", got, want)
	}
}

// namedDoc pairs a doc's repo-relative display name with its content, for
// tests that scan across README.md and docs/.
type namedDoc struct {
	name    string
	content string
}

// collectMarkdownDocs returns README.md plus every .md file under docs/
// (recursively — docs/adr/*.md, docs/console.md, docs/flake-options.md,
// docs/measurements/*.md, docs/reference.md, ...), for tests that scan doc
// content. MIGRATING.md is the one place deprecated spellings are expected
// and documented on purpose, so the docs/ walk skips it by name (not just
// because it currently lives at the repo root, outside docs/) — moving it
// under docs/ must not silently start linting it.
func collectMarkdownDocs(t *testing.T) []namedDoc {
	t.Helper()

	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}

	docs := []namedDoc{{"README.md", string(readme)}}

	docsDir := filepath.Join("..", "..", "docs")
	walkErr := filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		if info.Name() == "MIGRATING.md" {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relName, relErr := filepath.Rel(filepath.Join("..", ".."), path)
		if relErr != nil {
			relName = path
		}
		docs = append(docs, namedDoc{relName, string(content)})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk docs/: %v", walkErr)
	}

	return docs
}

// TestDocsHaveNoDeprecatedSpellings guards every doc collectMarkdownDocs
// returns against the deprecatedDocSpellings old settings.<section>.<knob>
// shim paths (and the bare flat structural-shim spellings) creeping back
// in. This is the integration counterpart to
// TestFindDeprecatedDocSpellings_DetectsReintroducedSpelling, which only
// exercises the checker against synthetic content — this test runs it
// against the real docs (#2566).
func TestDocsHaveNoDeprecatedSpellings(t *testing.T) {
	for _, doc := range collectMarkdownDocs(t) {
		if found := findDeprecatedDocSpellings(doc.content); len(found) > 0 {
			t.Errorf("%s contains %d deprecated spelling(s): %v", doc.name, len(found), found)
		}
	}
}

// TestTriageLabelMeta_ColorsAreDistinct guards against two label tiers
// visually colliding in the GitHub label UI by reusing the same hex color
// (#801) — TestReferenceDocLabelSnippetMatchesTriageDefaults checks
// docs/code parity per name but never asserts uniqueness across the map.
func TestTriageLabelMeta_ColorsAreDistinct(t *testing.T) {
	byColor := map[string][]string{}
	for name, meta := range doctor.TriageLabelMeta {
		byColor[meta.Color] = append(byColor[meta.Color], name)
	}
	for color, names := range byColor {
		if len(names) > 1 {
			t.Errorf("color %q reused by %d labels %v, want distinct colors", color, len(names), names)
		}
	}
}

// TestDoctor_ReadOnlyTokenGate_ReadWriteReportsNoOp verifies runDoctor
// surfaces checkReadOnlyTokenGate's outcome (issue #1950): under read-write,
// it prints an explicit no-op line rather than staying silent, so an
// operator scanning doctor output isn't left wondering whether the gate ran.
// A code-review round on issue #2942 found this test pinning the bug it was
// meant to catch: gateRegistry's two token gate entries' Applicable used to
// check only backend match, so under read-write walkGateRegistry still
// called Check (which self-noops and returns nil) and printed a false
// "ok: read-only-token-github"/"ok: read-only-token-forgejo" for a check
// that never ran against anything real -- silently dropping the explicit
// "... is a no-op" line the deleted reportReadOnlyTokenGate used to print
// for read-write. Applicable now also requires read-only
// (launchgates.go), so both token gates are skipped entirely under
// read-write (no Check call, no per-backend report line), and runDoctor
// (doctor.go) restores the explicit no-op line itself. issueTracker is set
// to forgejo (codeForge stays github) to prove *both* token gates stay
// silent, not just the github one.
func TestDoctor_ReadOnlyTokenGate_ReadWriteReportsNoOp(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	c := defaultLabelConfig()
	c.boxForgeAndIssueAccess = "read-write"
	c.issueTracker = "forgejo"

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ok: BOX_FORGE_AND_ISSUE_ACCESS=read-write — read-only token gate is a no-op") {
		t.Errorf("want the explicit read-write no-op line, got %q", out)
	}
	if strings.Contains(out, "read-only-token-github") {
		t.Errorf("want the read-only-token-github gate skipped entirely (Applicable requires read-only), got %q", out)
	}
	if strings.Contains(out, "read-only-token-forgejo") {
		t.Errorf("want the read-only-token-forgejo gate skipped entirely (Applicable requires read-only), got %q", out)
	}
}

// TestDoctor_ReadOnlyTokenGate_MissingBoxTokenFails verifies runDoctor fails
// under read-only when BOX_GH_TOKEN is unset, the same fail-closed outcome a
// live dispatch would hit at bootstrap.
func TestDoctor_ReadOnlyTokenGate_MissingBoxTokenFails(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	c := defaultLabelConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "launcher-token"
	t.Setenv("BOX_GH_TOKEN", "")

	var buf bytes.Buffer
	err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c))
	if err == nil || !strings.Contains(err.Error(), "BOX_GH_TOKEN") {
		t.Fatalf("runDoctor() error = %v, want a BOX_GH_TOKEN error", err)
	}
}

// TestDoctor_ReadOnlyTokenGate_NonIntrospectableTokenDoesNotClaimVerified
// verifies runDoctor's success line never claims a fine-grained PAT's write
// capability was confirmed (it wasn't -- the gate just accepted it on trust
// and printed a warning). A prior version printed a fixed "confirmed
// not write-capable" success line unconditionally, contradicting the
// warning it had just printed for exactly this case.
func TestDoctor_ReadOnlyTokenGate_NonIntrospectableTokenDoesNotClaimVerified(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	c := defaultLabelConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "launcher-token"
	c.repoSlug = "owner/repo"
	t.Setenv("BOX_GH_TOKEN", "github_pat_boxtoken")

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "WARNING") {
		t.Fatalf("want the gate's warning printed, got %q", out)
	}
	if strings.Contains(out, "confirmed not write-capable") {
		t.Errorf("doctor claimed write-capability was confirmed for a non-introspectable token, got %q", out)
	}
}

// TestDoctor_ReadOnlyForgejoTokenGate_MissingBoxTokenFails verifies runDoctor
// also surfaces checkReadOnlyForgejoTokenGate's outcome (issue #1964) when
// forgejo is the active backend: under read-only with BOX_FORGEJO_TOKEN
// unset, doctor fails the same fail-closed way a live dispatch would at
// bootstrap.
func TestDoctor_ReadOnlyForgejoTokenGate_MissingBoxTokenFails(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	c := defaultLabelConfig()
	c.codeForge = "forgejo"
	c.issueTracker = "forgejo"
	c.boxForgeAndIssueAccess = "read-only"
	c.forgejoToken = "launcher-token"
	t.Setenv("BOX_FORGEJO_TOKEN", "")

	var buf bytes.Buffer
	err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c))
	if err == nil || !strings.Contains(err.Error(), "BOX_FORGEJO_TOKEN") {
		t.Fatalf("runDoctor() error = %v, want a BOX_FORGEJO_TOKEN error", err)
	}
}

// TestDoctor_ReadOnlyForgejoTokenGate_DistinctTokenWarns verifies runDoctor
// prints the forgejo gate's non-introspectable warning (Forgejo has no
// scope-introspection endpoint) rather than claiming write-capability was
// confirmed, when BOX_FORGEJO_TOKEN is set and distinct from FORGEJO_TOKEN.
func TestDoctor_ReadOnlyForgejoTokenGate_DistinctTokenWarns(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	c := defaultLabelConfig()
	c.codeForge = "forgejo"
	c.issueTracker = "forgejo"
	c.boxForgeAndIssueAccess = "read-only"
	c.forgejoToken = "launcher-token"
	t.Setenv("BOX_FORGEJO_TOKEN", "box-token")

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "WARNING") {
		t.Fatalf("want the forgejo gate's warning printed, got %q", out)
	}
	if strings.Contains(out, "confirmed not write-capable") {
		t.Errorf("doctor claimed write-capability was confirmed for a forgejo token, got %q", out)
	}
}

// TestDoctor_ReadOnlyTokenGates_BothBackendsActiveOnDifferentAxes verifies
// runDoctor reports both the github and forgejo read-only token gates in a
// single call when the two backends are active on different axes at once
// (CODE_FORGE=github, ISSUE_TRACKER=forgejo) — a regression pin for the
// walk over gateRegistry's two token-gate entries (via runDoctor's
// walkGateRegistry call, issue #2942), which must run every matching gate
// rather than stopping after the first.
func TestDoctor_ReadOnlyTokenGates_BothBackendsActiveOnDifferentAxes(t *testing.T) {
	it := forge.NewFake()
	it.ProbeRepo = "PROJ"
	it.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	c := defaultLabelConfig()
	c.codeForge = "github"
	c.issueTracker = "forgejo"
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "launcher-token"
	c.repoSlug = "owner/repo"
	c.forgejoToken = "forgejo-launcher-token"
	t.Setenv("BOX_GH_TOKEN", "box-gh-token")
	t.Setenv("BOX_FORGEJO_TOKEN", "box-forgejo-token")

	var buf bytes.Buffer
	if err := runDoctor(it, cf, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	// walkGateRegistry (issue #2942) reports each passing gate with its
	// generic "ok: <name>" line rather than the row's bespoke
	// readOnlyGateOkMessage text the deleted reportReadOnlyTokenGates used
	// to print; the generic lines are equally proof both gates ran.
	if !strings.Contains(out, "ok: read-only-token-github") {
		t.Errorf("want the github gate's success line, got %q", out)
	}
	if !strings.Contains(out, "ok: read-only-token-forgejo") {
		t.Errorf("want the forgejo gate's success line, got %q", out)
	}
}

// TestLoadConfig_RunnerKind_NoRuntimeFallback_Bwrap pins issue #2538 AC1
// ("the artifact rides the document; the launcher performs no runtime-name
// comparison to determine kind"): RUNTIME=bwrap with RUNNER_KIND genuinely
// absent must NOT derive runnerKind from the runtime name -- it resolves to
// "", the same empty default every other absent getenvArtifact call gets.
func TestLoadConfig_RunnerKind_NoRuntimeFallback_Bwrap(t *testing.T) {
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "")
	os.Unsetenv("RUNNER_KIND")

	c := loadConfig()

	if c.runnerKind != "" {
		t.Errorf("loadConfig().runnerKind = %q, want %q (RUNTIME must not be consulted)", c.runnerKind, "")
	}
}

// TestLoadConfig_RunnerKind_ReadsArtifactRegardlessOfRuntime proves
// RUNNER_KIND is read verbatim from the artifact/env, independent of
// RUNTIME's value -- including a RUNTIME=bwrap/RUNNER_KIND=oci combination
// that a runtime-name comparison would get wrong.
func TestLoadConfig_RunnerKind_ReadsArtifactRegardlessOfRuntime(t *testing.T) {
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "oci")

	c := loadConfig()

	if c.runnerKind != "oci" {
		t.Errorf("loadConfig().runnerKind = %q, want %q (RUNNER_KIND read verbatim)", c.runnerKind, "oci")
	}
}

// TestLoadConfig_FlakeLauncherAttr_ReadsArtifact proves loadConfig() reads
// FLAKE_LAUNCHER_ATTR into config.flakeLauncherAttr, mirroring how
// flakeImageAttr/FLAKE_IMAGE_ATTR is nix-rendered into the artifacts section
// (issue #2677 slice 3): the launcher-currency check needs the launcher's own
// flake attr, distinct from the OCI image's.
func TestLoadConfig_FlakeLauncherAttr_ReadsArtifact(t *testing.T) {
	t.Setenv("FLAKE_LAUNCHER_ATTR", ".#launcher-currency")

	c := loadConfig()

	if c.flakeLauncherAttr != ".#launcher-currency" {
		t.Errorf("loadConfig().flakeLauncherAttr = %q, want %q", c.flakeLauncherAttr, ".#launcher-currency")
	}
}

// TestLoadConfig_LoadedLauncherHash_ReadsArtifact proves loadConfig() reads
// LAUNCHER_CURRENCY_HASH into config.loadedLauncherHash, mirroring how
// FLAKE_LAUNCHER_ATTR/flakeLauncherAttr is wired above (issue #1364 slice 4):
// freshness.Probe's launcher-staleness comparison needs the loaded
// launcher's own store hash, computed at build time by lib/preambles.nix and
// lib/mkHarness.nix (issue #2677) and rendered into the artifacts section
// alongside FLAKE_LAUNCHER_ATTR, distinct from the OCI image's IMAGE_TAG.
func TestLoadConfig_LoadedLauncherHash_ReadsArtifact(t *testing.T) {
	t.Setenv("LAUNCHER_CURRENCY_HASH", "abc123")

	c := loadConfig()

	if c.loadedLauncherHash != "abc123" {
		t.Errorf("loadConfig().loadedLauncherHash = %q, want %q", c.loadedLauncherHash, "abc123")
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// TestEngageAliasRemoved asserts that the deprecated `engage` subcommand
// handler has been deleted from main.go. The handler was removed in v0.2.0;
// this test prevents accidental re-introduction.
func TestEngageAliasRemoved(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	if strings.Contains(string(data), `args[0] == "engage"`) {
		t.Error(`main.go still dispatches the deprecated "engage" subcommand; remove the handler`)
	}
}

// TestBootstrapExitCode verifies bootstrapExitCode's full error-to-exit-code
// mapping (issue #2568 slice 1): nil maps to 0, an error wrapping
// errConfigInvalid (as bootstrap() now produces on a validate() failure)
// maps to the dedicated exitConfigInvalid (6), and any other error falls
// back to the generic 1 — mirroring TestExitCodeFor's table-driven shape for
// exitCodeFor.
func TestBootstrapExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"wrapped errConfigInvalid", fmt.Errorf("%w: %w", errConfigInvalid, errors.New("REPO_SLUG is required")), exitConfigInvalid},
		{"other error", errors.New("boom"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bootstrapExitCode(tc.err); got != tc.want {
				t.Errorf("bootstrapExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestBootstrapExitCode_ReadOnlyTokenGateMisconfigured_ExitsOne is the
// regression test for the review-flagged bug (issue #2569 follow-up):
// checkReadOnlyTokenGate/checkReadOnlyForgejoTokenGate are called directly
// by bootstrap() (bootstrap.go), and by preview() (preview.go) through
// gatedcontext.go's newGatedContext, not just from doctor.go's runDoctor.
// Wrapping their misconfiguration
// errors with bootstrap.go's errConfigInvalid (the sentinel meant only for
// bootstrap()'s own validate(c) failure) would make bootstrapExitCode award
// exitConfigInvalid (6) to a read-only-token misconfiguration hit by
// `spindrift dispatch`/`recover`/console, an undocumented change to that
// versioned exit code. It must instead fall through to the default exit 1,
// origin/main's historical behavior for this failure on those subcommands.
func TestBootstrapExitCode_ReadOnlyTokenGateMisconfigured_ExitsOne(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.repoSlug = "owner/repo"
	t.Setenv("BOX_GH_TOKEN", "")

	introspect := func(token, repoSlug string) (tokenIntrospectionResult, error) {
		t.Fatal("introspect should not be called when BOX_GH_TOKEN is unset")
		return tokenIntrospectionResult{}, nil
	}
	var buf bytes.Buffer
	_, err := checkReadOnlyTokenGate(c, introspect, &buf)
	if err == nil {
		t.Fatal("checkReadOnlyTokenGate() error = nil, want a misconfiguration error")
	}

	if got := bootstrapExitCode(err); got != 1 {
		t.Errorf("bootstrapExitCode(%v) = %d, want 1", err, got)
	}
}

// TestBootstrapExitCode_ReadOnlyForgejoTokenGateMisconfigured_ExitsOne is the
// Forgejo-side sibling of TestBootstrapExitCode_ReadOnlyTokenGateMisconfigured_ExitsOne
// above: checkReadOnlyForgejoTokenGate wraps the same errReadOnlyGateMisconfigured
// sentinel, but only the GitHub gate had a dispatch-path exit-code regression
// test pinning that bootstrapExitCode falls through to the default exit 1
// rather than errConfigInvalid's exitConfigInvalid (6).
func TestBootstrapExitCode_ReadOnlyForgejoTokenGateMisconfigured_ExitsOne(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.codeForge = "forgejo"
	c.issueTracker = "forgejo"
	c.repoSlug = "owner/repo"
	t.Setenv("BOX_FORGEJO_TOKEN", "")

	var buf bytes.Buffer
	_, err := checkReadOnlyForgejoTokenGate(c, &buf)
	if err == nil {
		t.Fatal("checkReadOnlyForgejoTokenGate() error = nil, want a misconfiguration error")
	}

	if got := bootstrapExitCode(err); got != 1 {
		t.Errorf("bootstrapExitCode(%v) = %d, want 1", err, got)
	}
}

// TestNewIssue_CarriesFieldsFromForgeIssue verifies newIssue copies all
// three fields — including priority — from a forge.Issue into the
// launcher's local issue type (issue #2925).
func TestNewIssue_CarriesFieldsFromForgeIssue(t *testing.T) {
	fi := forge.Issue{Number: "42", Title: "some title", Priority: forge.PriorityHigh}

	got := newIssue(fi)

	want := issue{number: "42", title: "some title", priority: forge.PriorityHigh}
	if got != want {
		t.Errorf("newIssue(%+v) = %+v, want %+v", fi, got, want)
	}
}
