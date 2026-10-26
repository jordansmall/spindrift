package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/testutil"
)

// writeFakeNix writes a fake `nix` on PATH that logs its own argv to logPath
// and execs whatever follows "--command" so the wrapped command actually runs.
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
// box's earlier devShell probe found one.
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

const (
	piMessageStart = `{"type":"message_start","message":{"role":"assistant","content":[],"stopReason":"pending"}}` + "\n"
	piDeltaHel     = `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hel"}}` + "\n"
	piDeltaLo      = `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"lo"}}` + "\n"
	piMessageEnd   = `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}],"stopReason":"stop"}}` + "\n"
	piToolUpdate   = `{"type":"tool_execution_update","toolCallId":"t1","partialResult":"out"}` + "\n"
)

// fixtureConfig returns an execConfig for a fake Driver that prints stream
// verbatim and then runs tail, a shell snippet.
func fixtureConfig(t *testing.T, driverName, stream, tail string) execConfig {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "stream.txt")
	if err := os.WriteFile(fixture, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	return execConfig{
		driver:       driverName,
		driverBin:    writeFakeDriver(t, dir, "fake-driver", "cat "+fixture+"\n"+tail),
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		issue:        "7",
	}
}

// runFixture runs a fake Driver that prints stream verbatim and returns what
// reached stdout and the Pass log.
func runFixture(t *testing.T, driverName, stream string) (stdout, log string) {
	t.Helper()
	cfg := fixtureConfig(t, driverName, stream, "")
	var out bytes.Buffer
	rc, err := run(cfg, &out)
	if err != nil || rc != 0 {
		t.Fatalf("run = (%d, %v), want (0, nil)", rc, err)
	}
	got, err := os.ReadFile(cfg.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), string(got)
}

// Only pi drops anything: claude and opencode streams reach the Pass log and
// stdout byte for byte, even when they look like pi deltas or end mid-line.
func TestRunPassthroughDriversKeepStreamByteIdentical(t *testing.T) {
	stream := piMessageStart + piDeltaHel + piDeltaLo + piMessageEnd + `{"type":"result","result":"no newline"}`
	for _, name := range []string{"claude", "opencode"} {
		t.Run(name, func(t *testing.T) {
			stdout, log := runFixture(t, name, stream)
			if stdout != stream {
				t.Errorf("stdout = %q, want %q", stdout, stream)
			}
			if log != stream {
				t.Errorf("log = %q, want %q", log, stream)
			}
		})
	}
}

// A finished assistant message's deltas are redundant with its message_end
// (issue #6066); everything else, tool updates included, passes through. The
// filter wraps the stdout tee too, so driver-exec's stdout loses them as well.
func TestRunPiDropsDeltasOfFinishedMessage(t *testing.T) {
	stdout, log := runFixture(t, "pi", piMessageStart+piDeltaHel+piToolUpdate+piDeltaLo+piMessageEnd)
	want := piMessageStart + piToolUpdate + piMessageEnd
	if log != want {
		t.Errorf("log = %q, want %q", log, want)
	}
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// A message cut off by the end of the stream is only recorded by its deltas.
func TestRunPiKeepsCoalescedDeltasOfUnfinishedMessage(t *testing.T) {
	_, log := runFixture(t, "pi", piMessageStart+piDeltaHel+piDeltaLo)
	if !strings.HasPrefix(log, piMessageStart) {
		t.Errorf("log = %q, want it to start with the message_start line", log)
	}
	if n := strings.Count(log, `"delta":"Hello"`); n != 1 {
		t.Errorf("log has %d coalesced \"Hello\" deltas, want 1: %q", n, log)
	}
	if strings.Contains(log, `"delta":"Hel"`) {
		t.Errorf("log still carries a raw partial delta: %q", log)
	}
}

// SIGTERM mid-message must not lose the held text: the Box stop path signals
// driver-exec, which has no other chance to write it out.
func TestRunSIGTERMFlushesHeldDeltasAndExitsSIGTERM(t *testing.T) {
	// exec so the sleep is the signalled child and holds no extra pipe copy.
	cfg := fixtureConfig(t, "pi", piMessageStart+piDeltaHel+piDeltaLo, "exec sleep 30\n")

	exited := make(chan int, 1)
	orig := exit
	exit = func(code int) { exited <- code }
	t.Cleanup(func() { exit = orig })

	done := make(chan struct{})
	go func() {
		defer close(done)
		var out bytes.Buffer
		_, _ = run(cfg, &out)
	}()

	// The message_start line passes the filter straight away, so it shows the
	// Driver is running and the handler is installed.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, _ := os.ReadFile(cfg.logPath); strings.Contains(string(b), piMessageStart) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the Driver's message_start in the Pass log")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-exited:
		if code != exitSIGTERM {
			t.Errorf("exit code = %d, want %d", code, exitSIGTERM)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the SIGTERM exit")
	}
	log, err := os.ReadFile(cfg.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), `"delta":"Hello"`) {
		t.Errorf("log = %q, want the coalesced partial message", log)
	}
	// The forwarded SIGTERM ends the Driver, so run returns instead of leaking it.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after the Driver was signalled")
	}
}
