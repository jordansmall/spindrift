package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeFakeDriverExec writes an executable shell script standing in for the
// real driver-exec binary: it appends its own argv to logPath (so a test can
// assert on call count and forwarded flags) and runs body.
func writeFakeDriverExec(t *testing.T, dir, logPath, body string) string {
	t.Helper()
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\n" + body
	path := filepath.Join(dir, "driver-exec")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunInvokesDriverExecOnceForwardingFlags verifies the orchestrator's S1
// tracer-bullet behaviour (issue #1996): exactly one driver-exec invocation,
// carrying every flag entrypoint.sh's own direct call passes today, and the
// scripted outcome line driver-exec would emit reaches the orchestrator's own
// stdout unchanged.
func TestRunInvokesDriverExecOnceForwardingFlags(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config{
		promptFile:   filepath.Join(dir, "prompt.txt"),
		agentsFile:   filepath.Join(dir, "agents.json"),
		sessionFile:  filepath.Join(dir, "session.txt"),
		driverBin:    "claude",
		driverFlags:  "--dangerously-skip-permissions",
		model:        "claude-sonnet-5",
		issue:        "7",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
	}

	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0", rc)
	}

	want := "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(calls, "\n"), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("driver-exec invocation count = %d, want 1 (log: %q)", len(lines), calls)
	}
	got := string(lines[0])
	for _, want := range []string{
		"--prompt-file " + cfg.promptFile,
		"--agents-file " + cfg.agentsFile,
		"--session-file " + cfg.sessionFile,
		"--driver-bin claude",
		"--driver-flags --dangerously-skip-permissions",
		"--model claude-sonnet-5",
		"--issue 7",
		"--log-path " + cfg.logPath,
		"--heartbeat-log " + cfg.heartbeatLog,
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("driver-exec argv = %q, want it to contain %q", got, want)
		}
	}
}

// TestRunPropagatesDriverExecExitCode verifies the orchestrator returns
// driver-exec's own exit code unchanged (issue #1996's "run still terminates
// on the unchanged SPINDRIFT_OUTCOME... status=ready|blocked" requirement
// depends on entrypoint.sh seeing the real Driver outcome, not a masked one).
func TestRunPropagatesDriverExecExitCode(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 3\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config{
		promptFile:   filepath.Join(dir, "prompt.txt"),
		driverBin:    "claude",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
	}

	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 3 {
		t.Errorf("exit code = %d, want 3", rc)
	}
}

// TestRunDevshellFlagsForwardedOnlyWhenSet verifies --devshell/--devshell-name
// reach driver-exec's argv when cfg.devshell is set, and are omitted
// entirely when it is not -- entrypoint.sh's own call only ever sets
// --devshell when phase_devshell_probe found a devShell (_use_dev_shell=1),
// omitting it entirely otherwise, so the orchestrator's forwarding must match.
func TestRunDevshellFlagsForwardedOnlyWhenSet(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config{
		promptFile:   filepath.Join(dir, "prompt.txt"),
		driverBin:    "claude",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	if bytes.Contains(calls, []byte("--devshell")) {
		t.Errorf("driver-exec argv = %q, want no --devshell flag when cfg.devshell is unset", calls)
	}

	cfg.devshell = true
	cfg.devshellName = "ci"
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	calls, err = os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	if !bytes.Contains(calls, []byte("--devshell --devshell-name ci")) {
		t.Errorf("driver-exec argv = %q, want it to contain %q", calls, "--devshell --devshell-name ci")
	}
}
