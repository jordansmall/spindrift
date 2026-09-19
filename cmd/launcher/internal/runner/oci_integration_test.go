//go:build integration

package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// integrationTestImage is a throwaway probe image. It never runs the real
// spindrift entrypoint, so it needs no digest pin the way the production
// nixBuilderImage does (oci.go isDigestPinned). requireRealOCI gates this file
// on a reachable daemon, so the runtime only pulls it where real container
// tooling and network access already exist.
const integrationTestImage = "docker.io/library/busybox:stable"

// runProbeAttemptTimeout bounds a single runProbe attempt, including the
// implicit image pull, so the deadline kills a registry hang well inside the
// 10m default go test timeout instead of reaching the panic (issue #2015).
// Three tests call runProbe, each retrying up to runProbeMaxAttempts times, so
// keep 3 * runProbeMaxAttempts * runProbeAttemptTimeout plus backoff under 10m.
const runProbeAttemptTimeout = 45 * time.Second

// runProbeMaxAttempts bounds how many times runProbe retries a transient
// registry failure before giving up and skipping (issue #2015).
const runProbeMaxAttempts = 2

// runProbeRetryBackoff is a short pause between retries so a retry doesn't
// hammer a registry that just failed.
const runProbeRetryBackoff = 3 * time.Second

// requireRealOCI returns the CLI name for the first runtime on PATH with a
// reachable daemon, skipping cleanly when neither is usable (issue #576).
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

// integrationContainerName avoids collisions with a concurrent test run or a
// leftover container from a prior one.
func integrationContainerName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("spindrift-integration-%d", time.Now().UnixNano())
}

func newIntegrationOCIAdapter(cli string) *ociAdapter {
	return &ociAdapter{cli: cli, image: integrationTestImage}
}

// ociProbeArgs reuses the real buildRunArgs output so every hardening and env
// flag reaches the runtime exactly as production sends it, swapping only the
// fixed image and entrypoint tail for integrationTestImage running script.
func ociProbeArgs(a *ociAdapter, box Box, script string) []string {
	args := a.buildRunArgs(box)
	args = args[:len(args)-2] // drop image + "/agent/entrypoint.sh"
	return append(args, a.image, "sh", "-c", script)
}

// runProbe returns only the container's stdout: the runtime writes image-pull
// progress and warnings to stderr, so CombinedOutput would fold that into the
// parse and break whichever probe runs first. A transient registry failure or
// a blown deadline retries up to runProbeMaxAttempts and then skips, while a
// genuine failure such as bad args fails on the first attempt (issue #2015).
func runProbe(t *testing.T, cli string, args []string) []byte {
	t.Helper()
	var lastErr error
	var lastStderr string
	for attempt := 1; attempt <= runProbeMaxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), runProbeAttemptTimeout)
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		cancel()
		if err == nil {
			return stdout.Bytes()
		}
		lastErr, lastStderr = err, stderr.String()
		if isRuntimeUnusableError(lastStderr) {
			t.Skipf("%s has no usable OCI runtime: %v: %s", cli, err, lastStderr)
		}
		if ctx.Err() != context.DeadlineExceeded && !isTransientRegistryError(lastStderr) {
			t.Fatalf("%s run failed: %v: %s", cli, err, lastStderr)
		}
		t.Logf("%s run attempt %d/%d hit a transient registry error, retrying: %v: %s", cli, attempt, runProbeMaxAttempts, err, lastStderr)
		if attempt < runProbeMaxAttempts {
			time.Sleep(runProbeRetryBackoff)
		}
	}
	t.Skipf("registry pull unavailable after %d attempts: %v: %s", runProbeMaxAttempts, lastErr, lastStderr)
	return nil
}

// statusField returns the sole value of the /proc/self/status line beginning
// with prefix, scanning line by line so an unexpected extra line cannot shift
// the parse.
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

// TestOCIIntegration_CapabilitiesDropped asserts from inside a real container
// that the kernel enforces --cap-drop=all, not just that the flag reaches argv
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

// TestOCIIntegration_NoNewPrivileges asserts from inside a real container that
// the kernel enforces --security-opt=no-new-privileges, not just that the flag
// reaches argv (issue #576).
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

// TestOCIIntegration_SecretNotOnContainerProcessArgv reads the container
// process's own /proc/self/cmdline. Env vars reach the container via envp
// (-e), not argv, so a regression that interpolated a secret into the command
// line would show up here (issue #576).
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
