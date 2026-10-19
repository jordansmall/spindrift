package runner_test

import (
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest/contract"
)

type fakeLifecycleHarness struct{}

var _ contract.LifecycleHarness = fakeLifecycleHarness{}

func (fakeLifecycleHarness) Exiting(_ *testing.T, _ string, code int) runner.Runner {
	f := runner.NewFake()
	if code != 0 {
		f.RunErr = &runner.RunError{ExitCode: code}
	}
	return f
}

func (fakeLifecycleHarness) Foreign(_ *testing.T, name string) runner.Runner {
	f := runner.NewFake()
	f.RunningNames = []string{name}
	return f
}

func (fakeLifecycleHarness) Killable(t *testing.T, _ string) (runner.Runner, func()) {
	f := runner.NewFake()
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.RunFunc = func(runner.Box) error {
		close(started)
		<-release
		return nil
	}
	return f, func() { <-started }
}

func TestFake_LifecycleContract(t *testing.T) {
	contract.Lifecycle(t, fakeLifecycleHarness{})
}
