package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

// writeFakeNix writes a fake `nix` on PATH that logs its own argv to logPath
// and execs whatever follows "--command", mirroring tests/fakes/nix under
// FAKE_NIX_DEV_SHELL_OK=1 so the wrapped command actually runs.
func writeFakeNix(t *testing.T, dir, logPath string) {
	t.Helper()
	body := `#!/bin/sh
echo "$@" >> ` + logPath + `
found=0
cmd=""
for arg in "$@"; do
  if [ "$found" = "1" ]; then
    cmd="$cmd $arg"
  elif [ "$arg" = "--command" ]; then
    found=1
  fi
done
if [ -n "$cmd" ]; then
  # shellcheck disable=SC2086
  exec $cmd
fi
`
	path := filepath.Join(dir, "nix")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeFakeDriver(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// driver-exec must return the Driver's own exit code unchanged when it runs
// the Driver directly, with no devShell (issue #626).
func TestRunDirectModePropagatesExitCode(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeDriver(t, dir, "fake-driver", "echo hi\nexit 7\n")
	cfg := execConfig{
		driverBin:    bin,
		args:         nil,
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "7",
	}
	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 7 {
		t.Errorf("exit code = %d, want 7", rc)
	}
}

// run must resolve cfg.driver through driver.New instead of the former
// hardcoded driver.New("claude") (issue #262 slice 4). An unknown driver name
// has to reach driver.New and error, which a silent fallback to claude would
// hide.
func TestRunUsesConfiguredDriverNotHardcodedClaude(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeDriver(t, dir, "fake-driver", "echo hi\nexit 0\n")
	cfg := execConfig{
		driver:       "not-a-real-driver",
		driverBin:    bin,
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "7",
	}
	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err == nil {
		t.Error("run: want an error for an unknown cfg.driver, got nil")
	}
}

// Both sinks matter: the launcher captures driver-exec's stdout byte for byte,
// and the Driver's outcome-extraction pass reads cfg.logPath afterward.
func TestRunTeesRawStreamToStdoutAndLogPath(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeDriver(t, dir, "fake-driver", `printf '{"type":"result","result":"done"}\n'`)
	logPath := filepath.Join(dir, "stream.log")
	cfg := execConfig{
		driverBin:    bin,
		logPath:      logPath,
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "7",
	}
	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0", rc)
	}
	want := `{"type":"result","result":"done"}` + "\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read logPath: %v", err)
	}
	if string(got) != want {
		t.Errorf("logPath content = %q, want %q", got, want)
	}
}

// driver-exec filters heartbeats in process since it absorbed the standalone
// heartbeat-filter binary (issue #626): the heartbeat file gets a
// human-readable status line, never raw stream-json.
func TestRunWritesHeartbeatToFileNotRawJSON(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeDriver(t, dir, "fake-driver", `printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}\n{"type":"result","num_turns":1}\n'`)
	heartbeatLog := filepath.Join(dir, "heartbeat.log")
	cfg := execConfig{
		driverBin:    bin,
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: heartbeatLog,
		issue:        "7",
	}
	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(heartbeatLog)
	if err != nil {
		t.Fatalf("read heartbeatLog: %v", err)
	}
	content := string(got)
	if !bytes.Contains(got, []byte("#7")) {
		t.Errorf("heartbeat missing issue prefix: %q", content)
	}
	if bytes.Contains(got, []byte(`"type":`)) {
		t.Errorf("heartbeat file contains raw JSON: %q", content)
	}
}

// cfg.topLevelRole must reach the Driver's heartbeat writer end to end
// (issue #2092). The fixture stream carries one top-level assistant event, with
// no parent_tool_use_id, so the header must read "reviewer" rather than the
// implementor default.
func TestRunTopLevelRoleAppliesToHeartbeatSwitchHeader(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	dir := t.TempDir()
	streamJSON := `{"type":"assistant","message":{"content":[{"type":"text","text":"Reviewing the change."}]}}` + "\n" +
		`{"type":"result","num_turns":1}` + "\n"
	bin := writeFakeDriver(t, dir, "fake-driver", `printf '`+streamJSON+`'`)
	heartbeatLog := filepath.Join(dir, "heartbeat.log")
	cfg := execConfig{
		driver:       "claude",
		driverBin:    bin,
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: heartbeatLog,
		issue:        "42",
		topLevelRole: "reviewer",
	}
	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(heartbeatLog)
	if err != nil {
		t.Fatalf("read heartbeatLog: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, rule+" reviewer ") {
		t.Errorf("heartbeat log missing reviewer switch header: %q", content)
	}
	if strings.Contains(content, rule+" implementor ") {
		t.Errorf("heartbeat log must not emit an implementor header when topLevelRole is set: %q", content)
	}
}

// driver-exec owns the devShell-first invocation path (ADR 0014) as one code
// path since it replaced entrypoint.sh's wrapper script (issue #626).
func TestRunDevshellWrapsCommand(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeDriver(t, dir, "fake-driver", "echo devshell-ran\nexit 0\n")
	nixLog := filepath.Join(dir, "nix.log")
	writeFakeNix(t, dir, nixLog)

	cfg := execConfig{
		driverBin:    bin,
		args:         []string{"--flag"},
		devshell:     true,
		devshellName: "ci",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "7",
	}
	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0", rc)
	}
	nixArgs, err := os.ReadFile(nixLog)
	if err != nil {
		t.Fatalf("read nixLog: %v", err)
	}
	got := string(nixArgs)
	if !strings.Contains(got, "develop .#ci --command "+bin+" --flag") {
		t.Errorf("nix invocation = %q, want it to contain %q", got, "develop .#ci --command "+bin+" --flag")
	}
	if !strings.Contains(stdout.String(), "devshell-ran") {
		t.Errorf("stdout = %q, want it to contain driver output", stdout.String())
	}
}

// writeFakeNixLaunchFail writes a fake `nix` that fails before exec-ing the
// wrapped command, leaving an empty output stream. That mirrors a devShell
// which no longer evaluates cleanly at Driver-run time even though
// entrypoint.sh's earlier phase_devshell_probe found one.
func writeFakeNixLaunchFail(t *testing.T, dir string) {
	t.Helper()
	body := "#!/bin/sh\nexit 1\n"
	path := filepath.Join(dir, "nix")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The relaunch-once-in-the-baked-env policy replaced entrypoint.sh's bash
// fallback (issue #626). When the devShell launch fails before the Driver
// writes anything, driver-exec relaunches directly and returns that direct
// run's exit code, not the failed launch's.
func TestRunRelaunchesInBakedEnvOnEmptyStreamLaunchFailure(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeDriver(t, dir, "fake-driver", `printf '{"type":"result"}\n'`+"\nexit 0\n")
	writeFakeNixLaunchFail(t, dir)

	cfg := execConfig{
		driverBin:    bin,
		devshell:     true,
		devshellName: "default",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "7",
	}
	var stdout bytes.Buffer
	var rc int
	testutil.CaptureStderr(t, func() {
		var runErr error
		rc, runErr = run(cfg, &stdout)
		if runErr != nil {
			t.Fatalf("run: %v", runErr)
		}
	})
	if rc != 0 {
		t.Errorf("exit code = %d, want 0 (relaunched direct run's exit code)", rc)
	}
	if !strings.Contains(stdout.String(), `"type":"result"`) {
		t.Errorf("stdout = %q, want the relaunched Driver's output", stdout.String())
	}
}

// Only an empty stream counts as a launch failure. A devShell run that produced
// output and then exited non-zero is a real task failure, so run must propagate
// that exit code instead of masking it with a relaunch.
func TestRunDoesNotRelaunchWhenDevshellStreamIsNonEmpty(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeDriver(t, dir, "fake-driver", "echo ran\nexit 3\n")
	nixLog := filepath.Join(dir, "nix.log")
	writeFakeNix(t, dir, nixLog)

	cfg := execConfig{
		driverBin:    bin,
		devshell:     true,
		devshellName: "default",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "7",
	}
	var stdout bytes.Buffer
	var rc int
	stderr := testutil.CaptureStderr(t, func() {
		var runErr error
		rc, runErr = run(cfg, &stdout)
		if runErr != nil {
			t.Fatalf("run: %v", runErr)
		}
	})
	if rc != 3 {
		t.Errorf("exit code = %d, want 3 (the devShell run's own exit code, no relaunch)", rc)
	}
	if strings.Contains(stderr, "relaunching in baked env") {
		t.Errorf("stderr = %q, want no relaunch observability line for a genuine task failure", stderr)
	}
}

// The bash to Go port dropped this observability line (issue #797). The
// relaunch branch has to log it to stderr, the same channel runOnce wires the
// Driver's cmd.Stderr to, so operators tailing the Box log see why the Driver
// output changed.
func TestRunLogsObservabilityEventOnRelaunch(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeDriver(t, dir, "fake-driver", `printf '{"type":"result"}\n'`+"\nexit 0\n")
	writeFakeNixLaunchFail(t, dir)

	cfg := execConfig{
		driverBin:    bin,
		devshell:     true,
		devshellName: "default",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "7",
	}
	var stdout bytes.Buffer
	stderr := testutil.CaptureStderr(t, func() {
		if _, err := run(cfg, &stdout); err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	if !strings.Contains(stderr, "relaunching in baked env") {
		t.Errorf("stderr = %q, want a relaunch observability line", stderr)
	}
}
