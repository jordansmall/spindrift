package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/hostop"
)

func shadowReplayFixture(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("SPINDRIFT_CONNECTOR_FIXTURES_DIR")
	if dir == "" {
		t.Skip("SPINDRIFT_CONNECTOR_FIXTURES_DIR is unset; it names the dir holding the fixture Connector .wasm files")
	}
	return filepath.Join(dir, "driver.wasm")
}

// shadowReplayArchive is a one-Pass host log stamped as driver's.
func shadowReplayArchive(driver string) string {
	enc := hostop.EncodeSpindriftOp
	return enc(hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &hostop.DispatchStart{RecordID: "r1", Driver: driver}}) +
		enc(hostop.SpindriftOp{Op: hostop.OpPassStart, Pass: 1, Role: "implement"}) +
		`{"type":"step_start"}` + "\n" +
		enc(hostop.SpindriftOp{Op: hostop.OpPassUsage, Pass: 1, Usage: &hostop.PassUsage{APICalls: 1}})
}

func TestShadowReplayVerb_ReplaysOnlyTheNamedDriversLogs(t *testing.T) {
	wasm := shadowReplayFixture(t)
	dir := t.TempDir()
	opencode := filepath.Join(dir, "issue-1.log")
	pi := filepath.Join(dir, "issue-2.log")
	archives := map[string]string{opencode: shadowReplayArchive("opencode"), pi: shadowReplayArchive("pi")}
	for path, body := range archives {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	code := cmdShadowReplay(driverRuntime(t), []string{"--driver", "opencode", "--connector", wasm, dir}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	sidecar := filepath.Join(dir, "issue-1.shadow-replay.jsonl")
	if _, err := os.Stat(sidecar); err != nil {
		t.Errorf("no sidecar for the opencode log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "issue-2.shadow-replay.jsonl")); err == nil {
		t.Error("the pi log got a sidecar")
	}
	out := stdout.String()
	if !strings.Contains(out, opencode+": replayed 1 Passes -> "+sidecar) {
		t.Errorf("stdout = %q, want the replayed line for the opencode log", out)
	}
	if !strings.Contains(out, pi+": skipped: ") {
		t.Errorf("stdout = %q, want the pi log reported skipped", out)
	}
	for path, body := range archives {
		if got, _ := os.ReadFile(path); string(got) != body {
			t.Errorf("%s changed", path)
		}
	}
}

func TestShadowReplayVerb_UsageErrors(t *testing.T) {
	wasm := shadowReplayFixture(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no driver", []string{"--connector", wasm}, "--driver"},
		{"no native Strategy", []string{"--driver", "claude", "--connector", wasm}, "no native Strategy"},
		{"no connector", []string{"--driver", "opencode"}, "--connector"},
		{"unknown flag", []string{"--driver", "opencode", "--bogus"}, "--bogus"},
		{"two dirs", []string{"--driver", "opencode", "--connector", wasm, "a", "b"}, "one"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := cmdShadowReplay(nil, tc.args, &stdout, &stderr); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestShadowReplayVerb_NoLogsIsAnError(t *testing.T) {
	dir := t.TempDir()
	// The Connector is only read, never loaded, when there is no log to replay.
	wasm := filepath.Join(dir, "driver.wasm")
	if err := os.WriteFile(wasm, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agent-logs-1"), 0o700); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdShadowReplay(driverRuntime(t), []string{"--driver", "opencode", "--connector", wasm, dir}, &stdout, &stderr)

	if code == 0 {
		t.Errorf("exit = 0 with no logs, want non-zero")
	}
	if !strings.Contains(stderr.String(), "agent-logs-<n>") {
		t.Errorf("stderr = %q, want the gh run download hint", stderr.String())
	}
}
