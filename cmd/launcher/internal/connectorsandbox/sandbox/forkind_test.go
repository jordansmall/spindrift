package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
)

const countingKind = "forkind-counting-test"

var countingBuilds int

func init() {
	runner.Register(runner.Strategy{
		Name: countingKind,
		New: func(runner.Config, inputdoc.Artifacts, runner.KnobLookup, string) (runner.Runner, error) {
			countingBuilds++
			return runner.NewFake(), nil
		},
	})
}

func TestForKindBuildsNoRunnerUntilALoad(t *testing.T) {
	countingBuilds = 0
	s := ForKind(countingKind, runner.Config{}, inputdoc.Artifacts{}, nil, t.TempDir())
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if countingBuilds != 0 {
		t.Fatalf("runner built %d times with no load, want 0", countingBuilds)
	}
}

func TestForKindStartFailureNamesTheRunner(t *testing.T) {
	// The Fake has no StartConnectorSandboxFunc, so its start refuses.
	s := ForKind(countingKind, runner.Config{}, inputdoc.Artifacts{}, nil, t.TempDir())
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	_, err := s.Load(context.Background(), []byte("not wasm"))
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) || !strings.Contains(err.Error(), runner.ErrFakeNoSandbox.Error()) {
		t.Fatalf("load = %v, want ErrConnectorSandboxStart carrying the runner's refusal", err)
	}
	if !strings.Contains(err.Error(), "runner "+countingKind+":") {
		t.Errorf("load = %v, want it to name runner %s", err, countingKind)
	}
}

func TestForKindUnknownKindFailsTheLoad(t *testing.T) {
	s := ForKind("no-such-kind", runner.Config{}, inputdoc.Artifacts{}, nil, t.TempDir())
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	_, err := s.Load(context.Background(), []byte("not wasm"))
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) || !strings.Contains(err.Error(), "no-such-kind") {
		t.Fatalf("load = %v, want ErrConnectorSandboxStart naming the unknown kind", err)
	}
}

func TestRunnerLabel(t *testing.T) {
	for _, tc := range []struct{ kind, runtime, want string }{
		{"", "podman", "oci (podman)"},
		{"oci", "docker", "oci (docker)"},
		{"bwrap", "podman", "bwrap"},
		{"bwrap", "bwrap", "bwrap"},
	} {
		if got := runnerLabel(tc.kind, tc.runtime); got != tc.want {
			t.Errorf("runnerLabel(%q, %q) = %q, want %q", tc.kind, tc.runtime, got, tc.want)
		}
	}
}
