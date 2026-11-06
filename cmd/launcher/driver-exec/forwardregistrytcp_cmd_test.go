package main

import (
	"bytes"
	"testing"
)

func TestRunForwardRegistryTCP_MissingListenPortFlag(t *testing.T) {
	var stdout bytes.Buffer
	rc := runForwardRegistryTCP([]string{"-upstream-host", "127.0.0.1", "-upstream-port", "1"}, &stdout)
	if rc != 1 {
		t.Fatalf("runForwardRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
}

func TestRunForwardRegistryTCP_MissingUpstreamHostFlag(t *testing.T) {
	var stdout bytes.Buffer
	rc := runForwardRegistryTCP([]string{"-listen-port", "1", "-upstream-port", "1"}, &stdout)
	if rc != 1 {
		t.Fatalf("runForwardRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
}

func TestRunForwardRegistryTCP_MissingUpstreamPortFlag(t *testing.T) {
	var stdout bytes.Buffer
	rc := runForwardRegistryTCP([]string{"-listen-port", "1", "-upstream-host", "127.0.0.1"}, &stdout)
	if rc != 1 {
		t.Fatalf("runForwardRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
}

// The secret must never have a flag fallback, because a flag value is
// visible via ps and proc. Every flag is present here, so only the missing
// env var can make the run fail.
func TestRunForwardRegistryTCP_MissingSecretEnv(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "")

	var stdout bytes.Buffer
	rc := runForwardRegistryTCP([]string{"-listen-port", "1", "-upstream-host", "127.0.0.1", "-upstream-port", "1"}, &stdout)
	if rc != 1 {
		t.Fatalf("runForwardRegistryTCP exit = %d, want 1 (stdout=%q)", rc, stdout.String())
	}
}
