package oci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
	"spindrift.dev/launcher/internal/runner/runnertest/contract"
)

const (
	sandboxTag       = "spindrift-connector-sandbox:test"
	sandboxFlakeAttr = ".#packages.x86_64-linux.connector-sandbox-image"
)

// hostModeEnv makes the test binary serve the Connector host protocol instead
// of running tests (see TestMain), standing in for the sandbox image's
// connector-host entrypoint.
const hostModeEnv = "SPINDRIFT_TEST_CONNECTOR_HOST"

func sandboxArtifacts() inputdoc.OCIArtifacts {
	return inputdoc.OCIArtifacts{
		ImageTag:                     inputdoc.ValueOf("spindrift:test"),
		ConnectorSandboxImageTag:     inputdoc.ValueOf(sandboxTag),
		ConnectorSandboxImageArchive: inputdoc.ValueOf("/nix/store/sandbox-archive"),
		ConnectorSandboxImageDrv:     inputdoc.ValueOf("/nix/store/sandbox.drv"),
		ConnectorSandboxFlakeAttr:    inputdoc.ValueOf(sandboxFlakeAttr),
		NixBuilderImage:              inputdoc.ValueOf("docker.io/nixos/nix@sha256:abc"),
		NixVolume:                    inputdoc.ValueOf("spindrift-nix"),
		FlakeImageAttr:               inputdoc.ValueOf(".#packages.x86_64-linux.agent-image"),
	}
}

func sandboxAdapter(t *testing.T, runtime string) *ociAdapter {
	t.Helper()
	r, err := New(runner.Config{Runtime: runtime}, sandboxArtifacts(), runnertest.Lookup(nil), "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r.(*ociAdapter)
}

func TestSandboxRenderingContract(t *testing.T) {
	contract.SandboxRender(t, func(t *testing.T, rt string) []string {
		script, dir := runnertest.NewFakeCLIBySubcommand(t, map[string]runnertest.Call{
			"image": {}, "run": {},
		})
		bin := t.TempDir()
		if err := os.Symlink(script, filepath.Join(bin, runner.BinaryFor(rt))); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		a := sandboxAdapter(t, rt)
		sb, err := a.StartConnectorSandbox(runner.ConnectorSandboxPrefix + "render")
		if err != nil {
			t.Fatalf("StartConnectorSandbox: %v", err)
		}
		_ = sb.Stdin.Close()
		if err := sb.Wait(); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		for i := 0; i < runnertest.CallCount(t, dir); i++ {
			if argv := runnertest.ReadCall(t, dir, i); argv[0] == "run" {
				if !slices.Contains(argv, sandboxTag) {
					t.Errorf("run argv does not name the sandbox image %q: %v", sandboxTag, argv)
				}
				return argv
			}
		}
		t.Fatal("fake CLI recorded no run invocation")
		return nil
	})
}

// A Connector sandbox must never see the Box's network config.
func TestSandboxArgsIgnoreBoxNetwork(t *testing.T) {
	t.Setenv("PODMAN_NETWORK", "host")
	a := sandboxAdapter(t, "podman")
	a.network = runner.OCINetwork{Arg: "host"}
	a.pidsLimit, a.memoryLimit = "9999", "99g"
	args := a.buildSandboxArgs(runner.ConnectorSandboxPrefix + "x")
	if slices.Contains(args, "host") || slices.Contains(args, "--pids-limit=9999") || slices.Contains(args, "--memory=99g") {
		t.Errorf("sandbox argv took a Box knob: %v", args)
	}
}

// ociSandboxHarness runs the real connector-host as the fake CLI's `run`.
type ociSandboxHarness struct{}

func (ociSandboxHarness) Sandbox(t *testing.T, name string) runner.Runner {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// $$ is the CLI's pid, which exec keeps for the host, so rm can end it and
	// the liveness probes can see it go.
	const live = `[ -e started ] && [ ! -e killed ] && kill -0 "$(cat pid)" 2>/dev/null`
	script, _ := runnertest.NewFakeCLIBySubcommand(t, map[string]runnertest.Call{
		"image": {},
		"run":   {Shell: fmt.Sprintf("echo $$ > pid; touch started; exec env %s=1 %q", hostModeEnv, self)},
		"rm":    {Shell: `touch killed; kill -9 "$(cat pid)" 2>/dev/null; exit 0`},
		"inspect": {
			Stdout: inspectJSONStdout(t, "cid-1", "running", time.Now()),
			Shell:  live + ` || { echo 'Error: no such object: "` + name + `"' >&2; exit 1; }`,
		},
		"ps": {Stdout: name, Shell: live + " || exit 0"},
	})
	r, err := New(runner.Config{Runtime: script}, sandboxArtifacts(), runnertest.Lookup(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOCI_ConnectorSandboxLifecycle(t *testing.T) {
	contract.SandboxLifecycle(t, ociSandboxHarness{})
}

// serveHostMode is TestMain's branch that turns the test binary into the host.
func serveHostMode() {
	err := connectorhost.ServeSessions(context.Background(), os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connector-host:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// An absent sandbox image is realized and loaded the way the Box image is.
func TestStartConnectorSandbox_LoadsAbsentImage(t *testing.T) {
	redirectImageLockDir(t)
	script, dir := runnertest.NewFakeCLIBySubcommand(t, map[string]runnertest.Call{
		"image": {Exit: 1}, "load": {}, "tag": {}, "run": {},
	})
	nixScript, nixDir := runnertest.NewFakeCLI(t, runnertest.Call{})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(_ string, args ...string) *exec.Cmd { return exec.Command(nixScript, args...) }

	r, err := New(runner.Config{Runtime: script}, sandboxArtifacts(), runnertest.Lookup(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := r.StartConnectorSandbox(runner.ConnectorSandboxPrefix + "load")
	if err != nil {
		t.Fatalf("StartConnectorSandbox: %v", err)
	}
	_ = sb.Stdin.Close()
	_ = sb.Wait()

	if got := runnertest.ReadCall(t, nixDir, 0); got[0] != "build" || !slices.Contains(got, "/nix/store/sandbox.drv^*") {
		t.Errorf("nix argv = %v, want a build of the sandbox drv", got)
	}
	var calls []string
	for i := 0; i < runnertest.CallCount(t, dir); i++ {
		calls = append(calls, strings.Join(runnertest.ReadCall(t, dir, i), " "))
	}
	want := []string{
		"image inspect " + sandboxTag,
		"image inspect " + sandboxTag, // re-probe under the realize lock
		"load -i /nix/store/sandbox-archive",
		"tag spindrift-connector-sandbox:latest " + sandboxTag,
	}
	for i, w := range want {
		if i >= len(calls) || calls[i] != w {
			t.Fatalf("runtime calls = %q, want prefix %q", calls, want)
		}
	}
}

func TestStartConnectorSandbox_NoImageInDocumentFailsClosed(t *testing.T) {
	r, err := New(runner.Config{Runtime: "podman"}, inputdoc.OCIArtifacts{}, runnertest.Lookup(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.StartConnectorSandbox(runner.ConnectorSandboxPrefix + "x"); err == nil {
		t.Fatal("StartConnectorSandbox with no sandbox image: got nil, want an error")
	}
}

// stubHostNix points the host `nix build` seam at a script that fails with
// stderr, returning the dir recording its calls.
func stubHostNix(t *testing.T, stderr string) string {
	t.Helper()
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 1, Stderr: stderr})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(_ string, args ...string) *exec.Cmd { return exec.Command(script, args...) }
	return dir
}

// With no Linux builder the sandbox image loads through the Nix builder
// container, as the Box image does, building the sandbox flake attr.
func TestStartConnectorSandbox_NoBuilderFallsBackToContainerBuild(t *testing.T) {
	redirectImageLockDir(t)
	nixDir := stubHostNix(t, "error: a Linux system is required to build a Linux derivation")
	script, dir := runnertest.NewFakeCLIBySubcommand(t, map[string]runnertest.Call{
		"image": {Exit: 1}, "load": {}, "tag": {}, "run": {},
	})
	r, err := New(runner.Config{Runtime: script}, sandboxArtifacts(), runnertest.Lookup(nil), "/work")
	if err != nil {
		t.Fatal(err)
	}

	sb, err := r.StartConnectorSandbox(runner.ConnectorSandboxPrefix + "nobuilder")
	if err != nil {
		t.Fatalf("StartConnectorSandbox: %v", err)
	}
	_ = sb.Stdin.Close()
	_ = sb.Wait()

	if got := runnertest.CallCount(t, nixDir); got != 1 {
		t.Errorf("host nix calls = %d, want 1", got)
	}
	var build, load, tag []string
	for i := 0; i < runnertest.CallCount(t, dir); i++ {
		argv := runnertest.ReadCall(t, dir, i)
		switch {
		case argv[0] == "run" && slices.Contains(argv, "spindrift-nix:/nix"):
			build = argv
		case argv[0] == "load":
			load = argv
		case argv[0] == "tag":
			tag = argv
		}
	}
	if build == nil {
		t.Fatal("no container build `run --rm -v spindrift-nix:/nix` recorded")
	}
	sh := build[len(build)-1]
	if !strings.Contains(sh, "build '"+sandboxFlakeAttr+"'") || strings.Contains(sh, "agent-image") {
		t.Errorf("container build command = %q, want a build of only %s", sh, sandboxFlakeAttr)
	}
	if len(load) != 3 || load[1] != "-i" || !strings.HasSuffix(load[2], "/image.tar") {
		t.Errorf("load argv = %v, want `load -i <tmp>/image.tar`", load)
	}
	if want := []string{"tag", "spindrift-connector-sandbox:latest", sandboxTag}; !slices.Equal(tag, want) {
		t.Errorf("tag argv = %v, want %v", tag, want)
	}
}

// A genuine derivation error is not a missing builder: it fails the start
// without spending a container build.
func TestStartConnectorSandbox_GenuineNixFailureSkipsContainerBuild(t *testing.T) {
	redirectImageLockDir(t)
	stubHostNix(t, "error: builder for '/nix/store/x.drv' failed with exit code 1")
	script, dir := runnertest.NewFakeCLIBySubcommand(t, map[string]runnertest.Call{"image": {Exit: 1}})
	r, err := New(runner.Config{Runtime: script}, sandboxArtifacts(), runnertest.Lookup(nil), "/work")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.StartConnectorSandbox(runner.ConnectorSandboxPrefix + "genuine")
	if err == nil {
		t.Fatal("StartConnectorSandbox: got nil, want the nix failure")
	}
	var exitErr *exec.ExitError
	if !strings.Contains(err.Error(), "Connector sandbox image") || !errors.As(err, &exitErr) {
		t.Errorf("error = %v, want it to name the Connector sandbox image and wrap the exit status", err)
	}
	for i := 0; i < runnertest.CallCount(t, dir); i++ {
		if argv := runnertest.ReadCall(t, dir, i); argv[0] != "image" {
			t.Errorf("runtime call %v after a genuine nix failure, want image inspects only", argv)
		}
	}
}
