package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/signalwire"
)

// TestRunOnce_SignalCarrierLog_NoOp pins slice 3b's "log mode is unchanged"
// acceptance criterion (issue #3725): with SignalCarrier unset and with the
// explicit "log" value, no SIGNAL_SOCKET_* env key, no extra socket mount and
// a zero Box.SignalSocket reach the Box, and -- with no registry routes --
// the transport probe never runs at all.
func TestRunOnce_SignalCarrierLog_NoOp(t *testing.T) {
	for _, carrier := range []string{"", "log"} {
		t.Run(fmt.Sprintf("carrier=%q", carrier), func(t *testing.T) {
			cfg := retryConfig(3, 0, 0)
			cfg.SignalCarrier = carrier

			fr := runner.NewFake()
			var sockets []runner.SocketMount
			var envSnapshot map[string]string
			var signalLoc runner.SignalSocketLocation
			fr.RunFunc = func(box runner.Box) error {
				sockets = box.Sockets
				envSnapshot = box.Env
				signalLoc = box.SignalSocket
				box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
				return nil
			}

			d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
			result := d.Run()

			if !result.Success {
				t.Fatalf("Run: want Success=true, got %+v", result)
			}
			if len(sockets) != 0 {
				t.Errorf("box.Sockets = %+v, want empty under the log carrier", sockets)
			}
			for _, key := range []string{"SIGNAL_SOCKET_ENDPOINT", "SIGNAL_SOCKET_SECRET"} {
				if v, ok := envSnapshot[key]; ok {
					t.Errorf("box.Env contains %q = %q under the log carrier, want absent", key, v)
				}
			}
			if signalLoc != (runner.SignalSocketLocation{}) {
				t.Errorf("box.SignalSocket = %+v, want the zero value under the log carrier", signalLoc)
			}
			if fr.RegistryProxyTransportCalls != 0 {
				t.Errorf("RegistryProxyTransportCalls = %d, want 0: neither the registry proxy nor the Signal socket is configured", fr.RegistryProxyTransportCalls)
			}
			if d.signalBuffer != nil {
				t.Errorf("d.signalBuffer = %v, want nil under the log carrier", d.signalBuffer)
			}
		})
	}
}

// TestRunOnce_SignalCarrierSocket_UnixVerdict_LiveMountAndRetainedBuffer pins
// three of slice 3b's socket-mode acceptance criteria at once (issue #3725):
// the listener is up and reachable before the container starts (the fake
// runner's Run dials the mounted socket and posts a comment through the real
// handler), the socket is in box.Sockets at runner.SignalSocketTarget with
// SIGNAL_SOCKET_ENDPOINT set to the in-Box target rather than the host path,
// and after the Dispatch returns the listener is closed (a fresh dial fails)
// while the buffer still holds what was posted.
func TestRunOnce_SignalCarrierSocket_UnixVerdict_LiveMountAndRetainedBuffer(t *testing.T) {
	cfg := retryConfig(3, 0, 0)
	cfg.SignalCarrier = "socket"

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")

	var sockets []runner.SocketMount
	var envSnapshot map[string]string
	var postStatus int
	fr.RunFunc = func(box runner.Box) error {
		sockets = box.Sockets
		envSnapshot = box.Env
		if len(sockets) == 1 {
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", sockets[0].Source)
				},
			}}
			resp, err := client.Post("http://signal/comment", "application/json", strings.NewReader(`{"body":"hello from the box"}`))
			if err != nil {
				t.Errorf("POST through signal socket: %v", err)
			} else {
				postStatus = resp.StatusCode
				resp.Body.Close() //nolint:errcheck
			}
		}
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if len(sockets) != 1 {
		t.Fatalf("box.Sockets = %+v, want exactly one entry", sockets)
	}
	if sockets[0].Target != runner.SignalSocketTarget {
		t.Errorf("box.Sockets[0].Target = %q, want %q", sockets[0].Target, runner.SignalSocketTarget)
	}
	if postStatus != http.StatusOK {
		t.Errorf("POST /comment status = %d, want 200 -- the listener must be live before the container starts", postStatus)
	}
	if got, want := envSnapshot["SIGNAL_SOCKET_ENDPOINT"], "unix://"+runner.SignalSocketTarget; got != want {
		t.Errorf("box.Env[SIGNAL_SOCKET_ENDPOINT] = %q, want %q (the in-Box mount target, not the host source)", got, want)
	}

	sourcePath := sockets[0].Source
	if _, err := net.Dial("unix", sourcePath); err == nil {
		t.Error("dial the signal socket after Run returned: want an error, the listener must be closed at Box exit")
	}

	if d.signalBuffer == nil {
		t.Fatal("d.signalBuffer is nil after a socket-carrier Run, want it retained")
	}
	c, ok := d.signalBuffer.Comment()
	if !ok || c.Body != "hello from the box" {
		t.Errorf("d.signalBuffer.Comment() = (%+v, %v), want the posted comment still readable after Close", c, ok)
	}
}

// TestRunOnce_SignalCarrierSocket_TCPVerdict verifies that under a TCP
// transport verdict, SIGNAL_SOCKET_SECRET is set, differs from RUN_NONCE, and
// box.SignalSocket carries the bound endpoint and the probe's add-host
// verdict (issue #3725).
func TestRunOnce_SignalCarrierSocket_TCPVerdict(t *testing.T) {
	cfg := retryConfig(3, 0, 0)
	cfg.SignalCarrier = "socket"

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewTCPEndpoint("host.docker.internal", "")
	fr.RegistryProxyTransportAddHost = true

	var loc runner.SignalSocketLocation
	var envSnapshot map[string]string
	var postStatus int
	fr.RunFunc = func(box runner.Box) error {
		loc = box.SignalSocket
		envSnapshot = box.Env
		if loc.Endpoint.Port() != "" {
			req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%s/comment", loc.Endpoint.Port()), strings.NewReader(`{"body":"hello"}`))
			if err != nil {
				t.Fatalf("build signal socket TCP request: %v", err)
			}
			req.Header.Set(signalwire.SecretHeader, envSnapshot["SIGNAL_SOCKET_SECRET"])
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("POST through signal socket TCP port: %v", err)
			} else {
				postStatus = resp.StatusCode
				resp.Body.Close() //nolint:errcheck
			}
		}
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if loc.Endpoint.Host() != "host.docker.internal" {
		t.Errorf("box.SignalSocket.Endpoint.Host() = %q, want %q", loc.Endpoint.Host(), "host.docker.internal")
	}
	if loc.Endpoint.Port() == "" || loc.Endpoint.Port() == "0" {
		t.Errorf("box.SignalSocket.Endpoint.Port() = %q, want a bound ephemeral port", loc.Endpoint.Port())
	}
	if !loc.TCPAddHost {
		t.Error("box.SignalSocket.TCPAddHost = false, want true: the probe reported an add-host verdict")
	}
	secret := envSnapshot["SIGNAL_SOCKET_SECRET"]
	if secret == "" {
		t.Error("box.Env[SIGNAL_SOCKET_SECRET] was empty, want a minted secret")
	}
	if secret == envSnapshot["RUN_NONCE"] {
		t.Error("SIGNAL_SOCKET_SECRET must not equal RUN_NONCE: separate credentials, separate security roles")
	}
	if got := envSnapshot["SIGNAL_SOCKET_ENDPOINT"]; got != fmt.Sprintf("http://host.docker.internal:%s", loc.Endpoint.Port()) {
		t.Errorf("box.Env[SIGNAL_SOCKET_ENDPOINT] = %q, want it to match box.SignalSocket.Endpoint", got)
	}
	if postStatus != http.StatusOK {
		t.Errorf("POST /comment status = %d, want 200", postStatus)
	}
}

// TestRunOnce_SignalCarrierSocket_TCPVerdictUnderNoHostLoopback_Fails pins
// slice 3b's startup-gate acceptance criterion (issue #3725): a TCP verdict
// under NETWORK_MODE=no-host-loopback can only be known after the
// per-Dispatch transport probe, so runOnce itself must reject it, naming the
// knob and the mode, before the Box ever runs.
func TestRunOnce_SignalCarrierSocket_TCPVerdictUnderNoHostLoopback_Fails(t *testing.T) {
	cfg := retryConfig(3, 0, 0)
	cfg.SignalCarrier = "socket"
	cfg.NetworkMode = runner.NetworkModeNoHostLoopback

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewTCPEndpoint("host.docker.internal", "")

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())

	env, err := buildBoxEnv(d.cfg, d.number, d.title, 0, "", d.nonce)
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	err = d.runOnce(d.logPath(), env, d.cacheDir)

	if err == nil {
		t.Fatal("runOnce: want a non-nil error for a TCP verdict under NETWORK_MODE=no-host-loopback")
	}
	if !strings.Contains(err.Error(), "BOX_SIGNAL_CARRIER") {
		t.Errorf("runOnce error = %q, want it to name BOX_SIGNAL_CARRIER", err.Error())
	}
	if !strings.Contains(err.Error(), runner.NetworkModeNoHostLoopback) {
		t.Errorf("runOnce error = %q, want it to name NETWORK_MODE=%s", err.Error(), runner.NetworkModeNoHostLoopback)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %d, want 0: the Box must never run when the startup gate rejects", len(fr.RunCalls))
	}
}

// TestRunOnce_SignalCarrierSocket_TransportProbeSharedWithRegistryProxy
// verifies ADR 0052's one-probe rule: with both a registry proxy and the
// socket signal carrier configured, exactly one RegistryProxyTransport call
// serves both features rather than one each.
func TestRunOnce_SignalCarrierSocket_TransportProbeSharedWithRegistryProxy(t *testing.T) {
	cfg := retryConfig(3, 0, 0)
	cfg.SignalCarrier = "socket"

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")
	fr.RunFunc = func(box runner.Box) error {
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if fr.RegistryProxyTransportCalls != 1 {
		t.Errorf("RegistryProxyTransportCalls = %d, want exactly 1 (ADR 0052 forbids a second probe)", fr.RegistryProxyTransportCalls)
	}
}

// TestRunOnce_SignalCarrierSocket_TransportProbeErrorsAbortsDispatch verifies
// that a probe taken for the Signal socket alone -- no registry routes
// configured -- reports as the Signal socket's own error, and never runs the
// Box.
func TestRunOnce_SignalCarrierSocket_TransportProbeErrorsAbortsDispatch(t *testing.T) {
	cfg := retryConfig(3, 0, 0)
	cfg.SignalCarrier = "socket"

	probeErr := errors.New("probe: exec failed")
	fr := runner.NewFake()
	fr.RegistryProxyTransportErr = probeErr

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())

	env, err := buildBoxEnv(d.cfg, d.number, d.title, 0, "", d.nonce)
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	err = d.runOnce(d.logPath(), env, d.cacheDir)

	if err == nil {
		t.Fatal("runOnce: want a non-nil error when the transport probe fails")
	}
	if !errors.Is(err, probeErr) {
		t.Errorf("runOnce error = %v, want it to wrap %v", err, probeErr)
	}
	if !strings.Contains(err.Error(), "signal socket") {
		t.Errorf("runOnce error = %q, want it attributed to the signal socket (no registry proxy configured)", err.Error())
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %d, want 0: the Box must never run when the transport probe itself errors", len(fr.RunCalls))
	}
}

// TestRunOnce_SignalCarrierSocket_MirrorNeverSplicesOutcomeLine pins the one
// interleaving that silently loses a dispatch (issue #3725): the Box's output
// copy and the Signal handler's spindrift_op mirror share the raw log file, so
// a signal POST landing while a stream line is still half-written must not cut
// that line in two. Here the half-written line is the SPINDRIFT_OUTCOME line
// itself -- spliced, outcome.Resolve finds no token-leading fielded outcome
// and a clean Box exit settles as a no-outcome run.
func TestRunOnce_SignalCarrierSocket_MirrorNeverSplicesOutcomeLine(t *testing.T) {
	cfg := retryConfig(3, 0, 0)
	cfg.SignalCarrier = "socket"

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")

	var postStatus int
	fr.RunFunc = func(box runner.Box) error {
		line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"]
		half := len(line) / 2
		box.Output.Write([]byte(line[:half])) //nolint:errcheck

		if len(box.Sockets) != 1 {
			t.Errorf("box.Sockets = %+v, want exactly one entry", box.Sockets)
		} else {
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", box.Sockets[0].Source)
				},
			}}
			resp, err := client.Post("http://signal/comment", "application/json", strings.NewReader(`{"body":"mid-line signal"}`))
			if err != nil {
				t.Errorf("POST through signal socket: %v", err)
			} else {
				postStatus = resp.StatusCode
				resp.Body.Close() //nolint:errcheck
			}
		}

		box.Output.Write([]byte(line[half:] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
	logPath := d.logPath()
	result := d.Run()

	if postStatus != http.StatusOK {
		t.Fatalf("POST /comment status = %d, want 200", postStatus)
	}
	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v -- the mirror line spliced the outcome line", result)
	}
	if !result.Resolved.Found {
		t.Fatalf("result.Resolved.Found = false, want the outcome resolved: %+v", result.Resolved)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read box log: %v", err)
	}
	var outcomeLine, mirrorLine int
	for i, ln := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(ln, "SPINDRIFT_OUTCOME "):
			if !strings.Contains(ln, "status=ready") || !strings.Contains(ln, "note=ok") {
				t.Errorf("box log line %d = %q, want the whole outcome line intact", i, ln)
			}
			outcomeLine = i + 1
		case strings.Contains(ln, `"spindrift_op"`):
			if !json.Valid([]byte(ln)) {
				t.Errorf("box log line %d = %q, want one whole spindrift_op JSON line", i, ln)
			}
			mirrorLine = i + 1
		}
	}
	if outcomeLine == 0 {
		t.Fatalf("box log has no intact SPINDRIFT_OUTCOME line:\n%s", data)
	}
	if mirrorLine == 0 {
		t.Fatalf("box log has no intact spindrift_op mirror line:\n%s", data)
	}
	if mirrorLine <= outcomeLine {
		t.Errorf("spindrift_op mirror on line %d, outcome on line %d: want the held mirror line flushed after the stream line completes", mirrorLine, outcomeLine)
	}
}
