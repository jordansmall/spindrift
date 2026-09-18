package main

import (
	"errors"
	"io"
	"os"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
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
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("LOCAL_ISSUES_DIR", t.TempDir())
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
}

// Returns a config suitable for merge-gate-adjacent tests (preflight, wiring
// through settle).
func baseConfig() config {
	return config{schemaConfig: schemaConfig{
		inProgressLabel:   "agent-in-progress",
		failedLabel:       "agent-failed",
		completeLabel:     "agent-complete",
		mergePollInterval: 0,   // no sleep in tests
		mergePollTimeout:  100, // large enough for multi-poll tests
		mergeMode:         "immediate",
		codeForge:         "github",
	}}
}

// Installs flags as the package-level schemaFlags table for the duration of t.
// A caller that reassigns schemaFlags again within t (e.g. per-subcase) still
// restores to the same pre-call value once t finishes.
func withSchemaFlags(t *testing.T, flags []flagEntry) {
	t.Helper()
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = flags
}

// This is the choiceKnobRegistry sibling of withSchemaFlags above.
func withChoiceKnobRegistry(t *testing.T, rows []choiceKnobRow) {
	t.Helper()
	orig := choiceKnobRegistry
	t.Cleanup(func() { choiceKnobRegistry = orig })
	choiceKnobRegistry = rows
}

// Pins that withSchemaFlags restores the ambient schemaFlags once the subtest
// that used it completes (issue #906).
func TestWithSchemaFlags_SwapsAndRestores(t *testing.T) {
	ambient := schemaFlags
	t.Run("swap", func(t *testing.T) {
		withSchemaFlags(t, []flagEntry{{env: "PROBE_KEY", dflt: "probe-value"}})
		if len(schemaFlags) != 1 || schemaFlags[0].env != "PROBE_KEY" {
			t.Fatalf("schemaFlags = %+v, want single PROBE_KEY entry", schemaFlags)
		}
	})
	got := schemaFlags
	if !reflect.DeepEqual(got, ambient) {
		t.Fatalf("schemaFlags not restored after subtest: got %+v, want %+v", got, ambient)
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
func testNewSettle(c config, it forge.IssueTracker, lw *localloop.Wired, cf forge.CodeForge) settle.Settler {
	forgeDesc, _ := backend.ByName(c.codeForge)
	trackerDesc, _ := backend.ByName(c.issueTracker)
	caps := forge.ResolveCapabilities(cf, it, forgeDesc, trackerDesc)
	return newSettle(c, it, lw, cf, caps)
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

// doctorReport never reads rc.capabilities, so this leaves it at its zero
// value.
func doctorReadContext(c config, f *forge.Fake) readContext {
	return readContext{config: c, issueTracker: f, codeForge: f}
}
