package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/outcome"
)

// config is the data one implementor pass needs to hand off to driver-exec
// (issue #1996), forwarded verbatim as that pass's own flags; run's
// multi-pass loop (issue #1998) reuses the same config across every pass it
// invokes, only ever overriding sessionFile per pass.
type config struct {
	promptFile   string
	agentsFile   string
	sessionFile  string
	driverBin    string
	driverFlags  string
	model        string
	devshell     bool
	devshellName string
	issue        string
	logPath      string
	heartbeatLog string
	// stateFile is the path to the run-state handoff artifact (issue #1997),
	// outside the repo like heartbeatLog. Empty disables read/write of it
	// entirely, for callers with no run-state to carry.
	stateFile string
	// scoutBriefPath is this pass's scout-brief path (conventionally
	// /tmp/brief.md), recorded into the run-state artifact rather than
	// inlined there.
	scoutBriefPath string
	// maxReviewRounds caps how many additional fresh-session passes a BLOCK
	// verdict may trigger (issue #1998): once this many extra passes have
	// been started in response to a BLOCK, the loop stops even if the
	// reviewer keeps blocking. The first pass itself never counts against
	// this cap -- only passes it (or a later one) triggers do. Zero means
	// no cap.
	maxReviewRounds int
	// maxSlices caps the total number of driver-exec invocations this run
	// makes, across every pass regardless of verdict (issue #1998) -- the
	// coarser backstop on top of maxReviewRounds. Zero means no cap.
	maxSlices int
}

// run loops driver-exec for as many passes as the implementor's own
// BLOCK/APPROVE review verdicts and cfg's numeric caps call for (issue
// #1998), each pass forwarding cfg as its own flags (ADR 0009 -- no
// CLI-specific assumptions beyond driver-exec's own surface) and streaming
// its raw stdout to stdout unchanged, across every pass. It reads the
// run-state handoff artifact at cfg.stateFile before the first pass and
// writes it back after every pass (issue #1997): a missing or corrupt state
// file degrades to a cold start (a zero RunState) rather than an error, so a
// crashed or evicted prior pass never blocks this one from running. The
// handoff artifact is a side channel to each pass's real outcome, not a gate
// on it: neither a read failure nor a write failure ever substitutes for, or
// masks, the Driver's own exit code -- both are reported to stderr and the
// pass proceeds as if no handoff existed.
//
// The first pass carries cfg.sessionFile verbatim, exactly as the S1
// tracer-bullet did (entrypoint.sh already renders it as an "initial" pin,
// never a --resume, for this call). Every pass after the first is a fresh
// Driver session with no session flags at all -- no --resume, ever -- since
// continuity across passes is carried by the run-state artifact, not a
// resumed transcript. The loop stops as soon as a pass's own output carries
// a terminal SPINDRIFT_OUTCOME line, or its verdict is anything but BLOCK
// (APPROVE, or no verdict at all -- the S1 single-pass shape), or either
// numeric cap is reached.
func run(cfg config, stdout io.Writer) (int, error) {
	state, err := ReadRunState(cfg.stateFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: read run state:", err)
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: "read", Error: err.Error()}))
		state = RunState{}
	}

	rc := 0
	reviewRounds := 0
	prevSeededPromptFile := ""
	for pass := 1; ; pass++ {
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_start", Pass: pass}))

		passCfg := cfg
		if pass > 1 {
			passCfg.sessionFile = ""
		}

		seededPromptFile, err := seedPromptFromState(cfg.promptFile, state)
		if err != nil {
			return 0, err
		}
		// The previous pass's own seeded file (if any) is no longer needed
		// once this pass has its own -- removed now rather than left for
		// the whole run, so a long-running loop doesn't accumulate one temp
		// file per pass. The very last pass's file is deliberately left on
		// disk: this is a short-lived, per-box tmp file, and the box's own
		// filesystem is destroyed with the container regardless. Checked
		// against cfg.promptFile itself, not just any temp file: a
		// zero-state pass's "seeded" file is cfg.promptFile itself unchanged
		// (seedPromptFromState's own no-op case).
		if prevSeededPromptFile != "" && prevSeededPromptFile != cfg.promptFile {
			os.Remove(prevSeededPromptFile)
		}
		prevSeededPromptFile = seededPromptFile
		passCfg.promptFile = seededPromptFile

		cmd, err := buildDriverExecCmd(passCfg)
		if err != nil {
			return 0, err
		}
		cmd.Stdout = stdout
		cmd.Stderr = os.Stderr
		runErr := cmd.Run()

		if exitErr, ok := runErr.(*exec.ExitError); ok {
			rc = exitErr.ExitCode()
		} else if runErr != nil {
			return 0, runErr
		} else {
			rc = 0
		}

		// An empty cfg.scoutBriefPath means the caller didn't supply one
		// this pass, not that the prior path is now unknown, so it leaves
		// the carried-forward value alone rather than clobbering it with "".
		if cfg.scoutBriefPath != "" {
			state.ScoutBriefPath = cfg.scoutBriefPath
		}
		// driver-exec (re-)creates passCfg.logPath fresh for this one pass
		// (issue #626's run.go: os.Create truncates), so by the time it
		// returns the file holds exactly this pass's own raw stream -- the
		// same file --log-path already pointed driver-exec at, read back
		// here instead of tapped from cmd.Stdout directly.
		verdict, hasOutcome := scanPassLog(passCfg.logPath)
		if verdict != "" {
			state.LastVerdict = verdict
			fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "verdict", Verdict: verdict}))
		}
		// A pass that never printed a terminal SPINDRIFT_OUTCOME line is
		// recorded on its own, distinct marker (issue #2036) -- whatever the
		// loop's decision below turns out to be (continue into a fresh pass,
		// or stop), so a mid-turn cutoff/park is visible for the exact pass
		// it happened on, rather than only inferable from the run's own final
		// decision reason once every pass is done.
		if !hasOutcome {
			fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_no_outcome", Pass: pass, Verdict: verdict, Reason: fmt.Sprintf("exit %d", rc)}))
		}
		if writeErr := WriteRunState(cfg.stateFile, state); writeErr != nil {
			fmt.Fprintln(os.Stderr, "orchestrator: write run state:", writeErr)
			fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: "write", Error: writeErr.Error()}))
		}

		decision, reason := "continue", ""
		switch {
		case hasOutcome:
			decision, reason = "stop", "outcome reached"
		case verdict == "":
			decision, reason = "stop", "no verdict"
		case verdict != "BLOCK":
			decision, reason = "stop", "verdict not BLOCK"
		case cfg.maxSlices > 0 && pass >= cfg.maxSlices:
			// The coarser backstop on top of every other cap (issue #1998):
			// reaching it always hard-stops.
			decision, reason = "stop", "max slices reached"
		case cfg.maxReviewRounds > 0 && reviewRounds >= cfg.maxReviewRounds:
			decision, reason = "stop", "max review rounds reached"
		}
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "decision", Decision: decision, Reason: reason}))
		if decision == "stop" {
			break
		}
		reviewRounds++
	}

	return rc, nil
}

// seedPromptFromState composes a fresh prompt file carrying promptFile's own
// content plus a summary of state -- last verdict, done/remaining slices,
// scout-brief path -- so each pass is "seeded from the run-state artifact"
// (issue #1998 AC1), not handed the same static prompt on every pass. This is
// also the "precision between-iteration instruction injection" issue #1999
// asks for: the explicit, inspectable "what is done, what the reviewer said"
// brief, composed from the handoff artifact rather than an implicit resumed
// session -- TestRunSeedsFixBriefWithDoneWorkAndVerdictAfterBlock asserts
// AC2's exact shape. When state is the zero value (the common cold-start
// pass, nothing carried forward yet) this returns promptFile unchanged and
// creates no temp file.
func seedPromptFromState(promptFile string, state RunState) (string, error) {
	if state.LastVerdict == "" && len(state.DoneSlices) == 0 && len(state.RemainingSlices) == 0 && state.ScoutBriefPath == "" {
		return promptFile, nil
	}

	original, err := os.ReadFile(promptFile)
	if err != nil {
		return "", fmt.Errorf("seed prompt from run state: %w", err)
	}

	var b strings.Builder
	b.WriteString("## Run-state handoff\n\n")
	b.WriteString("A prior pass in this run left this state behind. Resume from\n")
	b.WriteString("exactly this point -- don't redo already-done work.\n\n")
	if state.LastVerdict != "" {
		fmt.Fprintf(&b, "- Last reviewer verdict: %s\n", state.LastVerdict)
	}
	if len(state.DoneSlices) > 0 {
		fmt.Fprintf(&b, "- Done slices: %s\n", strings.Join(state.DoneSlices, ", "))
	}
	if len(state.RemainingSlices) > 0 {
		fmt.Fprintf(&b, "- Remaining slices: %s\n", strings.Join(state.RemainingSlices, ", "))
	}
	if state.ScoutBriefPath != "" {
		fmt.Fprintf(&b, "- Scout brief: %s\n", state.ScoutBriefPath)
	}
	b.WriteString("\n---\n\n")
	b.Write(original)

	f, err := os.CreateTemp("", "orchestrator-seeded-prompt-*.txt")
	if err != nil {
		return "", fmt.Errorf("seed prompt from run state: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return "", fmt.Errorf("seed prompt from run state: %w", err)
	}
	return f.Name(), nil
}

// scanPassLog scans one pass's raw Driver log for the two markers the
// orchestrator's own loop reacts to: a terminal SPINDRIFT_OUTCOME line (per
// the unchanged outcome.Parse grammar) and the reviewer's own
// "VERDICT: APPROVE|BLOCK" line (issue-prompt.md's REVIEW contract).
//
// The raw log is stream-json (claude.nix's flagsCommon bakes in
// --output-format stream-json): a bare-line scan of it directly would never
// match either marker, since both live inside JSON string fields -- a
// reviewer subagent's verdict text, in particular, only ever reaches the
// top-level Driver's own stream as a tool_result content string, not a line
// of its own. RenderTranscript (the claude Driver's own ADR 0009 strategy,
// already used by driver-exec's sibling console tooling) turns that back
// into readable "[role] text" lines first, matching how a human -- or this
// scan -- actually reads the transcript.
//
// Both markers are located by substring search, not a bare-line prefix
// match: RenderTranscript always leads a rendered line with "[role] " (a
// single-line final message carries the SPINDRIFT_OUTCOME text right after
// that prefix, not on a bare line of its own) and collapses a tool_result's
// own newlines behind "[role]   -> ", so "VERDICT: BLOCK" never leads its
// line either. outcome.ParseAnywhere (rather than Parse) tolerates the same
// thing for the outcome marker, including a claude markdown wrap around its
// own final-message line (issue #1611) landing harmlessly in the discarded
// nonce suffix once the token itself is found.
//
// Returns the last verdict seen ("" if none) and whether a valid outcome
// line was present at all.
func scanPassLog(logPath string) (verdict string, hasOutcome bool) {
	d, err := driver.New("claude")
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan pass log:", err)
		return "", false
	}
	rendered, err := d.RenderTranscript(logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan pass log:", err)
		return "", false
	}

	sc := bufio.NewScanner(strings.NewReader(rendered))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := findVerdict(line); ok {
			verdict = v
		}
		if _, ok := outcome.ParseAnywhere(line); ok {
			hasOutcome = true
		}
	}
	return verdict, hasOutcome
}

// findVerdict reports whether line carries a "VERDICT: APPROVE" or
// "VERDICT: BLOCK" marker anywhere in it, per review-prompt.md's documented
// output contract -- a substring match, not a bare-line prefix match, since
// RenderTranscript never renders a reviewer's verdict as a bare line (see
// scanPassLog). BLOCK is checked first: review-prompt.md's own output shape
// never carries both words in one line, but if a single collapsed
// tool_result summary ever did, favoring BLOCK is the fail-unsafe direction
// -- it costs another fix pass, never a premature merge.
func findVerdict(line string) (string, bool) {
	switch {
	case strings.Contains(line, "VERDICT: BLOCK"):
		return "BLOCK", true
	case strings.Contains(line, "VERDICT: APPROVE"):
		return "APPROVE", true
	default:
		return "", false
	}
}

// buildDriverExecCmd resolves driver-exec on PATH and returns it invoked with
// cfg's fields forwarded as its own flags, byte-identical to the flags
// entrypoint.sh's direct call passes today.
func buildDriverExecCmd(cfg config) (*exec.Cmd, error) {
	bin, err := exec.LookPath("driver-exec")
	if err != nil {
		return nil, err
	}
	args := []string{
		"--prompt-file", cfg.promptFile,
		"--agents-file", cfg.agentsFile,
		"--session-file", cfg.sessionFile,
		"--driver-bin", cfg.driverBin,
		"--driver-flags", cfg.driverFlags,
		"--model", cfg.model,
		"--issue", cfg.issue,
		"--log-path", cfg.logPath,
		"--heartbeat-log", cfg.heartbeatLog,
	}
	if cfg.devshell {
		args = append(args, "--devshell", "--devshell-name", cfg.devshellName)
	}
	return exec.Command(bin, args...), nil
}
