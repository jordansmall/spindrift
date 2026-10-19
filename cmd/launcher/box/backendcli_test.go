package main

import (
	"errors"
	"io"
	"os/exec"
	"testing"
)

// fjFixture scripts an fj on PATH for a forgejo-backend Box and records every
// fj command RunCmd is handed. The fj argv itself is pinned in the forgejo
// package; these tests cover when the Box reaches the backend's CLI setup.
type fjFixture struct {
	*fixture
	reg    *regFake
	fj     [][]string
	runErr error
}

func newFJFixture(t *testing.T) *fjFixture {
	t.Helper()
	g := &fjFixture{fixture: newFixture(t)}
	g.reg = newRegFake()
	g.d.Registry = g.reg.deps()
	g.d.LookPath = func(name string) (string, error) {
		if name != "fj" {
			return "", exec.ErrNotFound
		}
		return "/bin/fj", nil
	}
	g.d.RunCmd = func(cmd *exec.Cmd) error {
		if cmd.Args[0] == "fj" {
			g.reg.events = append(g.reg.events, "fj")
			_, _ = io.ReadAll(cmd.Stdin)
			g.fj = append(g.fj, cmd.Args)
			return g.runErr
		}
		return nil
	}
	g.knobs["FORGEJO_TOKEN"] = "s3cret"
	g.env.CodeForge = "forgejo"
	return g
}

func TestBackendCLI_ForgejoCodeForgeRunsFj(t *testing.T) {
	g := newFJFixture(t)
	g.run()
	if len(g.fj) != 1 {
		t.Fatalf("fj ran %d times for a forgejo code forge, want 1", len(g.fj))
	}
}

func TestBackendCLI_ForgejoTrackerAloneRunsFj(t *testing.T) {
	g := newFJFixture(t)
	g.env.CodeForge = "github"
	g.env.IssueTracker = "forgejo"
	g.run()
	if len(g.fj) != 1 {
		t.Fatalf("fj ran %d times for a forgejo tracker, want 1", len(g.fj))
	}
}

func TestBackendCLI_ForgejoOnBothAxesRunsOnce(t *testing.T) {
	g := newFJFixture(t)
	g.env.IssueTracker = "forgejo"
	g.run()
	if len(g.fj) != 1 {
		t.Fatalf("fj ran %d times for forgejo on both axes, want 1", len(g.fj))
	}
}

func TestBackendCLI_NonForgejoBackendsRunNoFj(t *testing.T) {
	g := newFJFixture(t)
	g.env.CodeForge = "github"
	g.env.IssueTracker = "github"
	g.run()
	if len(g.fj) != 0 {
		t.Fatalf("fj ran for a github-backend Box: %v", g.fj)
	}
}

func TestBackendCLI_UnsetBackendsDefaultToGitHub(t *testing.T) {
	g := newFJFixture(t)
	g.env.CodeForge = ""
	g.env.IssueTracker = ""
	g.run()
	if len(g.fj) != 0 {
		t.Fatalf("fj ran for a Box with no backend set: %v", g.fj)
	}
}

func TestBackendCLI_RunsForSelfContained(t *testing.T) {
	g := newFJFixture(t)
	g.env.SelfContained = true
	g.run()
	if len(g.fj) != 1 {
		t.Fatalf("fj ran %d times for a self-contained run, want 1", len(g.fj))
	}
}

func TestBackendCLI_RunsRegardlessOfWriteMode(t *testing.T) {
	g := newFJFixture(t)
	g.env.BoxWriteEnabled = false
	g.run()
	if len(g.fj) != 1 {
		t.Fatalf("fj ran %d times on a read-only Box, want 1", len(g.fj))
	}
}

func TestBackendCLI_RunsBeforeRegistryBindings(t *testing.T) {
	g := newFJFixture(t)
	g.run()
	if len(g.reg.events) < 2 || g.reg.events[0] != "fj" || g.reg.events[1] != "gate" {
		t.Fatalf("events = %v, want fj before the registry bindings", g.reg.events)
	}
}

func TestBackendCLI_FailureAbortsBeforeAnythingElse(t *testing.T) {
	g := newFJFixture(t)
	g.runErr = errors.New("exit status 1")
	rc, err := run(g.in, g.env, g.d)
	var pe *phaseError
	if !errors.As(err, &pe) || pe.phase != "backend-cli" {
		t.Fatalf("run() = (%d, %v), want a backend-cli phase error", rc, err)
	}
	if len(g.reg.events) != 1 || g.reg.events[0] != "fj" {
		t.Fatalf("events = %v, want only fj", g.reg.events)
	}
	if len(g.calls) != 0 || g.assembled != 0 {
		t.Fatalf("Driver ran (%d calls) or assembled (%d) after fj failed", len(g.calls), g.assembled)
	}
	if len(g.recoverCfgs) != 0 {
		t.Fatalf("recovery ran after fj failed: %+v", g.recoverCfgs)
	}
}
