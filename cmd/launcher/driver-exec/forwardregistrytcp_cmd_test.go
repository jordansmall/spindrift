package main

import (
	"bytes"
	"testing"
)

// TestIsForwardRegistryTCPInvocation verifies the verb dispatch predicate
// matches only on the "forward-registry-tcp" verb.
func TestIsForwardRegistryTCPInvocation(t *testing.T) {
	if isForwardRegistryTCPInvocation(nil) {
		t.Fatalf("isForwardRegistryTCPInvocation(nil) = true, want false")
	}
	if !isForwardRegistryTCPInvocation([]string{"forward-registry-tcp", "-listen-port", "1"}) {
		t.Fatalf("isForwardRegistryTCPInvocation([forward-registry-tcp ...]) = false, want true")
	}
	if isForwardRegistryTCPInvocation([]string{"bind-registry"}) {
		t.Fatalf("isForwardRegistryTCPInvocation([bind-registry]) = true, want false")
	}
}

// TestRunForwardRegistryTCP_MissingListenPortFlag verifies the CLI wrapper
// rejects an unset/zero -listen-port rather than trying to bind an
// arbitrary port.
func TestRunForwardRegistryTCP_MissingListenPortFlag(t *testing.T) {
	var stdout bytes.Buffer
	rc := runForwardRegistryTCP([]string{"-upstream-host", "127.0.0.1", "-upstream-port", "1"}, &stdout)
	if rc != 1 {
		t.Fatalf("runForwardRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
}

// TestRunForwardRegistryTCP_MissingUpstreamHostFlag verifies the CLI
// wrapper rejects an unset -upstream-host.
func TestRunForwardRegistryTCP_MissingUpstreamHostFlag(t *testing.T) {
	var stdout bytes.Buffer
	rc := runForwardRegistryTCP([]string{"-listen-port", "1", "-upstream-port", "1"}, &stdout)
	if rc != 1 {
		t.Fatalf("runForwardRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
}

// TestRunForwardRegistryTCP_MissingUpstreamPortFlag verifies the CLI
// wrapper rejects an unset/zero -upstream-port.
func TestRunForwardRegistryTCP_MissingUpstreamPortFlag(t *testing.T) {
	var stdout bytes.Buffer
	rc := runForwardRegistryTCP([]string{"-listen-port", "1", "-upstream-host", "127.0.0.1"}, &stdout)
	if rc != 1 {
		t.Fatalf("runForwardRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
}

// TestRunForwardRegistryTCP_MissingSecretEnv verifies the CLI wrapper
// rejects a run with every flag present but no REGISTRY_PROXY_TCP_SECRET in
// the environment -- the secret must never have a flag fallback (it would
// then be visible via ps/proc).
func TestRunForwardRegistryTCP_MissingSecretEnv(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "")

	var stdout bytes.Buffer
	rc := runForwardRegistryTCP([]string{"-listen-port", "1", "-upstream-host", "127.0.0.1", "-upstream-port", "1"}, &stdout)
	if rc != 1 {
		t.Fatalf("runForwardRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
}
