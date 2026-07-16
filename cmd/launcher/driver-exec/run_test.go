package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeNix writes a fake `nix` on PATH (via t.Setenv) that logs its own
// argv to logPath, and — mirroring tests/fakes/nix's FAKE_NIX_DEV_SHELL_OK=1
// behaviour — execs whatever follows "--command" so the wrapped command
// actually runs. Returns nothing; the caller reads logPath to assert on the
// invocation.
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

// writeFakeDriver writes an executable shell script at dir/name that runs
// body, and returns its absolute path.
func writeFakeDriver(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureStderr swaps os.Stderr for a pipe for the duration of fn, so a test
// can assert on what runOnce's hardcoded cmd.Stderr = os.Stderr wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestRunDirectModePropagatesExitCode verifies driver-exec returns the
// Driver's own exit code unchanged when run directly (no devShell), the
// simplest of the pipeline's invariants to preserve (issue #626).
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

// TestRunTeesRawStreamToStdoutAndLogPath verifies the Driver's raw stdout
// reaches both driver-exec's own stdout (the launcher's byte-exact capture
// channel) and cfg.logPath (which the Driver's outcome-extraction pass reads
// afterward) unchanged.
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

// TestRunWritesHeartbeatToFileNotRawJSON verifies driver-exec filters
// heartbeats in-process (absorbing the standalone heartbeat-filter binary,
// issue #626): the heartbeat file gets a human-readable status line, never
// raw stream-json.
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

// TestRunDevshellWrapsCommand verifies that with cfg.devshell set, run spawns
// the Driver via `nix develop .#<name> --command <absolute-driver-bin>
// <args...>` instead of invoking the Driver directly — the devShell-first
// invocation path (ADR 0014) driver-exec now owns as one code path instead of
// entrypoint.sh's separate wrapper script (issue #626).
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

// writeFakeNixLaunchFail writes a fake `nix` that always fails before
// exec-ing the wrapped command (an empty output stream), mirroring a devShell
// that no longer evaluates cleanly at Driver-run time even though the
// earlier probe (phase_devshell_probe, entrypoint.sh) found one.
func writeFakeNixLaunchFail(t *testing.T, dir string) {
	t.Helper()
	body := "#!/bin/sh\nexit 1\n"
	path := filepath.Join(dir, "nix")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRunRelaunchesInBakedEnvOnEmptyStreamLaunchFailure verifies the
// relaunch-once-in-the-baked-env policy (formerly entrypoint.sh's bash
// fallback, issue #626): when the devShell launch fails before the Driver
// produces any output (the log stays empty), driver-exec relaunches directly
// and returns that direct run's exit code instead of the failed launch's.
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
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0 (relaunched direct run's exit code)", rc)
	}
	if !strings.Contains(stdout.String(), `"type":"result"`) {
		t.Errorf("stdout = %q, want the relaunched Driver's output", stdout.String())
	}
}

// TestRunDoesNotRelaunchWhenDevshellStreamIsNonEmpty verifies the relaunch
// only triggers on a genuine launch failure (empty stream) — a devShell
// Driver run that produced output but exited non-zero (a real task failure)
// must propagate that exit code untouched, never mask it with a relaunch.
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
	stderr := captureStderr(t, func() {
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

// TestRunLogsObservabilityEventOnRelaunch verifies driver-exec restores the
// observability line dropped in the bash->Go port (issue #797): when the
// relaunch-on-empty-stream branch fires, it must log to stderr (the same
// channel runOnce wires the Driver's own cmd.Stderr to) so operators tailing
// the Box log see why the Driver output changed.
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
	stderr := captureStderr(t, func() {
		if _, err := run(cfg, &stdout); err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	if !strings.Contains(stderr, "relaunching in baked env") {
		t.Errorf("stderr = %q, want a relaunch observability line", stderr)
	}
}
