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
		"--issue 7",
		"--log-path " + cfg.logPath,
		"--heartbeat-log " + cfg.heartbeatLog,
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("driver-exec argv = %q, want it to contain %q", got, want)
		}
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

	verdict, hasOutcome := scanPassLog(logPath)
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

	verdict, hasOutcome := scanPassLog(logPath)
	if verdict != "" {
		t.Errorf("verdict = %q, want empty", verdict)
	}
	if hasOutcome {
		t.Error("hasOutcome = true, want false")
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
