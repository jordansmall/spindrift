package bwrap

import (
	"testing"

	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

func TestBwrapBuildEnsureReady_ReportsStartedThenFinished(t *testing.T) {
	drain := runnertest.InstallImageBuildPipe(t)
	runnertest.StubExec(t, &runner.ExecCommand, runnertest.Call{Exit: 0})
	a := &bwrapBuildAdapter{agentFilesDrv: "/fake/files.drv"}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	runnertest.WantImageBuildStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
}

func TestBwrapBuildEnsureReady_RealizeFailureStillReportsFinished(t *testing.T) {
	drain := runnertest.InstallImageBuildPipe(t)
	runnertest.StubExec(t, &runner.ExecCommand, runnertest.Call{Exit: 1})
	a := &bwrapBuildAdapter{agentFilesDrv: "/fake/files.drv"}

	if err := a.EnsureReady(); err == nil {
		t.Fatal("EnsureReady = nil, want the realize failure")
	}
	runnertest.WantImageBuildStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
}
