package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/registryprobe"
	"spindrift.dev/launcher/internal/unixsocket"
)

// testSocketDir returns a directory to bind a real unix socket under for a
// test, preferring t.TempDir() but falling back to a fresh dir directly
// under /tmp when that path would already overflow AF_UNIX's sun_path cap
// once a filename is joined onto it (issue #3077) -- a nix build sandbox's
// own working directory can nest deep enough to trigger this, unlike an
// ordinary `go test` invocation from a shell.
func testSocketDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if !unixsocket.TooLong(filepath.Join(dir, "probe.sock")) {
		return dir
	}
	fallback, err := os.MkdirTemp("/tmp", "spindrift-probe-test-*")
	if err != nil {
		t.Fatalf("mktemp under /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(fallback) })
	return fallback
}

// TestProbeRegistrySocketVisible_MissingPath verifies a path that doesn't
// exist at all is never visible.
func TestProbeRegistrySocketVisible_MissingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.sock")
	if probeRegistrySocketVisible(path) {
		t.Fatalf("probeRegistrySocketVisible(%q) = true, want false", path)
	}
}

// TestProbeRegistrySocketVisible_RegularFile verifies a regular file at the
// path (not a socket) is never visible -- this is the "wrong bind target"
// case, distinct from "nothing there at all".
func TestProbeRegistrySocketVisible_RegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-socket")
	writeTestFile(t, path, "hello\n")
	if probeRegistrySocketVisible(path) {
		t.Fatalf("probeRegistrySocketVisible(%q) = true, want false", path)
	}
}

// TestProbeRegistrySocketVisibleAndConnect_RealListener verifies a real
// unix listener bound at path is both visible and connectable -- the happy
// path issue #3111's probe is meant to confirm.
func TestProbeRegistrySocketVisibleAndConnect_RealListener(t *testing.T) {
	path := filepath.Join(testSocketDir(t), "probe.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen(unix, %q): %v", path, err)
	}
	defer ln.Close()

	if !probeRegistrySocketVisible(path) {
		t.Fatalf("probeRegistrySocketVisible(%q) = false, want true", path)
	}
	if ok, err := probeRegistrySocketConnect(path); !ok {
		t.Fatalf("probeRegistrySocketConnect(%q) = false, %v, want true, nil", path, err)
	}
}

// TestProbeRegistrySocketConnect_StaleSocketFile verifies a socket file
// that's still visible (the inode survives) but has nothing listening
// behind it anymore fails to connect -- the visible-but-unconnectable case
// issue #3111's probe exists to catch (e.g. a passthrough sharing layer
// that projects the inode without a live kernel endpoint).
func TestProbeRegistrySocketConnect_StaleSocketFile(t *testing.T) {
	path := filepath.Join(testSocketDir(t), "stale.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen(unix, %q): %v", path, err)
	}
	// net.UnixListener unlinks its socket file on Close by default; disable
	// that so the file survives Close and this test actually exercises the
	// "visible but nothing listening" case rather than "file gone too".
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	if !probeRegistrySocketVisible(path) {
		t.Fatalf("probeRegistrySocketVisible(%q) = false, want true (file should still exist after Close)", path)
	}
	if ok, err := probeRegistrySocketConnect(path); ok {
		t.Fatalf("probeRegistrySocketConnect(%q) = true, %v, want false, non-nil (nothing listening)", path, err)
	}
}

// TestRunProbeRegistrySocket_ConnectableSocket verifies the CLI wrapper
// exits registryprobe.ExitCapable and prints an "ok" line when the socket is
// visible and connectable.
func TestRunProbeRegistrySocket_ConnectableSocket(t *testing.T) {
	path := filepath.Join(testSocketDir(t), "probe.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen(unix, %q): %v", path, err)
	}
	defer ln.Close()

	var stdout bytes.Buffer
	rc := runProbeRegistrySocket([]string{"-path", path}, &stdout)
	if rc != registryprobe.ExitCapable {
		t.Fatalf("runProbeRegistrySocket exit = %d, want %d (stdout=%q)", rc, registryprobe.ExitCapable, stdout.String())
	}
}

// TestRunProbeRegistrySocket_StaleSocketFile verifies the CLI wrapper exits
// registryprobe.ExitIncapable -- not plain 1 -- for the clean "no" verdict,
// since 1 is also what an old driver-exec's default verb produces and the
// two must stay distinguishable (issue #3120).
func TestRunProbeRegistrySocket_StaleSocketFile(t *testing.T) {
	path := filepath.Join(testSocketDir(t), "stale.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen(unix, %q): %v", path, err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	var stdout bytes.Buffer
	rc := runProbeRegistrySocket([]string{"-path", path}, &stdout)
	if rc != registryprobe.ExitIncapable {
		t.Fatalf("runProbeRegistrySocket exit = %d, want %d (stdout=%q)", rc, registryprobe.ExitIncapable, stdout.String())
	}
}

// TestRunProbeRegistrySocket_MissingPathFlag verifies the CLI wrapper
// rejects an empty/unset -path flag rather than silently probing "", and
// that this usage error stays at plain 1 rather than the reserved
// ExitIncapable verdict code -- a missing flag was never tested, so it must
// not read as a tested-and-answered "no" (issue #3120).
func TestRunProbeRegistrySocket_MissingPathFlag(t *testing.T) {
	var stdout bytes.Buffer
	rc := runProbeRegistrySocket(nil, &stdout)
	if rc != 1 {
		t.Fatalf("runProbeRegistrySocket exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
	if rc == registryprobe.ExitIncapable {
		t.Fatalf("runProbeRegistrySocket exit = %d, must not equal ExitIncapable (%d): a usage error is not a verdict", rc, registryprobe.ExitIncapable)
	}
}

// TestIsProbeRegistrySocketInvocation verifies the verb dispatch predicate
// matches only on the "probe-registry-socket" verb.
func TestIsProbeRegistrySocketInvocation(t *testing.T) {
	if isProbeRegistrySocketInvocation(nil) {
		t.Fatalf("isProbeRegistrySocketInvocation(nil) = true, want false")
	}
	if !isProbeRegistrySocketInvocation([]string{"probe-registry-socket", "-path", "/foo.sock"}) {
		t.Fatalf("isProbeRegistrySocketInvocation([probe-registry-socket ...]) = false, want true")
	}
	if isProbeRegistrySocketInvocation([]string{"bind-registry"}) {
		t.Fatalf("isProbeRegistrySocketInvocation([bind-registry]) = true, want false")
	}
}
