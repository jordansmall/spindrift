//go:build integration

package runner

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// integrationTestImage is a small, well-known multi-arch image used only to
// probe hardening flags — it never runs the real spindrift entrypoint, so it
// doesn't need to be pinned by digest the way the production nixBuilderImage
// is (oci.go isDigestPinned). requireRealOCI gates this file on a reachable
// daemon, so it only ever pulls in an environment that already has real
// container tooling and network access (a genuine CI runner).
const integrationTestImage = "docker.io/library/busybox:stable"

// requireRealOCI returns the CLI name ("podman" or "docker") for the first
// runtime on PATH with a reachable daemon, skipping cleanly when neither is
// usable (issue #576's "skip on hosts with no real runtime").
func requireRealOCI(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("OCI integration test requires Linux")
	}
	for _, cli := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(cli); err != nil {
			continue
		}
		if err := exec.Command(cli, "info").Run(); err != nil {
			continue
		}
		return cli
	}
	t.Skip("neither podman nor docker has a reachable daemon on PATH")
	return ""
}

// integrationContainerName returns a name unique enough not to collide with
// a concurrent test run or a leftover container from a prior one.
func integrationContainerName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("spindrift-integration-%d", time.Now().UnixNano())
}

func newIntegrationOCIAdapter(cli string) *ociAdapter {
	return &ociAdapter{cli: cli, image: integrationTestImage}
}

// ociProbeArgs takes the real buildRunArgs() output for box, so every
// hardening/env flag reaches the runtime exactly as production would send
// it, and swaps the fixed "image /agent/entrypoint.sh" tail for
// integrationTestImage running script under sh -c.
func ociProbeArgs(a *ociAdapter, box Box, script string) []string {
	args := a.buildRunArgs(box)
	args = args[:len(args)-2] // drop image + "/agent/entrypoint.sh"
	return append(args, a.image, "sh", "-c", script)
}

// runProbe launches the probe container and returns only its stdout. The
// container's own output goes to stdout; the runtime's first-run image-pull
// progress ("Trying to pull...", "Copying blob...") and any warnings go to
// stderr, so we must keep the two apart — CombinedOutput would fold the pull
// progress into the parse and break it on whichever probe runs first.
func runProbe(t *testing.T, cli string, args []string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(cli, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s run failed: %v: %s", cli, err, stderr.String())
	}
	return stdout.Bytes()
}

// statusField returns the sole value of the /proc/self/status line beginning
// with prefix (e.g. "CapEff:\t0000000000000000" -> "0000000000000000"),
// scanning line by line so an unexpected extra line can't shift the parse.
func statusField(t *testing.T, out []byte, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, prefix) {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				t.Fatalf("unexpected %s line: %q", prefix, line)
			}
			return fields[1]
		}
	}
	t.Fatalf("no %q line in probe output: %q", prefix, out)
	return ""
}

// TestOCIIntegration_CapabilitiesDropped launches a real container and
// asserts, from inside it, that its effective capability set is empty — the
// kernel enforcing --cap-drop=all, not just the flag being present on argv
// (issue #576).
func TestOCIIntegration_CapabilitiesDropped(t *testing.T) {
	cli := requireRealOCI(t)
	a := newIntegrationOCIAdapter(cli)
	box := Box{Name: integrationContainerName(t), Env: map[string]string{}}
	t.Cleanup(func() { _ = exec.Command(cli, "rm", "-f", box.Name).Run() })

	args := ociProbeArgs(a, box, "grep ^CapEff /proc/self/status")
	if got := statusField(t, runProbe(t, cli, args), "CapEff:"); got != "0000000000000000" {
		t.Errorf("CapEff = %s, want all-zero (--cap-drop=all not enforced)", got)
	}
}

// TestOCIIntegration_NoNewPrivileges launches a real container and asserts,
// from inside it, that NoNewPrivs is set — the kernel enforcing
// --security-opt=no-new-privileges, not just the flag being present on argv
// (issue #576).
func TestOCIIntegration_NoNewPrivileges(t *testing.T) {
	cli := requireRealOCI(t)
	a := newIntegrationOCIAdapter(cli)
	box := Box{Name: integrationContainerName(t), Env: map[string]string{}}
	t.Cleanup(func() { _ = exec.Command(cli, "rm", "-f", box.Name).Run() })

	args := ociProbeArgs(a, box, "grep ^NoNewPrivs /proc/self/status")
	if got := statusField(t, runProbe(t, cli, args), "NoNewPrivs:"); got != "1" {
		t.Errorf("NoNewPrivs = %s, want \"1\" (--security-opt=no-new-privileges not enforced)", got)
	}
}

// TestOCIIntegration_SecretNotOnContainerProcessArgv sets a secret in
// Box.Env and asserts, by reading the running container process's own
// /proc/self/cmdline, that it never reaches argv — env vars reach the
// container via envp (-e), not argv, so a regression that started
// interpolating a secret into the command line would show up here (issue
// #576).
func TestOCIIntegration_SecretNotOnContainerProcessArgv(t *testing.T) {
	cli := requireRealOCI(t)
	const marker = "spindrift-integration-secret-9f3c2a"
	a := newIntegrationOCIAdapter(cli)
	box := Box{Name: integrationContainerName(t), Env: map[string]string{"GH_TOKEN": marker}}
	t.Cleanup(func() { _ = exec.Command(cli, "rm", "-f", box.Name).Run() })

	args := ociProbeArgs(a, box, "cat /proc/self/cmdline")
	out := runProbe(t, cli, args)
	if bytes.Contains(out, []byte(marker)) {
		t.Errorf("secret %q found in the container process's own argv: %q", marker, out)
	}
}
