package oci

import (
	"os/exec"
	"testing"

	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

func TestOCIEnsureReady_WarmReportsNothing(t *testing.T) {
	redirectImageLockDir(t)
	drain := runnertest.InstallImageBuildPipe(t)
	cli, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0}) // image inspect: present
	a := &ociAdapter{cli: cli, image: "spindrift:abc123"}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	runnertest.WantImageBuildStates(t, drain())
}

func TestOCIEnsureReady_SiblingRealisedReportsNothing(t *testing.T) {
	redirectImageLockDir(t)
	drain := runnertest.InstallImageBuildPipe(t)
	cli, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 1}, runnertest.Call{Exit: 0}) // absent, then present under the lock
	a := &ociAdapter{cli: cli, image: "spindrift:abc123"}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	runnertest.WantImageBuildStates(t, drain())
}

func TestOCIEnsureReady_ColdBuildReportsStartedBeforeNixThenFinished(t *testing.T) {
	redirectImageLockDir(t)
	drain := runnertest.InstallImageBuildPipe(t)
	cli, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 1}, runnertest.Call{Exit: 1}, runnertest.Call{}, runnertest.Call{})
	nixScript, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := runner.ExecCommand
	t.Cleanup(func() { runner.ExecCommand = orig })
	var beforeNix []string
	runner.ExecCommand = func(name string, args ...string) *exec.Cmd {
		beforeNix = drain()
		return exec.Command(nixScript, args...)
	}
	a := &ociAdapter{
		cli:          cli,
		image:        "spindrift:abc123",
		imageDrv:     "/nix/store/fake.drv",
		imageArchive: "/tmp/spindrift-image.tar",
		imageTag:     "spindrift:abc123",
	}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	runnertest.WantImageBuildStates(t, beforeNix, report.ImageBuildStarted)
	runnertest.WantImageBuildStates(t, drain(), report.ImageBuildFinished)
}

func TestOCIEnsureReady_ColdBuildFailureStillReportsFinished(t *testing.T) {
	redirectImageLockDir(t)
	drain := runnertest.InstallImageBuildPipe(t)
	cli, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 1}, runnertest.Call{Exit: 1})
	runnertest.StubExec(t, &runner.ExecCommand, runnertest.Call{Exit: 1, Stdout: "genuine derivation error"})
	a := &ociAdapter{cli: cli, image: "spindrift:abc123", imageDrv: "/nix/store/fake.drv"}

	if err := a.EnsureReady(); err == nil {
		t.Fatal("EnsureReady = nil, want the nix build failure")
	}
	runnertest.WantImageBuildStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
}

func TestImageBuild_NoReporterInstalledIsHarmless(t *testing.T) {
	redirectImageLockDir(t)
	cli, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	a := &ociAdapter{cli: cli, image: "spindrift:abc123"}
	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
}
