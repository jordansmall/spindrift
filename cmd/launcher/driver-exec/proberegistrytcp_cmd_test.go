package main

import (
	"bytes"
	"net"
	"strconv"
	"testing"

	"spindrift.dev/launcher/internal/registryprobe"
)

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

// Issue #3111: the live reachability sub-probe must confirm a connectable
// listener before the launcher trusts an --add-host host-gateway route into
// the Box.
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

// Opening a port and closing it first proves the port number is real but
// freed, which is the unreachable-route case the sub-probe exists to catch.
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

// Issue #3120: the clean "no" verdict exits ExitIncapable, not plain 1,
// because 1 is also what an old driver-exec's default verb produces and the
// two must stay distinguishable.
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

// Issue #3120: a missing flag means the route was never tested, so the usage
// error stays at plain 1 and must not read as a tested-and-answered "no" by
// reusing the reserved ExitIncapable code.
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

// Issue #3120: an unset or zero -port stays at plain 1 for the same reason
// as the missing-host case above.
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
