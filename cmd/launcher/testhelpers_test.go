package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/testutil"
)

// The lifecycle-label set mirrored from lib/env-schema.nix and pinned against
// the agent workflows by nix/checks/dispatch-labels.nix (issue #460).
// forge.NewFake takes labels as an explicit constructor argument, so tests
// share this one value instead of each restating the four strings.
var testDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// Sets the env vars a fully-local (ISSUE_TRACKER=local, CODE_FORGE=local)
// newReadContext fixture needs (issue #2945).
func setFullyLocalEnv(t *testing.T) {
	t.Helper()
	setKnob(t, "ISSUE_TRACKER", "local")
	setKnob(t, "CODE_FORGE", "local")
	setKnob(t, "LOCAL_ISSUES_DIR", t.TempDir())
	setKnob(t, "MERGE_MODE", "immediate")
	setKnob(t, "CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	setKnob(t, "GIT_USER_NAME", "bot")
	setKnob(t, "GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
}

// Returns a config suitable for merge-gate-adjacent tests (preflight, wiring
// through settle).
func baseConfig() config {
	return config{Knobs: inputdoc.Knobs{
		InProgressLabel:   "agent-in-progress",
		FailedLabel:       "agent-failed",
		CompleteLabel:     "agent-complete",
		MergePollInterval: 0,   // no sleep in tests
		MergePollTimeout:  100, // large enough for multi-poll tests
		MergeMode:         "immediate",
		CodeForge:         "github",
		IssueTracker:      "github",
	}}
}

// Installs flags as the package-level inputdoc.SchemaFlags table for the
// duration of t. Everything that reads the table sees the swap, including
// inputdoc's Document.Setting, SchemaDefault and IsDaemonOnly. A caller that
// reassigns inputdoc.SchemaFlags again within t (e.g. per-subcase) still
// restores to the same pre-call value once t finishes.
func withSchemaFlags(t *testing.T, flags []inputdoc.FlagEntry) {
	t.Helper()
	orig := inputdoc.SchemaFlags
	t.Cleanup(func() { inputdoc.SchemaFlags = orig })
	inputdoc.SchemaFlags = flags
}

// This is the choiceKnobRegistry sibling of withSchemaFlags above.
func withChoiceKnobRegistry(t *testing.T, rows []choiceKnobRow) {
	t.Helper()
	orig := choiceKnobRegistry
	t.Cleanup(func() { choiceKnobRegistry = orig })
	choiceKnobRegistry = rows
}

// Pins that withSchemaFlags restores the ambient inputdoc.SchemaFlags once the subtest
// that used it completes (issue #906).
func TestWithSchemaFlags_SwapsAndRestores(t *testing.T) {
	ambient := inputdoc.SchemaFlags
	t.Run("swap", func(t *testing.T) {
		withSchemaFlags(t, []inputdoc.FlagEntry{{Env: "PROBE_KEY", Dflt: "probe-value"}})
		if len(inputdoc.SchemaFlags) != 1 || inputdoc.SchemaFlags[0].Env != "PROBE_KEY" {
			t.Fatalf("inputdoc.SchemaFlags = %+v, want single PROBE_KEY entry", inputdoc.SchemaFlags)
		}
	})
	got := inputdoc.SchemaFlags
	if !reflect.DeepEqual(got, ambient) {
		t.Fatalf("inputdoc.SchemaFlags not restored after subtest: got %+v, want %+v", got, ambient)
	}
}

func tempLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dispatch.HostLogDirFor(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The zero-value Config is enough because these call sites only exercise the
// shared parent-resolution wiring (issue #1810). Tests that do depend on
// AccumulationRepoDir, BaseBranch, or git identity build their own
// localloop.Config directly.
func testWired(it forge.IssueTracker) *localloop.Wired {
	return localloop.Wire(localloop.Config{}, it)
}

// Resolves Capabilities from it and cf the same way newReadContext does, so a
// test gets correct pr/landing behavior for whatever fake shape it passes
// without wiring Capabilities by hand.
// The concrete type lets a test hand the same Settle's Registry() to the entry
// point under test, as bootstrap does.
func testNewSettle(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge) *settle.Settle {
	forgeDesc, _ := backend.ByName(c.CodeForge)
	trackerDesc, _ := backend.ByName(c.IssueTracker)
	caps := forge.ResolveCapabilities(cf, it, forgeDesc, trackerDesc)
	return settle.New(settleConfig(c, lw, cf, caps), it, cf)
}

// testSettlers is newSettlers for the strategy c's kind names, failing the
// test on an unhandled one.
func testSettlers(t *testing.T, c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge, caps forge.Capabilities) (settle.MergeSettler, *settle.ResearchSettle) {
	t.Helper()
	mergeSettle, verdict, _, err := newSettlers(c.kind().Settle, c, it, lw, cf, caps)
	if err != nil {
		t.Fatal(err)
	}
	var merge settle.MergeSettler
	if mergeSettle != nil {
		merge = mergeSettle
	}
	return merge, verdict
}

// Resolves Capabilities the way newReadContext does (issue #2946), so a test
// does not have to hand-list which optional interfaces its fake implements.
func capsFor(it forge.IssueTracker, cf forge.CodeForge) forge.Capabilities {
	return forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
}

var boxErr = errors.New("exit 1")

// Redirects os.Stdout for the duration of fn and returns what fn wrote there,
// for tests that assert on a diagnostic printed straight to stdout rather than
// returned through an error or logger.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing stdout pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out)
}

// Uses the real claude Driver, whose ClassifyTransient degrades to
// Terminal/TaskFailed on a log with no transient markers, matching
// newDriver(c)'s production default. r may be nil for tests that never
// exercise a Fix or Run call.
func testFactory(t *testing.T, dir string, r runner.Runner) *dispatch.Factory {
	t.Helper()
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	f, err := dispatch.NewFactory(dispatch.Config{
		Policy: retry.Policy{Max: 3, Unit: 0, Jitter: 0},
	}, dir, r, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(f.Cleanup)
	return f
}

// The tracker is filing-capable so doctor --butler's resolveButlerSettings
// clears its host-side issue filing gate (issue #6343); tests probing the gate
// override capabilities.
func doctorReadContext(c config, f *forge.Fake) readContext {
	return readContext{config: c, issueTracker: f, codeForge: f, capabilities: fakeCapabilities(f)}
}

// fakeCapabilities resolves a filing-capable Fake's capabilities the way
// production does, through forge.ResolveCapabilities.
func fakeCapabilities(f *forge.Fake) forge.Capabilities {
	return forge.ResolveCapabilities(f, f.AsIssueFiler(), backend.Descriptor{}, backend.Descriptor{})
}

// mustInitCheckout builds the bare-bones repo the package's git fixtures start
// from: dir initialised on branch, with the repo-local test@example.com / Test
// identity later commits rely on, and one committed base.txt.
func mustInitCheckout(t *testing.T, dir, branch string) {
	t.Helper()
	testutil.GitRun(t, dir, "init", "-b", branch)
	testutil.GitRun(t, dir, "config", "user.email", "test@example.com")
	testutil.GitRun(t, dir, "config", "user.name", "Test")
	testutil.WriteFixture(t, dir, "base.txt", "base")
	testutil.GitRun(t, dir, "add", "base.txt")
	testutil.GitRun(t, dir, "commit", "-m", "base")
}

// testFlagArgs is the explicit-flag tier a test built with setKnob: what a
// command line would have carried. Resolve ranks it over the document and the
// schema default, the tier the knob env vars used to occupy.
var testFlagArgs []string

// testDoc is the document tier a test built with useDoc; nil is no document.
var testDoc *inputdoc.Document

// Re-resolves launcherInput from testFlagArgs and testDoc; every helper that
// changes either goes through it so the two never drift.
func resolveTestInput(t *testing.T) {
	t.Helper()
	r, err := inputdoc.Resolve(testFlagArgs, testDoc, nil)
	if err != nil {
		t.Fatalf("inputdoc.Resolve(%v): %v", testFlagArgs, err)
	}
	launcherInput = r
}

// Restores the package-level Launcher input state when the calling test ends.
func restoreLauncherInput(t *testing.T) {
	t.Helper()
	prevArgs, prevDoc, prevInput := testFlagArgs, testDoc, launcherInput
	t.Cleanup(func() { testFlagArgs, testDoc, launcherInput = prevArgs, prevDoc, prevInput })
}

// The one seam tests configure a non-secret schema knob through.
func setKnob(t *testing.T, env, value string) {
	t.Helper()
	e, ok := inputdoc.SchemaFlag(env)
	if !ok {
		t.Fatalf("setKnob(%q): not a schema knob; use t.Setenv for secrets and plumbing", env)
	}
	restoreLauncherInput(t)
	testFlagArgs = append(slices.Clone(testFlagArgs), e.Argv(value)...)
	resolveTestInput(t)
}

// Points the document tier at doc for the calling test only, so no
// test's document leaks into the next.
func useDoc(t *testing.T, doc *inputdoc.Document) {
	t.Helper()
	restoreLauncherInput(t)
	testDoc = doc
	resolveTestInput(t)
}

// mainRunT is mainRun with the Launcher input state it sets rolled back at
// test end. A useDoc document reaches mainRun the way a real one does, through
// --input, unless argv already names one.
func mainRunT(t *testing.T, argv []string, stdout, stderr io.Writer) int {
	t.Helper()
	restoreLauncherInput(t)
	if testDoc != nil && !slices.ContainsFunc(argv, func(a string) bool { return a == "--input" || strings.HasPrefix(a, "--input=") }) {
		raw, err := json.Marshal(testDoc)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "input.json")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		argv = append([]string{"--input", path}, argv...)
	}
	return mainRun(argv, stdout, stderr)
}

// mainRunKnobsAsFlags is mainRunT for tests whose setup helpers call setKnob:
// mainRun resolves only its own argv, so the knobs ride ahead of it as the
// flags a real invocation would carry.
func mainRunKnobsAsFlags(t *testing.T, argv []string, stdout, stderr io.Writer) int {
	t.Helper()
	return mainRunT(t, append(slices.Clone(testFlagArgs), argv...), stdout, stderr)
}

// The host-git-config-aware read loadSchemaConfig uses for the members
// carrying a hostGitConfigKey.
func hostGitConfig(env string) string {
	return launcherKnobSource{launcherInput.Source()}.HostGitConfig(env)
}

// parseResolved is mainRun's argv front half: parseFlags, then Resolve of the
// schema tokens it classified over the loaded document, left in launcherInput
// until the test ends.
func parseResolved(t *testing.T, argv []string) ([]string, error) {
	t.Helper()
	restoreLauncherInput(t)
	rest, schemaArgs, err := parseFlags(argv)
	if err != nil {
		return nil, err
	}
	testFlagArgs = append(slices.Clone(testFlagArgs), schemaArgs...)
	resolveTestInput(t)
	return rest, nil
}
