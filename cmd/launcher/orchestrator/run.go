package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
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
	// driver is the Driver's registry name (ADR 0009, e.g. "claude" or
	// "opencode"), forwarded verbatim as driver-exec's own --driver flag on
	// every pass this run invokes, and used by scanPassLog/scanReviewLog to
	// resolve the same Driver's RenderTranscript strategy rather than a
	// hardcoded "claude". Empty defaults to "claude", matching driver.New's
	// own convention.
	driver       string
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
	// reviewPromptFile is the code-owned review pass's own prompt file
	// (issue #2037): a distinct driver-exec invocation against
	// reviewPromptFile, scanned by scanReviewLog rather than scanPassLog,
	// replaces the implementor's own inline "spawn a reviewer subagent,
	// loop until no blocking findings" prose. Empty disables the review
	// pass entirely -- run keeps its pre-#2037 single-loop behavior,
	// unchanged bit-for-bit -- so entrypoint.sh sets it only on the
	// ORCHESTRATOR-on work-dispatch path (ADR 0035's master switch; there
	// is no separate review-pass sub-knob).
	reviewPromptFile string
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
	if cfg.reviewPromptFile != "" {
		return runWithReviewPass(cfg, stdout)
	}

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

		// The very last pass's seeded file is deliberately left on disk by
		// seedAndInvokePass: this is a short-lived, per-box tmp file, and
		// the box's own filesystem is destroyed with the container
		// regardless.
		var seededPromptFile string
		rc, seededPromptFile, err = seedAndInvokePass(cfg, state, prevSeededPromptFile, pass, stdout)
		if err != nil {
			return 0, err
		}
		prevSeededPromptFile = seededPromptFile

		// An empty cfg.scoutBriefPath means the caller didn't supply one
		// this pass, not that the prior path is now unknown, so it leaves
		// the carried-forward value alone rather than clobbering it with "".
		if cfg.scoutBriefPath != "" {
			state.ScoutBriefPath = cfg.scoutBriefPath
		}
		// driver-exec (re-)creates cfg.logPath fresh for this one pass
		// (issue #626's run.go: os.Create truncates), so by the time it
		// returns the file holds exactly this pass's own raw stream -- the
		// same file --log-path already pointed driver-exec at, read back
		// here instead of tapped from cmd.Stdout directly.
		verdict, hasOutcome := scanPassLog(cfg.logPath, cfg.driver)
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

// runWithReviewPass implements the #2037 code-owned review pass: instead of
// one pass looping on its own inline "spawn a reviewer subagent, repeat until
// no blocking findings" prose, the orchestrator alternates two structurally
// different fresh-session invocations -- an implement/fix pass against
// cfg.promptFile, and a review pass against the distinct
// cfg.reviewPromptFile -- with the review pass's own verdict, scanned from
// its own log via scanReviewLog, driving the loop instead of the implement/
// fix pass's. Only run (cfg.reviewPromptFile != "") calls this; entrypoint.sh
// sets that field exactly when ORCHESTRATOR is on (ADR 0035's master switch
// -- no separate review-pass sub-knob), so run's pre-#2037 callers are
// unaffected.
//
// An implement/fix pass's prompt is stripped of the self-review loop under
// the orchestrator (agent/entrypoint.sh, issue-prompt.md's REVIEW section):
// it stops after COMMIT unless the seeded run-state above it already shows
// an APPROVE verdict, in which case it proceeds straight to landing the
// change and its own terminal SPINDRIFT_OUTCOME. So the sequence this loop
// drives is implement -> review -> (BLOCK) fix -> review -> ... -> (APPROVE)
// land, where "land" is just another fix-role pass that happens to find
// nothing left to fix. The loop's own hasOutcome check (unchanged from run's
// legacy loop) is what actually stops it, once that land pass reaches its
// own outcome -- there is no separate "land" pass kind in the Go code.
func runWithReviewPass(cfg config, stdout io.Writer) (int, error) {
	state, err := ReadRunState(cfg.stateFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: read run state:", err)
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: "read", Error: err.Error()}))
		state = RunState{}
	}

	rc := 0
	reviewRounds := 0
	pass := 0
	implRole := "implement"
	prevSeededPromptFile := ""
	for {
		// ---- implement/fix pass: cfg.promptFile, seeded from state ----
		pass++
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_start", Pass: pass, Role: implRole}))

		var seededPromptFile string
		rc, seededPromptFile, err = seedAndInvokePass(cfg, state, prevSeededPromptFile, pass, stdout)
		if err != nil {
			return 0, err
		}
		prevSeededPromptFile = seededPromptFile

		if cfg.scoutBriefPath != "" {
			state.ScoutBriefPath = cfg.scoutBriefPath
		}
		// Verdict authority belongs solely to the review pass below under
		// this loop -- an implement/fix pass's own prompt has the
		// self-review loop stripped, so its log is scanned only for
		// hasOutcome; any VERDICT-shaped text it happens to contain is not
		// state.LastVerdict's source of truth here.
		_, hasOutcome := scanPassLog(cfg.logPath, cfg.driver)
		if !hasOutcome {
			fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_no_outcome", Pass: pass, Reason: fmt.Sprintf("exit %d", rc)}))
		}
		if writeErr := WriteRunState(cfg.stateFile, state); writeErr != nil {
			fmt.Fprintln(os.Stderr, "orchestrator: write run state:", writeErr)
			fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: "write", Error: writeErr.Error()}))
		}

		decision, reason := "continue", ""
		switch {
		case hasOutcome:
			decision, reason = "stop", "outcome reached"
		// After an APPROVE verdict the land pass above runs exactly once (see
		// the review decision block below): a land pass cut off before its
		// own terminal SPINDRIFT_OUTCOME is recovered by the within-pass
		// required_marker_gate session-resume nudge (issue #2044,
		// agent/entrypoint.sh) inside that single land driver-exec, not by
		// re-entering the review->land cycle here -- a fresh land pass would
		// re-invoke the Filer / FILE ISSUES step on every extra lap,
		// bounded only by the coarse maxSlices (issue #2069). This stop is
		// the bound; it emits the existing decision op so an operator sees
		// why the run ended.
		case state.LastVerdict == "APPROVE":
			decision, reason = "stop", "land pass reached no terminal outcome after APPROVE"
		case cfg.maxSlices > 0 && pass >= cfg.maxSlices:
			decision, reason = "stop", "max slices reached"
		}
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "decision", Decision: decision, Reason: reason}))
		if decision == "stop" {
			break
		}

		// ---- review pass: cfg.reviewPromptFile, always a fresh session ----
		pass++
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_start", Pass: pass, Role: "review"}))

		reviewCfg := cfg
		reviewCfg.promptFile = cfg.reviewPromptFile
		reviewCfg.sessionFile = ""

		rc, err = invokeDriverExec(reviewCfg, stdout)
		if err != nil {
			return 0, err
		}

		reviewVerdict, findings := scanReviewLog(cfg.logPath, cfg.driver)
		if reviewVerdict != "" {
			state.LastVerdict = reviewVerdict
			fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "verdict", Verdict: reviewVerdict}))
		}
		state.ReviewFindings = findings
		if writeErr := WriteRunState(cfg.stateFile, state); writeErr != nil {
			fmt.Fprintln(os.Stderr, "orchestrator: write run state:", writeErr)
			fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: "write", Error: writeErr.Error()}))
		}

		// An APPROVE verdict deliberately falls through to "continue" here
		// (none of the cases below matches it), entering the land pass at
		// the top of the loop exactly once -- see the land-block comment
		// above for why that single land pass is terminal on APPROVE (issue
		// #2069).
		decision, reason = "continue", ""
		switch {
		case reviewVerdict == "":
			decision, reason = "stop", "no verdict"
		case cfg.maxSlices > 0 && pass >= cfg.maxSlices:
			decision, reason = "stop", "max slices reached"
		case reviewVerdict == "BLOCK" && cfg.maxReviewRounds > 0 && reviewRounds >= cfg.maxReviewRounds:
			decision, reason = "stop", "max review rounds reached"
		}
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "decision", Decision: decision, Reason: reason}))
		if decision == "stop" {
			break
		}
		if reviewVerdict == "BLOCK" {
			reviewRounds++
		}
		implRole = "fix"
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
	if state.LastVerdict == "" && len(state.DoneSlices) == 0 && len(state.RemainingSlices) == 0 && state.ScoutBriefPath == "" && state.ReviewFindings == "" {
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
	if state.ReviewFindings != "" {
		fmt.Fprintf(&b, "- Reviewer findings:\n\n%s\n", state.ReviewFindings)
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
// line was present at all. driverName selects the RenderTranscript strategy
// (issue #262 slice 4) -- the same Driver name this run's own cfg.driver
// carries, not a hardcoded "claude".
func scanPassLog(logPath, driverName string) (verdict string, hasOutcome bool) {
	d, err := driver.New(driverName)
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

// scanReviewLog scans a code-owned review pass's own rendered log (issue
// #2037) -- a distinct driver-exec invocation against cfg.reviewPromptFile,
// never a subagent nested inside an implement/fix pass -- for its verdict and
// the findings text (the "VERDICT: ..." line plus its own Blocking/Non-
// blocking sections) that message carries. Unlike scanPassLog's callers,
// where the verdict only ever reaches the transcript as a subagent's
// tool_result (RenderTranscript collapses that block's internal newlines
// behind "[role]   -> "), a review pass's verdict is its own top-level final
// assistant message: RenderTranscript's "text" case only TrimSpaces it, so
// its internal newlines survive into the rendered transcript verbatim, and
// findings can be sliced out from the verdict line onward.
//
// Returns ("", "") when no verdict line is found at all. Mirrors findVerdict's
// last-wins, fail-unsafe-toward-BLOCK resolution when more than one verdict
// line appears (review-prompt.md's own contract only ever emits one, but nothing
// stops a rendering quirk from producing more). driverName selects the
// RenderTranscript strategy (issue #262 slice 4), the same as scanPassLog's
// own parameter.
func scanReviewLog(logPath, driverName string) (verdict, findings string) {
	d, err := driver.New(driverName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan review log:", err)
		return "", ""
	}
	rendered, err := d.RenderTranscript(logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan review log:", err)
		return "", ""
	}

	lines := strings.Split(rendered, "\n")
	verdictLine := -1
	for i, line := range lines {
		if v, ok := findVerdict(strings.TrimSpace(line)); ok {
			verdict = v
			verdictLine = i
		}
	}
	if verdictLine == -1 {
		return "", ""
	}

	// RenderTranscript prefixes only the first physical line of a multi-line
	// assistant message with "[role] " (see scanPassLog's own comment) --
	// strip it here so the seeded fix-pass brief carries the reviewer's
	// findings text alone, not a rendering artifact.
	first := lines[verdictLine]
	if loc := renderedEventPrefix.FindStringIndex(first); loc != nil {
		first = first[loc[1]:]
	}
	findingsLines := []string{first}
	// Every subsequent physical line belongs to this same message only until
	// the next "[role] "-prefixed line -- a fresh rendered event, not a
	// continuation of the verdict message's own embedded newlines (see
	// RenderTranscript: every event gets its own "lines" entry, but only a
	// multi-line entry's own first line carries the prefix). Stopping there
	// keeps a well-behaved review pass's findings exactly what its final
	// message contained, not whatever content RenderTranscript happens to
	// render afterward (review-prompt.md's own contract says there should be
	// none, but a rendering quirk or a misbehaving turn shouldn't corrupt the
	// seeded fix-pass brief).
	for _, l := range lines[verdictLine+1:] {
		if renderedEventPrefix.MatchString(l) {
			break
		}
		findingsLines = append(findingsLines, l)
	}
	findings = strings.TrimSpace(strings.Join(findingsLines, "\n"))
	return verdict, findings
}

// renderedEventPrefix matches RenderTranscript's own "[role] " event prefix
// (transcript_render.go) at the start of a line: a bracketed, non-empty,
// non-whitespace role name followed by a space -- tighter than a bare "["
// prefix, which a finding's own text (review-prompt.md's contract never
// starts one with "[", but nothing enforces that) could otherwise trip.
var renderedEventPrefix = regexp.MustCompile(`^\[\S+\] `)

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
	case strings.Contains(line, VerdictBlock):
		return "BLOCK", true
	case strings.Contains(line, VerdictApprove):
		return "APPROVE", true
	default:
		return "", false
	}
}

// invokeDriverExec runs one driver-exec pass against cfg, streaming its raw
// stdout to stdout unchanged, and returns its exit code -- 0 for a clean
// exit, or the process's own code when it exited non-zero. Shared by both
// run's legacy single-loop and runWithReviewPass's implement/review/fix
// loop, so exit-code translation lives in exactly one place.
func invokeDriverExec(cfg config, stdout io.Writer) (int, error) {
	cmd, err := buildDriverExecCmd(cfg)
	if err != nil {
		return 0, err
	}
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()

	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	if runErr != nil {
		return 0, runErr
	}
	return 0, nil
}

// seedAndInvokePass seeds cfg.promptFile from state (removing the previous
// pass's own seeded file first, per seedPromptFromState's caller contract --
// prevSeededPromptFile is "" on the first pass, and left alone by
// seedPromptFromState's own no-op case when state carries nothing new to
// seed), pins cfg.sessionFile verbatim only for pass 1 and runs every pass
// after it sessionless, and invokes driver-exec. Returns the pass's exit
// code and its own seeded prompt file, for the caller to track as its next
// prevSeededPromptFile. Shared by run's legacy single loop and
// runWithReviewPass's implement/fix pass -- the one piece of per-pass
// bookkeeping identical between them; each keeps its own scan-and-decide
// logic afterward, since a legacy pass's own verdict drives its loop while
// an implement/fix pass's does not.
func seedAndInvokePass(cfg config, state RunState, prevSeededPromptFile string, pass int, stdout io.Writer) (rc int, seededPromptFile string, err error) {
	seededPromptFile, err = seedPromptFromState(cfg.promptFile, state)
	if err != nil {
		return 0, "", err
	}
	if prevSeededPromptFile != "" && prevSeededPromptFile != cfg.promptFile {
		os.Remove(prevSeededPromptFile)
	}

	passCfg := cfg
	passCfg.promptFile = seededPromptFile
	if pass > 1 {
		passCfg.sessionFile = ""
	}

	rc, err = invokeDriverExec(passCfg, stdout)
	return rc, seededPromptFile, err
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
		"--driver", cfg.driver,
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
