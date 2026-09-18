package runner

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// RunFunc must win over RunErrs/RunErr: it is how waves tests control
// completion order and timing without real sleeps.
func TestFakeRunFuncOverridesDefault(t *testing.T) {
	f := NewFake()
	f.RunErr = errors.New("exit 1")
	var got Box
	f.RunFunc = func(box Box) error {
		got = box
		return nil
	}

	if err := f.Run(Box{Issue: "7"}); err != nil {
		t.Fatalf("Run: got %v, want nil (RunFunc must override RunErr)", err)
	}
	if got.Issue != "7" {
		t.Errorf("RunFunc box: got Issue=%q, want \"7\"", got.Issue)
	}
	if len(f.RunCalls) != 1 || f.RunCalls[0].Issue != "7" {
		t.Errorf("RunCalls: got %v, want one call for issue 7", f.RunCalls)
	}
}

// Scripting one Endpoint value replaced the old four-tuple (socketCapable,
// tcpHost, tcpAddHost, err), so a caller can no longer script an incoherent
// combination such as a socket-capable answer that also carries a TCP host.
func TestFakeRegistryProxyTransport_ReturnsScriptedEndpoint(t *testing.T) {
	f := NewFake()
	want := registrymanifest.NewTCPEndpoint("host.docker.internal", "")
	f.RegistryProxyTransportEndpoint = want
	f.RegistryProxyTransportAddHost = true

	endpoint, addHost, err := f.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: %v", err)
	}
	if endpoint != want {
		t.Errorf("RegistryProxyTransport endpoint = %+v, want %+v", endpoint, want)
	}
	if !addHost {
		t.Error("RegistryProxyTransport: want addHost=true")
	}
	if f.RegistryProxyTransportCalls != 1 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 1", f.RegistryProxyTransportCalls)
	}
}

// Orphan detection on Console startup (issue #651) needs a fake source of
// "still running" sandbox names with no live goroutine tracking them.
func TestFakeListRunning_ReturnsConfiguredNames(t *testing.T) {
	f := NewFake()
	f.RunningNames = []string{"agent-issue-42", "agent-issue-43"}

	got, err := f.ListRunning()
	if err != nil {
		t.Fatalf("ListRunning: %v", err)
	}
	if len(got) != 2 || got[0] != "agent-issue-42" || got[1] != "agent-issue-43" {
		t.Errorf("ListRunning = %v, want [agent-issue-42 agent-issue-43]", got)
	}
}
