package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeFakeDriverExec writes an executable shell script standing in for the
// real driver-exec binary: it appends its own argv to callLog (so a test can
// assert on call count and forwarded flags), exports the value of its own
// --log-path flag as $DRIVER_LOG_PATH (so body can write to the same file
// the real driver-exec would have -- and RenderTranscript scans -- instead
// of printing bare text to stdout, which the real stream-json Driver never
// does), and runs body.
func writeFakeDriverExec(t *testing.T, dir, callLog, body string) string {
	t.Helper()
	preamble := `log_path=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--log-path" ]; then
    log_path="$arg"
  fi
  prev="$arg"
done
export DRIVER_LOG_PATH="$log_path"
`
	script := "#!/bin/sh\necho \"$@\" >> " + callLog + "\n" + preamble + body
	path := filepath.Join(dir, "driver-exec")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeStreamJSONLine appends a stream-json event to $DRIVER_LOG_PATH
// carrying kind ("VERDICT: BLOCK", "VERDICT: APPROVE", or a full
// SPINDRIFT_OUTCOME line) the same way a real claude turn would surface it:
// a verdict as a subagent's tool_result content, an outcome line as the
// implementor's own final assistant text -- matching what
// scanPassLog/RenderTranscript actually parse (issue #1998 review).
func streamJSONVerdictLine(text string) string {
	return `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"` + text + `"}]}}` + "\n"
}

func streamJSONOutcomeLine(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
}

// blockThenApproveFakeDriverBody returns a writeFakeDriverExec body scripting
// a fake driver-exec to BLOCK its first pass and APPROVE-with-outcome every
// pass after, keyed off callLog's own line count (its own invocation tally)
// rather than an in-process counter, so the same script works whether it
// runs once or is reused verbatim by more than one test.
func blockThenApproveFakeDriverBody(callLog string) string {
	return fmt.Sprintf(`n=$(wc -l < "%s")
if [ "$n" -eq 1 ]; then
  printf '%%s' '%s' >> "$DRIVER_LOG_PATH"
else
  printf '%%s%%s' '%s' '%s' >> "$DRIVER_LOG_PATH"
fi
exit 0
`, callLog,
		streamJSONVerdictLine("VERDICT: BLOCK"),
		streamJSONVerdictLine("VERDICT: APPROVE"),
		streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc"))
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
		effort:       "high",
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

	// stdout also now carries the orchestrator's own pass_start marker
	// (issue #2027), interleaved ahead of the pass's own output -- the
	// outcome line itself must still reach stdout byte-for-byte unchanged.
	want := "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
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
		"--effort high",
		"--issue 7",
		"--log-path " + cfg.logPath,
		"--heartbeat-log " + cfg.heartbeatLog,
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("driver-exec argv = %q, want it to contain %q", got, want)
		}
	}
}

// TestBuildDriverExecCmdForwardsDriverFlag verifies buildDriverExecCmd
// forwards cfg.driver as driver-exec's own --driver flag (issue #262 slice
// 4) -- driver-exec resolves its own argv shape and exit-code handling from
// this, so the orchestrator's own configured Driver name must reach every
// pass it invokes, not just --driver-bin/--driver-flags.
func TestBuildDriverExecCmdForwardsDriverFlag(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config{
		driver:    "opencode",
		driverBin: "opencode",
	}
	cmd, err := buildDriverExecCmd(cfg)
	if err != nil {
		t.Fatalf("buildDriverExecCmd: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--driver opencode") {
		t.Errorf("driver-exec argv = %q, want it to contain %q", got, "--driver opencode")
	}
}

// TestBuildDriverExecCmdForwardsEffortFlag verifies buildDriverExecCmd
// forwards cfg.effort as driver-exec's own --effort flag unconditionally
// (issue #2241 slice 3) -- driver-exec's own buildDriverArgs decides whether
// to actually emit the downstream flag based on non-empty, so the
// orchestrator forwards the raw value the same way it does for cfg.model,
// including when it's empty.
func TestBuildDriverExecCmdForwardsEffortFlag(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config{
		driverBin: "claude",
		effort:    "high",
	}
	cmd, err := buildDriverExecCmd(cfg)
	if err != nil {
		t.Fatalf("buildDriverExecCmd: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--effort high") {
		t.Errorf("driver-exec argv = %q, want it to contain %q", got, "--effort high")
	}

	cfg.effort = ""
	cmd, err = buildDriverExecCmd(cfg)
	if err != nil {
		t.Fatalf("buildDriverExecCmd: %v", err)
	}
	got = strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--effort") {
		t.Errorf("driver-exec argv = %q, want it to still contain the --effort flag (unconditionally forwarded, matching --model) when cfg.effort is empty", got)
	}
}

// TestBuildDriverExecCmdForwardsTopLevelRoleFlag verifies buildDriverExecCmd
// forwards cfg.topLevelRole as driver-exec's own --top-level-role flag when
// set (issue #2092), and omits the flag entirely -- not just an empty value
// -- when cfg.topLevelRole is "", keeping the legacy run() path's argv shape
// byte-identical to before this field existed.
func TestBuildDriverExecCmdForwardsTopLevelRoleFlag(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config{
		driverBin:    "claude",
		topLevelRole: "reviewer",
	}
	cmd, err := buildDriverExecCmd(cfg)
	if err != nil {
		t.Fatalf("buildDriverExecCmd: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--top-level-role reviewer") {
		t.Errorf("driver-exec argv = %q, want it to contain %q", got, "--top-level-role reviewer")
	}

	cfg.topLevelRole = ""
	cmd, err = buildDriverExecCmd(cfg)
	if err != nil {
		t.Fatalf("buildDriverExecCmd: %v", err)
	}
	got = strings.Join(cmd.Args, " ")
	if strings.Contains(got, "--top-level-role") {
		t.Errorf("driver-exec argv = %q, want no --top-level-role flag when cfg.topLevelRole is empty", got)
	}
}

// TestRunEmitsPassStartMarkerOnStdout verifies run prints a machine-readable
// "spindrift_op" pass_start marker to stdout before invoking driver-exec for
// each pass (issue #2027), so the heartbeat parser can surface the
// orchestrator's own operations live -- not just reconstruct them from the
// raw log afterward. The marker must reach stdout alongside the pass's own
// (here scripted) outcome line, not replace it.
func TestRunEmitsPassStartMarkerOnStdout(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf 'SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc\n'
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config{
		promptFile:   filepath.Join(dir, "prompt.txt"),
		driverBin:    "claude",
		issue:        "7",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"pass_start","pass":1}`) {
		t.Errorf("stdout = %q, want a pass_start marker for pass 1", stdout.String())
	}
	if !strings.Contains(stdout.String(), "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") {
		t.Errorf("stdout = %q, want the pass's own outcome line still present unchanged", stdout.String())
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

// TestRunSurfacesNoOutcomeMarkerAndPropagatesExitCodeWhenPassStalls covers
// the other #2036 fixture the issue asks for alongside a clean no-outcome
// exit: a pass that stalled mid-turn and was killed out from under it by
// something outside the orchestrator's own control (a signal-terminated
// process reports a 128+signal exit code by convention; 137 is SIGKILL),
// modeled here as driver-exec exiting 137 immediately rather than an actual
// hang -- the orchestrator has no per-pass timeout of its own and this test
// does not add one, so it only proves run reacts deterministically once the
// pass *has* ended, not that a genuinely wedged driver-exec is bounded.
// run must still return a deterministic (rc, nil) and still emit the same
// pass_no_outcome marker TestRunEmitsNoOutcomeMarkerOnStdout asserts for a
// clean exit, so a killed pass is exactly as visible in the heartbeat
// stream as one that simply forgot to print its outcome. The raw exit code
// is deliberately left to propagate unchanged here, same as
// TestRunPropagatesDriverExecExitCode above: a non-zero exit is the
// launcher's own signal to retry the whole run (agent/entrypoint.sh's
// comment on this exact point, main()), a decision this issue does not
// revisit -- only the missing visibility into *why* did.
func TestRunSurfacesNoOutcomeMarkerAndPropagatesExitCodeWhenPassStalls(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 137\n")
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
	if rc != 137 {
		t.Errorf("exit code = %d, want 137 propagated unchanged", rc)
	}
	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"pass_no_outcome","pass":1,"reason":"exit 137"}`) {
		t.Errorf("stdout = %q, want a pass_no_outcome marker even though the pass never wrote a log at all", stdout.String())
	}
}

// TestRunReadsAndWritesRunState verifies run reads whatever run-state a prior
// pass left at cfg.stateFile, carries its done/remaining slices and last
// verdict forward unchanged (issue #1997: on this tracer-bullet single pass
// there is no new slice/verdict information to add), and writes back the
// current pass's scout-brief path -- establishing the read/write seam a
// later multi-pass loop extends, without changing this pass's own behaviour.
func TestRunReadsAndWritesRunState(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateFile := filepath.Join(dir, "run-state.json")
	prior := RunState{
		DoneSlices:      []string{"scout"},
		RemainingSlices: []string{"implement"},
		LastVerdict:     "BLOCK",
	}
	if err := WriteRunState(stateFile, prior); err != nil {
		t.Fatalf("seed WriteRunState: %v", err)
	}

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:     promptFile,
		driverBin:      "claude",
		logPath:        filepath.Join(dir, "stream.log"),
		heartbeatLog:   filepath.Join(dir, "heartbeat.log"),
		stateFile:      stateFile,
		scoutBriefPath: filepath.Join(dir, "brief.md"),
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := ReadRunState(stateFile)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !reflect.DeepEqual(got.DoneSlices, prior.DoneSlices) {
		t.Errorf("DoneSlices = %v, want carried forward unchanged %v", got.DoneSlices, prior.DoneSlices)
	}
	if !reflect.DeepEqual(got.RemainingSlices, prior.RemainingSlices) {
		t.Errorf("RemainingSlices = %v, want carried forward unchanged %v", got.RemainingSlices, prior.RemainingSlices)
	}
	if got.LastVerdict != prior.LastVerdict {
		t.Errorf("LastVerdict = %q, want carried forward unchanged %q", got.LastVerdict, prior.LastVerdict)
	}
	if got.ScoutBriefPath != cfg.scoutBriefPath {
		t.Errorf("ScoutBriefPath = %q, want %q", got.ScoutBriefPath, cfg.scoutBriefPath)
	}
}

// TestRunEmitsRunStateErrorMarkerOnWriteFailure verifies run prints a
// "spindrift_op" run_state_error marker to stdout when WriteRunState fails
// (issue #2027) -- the failure already degrades gracefully (the pass's own
// exit code is untouched), but today it was only visible in stderr/the raw
// log, never in the live heartbeat stream.
func TestRunEmitsRunStateErrorMarkerOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 3\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config{
		promptFile:   filepath.Join(dir, "prompt.txt"),
		driverBin:    "claude",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		stateFile:    filepath.Join(dir, "missing-parent", "run-state.json"),
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"run_state_error","phase":"write"`) {
		t.Errorf("stdout = %q, want a run_state_error write marker", stdout.String())
	}
}

// TestRunEmitsRunStateErrorMarkerOnReadFailure verifies run prints a
// "spindrift_op" run_state_error marker to stdout when ReadRunState fails on
// a corrupt --state-file (issue #2027) -- the read failure already degrades
// to a cold start rather than blocking the pass, but was previously silent
// outside stderr/the raw log.
func TestRunEmitsRunStateErrorMarkerOnReadFailure(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateFile := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(stateFile, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:   filepath.Join(dir, "prompt.txt"),
		driverBin:    "claude",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		stateFile:    stateFile,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"run_state_error","phase":"read"`) {
		t.Errorf("stdout = %q, want a run_state_error read marker", stdout.String())
	}
}

// TestRunPreservesDriverExitCodeWhenRunStateWriteFails verifies a run-state
// persistence failure never masks the Driver's own exit code (issue #1997
// review): the handoff artifact is a side channel to the pass's real
// outcome, not a gate on it, so a --state-file whose parent directory
// doesn't exist -- ReadRunState treats it as "no prior state" like any other
// missing file, but WriteRunState genuinely fails writing it back -- must
// not turn a real driver exit code into a different, misleading one.
func TestRunPreservesDriverExitCodeWhenRunStateWriteFails(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 3\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config{
		promptFile:   filepath.Join(dir, "prompt.txt"),
		driverBin:    "claude",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		stateFile:    filepath.Join(dir, "missing-parent", "run-state.json"),
	}

	rc, err := run(cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 3 {
		t.Errorf("exit code = %d, want 3 (the Driver's own exit code)", rc)
	}
}

// TestRunProceedsOnCorruptRunState verifies a corrupt --state-file (a
// partial write from a killed prior pass, or hand-edited garbage) never
// blocks the Driver from running (issue #1997 review): the handoff artifact
// is a side channel to the pass's real outcome on the write side already, so
// the read side must honor the same "never gate the pass" contract instead
// of aborting before driver-exec ever runs.
func TestRunProceedsOnCorruptRunState(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateFile := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(stateFile, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:   filepath.Join(dir, "prompt.txt"),
		driverBin:    "claude",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		stateFile:    stateFile,
	}

	rc, err := run(cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0", rc)
	}
	if _, err := os.Stat(callLog); err != nil {
		t.Errorf("driver-exec was never invoked despite the corrupt state file: %v", err)
	}
}

// TestRunKeepsPriorScoutBriefPathWhenConfigOmitsIt verifies an empty
// cfg.scoutBriefPath never clobbers a prior pass's recorded scout-brief path
// with an empty string (issue #1997 review) -- only a caller that actually
// supplies a new path updates the field.
func TestRunKeepsPriorScoutBriefPathWhenConfigOmitsIt(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, "exit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateFile := filepath.Join(dir, "run-state.json")
	prior := RunState{ScoutBriefPath: "/tmp/brief.md"}
	if err := WriteRunState(stateFile, prior); err != nil {
		t.Fatal(err)
	}

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:   promptFile,
		driverBin:    "claude",
		logPath:      filepath.Join(dir, "stream.log"),
		heartbeatLog: filepath.Join(dir, "heartbeat.log"),
		stateFile:    stateFile,
		// scoutBriefPath intentionally left unset.
	}

	if _, err := run(cfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := ReadRunState(stateFile)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.ScoutBriefPath != "/tmp/brief.md" {
		t.Errorf("ScoutBriefPath = %q, want prior value %q preserved", got.ScoutBriefPath, "/tmp/brief.md")
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

// TestRunEmitsVerdictMarkerOnStdout verifies run prints a "spindrift_op"
// verdict marker to stdout for each pass whose scanned log carries a
// VERDICT line (issue #2027), reflecting the same verdict the loop itself
// reacted to (BLOCK then APPROVE here) -- not just the raw log's own
// buried marker, which never surfaces live.
func TestRunEmitsVerdictMarkerOnStdout(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, blockThenApproveFakeDriverBody(callLog))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		issue:           "7",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		stateFile:       filepath.Join(dir, "run-state.json"),
		maxReviewRounds: 3,
		maxSlices:       5,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, `"spindrift_op":{"op":"verdict","verdict":"BLOCK"}`) {
		t.Errorf("stdout = %q, want a BLOCK verdict marker", out)
	}
	if !strings.Contains(out, `"spindrift_op":{"op":"verdict","verdict":"APPROVE"}`) {
		t.Errorf("stdout = %q, want an APPROVE verdict marker", out)
	}
}

// TestRunEmitsDecisionMarkerOnStdout verifies run prints a "spindrift_op"
// decision marker to stdout at the end of every pass, carrying "continue"
// when the loop runs another pass and "stop" with a reason when it halts
// (issue #2027) -- here a BLOCK pass continues, then the terminal outcome on
// the APPROVE pass stops.
func TestRunEmitsDecisionMarkerOnStdout(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, blockThenApproveFakeDriverBody(callLog))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		issue:           "7",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		stateFile:       filepath.Join(dir, "run-state.json"),
		maxReviewRounds: 3,
		maxSlices:       5,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, `"spindrift_op":{"op":"decision","decision":"continue"}`) {
		t.Errorf("stdout = %q, want a continue decision marker after pass 1's BLOCK", out)
	}
	if !strings.Contains(out, `"decision":"stop","reason":"outcome reached"`) {
		t.Errorf("stdout = %q, want a stop decision marker with reason after pass 2's terminal outcome", out)
	}
}

// TestRunEmitsNoVerdictStopReason verifies the decision marker's reason
// distinguishes "no verdict at all" (the S1 single-pass shape, review
// finding on issue #2027) from "verdict was APPROVE/BLOCK-but-not-BLOCK" --
// a bare pass with neither a VERDICT line nor a terminal outcome stops the
// loop the same way verdict != "BLOCK" always has, but the surfaced reason
// must not claim a verdict existed when none did.
func TestRunEmitsNoVerdictStopReason(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf '%s' '`+streamJSONOutcomeLine("Just narration, no verdict or outcome.")+`' > "$DRIVER_LOG_PATH"
exit 0
`)
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

	if !strings.Contains(stdout.String(), `"decision":"stop","reason":"no verdict"`) {
		t.Errorf("stdout = %q, want the no-verdict stop reason", stdout.String())
	}
	if strings.Contains(stdout.String(), "verdict not BLOCK") {
		t.Errorf("stdout = %q, want no misleading 'verdict not BLOCK' reason when no verdict was ever seen", stdout.String())
	}
}

// TestRunEmitsNoOutcomeMarkerOnStdout verifies run prints a distinct
// "pass_no_outcome" spindrift_op marker whenever a pass's own log carries no
// terminal SPINDRIFT_OUTCOME line (issue #2036) -- a mid-turn cutoff or park
// must be individually visible in the heartbeat stream for that exact pass,
// not just inferable from the final decision's reason once the whole loop
// stops.
func TestRunEmitsNoOutcomeMarkerOnStdout(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf '%s' '`+streamJSONOutcomeLine("Just narration, no verdict or outcome.")+`' > "$DRIVER_LOG_PATH"
exit 0
`)
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

	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"pass_no_outcome","pass":1`) {
		t.Errorf("stdout = %q, want a pass_no_outcome marker for pass 1", stdout.String())
	}
}

// TestRunEmitsCapReachedStopReasonOnStdout verifies the decision marker's
// reason distinguishes which numeric cap stopped the loop (issue #2027):
// maxReviewRounds here, so a never-converging BLOCK reviewer's final marker
// names that cap rather than a generic "stop".
func TestRunEmitsCapReachedStopReasonOnStdout(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf '%s' '`+streamJSONVerdictLine("VERDICT: BLOCK")+`' > "$DRIVER_LOG_PATH"
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		maxReviewRounds: 2,
		maxSlices:       0,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(stdout.String(), `"decision":"stop","reason":"max review rounds reached"`) {
		t.Errorf("stdout = %q, want the cap-reached stop reason", stdout.String())
	}
}

// reviewPassFakeDriverBody returns a writeFakeDriverExec body scripting a
// fake driver-exec for the #2037 review-pass loop: implement/fix passes (odd
// calls) never emit a verdict or outcome of their own -- self-review is
// stripped from their prompt under the orchestrator, so this fixture mirrors
// that -- while review passes (even calls) BLOCK on the first review and
// APPROVE on the second; the pass after that APPROVE (call 5, a "land" pass
// seeded with the APPROVE verdict) emits the run's only terminal outcome.
func reviewPassFakeDriverBody(callLog string) string {
	return fmt.Sprintf(`: > "$DRIVER_LOG_PATH"
n=$(wc -l < "%s")
case "$n" in
  2) printf '%%s' '%s' | tee -a "$DRIVER_LOG_PATH" ;;
  4) printf '%%s' '%s' | tee -a "$DRIVER_LOG_PATH" ;;
  5) printf '%%s' '%s' | tee -a "$DRIVER_LOG_PATH" ;;
esac
exit 0
`, callLog,
		streamJSONOutcomeLine("VERDICT: BLOCK\\n\\n## Blocking\\n- run.go:1 -- bug\\n\\n## Non-blocking\\n- none"),
		streamJSONOutcomeLine("VERDICT: APPROVE\\n\\n## Blocking\\n- none\\n\\n## Non-blocking\\n- none"),
		streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc"))
}

// TestRunWithReviewPassSequenceOnBlockThenApprove verifies the #2037
// implement -> review -> (BLOCK) fix -> review -> (APPROVE) land loop end to
// end against a fake driver-exec: 5 invocations (implement, review-BLOCK,
// fix, review-APPROVE, land), each review pass a distinct fresh-session
// invocation against cfg.reviewPromptFile (never the implementor's own
// promptFile), the fix pass seeded with the review's own findings, and the
// run's own terminal SPINDRIFT_OUTCOME reached only once a review pass has
// APPROVEd.
func TestRunWithReviewPassSequenceOnBlockThenApprove(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, reviewPassFakeDriverBody(callLog))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(sessionFile, []byte("--session-id fake-id"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(dir, "run-state.json")

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		sessionFile:      sessionFile,
		driverBin:        "claude",
		issue:            "7",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		stateFile:        stateFile,
		maxReviewRounds:  3,
		maxSlices:        10,
	}

	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0", rc)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("driver-exec invocation count = %d, want 5 (log: %q)", len(lines), calls)
	}

	if !strings.Contains(lines[0], "--session-file "+sessionFile) {
		t.Errorf("pass 1 (implement) argv = %q, want the pinned --session-file %q", lines[0], sessionFile)
	}
	if got := flagValue(lines[0], "--prompt-file"); got != promptFile {
		t.Errorf("pass 1 --prompt-file = %q, want the original %q (no prior state to seed from)", got, promptFile)
	}

	if got := flagValue(lines[1], "--prompt-file"); got != reviewPromptFile {
		t.Errorf("pass 2 (review) --prompt-file = %q, want cfg.reviewPromptFile %q unseeded", got, reviewPromptFile)
	}
	if !strings.Contains(lines[1], "--session-file  --driver-bin") {
		t.Errorf("pass 2 (review) argv = %q, want an empty --session-file (always fresh)", lines[1])
	}

	fixPromptFile := flagValue(lines[2], "--prompt-file")
	if fixPromptFile == "" || fixPromptFile == promptFile || fixPromptFile == reviewPromptFile {
		t.Fatalf("pass 3 (fix) --prompt-file = %q, want a fresh seeded file", fixPromptFile)
	}
	if !strings.Contains(lines[2], "--session-file  --driver-bin") {
		t.Errorf("pass 3 (fix) argv = %q, want an empty --session-file (fresh session)", lines[2])
	}

	if got := flagValue(lines[3], "--prompt-file"); got != reviewPromptFile {
		t.Errorf("pass 4 (review) --prompt-file = %q, want cfg.reviewPromptFile %q unseeded", got, reviewPromptFile)
	}

	landPromptFile := flagValue(lines[4], "--prompt-file")
	if landPromptFile == "" || landPromptFile == promptFile || landPromptFile == reviewPromptFile {
		t.Fatalf("pass 5 (land) --prompt-file = %q, want a fresh seeded file", landPromptFile)
	}
	landSeeded, err := os.ReadFile(landPromptFile)
	if err != nil {
		t.Fatalf("read seeded land prompt: %v", err)
	}
	if !strings.Contains(string(landSeeded), "Last reviewer verdict: APPROVE") {
		t.Errorf("pass 5 (land) seeded prompt = %q, want it to carry the APPROVE verdict", landSeeded)
	}

	if !strings.Contains(stdout.String(), "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") {
		t.Errorf("stdout = %q, want the final pass's own outcome line present unchanged", stdout.String())
	}

	for _, want := range []string{
		`"spindrift_op":{"op":"pass_start","pass":1,"role":"implement"}`,
		`"spindrift_op":{"op":"pass_start","pass":2,"role":"review"}`,
		`"spindrift_op":{"op":"pass_start","pass":3,"role":"fix"}`,
		`"spindrift_op":{"op":"pass_start","pass":4,"role":"review"}`,
		`"spindrift_op":{"op":"pass_start","pass":5,"role":"fix"}`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
		}
	}

	got, err := ReadRunState(stateFile)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.LastVerdict != "APPROVE" {
		t.Errorf("LastVerdict = %q, want %q", got.LastVerdict, "APPROVE")
	}
}

// TestRunWithReviewPassSendsTopLevelRoleReviewerForReviewPassAndImplementorForImplementFixPasses
// verifies runWithReviewPass forwards the correct --top-level-role to
// driver-exec on every pass (issue #2092): "reviewer" for the code-owned
// review pass, "implementor" for every implement/fix/land pass -- reusing
// the same implement -> review -> fix -> review -> land fixture as
// TestRunWithReviewPassSequenceOnBlockThenApprove.
func TestRunWithReviewPassSendsTopLevelRoleReviewerForReviewPassAndImplementorForImplementFixPasses(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, reviewPassFakeDriverBody(callLog))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(sessionFile, []byte("--session-id fake-id"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(dir, "run-state.json")

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		sessionFile:      sessionFile,
		driverBin:        "claude",
		issue:            "7",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		stateFile:        stateFile,
		maxReviewRounds:  3,
		maxSlices:        10,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("driver-exec invocation count = %d, want 5 (log: %q)", len(lines), calls)
	}

	wantRoles := []string{"implementor", "reviewer", "implementor", "reviewer", "implementor"}
	for i, wantRole := range wantRoles {
		if got := flagValue(lines[i], "--top-level-role"); got != wantRole {
			t.Errorf("pass %d --top-level-role = %q, want %q (argv: %q)", i+1, got, wantRole, lines[i])
		}
	}
}

// noOutcomeAfterApproveFakeDriverBody returns a writeFakeDriverExec body
// scripting a fake driver-exec for issue #2069: the implement pass (call 1)
// and the land pass (call 3, seeded with the review's own APPROVE verdict)
// each emit nothing of their own -- no verdict, no terminal outcome -- while
// the review pass (call 2) APPROVEs outright on its first round. This is the
// "land pass cut off before its own terminal SPINDRIFT_OUTCOME" shape the
// orchestrator must stop on rather than re-entering the review->land cycle.
func noOutcomeAfterApproveFakeDriverBody(callLog string) string {
	return fmt.Sprintf(`: > "$DRIVER_LOG_PATH"
n=$(wc -l < "%s")
case "$n" in
  2) printf '%%s' '%s' | tee -a "$DRIVER_LOG_PATH" ;;
esac
exit 0
`, callLog,
		streamJSONOutcomeLine("VERDICT: APPROVE\\n\\n## Blocking\\n- none\\n\\n## Non-blocking\\n- none"))
}

// TestRunWithReviewPassStopsWhenLandPassProducesNoOutcomeAfterApprove
// verifies issue #2069: once a review pass APPROVEs, the land pass the
// orchestrator runs next (seeded with that APPROVE verdict) runs exactly
// once. If that land pass is cut off before printing its own terminal
// SPINDRIFT_OUTCOME, the loop must stop with a plain "decision" stop op
// instead of re-entering the review->land cycle (which would re-invoke the Filer /
// FILE ISSUES step on every extra lap, bounded only by the coarse
// maxSlices). Exactly 3 driver-exec invocations: implement, review-APPROVE,
// land-with-no-outcome -- and critically no 4th pass_start (no re-loop into
// another review pass).
func TestRunWithReviewPassStopsWhenLandPassProducesNoOutcomeAfterApprove(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, noOutcomeAfterApproveFakeDriverBody(callLog))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(sessionFile, []byte("--session-id fake-id"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(dir, "run-state.json")

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		sessionFile:      sessionFile,
		driverBin:        "claude",
		issue:            "7",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		stateFile:        stateFile,
		maxReviewRounds:  3,
		maxSlices:        10,
	}

	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0", rc)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("driver-exec invocation count = %d, want 3 (log: %q)", len(lines), calls)
	}

	if !strings.Contains(stdout.String(), `"decision":"stop","reason":"land pass reached no terminal outcome after APPROVE"`) {
		t.Errorf("stdout = %q, want the land-pass-no-outcome stop reason", stdout.String())
	}

	if !strings.Contains(stdout.String(), `"spindrift_op":{"op":"pass_start","pass":3,"role":"fix"}`) {
		t.Errorf("stdout = %q, want the land pass's own pass_start", stdout.String())
	}
	if strings.Contains(stdout.String(), `"spindrift_op":{"op":"pass_start","pass":4`) {
		t.Errorf("stdout = %q, want no pass 4 (no re-loop into another review pass)", stdout.String())
	}

	got, err := ReadRunState(stateFile)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.LastVerdict != "APPROVE" {
		t.Errorf("LastVerdict = %q, want %q", got.LastVerdict, "APPROVE")
	}
}

// TestRunWithReviewPassSeedsFixPassWithReviewFindings verifies AC (issue
// #2037): the fix pass the orchestrator runs after a review pass's BLOCK is
// seeded not just with the bare verdict word (that narrower claim is
// TestRunWithReviewPassSequenceOnBlockThenApprove above) but with the review
// pass's own findings text. Stops at 3 calls (implement, review-BLOCK, fix)
// by having the fix pass itself emit the terminal outcome, so its own seeded
// --prompt-file -- the run's last pass -- is the one run deliberately leaves
// on disk to inspect afterward.
func TestRunWithReviewPassSeedsFixPassWithReviewFindings(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	body := fmt.Sprintf(`: > "$DRIVER_LOG_PATH"
n=$(wc -l < "%s")
case "$n" in
  2) printf '%%s' '%s' >> "$DRIVER_LOG_PATH" ;;
  3) printf '%%s' '%s' >> "$DRIVER_LOG_PATH" ;;
esac
exit 0
`, callLog,
		streamJSONOutcomeLine("VERDICT: BLOCK\\n\\n## Blocking\\n- run.go:1 -- bug"),
		streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc"))
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		driverBin:        "claude",
		issue:            "7",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		stateFile:        filepath.Join(dir, "run-state.json"),
		maxReviewRounds:  3,
		maxSlices:        10,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("driver-exec invocation count = %d, want 3 (log: %q)", len(lines), calls)
	}

	fixPromptFile := flagValue(lines[2], "--prompt-file")
	seeded, err := os.ReadFile(fixPromptFile)
	if err != nil {
		t.Fatalf("read seeded fix prompt: %v", err)
	}
	for _, want := range []string{"ORIGINAL PROMPT TEXT", "Last reviewer verdict: BLOCK", "## Blocking", "run.go:1 -- bug"} {
		if !strings.Contains(string(seeded), want) {
			t.Errorf("fix pass seeded prompt = %q, want it to contain %q", seeded, want)
		}
	}
}

// TestRunWithReviewPassTerminatesOnMaxReviewRoundsCap verifies maxReviewRounds
// (issue #2037) bounds the review-pass loop the same way it already bounds
// the legacy single loop: a review pass that BLOCKs every time stops once
// that many additional BLOCK-triggered fix passes have run, each still
// paired with its own review pass.
func TestRunWithReviewPassTerminatesOnMaxReviewRoundsCap(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	body := `: > "$DRIVER_LOG_PATH"
n=$(wc -l < "` + callLog + `")
if [ $((n % 2)) -eq 0 ]; then
  printf '%s' '` + streamJSONOutcomeLine("VERDICT: BLOCK") + `' | tee -a "$DRIVER_LOG_PATH"
fi
exit 0
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("review prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		driverBin:        "claude",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		maxReviewRounds:  2,
		maxSlices:        0,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	// implement1, review1(BLOCK), fix2, review2(BLOCK), fix3, review3(BLOCK, cap hit)
	if len(lines) != 6 {
		t.Fatalf("driver-exec invocation count = %d, want 6 (log: %q)", len(lines), calls)
	}
	if !strings.Contains(stdout.String(), `"decision":"stop","reason":"max review rounds reached"`) {
		t.Errorf("stdout = %q, want the cap-reached stop reason", stdout.String())
	}
}

// TestRunWithReviewPassTerminatesOnMaxSlicesCap verifies maxSlices (issue
// #2037) is a coarser backstop on the review-pass loop too, counted across
// both implement/fix and review invocations -- not reset or doubled by the
// new pass kind.
func TestRunWithReviewPassTerminatesOnMaxSlicesCap(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	body := `: > "$DRIVER_LOG_PATH"
n=$(wc -l < "` + callLog + `")
if [ $((n % 2)) -eq 0 ]; then
  printf '%s' '` + streamJSONOutcomeLine("VERDICT: BLOCK") + `' | tee -a "$DRIVER_LOG_PATH"
fi
exit 0
`
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("review prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		driverBin:        "claude",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
		maxReviewRounds:  0,
		maxSlices:        3,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("driver-exec invocation count = %d, want 3 (maxSlices cap, log: %q)", len(lines), calls)
	}
	if !strings.Contains(stdout.String(), `"decision":"stop","reason":"max slices reached"`) {
		t.Errorf("stdout = %q, want the cap-reached stop reason", stdout.String())
	}
}

// TestRunWithReviewPassStopsWithNoVerdictStopReason verifies a review pass
// that produces no VERDICT line at all (a malfunctioning or truncated review
// session) stops the loop immediately with a "no verdict" reason, the same
// fail-stop the legacy loop gives an implementor pass with no verdict --
// rather than looping forever or silently treating it as an APPROVE.
func TestRunWithReviewPassStopsWithNoVerdictStopReason(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `: > "$DRIVER_LOG_PATH"
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	if err := os.WriteFile(reviewPromptFile, []byte("review prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		driverBin:        "claude",
		logPath:          filepath.Join(dir, "stream.log"),
		heartbeatLog:     filepath.Join(dir, "heartbeat.log"),
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("driver-exec invocation count = %d, want 2 (implement, review), log: %q", len(lines), calls)
	}
	if !strings.Contains(stdout.String(), `"decision":"stop","reason":"no verdict"`) {
		t.Errorf("stdout = %q, want the no-verdict stop reason", stdout.String())
	}
}

// TestRunLoopsOnBlockThenApproveWithFreshSessionPerPass verifies the S3
// multi-pass loop (issue #1998): a fake driver-exec that BLOCKs its first
// pass and APPROVEs (with a terminal outcome) its second drives the
// orchestrator to invoke driver-exec exactly twice, the second time with no
// session flags at all (a fresh Driver session -- no --resume -- rather than
// the first pass's pinned cfg.sessionFile), and to persist the final verdict
// to the run-state handoff artifact.
func TestRunLoopsOnBlockThenApproveWithFreshSessionPerPass(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, blockThenApproveFakeDriverBody(callLog))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	stateFile := filepath.Join(dir, "run-state.json")
	cfg := config{
		promptFile:      promptFile,
		sessionFile:     filepath.Join(dir, "session.txt"),
		driverBin:       "claude",
		issue:           "7",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		stateFile:       stateFile,
		maxReviewRounds: 3,
		maxSlices:       5,
	}
	if err := os.WriteFile(cfg.sessionFile, []byte("--session-id fake-id"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0", rc)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("driver-exec invocation count = %d, want 2 (log: %q)", len(lines), calls)
	}
	if !strings.Contains(lines[0], "--session-file "+cfg.sessionFile) {
		t.Errorf("pass 1 argv = %q, want it to carry the pinned --session-file %q", lines[0], cfg.sessionFile)
	}
	if !strings.Contains(lines[1], "--session-file  --driver-bin") {
		t.Errorf("pass 2 argv = %q, want an empty --session-file (fresh session, no --resume)", lines[1])
	}
	if strings.Contains(lines[1], cfg.sessionFile) {
		t.Errorf("pass 2 argv = %q, want it NOT to carry pass 1's pinned session file", lines[1])
	}

	got, err := ReadRunState(stateFile)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.LastVerdict != "APPROVE" {
		t.Errorf("LastVerdict = %q, want %q", got.LastVerdict, "APPROVE")
	}
}

// flagValue returns the value following flag in a space-joined argv line
// logged by writeFakeDriverExec's fake driver-exec, or "" if flag is absent.
// Only safe for flags whose value is never itself empty (word-splitting
// collapses an empty value's surrounding double space, see the
// --session-file assertions above).
func flagValue(argvLine, flag string) string {
	fields := strings.Fields(argvLine)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// TestRunSeedsSubsequentPassPromptFromRunState verifies AC1 (issue #1998):
// every pass is "seeded from the run-state artifact", not just handed the
// same static prompt file every time. A fake driver BLOCKs its first pass
// (leaving a verdict in run-state.json) and APPROVEs its second; the second
// pass's own --prompt-file must be a fresh file combining the original
// prompt with that carried-forward state, not cfg.promptFile verbatim.
func TestRunSeedsSubsequentPassPromptFromRunState(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, blockThenApproveFakeDriverBody(callLog))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		issue:           "7",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		stateFile:       filepath.Join(dir, "run-state.json"),
		maxReviewRounds: 3,
		maxSlices:       5,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("driver-exec invocation count = %d, want 2 (log: %q)", len(lines), calls)
	}

	if got := flagValue(lines[0], "--prompt-file"); got != promptFile {
		t.Errorf("pass 1 --prompt-file = %q, want the original %q (no prior state to seed from)", got, promptFile)
	}

	pass2PromptFile := flagValue(lines[1], "--prompt-file")
	if pass2PromptFile == "" || pass2PromptFile == promptFile {
		t.Fatalf("pass 2 --prompt-file = %q, want a fresh seeded file distinct from %q", pass2PromptFile, promptFile)
	}
	seeded, err := os.ReadFile(pass2PromptFile)
	if err != nil {
		t.Fatalf("read seeded pass 2 prompt file: %v", err)
	}
	if !strings.Contains(string(seeded), "ORIGINAL PROMPT TEXT") {
		t.Errorf("pass 2 prompt = %q, want it to still carry the original prompt text", seeded)
	}
	if !strings.Contains(string(seeded), "BLOCK") {
		t.Errorf("pass 2 prompt = %q, want it to carry pass 1's BLOCK verdict from the run-state artifact", seeded)
	}
}

// TestSeedPromptFromStateIncludesReviewFindings verifies seedPromptFromState
// (issue #2037) renders state.ReviewFindings -- the code-owned review pass's
// own Blocking/Non-blocking findings text -- into the seeded prompt, not just
// the bare LastVerdict word (that narrower claim is #1998/#1999's own
// TestRunSeedsFixBriefWithDoneWorkAndVerdictAfterBlock below), so a fix pass
// knows exactly what to fix rather than only that something blocked.
func TestSeedPromptFromStateIncludesReviewFindings(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := RunState{
		LastVerdict:    "BLOCK",
		ReviewFindings: "VERDICT: BLOCK\n\n## Blocking\n- run.go:42 -- missing nil check",
	}

	seeded, err := seedPromptFromState(promptFile, state)
	if err != nil {
		t.Fatalf("seedPromptFromState: %v", err)
	}
	if seeded == promptFile {
		t.Fatalf("seedPromptFromState returned the original file unchanged, want a fresh seeded file")
	}
	got, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("read seeded prompt: %v", err)
	}
	if !strings.Contains(string(got), "## Blocking\n- run.go:42 -- missing nil check") {
		t.Errorf("seeded prompt = %q, want it to carry the reviewer's findings verbatim", got)
	}
}

// TestRunSeedsFixBriefWithDoneWorkAndVerdictAfterBlock verifies AC2 (issue
// #1999): after a scripted BLOCK, the next pass's own seeded prompt carries
// the scoped fix brief -- both what is already done (a DoneSlices list this
// fixture seeds into the run-state artifact up front, standing in for
// whatever an earlier pass in the same run would have left behind) and the
// verdict that triggered the fix pass -- not just the bare verdict word alone
// (that narrower claim is #1998's own TestRunSeedsSubsequentPassPromptFromRunState).
func TestRunSeedsFixBriefWithDoneWorkAndVerdictAfterBlock(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, blockThenApproveFakeDriverBody(callLog))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}

	stateFile := filepath.Join(dir, "run-state.json")
	if err := WriteRunState(stateFile, RunState{DoneSlices: []string{"scout", "implement seam A"}}); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		issue:           "7",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		stateFile:       stateFile,
		maxReviewRounds: 3,
		maxSlices:       5,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("driver-exec invocation count = %d, want 2 (log: %q)", len(lines), calls)
	}

	pass2PromptFile := flagValue(lines[1], "--prompt-file")
	if pass2PromptFile == "" || pass2PromptFile == promptFile {
		t.Fatalf("pass 2 --prompt-file = %q, want a fresh seeded file distinct from %q", pass2PromptFile, promptFile)
	}
	seeded, err := os.ReadFile(pass2PromptFile)
	if err != nil {
		t.Fatalf("read seeded pass 2 prompt file: %v", err)
	}
	for _, want := range []string{"Done slices: scout, implement seam A", "Last reviewer verdict: BLOCK"} {
		if !strings.Contains(string(seeded), want) {
			t.Errorf("pass 2 prompt = %q, want the scoped fix brief to carry %q", seeded, want)
		}
	}
}

// TestRunTerminatesOnMaxReviewRoundsCap verifies a reviewer that never
// converges (BLOCK on every pass) does not loop forever: maxReviewRounds
// (issue #1998) stops the loop deterministically once that many additional,
// BLOCK-triggered passes have run on top of the first.
func TestRunTerminatesOnMaxReviewRoundsCap(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf '%s' '`+streamJSONVerdictLine("VERDICT: BLOCK")+`' > "$DRIVER_LOG_PATH"
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		maxReviewRounds: 2,
		maxSlices:       0,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("driver-exec invocation count = %d, want 3 (the first pass plus maxReviewRounds=2 additional, log: %q)", len(lines), calls)
	}
}

// TestRunSurfacesNoOutcomePatternAcrossEveryPassUntilCapReached is the #2019
// attempt-1 regression fixture (issue #2036): a dogfood run where the
// orchestrator path was active, the reviewer never converged, and the run
// ended without ever printing a SPINDRIFT_OUTCOME line -- lost as
// agent-failed even though #2011/#2012 were meant to close exactly this
// class of failure. Every pass here BLOCKs without ever reaching a terminal
// outcome, the same shape a systemically parking Driver would produce.
// Asserts three things the #2019 shape needs, none of which
// TestRunTerminatesOnMaxReviewRoundsCap itself checks: (1) every single pass
// -- not just the run's final one -- gets its own "pass_no_outcome" marker,
// so an operator watching the heartbeat stream sees the no-outcome pattern
// recur pass over pass rather than only a generic cap-reached line at the
// very end; (2) the loop still stops deterministically once the cap is hit
// (never a silent mid-turn death); (3) the run's own exit code is 0 -- the
// same "driver exited cleanly, just never signaled" contract
// agent/entrypoint.sh's resume-nudge and backstop (#1607, #2012) already
// key off of -- so committed/staged work from this run remains reachable by
// that salvage path instead of being discarded by a propagated failure exit.
func TestRunSurfacesNoOutcomePatternAcrossEveryPassUntilCapReached(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf '%s' '`+streamJSONVerdictLine("VERDICT: BLOCK")+`' > "$DRIVER_LOG_PATH"
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		maxReviewRounds: 2,
		maxSlices:       0,
	}

	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("rc = %d, want 0 so entrypoint.sh's resume-nudge/backstop can still salvage committed work", rc)
	}

	out := stdout.String()
	for pass := 1; pass <= 3; pass++ {
		want := fmt.Sprintf(`"spindrift_op":{"op":"pass_no_outcome","pass":%d,"verdict":"BLOCK","reason":"exit 0"}`, pass)
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want a pass_no_outcome marker for pass %d", out, pass)
		}
	}
	if !strings.Contains(out, `"decision":"stop","reason":"max review rounds reached"`) {
		t.Errorf("stdout = %q, want the cap-reached stop reason", out)
	}
}

// TestRunTerminatesOnMaxSlicesCap verifies maxSlices (issue #1998) is an
// independent, coarser cap on total driver-exec invocations: even with
// maxReviewRounds disabled (0 == no cap), a never-converging reviewer still
// stops once maxSlices total passes have run.
func TestRunTerminatesOnMaxSlicesCap(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	writeFakeDriverExec(t, dir, callLog, `printf '%s' '`+streamJSONVerdictLine("VERDICT: BLOCK")+`' > "$DRIVER_LOG_PATH"
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		maxReviewRounds: 0,
		maxSlices:       3,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("driver-exec invocation count = %d, want 3 (maxSlices cap, log: %q)", len(lines), calls)
	}
}

// TestRunColdStartsAcrossMultiplePassesWhenStateFileMissing verifies a
// --state-file that has never been written (a fresh box, or one where the
// prior state was evicted) degrades to a cold start rather than an error,
// and that this holds across every pass of a multi-pass loop, not just the
// first (issue #1998).
func TestRunColdStartsAcrossMultiplePassesWhenStateFileMissing(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	body := fmt.Sprintf(`n=$(wc -l < %s)
if [ "$n" -lt 3 ]; then
  printf '%%s' '%s' > "$DRIVER_LOG_PATH"
else
  printf '%%s' '%s' > "$DRIVER_LOG_PATH"
fi
exit 0
`, callLog, streamJSONVerdictLine("VERDICT: BLOCK"), streamJSONVerdictLine("VERDICT: APPROVE"))
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		stateFile:       filepath.Join(dir, "never-written-until-now.json"),
		maxReviewRounds: 5,
		maxSlices:       5,
	}

	var stdout bytes.Buffer
	rc, err := run(cfg, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0", rc)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("driver-exec invocation count = %d, want 3 (log: %q)", len(lines), calls)
	}

	got, err := ReadRunState(cfg.stateFile)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.LastVerdict != "APPROVE" {
		t.Errorf("LastVerdict = %q, want %q", got.LastVerdict, "APPROVE")
	}
}

// TestScanPassLogDetectsOutcomeThroughStreamJSONAndMarkdownWrap verifies
// scanPassLog against a realistic claude stream-json log (issue #1998
// review): a bare-line scan of the raw JSONL would never see either marker,
// since both live inside JSON string fields. It also covers claude
// sometimes wrapping its own final-message line in backticks (issue #1611)
// -- the same wrapping claude.nix's bash-side extraction already strips.
func TestScanPassLogDetectsOutcomeThroughStreamJSONAndMarkdownWrap(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONVerdictLine("VERDICT: BLOCK") +
		streamJSONOutcomeLine("`SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc`")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	verdict, hasOutcome := scanPassLog(logPath, "claude")
	if verdict != "BLOCK" {
		t.Errorf("verdict = %q, want %q", verdict, "BLOCK")
	}
	if !hasOutcome {
		t.Error("hasOutcome = false, want true (backtick-wrapped outcome line should still be detected)")
	}
}

// TestScanPassLogFindsNothingInPlainStreamJSONNarration verifies a pass
// with ordinary narration and no verdict/outcome marker scans as empty
// rather than a false positive.
func TestScanPassLogFindsNothingInPlainStreamJSONNarration(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine("Investigating the failing test.")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	verdict, hasOutcome := scanPassLog(logPath, "claude")
	if verdict != "" {
		t.Errorf("verdict = %q, want empty", verdict)
	}
	if hasOutcome {
		t.Error("hasOutcome = true, want false")
	}
}

// TestScanReviewLogExtractsVerdictAndFindings verifies scanReviewLog (issue
// #2037) reads a standalone review pass's own rendered transcript -- unlike
// scanPassLog's implement/fix-pass callers, which only ever see a verdict
// collapsed into a subagent's tool_result, a code-owned review pass's verdict
// is its own top-level final assistant message, so RenderTranscript preserves
// its internal newlines verbatim (transcript_render.go's TrimSpace-only
// handling of a "text" block, as opposed to a tool_result's own
// newline-collapsing). scanReviewLog must return both the verdict word and
// the message's own Blocking/Non-blocking findings text, not just the verdict.
func TestScanReviewLogExtractsVerdictAndFindings(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	verdictMessage := "VERDICT: BLOCK\\n\\n## Blocking\\n- run.go:42 -- missing nil check\\n\\n## Non-blocking\\n- none"
	content := streamJSONOutcomeLine(verdictMessage)
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	verdict, findings := scanReviewLog(logPath, "claude")
	if verdict != "BLOCK" {
		t.Errorf("verdict = %q, want %q", verdict, "BLOCK")
	}
	for _, want := range []string{"VERDICT: BLOCK", "## Blocking", "run.go:42 -- missing nil check", "## Non-blocking"} {
		if !strings.Contains(findings, want) {
			t.Errorf("findings = %q, want it to contain %q", findings, want)
		}
	}
}

// TestScanReviewLogStopsFindingsAtTheNextRenderedEvent verifies scanReviewLog
// (issue #2037 review) bounds findings to the verdict message itself: a
// review pass that keeps talking after its final verdict message (a
// misbehaving turn, or a rendering quirk) gets a second, separate rendered
// line of its own -- distinguishable from the verdict message's own embedded
// newlines by carrying a fresh "[role] " prefix -- and that trailing content
// must not leak into the seeded fix-pass brief.
func TestScanReviewLogStopsFindingsAtTheNextRenderedEvent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine("VERDICT: BLOCK\\n\\n## Blocking\\n- run.go:42 -- missing nil check") +
		streamJSONOutcomeLine("unrelated trailing narration")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, findings := scanReviewLog(logPath, "claude")
	if strings.Contains(findings, "unrelated trailing narration") {
		t.Errorf("findings = %q, want it to stop before the next rendered event", findings)
	}
	if !strings.Contains(findings, "## Blocking") {
		t.Errorf("findings = %q, want the verdict message's own findings still present", findings)
	}
}

// TestScanReviewLogFindsNothingInPlainNarration verifies a review pass log
// with no VERDICT marker at all scans as empty rather than a false positive
// -- the orchestrator's own "no verdict" stop reason relies on this.
func TestScanReviewLogFindsNothingInPlainNarration(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine("Investigating the diff.")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	verdict, findings := scanReviewLog(logPath, "claude")
	if verdict != "" {
		t.Errorf("verdict = %q, want empty", verdict)
	}
	if findings != "" {
		t.Errorf("findings = %q, want empty", findings)
	}
}

// TestFindVerdictPrefersBLOCKOnTie verifies findVerdict resolves a line
// carrying both marker words to BLOCK -- the fail-unsafe direction (another
// fix pass, never a premature stop) -- rather than whichever happens to
// come first in the switch (issue #1998 review).
func TestFindVerdictPrefersBLOCKOnTie(t *testing.T) {
	v, ok := findVerdict("VERDICT: APPROVE mentions VERDICT: BLOCK too")
	if !ok || v != "BLOCK" {
		t.Errorf("findVerdict = (%q, %v), want (\"BLOCK\", true)", v, ok)
	}
}

// TestRunRemovesPriorPassSeededPromptFileButKeepsTheLast verifies a
// multi-pass loop does not accumulate one seeded-prompt temp file per pass
// (issue #1998 review): once a later pass has its own seeded file, the
// previous pass's is removed, while the last pass's is deliberately left on
// disk (the box's own filesystem is destroyed with the container anyway).
func TestRunRemovesPriorPassSeededPromptFileButKeepsTheLast(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	body := fmt.Sprintf(`n=$(wc -l < %s)
if [ "$n" -lt 3 ]; then
  printf '%%s' '%s' > "$DRIVER_LOG_PATH"
else
  printf '%%s' '%s' > "$DRIVER_LOG_PATH"
fi
exit 0
`, callLog, streamJSONVerdictLine("VERDICT: BLOCK"), streamJSONVerdictLine("VERDICT: APPROVE"))
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(dir, "run-state.json")
	if err := WriteRunState(stateFile, RunState{LastVerdict: "BLOCK"}); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		promptFile:      promptFile,
		driverBin:       "claude",
		logPath:         filepath.Join(dir, "stream.log"),
		heartbeatLog:    filepath.Join(dir, "heartbeat.log"),
		stateFile:       stateFile,
		maxReviewRounds: 5,
		maxSlices:       5,
	}

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("driver-exec invocation count = %d, want 3 (log: %q)", len(lines), calls)
	}

	pass1PromptFile := flagValue(lines[0], "--prompt-file")
	pass2PromptFile := flagValue(lines[1], "--prompt-file")
	pass3PromptFile := flagValue(lines[2], "--prompt-file")

	for _, p := range []string{pass1PromptFile, pass2PromptFile, pass3PromptFile} {
		if p == "" || p == promptFile {
			t.Fatalf("pass prompt file = %q, want a distinct seeded file (every pass here starts with non-empty state)", p)
		}
	}
	if _, err := os.Stat(pass1PromptFile); !os.IsNotExist(err) {
		t.Errorf("pass 1's seeded prompt file still exists after pass 2 ran: %v", err)
	}
	if _, err := os.Stat(pass2PromptFile); !os.IsNotExist(err) {
		t.Errorf("pass 2's seeded prompt file still exists after pass 3 ran: %v", err)
	}
	if _, err := os.Stat(pass3PromptFile); err != nil {
		t.Errorf("pass 3's (last) seeded prompt file was removed, want it left on disk: %v", err)
	}
}
