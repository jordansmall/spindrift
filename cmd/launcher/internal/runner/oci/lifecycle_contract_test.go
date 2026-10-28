package oci

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
	"spindrift.dev/launcher/internal/runner/runnertest/contract"
)

// ociLifecycleHarness drives the real adapter against the recording fake CLI,
// so the contract needs no container runtime. The fake dispatches on the
// runtime subcommand, with marker files in its dir standing in for container
// state.
type ociLifecycleHarness struct{}

var _ contract.LifecycleHarness = ociLifecycleHarness{}

func (ociLifecycleHarness) adapter(script string) *ociAdapter {
	return &ociAdapter{cli: script, image: "spindrift:test"}
}

func (h ociLifecycleHarness) Exiting(t *testing.T, _ string, code int) runner.Runner {
	script, _ := runnertest.NewFakeCLIBySubcommand(t, map[string]runnertest.Call{
		"inspect": {Exit: 1, Stderr: `Error: no such object: "x"`},
		"ps":      {},
		"run":     {Exit: code},
	})
	return h.adapter(script)
}

func (h ociLifecycleHarness) Foreign(t *testing.T, name string) runner.Runner {
	// run is deliberately unscripted: reaching it fails with exit 127, which
	// is not ErrAlreadyRunning.
	script, _ := runnertest.NewFakeCLIBySubcommand(t, map[string]runnertest.Call{
		"inspect": {Stdout: inspectJSONStdout(t, "cid-1", "running", time.Now())},
		"ps":      {Stdout: name},
	})
	return h.adapter(script)
}

func (h ociLifecycleHarness) Killable(t *testing.T, name string) (runner.Runner, func()) {
	// run is scripted to exit 137 once killed, so the contract's kill-by-signal
	// assertion checks pass-through of that code, not real podman signal
	// semantics.
	const live = `[ -e started ] && [ ! -e killed ]`
	script, dir := runnertest.NewFakeCLIBySubcommand(t, map[string]runnertest.Call{
		"run": {
			Exit:  137,
			Shell: "touch started; while [ ! -e killed ]; do sleep 0.01; done",
		},
		"rm": {Shell: "touch killed"},
		"inspect": {
			Stdout: inspectJSONStdout(t, "cid-1", "running", time.Now()),
			Shell:  live + ` || { echo 'Error: no such object: "` + name + `"' >&2; exit 1; }`,
		},
		"ps": {
			Stdout: name,
			Shell:  live + " || exit 0",
		},
	})
	// Releases a run the contract failed to Kill so a failing test leaves no
	// polling shell behind.
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "killed"), nil, 0o644) })
	started := func() {
		runnertest.Eventually(t, 10*time.Second, "fake run of "+name+" starting", func() bool {
			_, err := os.Stat(filepath.Join(dir, "started"))
			return err == nil
		})
	}
	return h.adapter(script), started
}

func TestOCI_LifecycleContract(t *testing.T) {
	contract.Lifecycle(t, ociLifecycleHarness{})
}
