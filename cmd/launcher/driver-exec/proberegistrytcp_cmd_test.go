package main

import (
	"bytes"
	"net"
	"strconv"
	"testing"

	"spindrift.dev/launcher/internal/registryprobe"
)

// listenerPort extracts the numeric port a *net.TCPListener bound to, for
// dialing it back by host/port the same way the real probe is invoked.
func listenerPort(t *testing.T, ln net.Listener) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("net.SplitHostPort(%q): %v", ln.Addr().String(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("strconv.Atoi(%q): %v", portStr, err)
	}
	return port
}

// TestProbeRegistryTCPConnect_RealListener verifies a real TCP listener on
// 127.0.0.1 is connectable -- the happy path issue #3111's live reachability
// sub-probe is meant to confirm before the launcher trusts an
// --add-host host-gateway route into the Box.
func TestProbeRegistryTCPConnect_RealListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(tcp, 127.0.0.1:0): %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	ok, err := probeRegistryTCPConnect("127.0.0.1", listenerPort(t, ln))
	if !ok {
		t.Fatalf("probeRegistryTCPConnect = (%v, %v), want (true, nil)", ok, err)
	}
	if err != nil {
		t.Fatalf("probeRegistryTCPConnect error = %v, want nil", err)
	}
}

// TestProbeRegistryTCPConnect_NothingListening verifies a port nothing
// listens on (proven by opening then closing it first, so the port is real
// but freed) fails to connect and reports a non-nil error -- the
// unreachable-route case the sub-probe exists to catch.
func TestProbeRegistryTCPConnect_NothingListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(tcp, 127.0.0.1:0): %v", err)
	}
	port := listenerPort(t, ln)
	ln.Close()

	ok, err := probeRegistryTCPConnect("127.0.0.1", port)
	if ok {
		t.Fatalf("probeRegistryTCPConnect = (%v, %v), want (false, non-nil)", ok, err)
	}
	if err == nil {
		t.Fatalf("probeRegistryTCPConnect error = nil, want non-nil")
	}
}

// TestRunProbeRegistryTCP_Connectable verifies the CLI wrapper exits
// registryprobe.ExitCapable and prints an "ok" line when host:port is
// reachable.
func TestRunProbeRegistryTCP_Connectable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(tcp, 127.0.0.1:0): %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	port := listenerPort(t, ln)
	var stdout bytes.Buffer
	rc := runProbeRegistryTCP([]string{"-host", "127.0.0.1", "-port", strconv.Itoa(port)}, &stdout)
	if rc != registryprobe.ExitCapable {
		t.Fatalf("runProbeRegistryTCP exit = %d, want %d (stdout=%q)", rc, registryprobe.ExitCapable, stdout.String())
	}
	if got := stdout.String(); !bytes.Contains([]byte(got), []byte("ok: 127.0.0.1:"+strconv.Itoa(port))) {
		t.Fatalf("runProbeRegistryTCP stdout = %q, want to contain %q", got, "ok: 127.0.0.1:"+strconv.Itoa(port))
	}
}

// TestRunProbeRegistryTCP_NotConnectable verifies the CLI wrapper exits
// registryprobe.ExitIncapable -- not plain 1 -- and prints a "not
// connectable" line for the clean "no" verdict, since 1 is also what an old
// driver-exec's default verb produces and the two must stay distinguishable
// (issue #3120).
func TestRunProbeRegistryTCP_NotConnectable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(tcp, 127.0.0.1:0): %v", err)
	}
	port := listenerPort(t, ln)
	ln.Close()

	var stdout bytes.Buffer
	rc := runProbeRegistryTCP([]string{"-host", "127.0.0.1", "-port", strconv.Itoa(port)}, &stdout)
	if rc != registryprobe.ExitIncapable {
		t.Fatalf("runProbeRegistryTCP exit = %d, want %d (stdout=%q)", rc, registryprobe.ExitIncapable, stdout.String())
	}
	if got := stdout.String(); !bytes.Contains([]byte(got), []byte("not connectable: 127.0.0.1:"+strconv.Itoa(port))) {
		t.Fatalf("runProbeRegistryTCP stdout = %q, want to contain %q", got, "not connectable: 127.0.0.1:"+strconv.Itoa(port))
	}
}

// TestRunProbeRegistryTCP_MissingHostFlag verifies the CLI wrapper rejects
// an empty/unset -host flag rather than silently probing "", and that this
// usage error stays at plain 1 rather than the reserved ExitIncapable
// verdict code -- a missing flag was never tested, so it must not read as a
// tested-and-answered "no" (issue #3120).
func TestRunProbeRegistryTCP_MissingHostFlag(t *testing.T) {
	var stdout bytes.Buffer
	rc := runProbeRegistryTCP([]string{"-port", "1234"}, &stdout)
	if rc != 1 {
		t.Fatalf("runProbeRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
	if rc == registryprobe.ExitIncapable {
		t.Fatalf("runProbeRegistryTCP exit = %d, must not equal ExitIncapable (%d): a usage error is not a verdict", rc, registryprobe.ExitIncapable)
	}
}

// TestRunProbeRegistryTCP_MissingPortFlag verifies the CLI wrapper rejects
// an unset/zero -port flag rather than silently probing port 0, staying at
// plain 1 rather than the reserved ExitIncapable verdict code for the same
// reason as the missing-host case above.
func TestRunProbeRegistryTCP_MissingPortFlag(t *testing.T) {
	var stdout bytes.Buffer
	rc := runProbeRegistryTCP([]string{"-host", "127.0.0.1"}, &stdout)
	if rc != 1 {
		t.Fatalf("runProbeRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
	if rc == registryprobe.ExitIncapable {
		t.Fatalf("runProbeRegistryTCP exit = %d, must not equal ExitIncapable (%d): a usage error is not a verdict", rc, registryprobe.ExitIncapable)
	}
}

// TestIsProbeRegistryTCPInvocation verifies the verb dispatch predicate
// matches only on the "probe-registry-tcp" verb.
func TestIsProbeRegistryTCPInvocation(t *testing.T) {
	if isProbeRegistryTCPInvocation(nil) {
		t.Fatalf("isProbeRegistryTCPInvocation(nil) = true, want false")
	}
	if !isProbeRegistryTCPInvocation([]string{"probe-registry-tcp", "-host", "127.0.0.1", "-port", "1234"}) {
		t.Fatalf("isProbeRegistryTCPInvocation([probe-registry-tcp ...]) = false, want true")
	}
	if isProbeRegistryTCPInvocation([]string{"probe-registry-socket"}) {
		t.Fatalf("isProbeRegistryTCPInvocation([probe-registry-socket]) = true, want false")
	}
}
