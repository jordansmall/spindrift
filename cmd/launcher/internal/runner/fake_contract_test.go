package runner_test

import (
	"testing"

	"spindrift.dev/launcher/internal/fixturebuild"
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

// fakeSandboxHarness starts the real connector-host as a plain child process:
// the Fake has no runtime to isolate it in.
type fakeSandboxHarness struct{}

var _ contract.SandboxHarness = fakeSandboxHarness{}

func (fakeSandboxHarness) Sandbox(t *testing.T, _ string) runner.Runner {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = func(string) (runner.ConnectorSandbox, error) {
		cmd := fixturebuild.ConnectorHostCmd(t)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return runner.ConnectorSandbox{}, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return runner.ConnectorSandbox{}, err
		}
		if err := cmd.Start(); err != nil {
			return runner.ConnectorSandbox{}, err
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return runner.ConnectorSandbox{
			Stdin:  stdin,
			Stdout: stdout,
			Wait:   cmd.Wait,
			Stderr: func() string { return "" },
		}, nil
	}
	return f
}

func TestFake_ConnectorSandboxLifecycle(t *testing.T) {
	contract.SandboxLifecycle(t, fakeSandboxHarness{})
}
