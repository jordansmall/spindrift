package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/runstate"
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
	effort       string
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
	// passSummaryPath is this pass's own pass-summary path (conventionally
	// /tmp/pass-summary.md), recorded into the run-state artifact rather than
	// inlined there.
	passSummaryPath string
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
	// topLevelRole is the resolution role forwarded as driver-exec's own
	// --top-level-role flag for this pass (issue #2092): driverkit.ImplementorRole
	// for an implement/fix/land pass, driverkit.ReviewerRole for the code-owned
	// review pass. Empty omits the flag entirely -- driver-exec then defaults
	// to the implementor role -- which is what the legacy run() single-loop
	// path (no reviewPromptFile) leaves it, keeping that path's argv shape
	// byte-identical to before this field existed.
	topLevelRole string
	// reviewModel is the code-owned review pass's own --model value (issue
	// #2277): when set, runWithReviewPass forwards it as the review pass's
	// driver-exec --model flag instead of the coordinator's own model,
	// letting a reviewer model be configured distinctly from the
	// implementor/coordinator one. Empty falls back to cfg.model, matching
	// pre-#2277 behavior of the review pass silently reusing the
	// coordinator's model.
	reviewModel string
	// reviewEffort is the code-owned review pass's own --effort value (issue
	// #2387): when set, runWithReviewPass forwards it as the review pass's
	// driver-exec --effort flag instead of the coordinator's own effort,
	// letting a reviewer effort be configured distinctly from the
	// implementor/coordinator one. Empty falls back to cfg.effort, matching
	// pre-#2387 behavior of the review pass silently reusing the
	// coordinator's effort.
	reviewEffort string
	// workerPromptFile is the base prompt seedWorkerPrompt composes each
	// dispatched worker's own addendum onto (issue #2059). Empty disables
	// parallel worker dispatch entirely: a coordinator pass's slice manifest
	// is never dispatched, matching every other "empty disables this
	// feature" field in this struct (reviewPromptFile, above).
	workerPromptFile string
	// workerWorkDir holds every dispatched worker's own quarantined log,
	// heartbeat log, result, and done-sentinel files (WorkerOptions.WorkDir).
	// Only meaningful when workerPromptFile is set.
	workerWorkDir string
	// workerTimeout bounds each dispatched worker's own join
	// (WorkerOptions.Timeout); <= 0 falls back to LaunchWorkers' own
	// defaultWorkerTimeout.
	workerTimeout time.Duration
	// maxParallelWorkers caps how many dispatched workers LaunchWorkers runs
	// concurrently (WorkerOptions.MaxParallel, issue #2495); <= 0 falls back
	// to LaunchWorkers' own defaultMaxParallelWorkers. Only meaningful when
	// workerPromptFile is set.
	maxParallelWorkers int
	// argvPromptStyle is forwarded verbatim as driver-exec's own
	// --argv-prompt-style flag (issue #2534 follow-up): how the prompt is
	// spliced into the Driver's argv ("flag" or "positional"). Empty falls
	// back to driver-exec's own "flag" default.
	argvPromptStyle string
	// argvPromptFlag is forwarded verbatim as driver-exec's own
	// --argv-prompt-flag flag: the flag preceding the prompt when
	// argvPromptStyle is "flag" (e.g. claude's "-p"). Empty is a meaningful
	// value here, not a sentinel -- it matches driver-exec's own "" default.
	argvPromptFlag string
	// argvModelFlag is forwarded verbatim as driver-exec's own
	// --argv-model-flag flag: the flag preceding the model value. Empty
	// falls back to driver-exec's own "--model" default.
	argvModelFlag string
	// argvModelOmitEmpty is forwarded as driver-exec's own bare
	// --argv-model-omit-empty boolean flag, only when true (issue #2534
	// follow-up, mirroring topLevelRole's "only append when set" pattern
	// above): omits the model slot entirely when -model is empty, instead of
	// emitting argvModelFlag with an empty value.
	argvModelOmitEmpty bool
	// argvAgentsFlag is forwarded verbatim as driver-exec's own
	// --argv-agents-flag flag: the flag preceding --agents-file's content.
	// Empty is a meaningful value here, not a sentinel -- it matches
	// driver-exec's own "" default (no --agents equivalent for this Driver).
	argvAgentsFlag string
	// argvEffortFlag is forwarded verbatim as driver-exec's own
	// --argv-effort-flag flag: the flag preceding the effort value. Empty
	// falls back to driver-exec's own "--effort" default.
	argvEffortFlag string
	// argvOrder is forwarded verbatim as driver-exec's own --argv-order
	// flag: the space-separated argv slot order (a permutation of prompt
	// model agents session driverFlags effort). Empty falls back to
	// driver-exec's own default order.
	argvOrder string
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
// overrideIfSet writes src into *dst when src is non-empty, leaving *dst
// untouched otherwise -- the shared shape behind every per-pass config field
// (reviewModel, reviewEffort, ...) that overrides a coordinator-inherited
// value only when explicitly configured.
func overrideIfSet(dst *string, src string) {
	if src != "" {
		*dst = src
	}
}

// passOutcome is what the caller has already derived from this pass's own
// log, before persisting -- passed to applyDecision.
type passOutcome struct {
	verdict passmachine.Verdict
	// emitVerdictOp is true when this pass kind's own verdict is
	// authoritative and non-empty.
	emitVerdictOp bool
	hasOutcome    bool
	// checkHasOutcome is false for a review pass -- it never emits
	// pass_no_outcome.
	checkHasOutcome bool
	// exitCode is rc, for pass_no_outcome's Reason field.
	exitCode int
	// pass is the 1-indexed pass count, for pass_no_outcome's Pass field.
	pass int
}

// landPhase converts state.TerminalLand's persisted bool into the machine's
// own LandPhase state (issue #2548 AC2) at the two call sites that build a
// passmachine.Input for an implement/fix/land or review decision.
func landPhase(terminalLand bool) passmachine.LandPhase {
	if terminalLand {
		return passmachine.LandPhaseTerminalCommitted
	}
	return passmachine.LandPhaseActive
}

// applyDecision is the one shared persist/emit helper (issue #2548)
// replacing the four duplicated blocks in run() and runWithReviewPass(): it
// emits the verdict/pass_no_outcome ops (if applicable), writes state to
// disk, computes the Decision via passmachine.Transition, applies any
// LandPhase/CapFired mutation to state (for the NEXT pass's own write --
// this pass's write above deliberately happens BEFORE that mutation,
// preserving the original blocks' own write-before-decide order), and emits
// the decision op.
func applyDecision(stateFile string, state *runstate.RunState, stdout io.Writer, out passOutcome, in passmachine.Input) passmachine.Decision {
	if out.emitVerdictOp {
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "verdict", Verdict: string(out.verdict)}))
	}
	if out.checkHasOutcome && !out.hasOutcome {
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_no_outcome", Pass: out.pass, Verdict: string(out.verdict), Reason: fmt.Sprintf("exit %d", out.exitCode)}))
	}
	if writeErr := runstate.WriteRunState(stateFile, *state); writeErr != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: write run state:", writeErr)
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: "write", Error: writeErr.Error()}))
	}
	d := passmachine.Transition(in)
	if d.LandPhase == passmachine.LandPhaseTerminalCommitted {
		state.TerminalLand = true
		state.CapFired = d.CapFired
	}
	decisionStr := "continue"
	if !d.Continue {
		decisionStr = "stop"
	}
	fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "decision", Decision: decisionStr, Reason: d.Reason}))
	return d
}

func run(cfg config, stdout io.Writer) (int, error) {
	if cfg.reviewPromptFile != "" {
		return runWithReviewPass(cfg, stdout)
	}

	state, err := runstate.ReadRunState(cfg.stateFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: read run state:", err)
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: "read", Error: err.Error()}))
		state = runstate.RunState{}
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
		recordPassSummary(cfg, &state)
		// driver-exec (re-)creates cfg.logPath fresh for this one pass
		// (issue #626's run.go: os.Create truncates), so by the time it
		// returns the file holds exactly this pass's own raw stream -- the
		// same file --log-path already pointed driver-exec at, read back
		// here instead of tapped from cmd.Stdout directly.
		verdict, hasOutcome := scanPassLog(cfg.logPath, cfg.driver)
		if verdict != "" {
			state.LastVerdict = verdict
		}
		// A pass that never printed a terminal SPINDRIFT_OUTCOME line is
		// recorded on its own, distinct marker (issue #2036) -- whatever the
		// loop's decision below turns out to be (continue into a fresh pass,
		// or stop), so a mid-turn cutoff/park is visible for the exact pass
		// it happened on, rather than only inferable from the run's own final
		// decision reason once every pass is done.
		d := applyDecision(cfg.stateFile, &state, stdout, passOutcome{
			verdict:         passmachine.Verdict(verdict),
			emitVerdictOp:   verdict != "",
			hasOutcome:      hasOutcome,
			checkHasOutcome: true,
			exitCode:        rc,
			pass:            pass,
		}, passmachine.Input{
			PassJustExecuted: passmachine.KindLegacy,
			Verdict:          passmachine.Verdict(verdict),
			HasOutcome:       hasOutcome,
			Pass:             pass,
			ReviewRounds:     reviewRounds,
			Caps:             passmachine.Caps{MaxSlices: cfg.maxSlices, MaxReviewRounds: cfg.maxReviewRounds},
		})
		if !d.Continue {
			break
		}
		if d.IncrementReviewRounds {
			reviewRounds++
		}
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
	// Every implement/fix/land pass this loop invokes below (seedAndInvokePass
	// copies cfg by value, so this local mutation flows into each of its own
	// passCfg) carries the implementor top-level role; the review pass, built
	// separately below, overrides its own copy to driverkit.ReviewerRole (issue
	// #2092).
	cfg.topLevelRole = driverkit.ImplementorRole

	state, err := runstate.ReadRunState(cfg.stateFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: read run state:", err)
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: "read", Error: err.Error()}))
		state = runstate.RunState{}
	}

	rc := 0
	reviewRounds := 0
	findingsLogRounds := 0
	pass := 0
	passKind := passmachine.KindImplement
	prevSeededPromptFile := ""
	for {
		// ---- implement/fix pass: cfg.promptFile, seeded from state ----
		pass++
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_start", Pass: pass, Role: passKind.String()}))

		var seededPromptFile string
		rc, seededPromptFile, err = seedAndInvokePass(cfg, state, prevSeededPromptFile, pass, stdout)
		if err != nil {
			return 0, err
		}
		prevSeededPromptFile = seededPromptFile

		if cfg.scoutBriefPath != "" {
			state.ScoutBriefPath = cfg.scoutBriefPath
		}
		recordPassSummary(cfg, &state)
		// Verdict authority belongs solely to the review pass below under
		// this loop -- an implement/fix pass's own prompt has the
		// self-review loop stripped, so its log is scanned only for
		// hasOutcome; any VERDICT-shaped text it happens to contain is not
		// state.LastVerdict's source of truth here.
		_, hasOutcome := scanPassLog(cfg.logPath, cfg.driver)
		// dispatchManifestIfPresent (which calls LaunchWorkers and blocks for
		// up to the full worker timeout) must never fire on a pass that has
		// already reached a terminal outcome, nor on the already-committed
		// terminal land pass (state.TerminalLand, set by a PRIOR iteration's
		// maxSlices cap case below) -- either way the switch below is about
		// to stop the loop a few lines down, so dispatching workers here
		// would be wasted work that also defeats the maxSlices/TerminalLand
		// cap's intent to bound total dispatch, not just pass count (issue
		// #2059 review finding). state.TerminalLand at this point reflects
		// only a prior pass's decision -- this switch's own maxSlices case
		// sets it for THIS pass later, after this call already ran.
		manifestDispatched := false
		if !hasOutcome && !state.TerminalLand {
			manifestDispatched = dispatchManifestIfPresent(cfg, &state, stdout)
		}
		// A pass that never printed a terminal SPINDRIFT_OUTCOME line is
		// recorded on its own, distinct marker (issue #2036) -- whatever the
		// loop's decision below turns out to be (continue into a fresh pass,
		// or stop), so a mid-turn cutoff/park is visible for the exact pass
		// it happened on, rather than only inferable from the run's own final
		// decision reason once every pass is done.
		d := applyDecision(cfg.stateFile, &state, stdout, passOutcome{
			checkHasOutcome: true,
			hasOutcome:      hasOutcome,
			exitCode:        rc,
			pass:            pass,
		}, passmachine.Input{
			PassJustExecuted:   passKind,
			HasOutcome:         hasOutcome,
			Pass:               pass,
			Caps:               passmachine.Caps{MaxSlices: cfg.maxSlices, MaxReviewRounds: cfg.maxReviewRounds},
			LandPhase:          landPhase(state.TerminalLand),
			LastVerdict:        passmachine.Verdict(state.LastVerdict),
			ManifestDispatched: manifestDispatched,
		})
		if !d.Continue {
			break
		}
		switch d.NextPass {
		case passmachine.KindFix:
			// A manifest-dispatch pass's only job was to declare the
			// manifest and stop (issue #2059 AC1) -- there is nothing yet
			// for a review pass to review, so the next pass is another
			// implement/fix pass, seeded with state.WorkerFindings above.
			passKind = passmachine.KindFix
			continue
		case passmachine.KindLand:
			// The cap already used up this run's budget -- skip the review
			// pass this iteration entirely rather than spending one more
			// driver-exec invocation on it; the loop's own bound (the
			// state.TerminalLand case above) guarantees this land pass is
			// the run's last one regardless of what it finds.
			passKind = passmachine.KindLand
			continue
		case passmachine.KindReview:
			// implementFixTransition's own fallthrough case: neither
			// manifest dispatch nor a cap fired, so this pass's own
			// implement/fix/land work is done and a fresh review pass runs
			// below.
		default:
			// d.Continue is true but NextPass is none of the three kinds
			// implementFixTransition ever returns on a continue decision
			// (issue #2548 review) -- report it loudly instead of silently
			// falling into a review pass for an unmapped kind.
			fmt.Fprintf(os.Stderr, "orchestrator: internal error: unexpected NextPass %q on continue decision; treating as review pass\n", d.NextPass)
		}

		// ---- review pass: cfg.reviewPromptFile, always a fresh session ----
		pass++
		fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_start", Pass: pass, Role: passmachine.KindReview.String()}))

		reviewCfg := cfg
		reviewCfg.promptFile = cfg.reviewPromptFile
		reviewCfg.sessionFile = ""
		reviewCfg.topLevelRole = driverkit.ReviewerRole
		// Issue #2277 / #2387: a configured reviewer model/effort overrides
		// the coordinator value reviewCfg otherwise inherited via
		// `reviewCfg := cfg` above; an unset override leaves that inherited
		// cfg value in place, so the review pass falls back to the
		// coordinator's own model/effort.
		overrideIfSet(&reviewCfg.model, cfg.reviewModel)
		overrideIfSet(&reviewCfg.effort, cfg.reviewEffort)

		rc, err = invokeDriverExec(reviewCfg, stdout)
		if err != nil {
			return 0, err
		}

		reviewVerdict, findings := scanReviewLog(cfg.logPath, cfg.driver)
		if reviewVerdict != "" {
			state.LastVerdict = reviewVerdict
		}
		state.ReviewFindings = findings
		findingsLogRounds++
		if err := appendFindingsLogRound(&state, findingsLogRounds, reviewVerdict, findings); err != nil {
			fmt.Fprintln(os.Stderr, "orchestrator: append findings log:", err)
			fmt.Fprint(stdout, claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "run_state_error", Phase: "findings_log", Error: err.Error()}))
		}

		// An APPROVE verdict deliberately falls through to "continue" here
		// (none of the cases below matches it), entering the land pass at
		// the top of the loop exactly once -- see the land-block comment
		// above for why that single land pass is terminal on APPROVE (issue
		// #2069).
		//
		// Issue #2457: a review pass that never resolved into a verdict at
		// all (a malfunctioning/truncated review session), the coarse
		// maxSlices backstop, and the maxReviewRounds cap all used to stop
		// the run outright here. Now each commits the run to one more
		// terminal "land" pass instead -- mirroring the implement/fix
		// block's own maxSlices case above -- so a run that exhausts its
		// budget still gets a chance to land and report an honest outcome
		// rather than exiting outcome-less. The implement/fix block's own
		// state.TerminalLand case (already true by the time this land pass's
		// own iteration reaches it) is what actually bounds this to exactly
		// one extra pass.
		d = applyDecision(cfg.stateFile, &state, stdout, passOutcome{
			verdict:       passmachine.Verdict(reviewVerdict),
			emitVerdictOp: reviewVerdict != "",
		}, passmachine.Input{
			PassJustExecuted: passmachine.KindReview,
			Verdict:          passmachine.Verdict(reviewVerdict),
			Pass:             pass,
			ReviewRounds:     reviewRounds,
			Caps:             passmachine.Caps{MaxSlices: cfg.maxSlices, MaxReviewRounds: cfg.maxReviewRounds},
			LandPhase:        landPhase(state.TerminalLand),
		})
		if !d.Continue {
			break
		}
		if d.IncrementReviewRounds {
			reviewRounds++
		}
		passKind = d.NextPass
	}

	return rc, nil
}

// seedPromptFromState composes a fresh prompt file carrying promptFile's own
// content plus a summary of state -- last verdict, scout-brief path,
// pass-summary path -- so each pass is "seeded from the run-state artifact"
// (issue #1998 AC1), not handed the same static prompt on every pass. This is
// also the "precision between-iteration instruction injection" issue #1999
// asks for: the explicit, inspectable "what the reviewer said" brief,
// composed from the handoff artifact rather than an implicit resumed session
// -- TestRunSeedsFixBriefWithVerdictAfterBlock asserts this shape. When state
// is the zero value (the common cold-start pass, nothing carried forward yet)
// this returns promptFile unchanged and creates no temp file.
func seedPromptFromState(promptFile string, state runstate.RunState) (string, error) {
	if state.IsEmpty() {
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
	if state.ScoutBriefPath != "" {
		fmt.Fprintf(&b, "- Scout brief: %s\n", state.ScoutBriefPath)
	}
	if state.PassSummaryPath != "" {
		fmt.Fprintf(&b, "- Pass summary: %s\n", state.PassSummaryPath)
	}
	if state.ReviewFindings != "" {
		fmt.Fprintf(&b, "- Reviewer findings:\n\n%s\n", state.ReviewFindings)
	}
	// A recorded FindingsLogPath whose file no longer exists degrades the
	// same way an unset path does (AC4: "a missing log degrades to the
	// current last-findings-only behavior, not an error") -- skip the
	// bullet rather than point the land pass at a file that isn't there.
	if state.FindingsLogPath != "" {
		if _, err := os.Stat(state.FindingsLogPath); err == nil {
			fmt.Fprintf(&b, "- Findings log: %s (every review round's own findings, one \"## Round N\" section per round -- when you reach FILE ISSUES, read this file and run the same non-blocking triage from REVIEW over the union of every round's non-blocking findings, not just this round's Reviewer findings above; a finding already fixed inline in an earlier round's fix pass is resolved, not re-filed)\n", state.FindingsLogPath)
		}
	}
	if state.WorkerFindings != "" {
		fmt.Fprintf(&b, "- Worker dispatch results:\n\n%s\n", state.WorkerFindings)
	}
	if state.TerminalLand {
		b.WriteString("\n")
		fmt.Fprintf(&b, "This is the run's terminal pass: %s, and the run has\n", state.CapFired)
		b.WriteString("committed to this one last implement/fix pass instead of stopping\n")
		b.WriteString("outcome-less. This overrides review-loop-orchestrator.md's \"stop your\n")
		b.WriteString("turn now, right after COMMIT\" instruction for a non-APPROVE-seeded\n")
		b.WriteString("pass -- on this pass, proceed through FILE ISSUES, LAND THE CHANGE,\n")
		b.WriteString("OPEN A PULL REQUEST, and OUTCOME regardless of verdict. If blocking\n")
		b.WriteString("review findings remain unresolved, land anyway and report that\n")
		b.WriteString("plainly in the OUTCOME note as a real status, not a bare success.\n")
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
// Returns the BLOCK-dominant verdict ("" if none) and whether a valid
// outcome line was present at all. driverName selects the RenderTranscript
// strategy (issue #262 slice 4) -- the same Driver name this run's own
// cfg.driver carries, not a hardcoded "claude".
//
// Aggregation is BLOCK-dominant, not last-match-wins (issue #2546): a
// nested subagent's tool_result -- the only place a verdict ever reaches
// this transcript -- can carry untrusted content (a finding's own quoted
// text, a diff hunk, a tool's own output) that itself contains the
// substring "VERDICT: APPROVE" anywhere after a genuine BLOCK line. A
// naive last-match-wins scan would let that injected text silently flip
// the aggregate result from BLOCK to APPROVE. Instead, a BLOCK match on
// any line anywhere in the transcript wins outright, regardless of
// ordering; only when no line ever matches BLOCK does an APPROVE match
// count.
func scanPassLog(logPath, driverName string) (verdict string, hasOutcome bool) {
	d, err := driver.New(driverName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan pass log:", err)
		return "", false
	}
	rendered, err := d.RenderTranscript(logPath, driverkit.RenderOptions{TopLevelRole: driverkit.ImplementorRole})
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan pass log:", err)
		return "", false
	}

	var sawBlock, sawApprove bool
	sc := bufio.NewScanner(strings.NewReader(rendered))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := findVerdict(line); ok {
			switch v {
			case "BLOCK":
				sawBlock = true
			case "APPROVE":
				sawApprove = true
			}
		}
		if _, ok := outcome.ParseAnywhere(line); ok {
			hasOutcome = true
		}
	}
	switch {
	case sawBlock:
		verdict = "BLOCK"
	case sawApprove:
		verdict = "APPROVE"
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
// its internal newlines survive into the rendered transcript verbatim.
//
// Per review-prompt.md's own contract, the verdict is the reviewer's final
// top-level message's FIRST LINE, and nothing else (issue #2546): unlike
// scanPassLog's substring-anywhere findVerdict, a line only counts here when,
// after stripping the "[role] " render prefix, it strictly STARTS WITH
// VerdictBlock or VerdictApprove. That keeps a finding elsewhere in the same
// message -- e.g. "the prior fix pass returned VERDICT: APPROVE but missed
// X" -- from ever being mistaken for the real verdict, since it never leads
// its own rendered block.
//
// scanReviewLog walks every top-level ([]driverkit.ReviewerRole-prefixed)
// rendered block in order and keeps the LAST one whose first line strictly
// matches, last-wins the same way findVerdict resolves multiple matches --
// so a review pass that keeps talking after its real verdict message (a
// misbehaving turn, or a rendering quirk) doesn't erase an already-found
// verdict merely because its own trailing chatter carries no verdict marker
// of its own; it only gets overridden by a LATER block that itself opens
// with a strict verdict prefix. Findings are then sliced from that same
// winning block's first line onward, stopping before the next
// role-prefixed line, exactly as before.
//
// Returns ("", "") when no block's first line ever strictly matches --
// review-prompt.md's own contract violated outright, not merely quoted
// elsewhere -- regardless of what verdict literals appear anywhere else in
// the transcript. driverName selects the RenderTranscript strategy (issue
// #262 slice 4), the same as scanPassLog's own parameter.
func scanReviewLog(logPath, driverName string) (verdict, findings string) {
	d, err := driver.New(driverName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan review log:", err)
		return "", ""
	}
	rendered, err := d.RenderTranscript(logPath, driverkit.RenderOptions{TopLevelRole: driverkit.ReviewerRole})
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: scan review log:", err)
		return "", ""
	}

	lines := strings.Split(rendered, "\n")
	blockLine := -1
	for i, line := range lines {
		// renderedRolePrefixRe (manifest.go) is the same "[role]" prefix
		// matcher scanForManifest uses to attribute a rendered line to its
		// role -- a bare physical-line continuation of a prior multi-line
		// block carries no prefix at all and never starts a new block.
		m := renderedRolePrefixRe.FindStringSubmatch(line)
		if m == nil || m[1] != driverkit.ReviewerRole {
			continue
		}
		text := line
		if loc := renderedEventPrefix.FindStringIndex(text); loc != nil {
			text = text[loc[1]:]
		}
		switch {
		case strings.HasPrefix(text, VerdictBlock):
			verdict = "BLOCK"
			blockLine = i
		case strings.HasPrefix(text, VerdictApprove):
			verdict = "APPROVE"
			blockLine = i
		}
	}
	if blockLine == -1 {
		return "", ""
	}

	// RenderTranscript prefixes only the first physical line of a multi-line
	// assistant message with "[role] " (see scanPassLog's own comment) --
	// strip it here so the seeded fix-pass brief carries the reviewer's
	// findings text alone, not a rendering artifact.
	first := lines[blockLine]
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
	for _, l := range lines[blockLine+1:] {
		if renderedEventPrefix.MatchString(l) {
			break
		}
		findingsLines = append(findingsLines, l)
	}
	findings = strings.TrimSpace(strings.Join(findingsLines, "\n"))
	return verdict, findings
}

// appendFindingsLogRound appends round's own review findings to the per-run
// findings log (issue #2552), creating the log file on first use and
// recording its path in state.FindingsLogPath. A round with no findings text
// (no verdict at all, or an unparseable review log) is skipped -- there is
// nothing to append, and an empty section would confuse the per-round
// numbering. Best-effort: a failure here is logged to stderr and never
// treated as fatal to the pass, matching every other handoff-artifact write
// in this file (see applyDecision's own runstate.WriteRunState error
// handling).
func appendFindingsLogRound(state *runstate.RunState, round int, verdict, findings string) error {
	if findings == "" {
		return nil
	}
	if state.FindingsLogPath == "" {
		f, err := os.CreateTemp("", "orchestrator-findings-log-*.md")
		if err != nil {
			return fmt.Errorf("create findings log: %w", err)
		}
		path := f.Name()
		if err := f.Close(); err != nil {
			return fmt.Errorf("create findings log: %w", err)
		}
		state.FindingsLogPath = path
	}
	f, err := os.OpenFile(state.FindingsLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("append findings log: %w", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "## Round %d (verdict: %s)\n\n%s\n\n", round, verdict, findings); err != nil {
		return fmt.Errorf("append findings log: %w", err)
	}
	return nil
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

// recordPassSummary records cfg.passSummaryPath into state.PassSummaryPath
// only when this pass's own invocation actually left a file there, verified
// via os.Stat -- seedAndInvokePass's own guard only unlinks a leftover file
// when state.PassSummaryPath was already "" going into this pass (nothing
// this round referenced it), so a killed-mid-turn pass, or one that simply
// never wrote a summary, can still leave the path missing here; the state
// must not seed the next pass with a reference to a summary that doesn't
// exist. An empty cfg.passSummaryPath means the caller didn't supply one
// this pass, not that the prior path is now unknown, so it leaves the
// carried-forward value alone rather than clobbering it with "". Shared by
// run and runWithReviewPass, which otherwise duplicated this block
// verbatim.
func recordPassSummary(cfg config, state *runstate.RunState) {
	if cfg.passSummaryPath == "" {
		return
	}
	if _, statErr := os.Stat(cfg.passSummaryPath); statErr == nil {
		state.PassSummaryPath = cfg.passSummaryPath
	} else {
		state.PassSummaryPath = ""
	}
}

// seedAndInvokePass seeds cfg.promptFile from state (removing the previous
// pass's own seeded file first, per seedPromptFromState's caller contract --
// prevSeededPromptFile is "" on the first pass, and left alone by
// seedPromptFromState's own no-op case when state carries nothing new to
// seed), pins cfg.sessionFile verbatim only for pass 1 and runs every pass
// after it sessionless, invokes driver-exec, and conditionally clears
// cfg.passSummaryPath -- only when state.PassSummaryPath == "" going in
// (nothing this round references it), matching recordPassSummary's own
// guard for interpreting whatever file is left behind afterward. Returns
// the pass's exit code and its own seeded prompt file, for the caller to
// track as its next prevSeededPromptFile. Shared by run's legacy single
// loop and runWithReviewPass's implement/fix pass -- the one piece of
// per-pass bookkeeping identical between them; each keeps its own
// scan-and-decide logic afterward, since a legacy pass's own verdict
// drives its loop while an implement/fix pass's does not.
func seedAndInvokePass(cfg config, state runstate.RunState, prevSeededPromptFile string, pass int, stdout io.Writer) (rc int, seededPromptFile string, err error) {
	seededPromptFile, err = seedPromptFromState(cfg.promptFile, state)
	if err != nil {
		return 0, "", err
	}
	if prevSeededPromptFile != "" && prevSeededPromptFile != cfg.promptFile {
		os.Remove(prevSeededPromptFile)
	}
	// Only clear a leftover file when nothing this round references it
	// (state.PassSummaryPath == ""): a killed-mid-turn pass with nothing to
	// report can otherwise leave cfg.passSummaryPath holding pure orphaned
	// garbage from an even-earlier pass, which the post-pass os.Stat check
	// in run/runWithReviewPass would then wrongly attribute as this pass's
	// own fresh summary. When state.PassSummaryPath != "", this pass's own
	// seeded prompt (via seedPromptFromState above) just told the agent to
	// read this exact file -- removing it here would delete the file out
	// from under that reference before the agent ever gets to read it.
	if cfg.passSummaryPath != "" && state.PassSummaryPath == "" {
		os.Remove(cfg.passSummaryPath)
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
		"--effort", cfg.effort,
		"--issue", cfg.issue,
		"--log-path", cfg.logPath,
		"--heartbeat-log", cfg.heartbeatLog,
		"--argv-prompt-style", cfg.argvPromptStyle,
		"--argv-prompt-flag", cfg.argvPromptFlag,
		"--argv-model-flag", cfg.argvModelFlag,
		"--argv-agents-flag", cfg.argvAgentsFlag,
		"--argv-effort-flag", cfg.argvEffortFlag,
		"--argv-order", cfg.argvOrder,
	}
	if cfg.devshell {
		args = append(args, "--devshell", "--devshell-name", cfg.devshellName)
	}
	if cfg.topLevelRole != "" {
		args = append(args, "--top-level-role", cfg.topLevelRole)
	}
	if cfg.argvModelOmitEmpty {
		args = append(args, "--argv-model-omit-empty")
	}
	return exec.Command(bin, args...), nil
}
