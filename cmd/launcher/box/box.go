package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/bundleout"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/markergate"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/outcomebackstop"
	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/signalwire"
)

// Each corrective resume is a single pass: the launcher's retry path owns
// anything beyond one, and a nudge that loops would re-enter the
// implement/review/fix cycle (issues #2065, #2975).
const (
	outcomeNudgeCap  = 1
	prIntentNudgeCap = 1
)

// runStateFile mirrors the orchestrator's own --state-file default (issue
// #1997); a missing file degrades inside the backstop (issue #2459).
const runStateFile = "/tmp/run-state.json"

// inputs are the facts entrypoint.sh holds when it hands over after the first
// Driver run.
type inputs struct {
	DriverExitCode int
	// OutcomeLine was already printed by the entrypoint after the first run;
	// box never reprints it.
	OutcomeLine       string
	StreamLog         string
	DriverTextLog     string
	HandoffFile       string
	ResumeSessionFile string
	WorkDir           string
	OutboxDir         string
}

type deps struct {
	// Orchestrate runs one orchestrator pass with argv and returns its exit
	// code.
	Orchestrate  func(argv []string) int
	SignalStatus func() (signalwire.Status, error)
	Backstop     func(cfg outcomebackstop.Config, w io.Writer) error
	Demote       func(cfg outcomebackstop.Config, priorOutcomeLine string, w io.Writer) error
	BundleOut    func(cfg bundleout.Config, w io.Writer) error
	// WarnLockfiles is the best-effort settle-time lockfile scan.
	WarnLockfiles func(w io.Writer, workDir string)
	Getenv        func(string) string
	Stdout        io.Writer
	Stderr        io.Writer
}

// phaseError names the phase whose failure aborted the entrypoint under
// `set -e`; mainRun prints it as `box: <phase>: <err>` and exits 1.
type phaseError struct {
	phase string
	err   error
}

func (e *phaseError) Error() string { return e.phase + ": " + e.err.Error() }
func (e *phaseError) Unwrap() error { return e.err }

func phaseErr(phase string, err error) error { return &phaseError{phase: phase, err: err} }

// state is what the entrypoint's _last_* locals and claude_rc held between
// phases.
type state struct {
	rc          int
	outcomeLine string
	streamLog   string
	textLog     string
	// viaBackstop marks a ready status manufactured by the synthetic backstop
	// rather than reported by the driver (issue #2448).
	viaBackstop bool
}

type boxRun struct {
	in         inputs
	env        promptassembly.Env
	d          deps
	st         state
	adviseOnly bool
	needsBox   bool // the outbox is mounted host-side
	relay      bool // read-only Box handing off through the outbox
	kind       string
	carrier    string
}

// run sequences everything entrypoint.sh's main() did after its first Driver
// run and returns the exit code the entrypoint would have exited with.
func run(in inputs, env promptassembly.Env, d deps) (int, error) {
	r := &boxRun{in: in, env: env, d: d}
	r.st = state{rc: in.DriverExitCode, outcomeLine: in.OutcomeLine, streamLog: in.StreamLog, textLog: in.DriverTextLog}

	r.kind = env.DispatchKind
	if r.kind == "" {
		r.kind = dispatchkind.Work.Name
	}
	desc, ok := dispatchkind.ByName(r.kind)
	if !ok {
		return 0, phaseErr("advise-only", fmt.Errorf("unrecognized DISPATCH_KIND=%s", r.kind))
	}
	r.adviseOnly = desc.AdviseOnly
	r.carrier = env.SignalCarrier
	if r.carrier == "" {
		r.carrier = "log"
	}
	// An empty BOX_WRITE_ENABLED plus an outbox-relay-capable backend is a
	// read-only Box that hands off via the outbox (issues #2094, #2267).
	r.relay = !env.BoxWriteEnabled && env.OutboxRelayCapable
	r.needsBox = env.HostMediatedRemote || r.relay

	recovered, err := r.outcomeNudge()
	if err != nil {
		return 0, err
	}
	if err := r.backstop(recovered); err != nil {
		return 0, err
	}
	if err := r.prIntentNudge(); err != nil {
		return 0, err
	}
	if err := r.demoteAlreadyResolved(); err != nil {
		return 0, err
	}
	// Only a self-contained dispatch has no clone to scan; a crashed run can
	// still have committed a stale lockfile (issue #3199).
	if !env.SelfContained {
		d.WarnLockfiles(d.Stdout, in.WorkDir)
	}
	if err := r.bundleOut(); err != nil {
		return 0, err
	}
	r.say("==> entrypoint complete for %s", env.DispatchKey)
	return r.st.rc, nil
}

func (r *boxRun) say(format string, a ...any) {
	fmt.Fprintf(r.d.Stdout, format+"\n", a...)
}

func (r *boxRun) warn(phase string, err error) {
	if err != nil {
		fmt.Fprintf(r.d.Stderr, "box: %s: %v\n", phase, err)
	}
}

// outcomeNudge resumes the pinned session when a clean exit left no
// SPINDRIFT_OUTCOME marker (issues #1607, #2044, #2511, #2978). Research pins
// no session and a non-zero exit is the launcher's retry path. It reports
// whether a resume ran.
func (r *boxRun) outcomeNudge() (bool, error) {
	resumed := false
	for attempts := 0; attempts < outcomeNudgeCap; attempts++ {
		if r.st.rc != 0 || r.adviseOnly {
			break
		}
		cfg := markergate.NudgeConfig{
			Marker:        markergate.MarkerOutcome,
			Issue:         r.env.IssueNumber,
			Landing:       r.env.Branch,
			LogPath:       r.st.textLog,
			SignalCarrier: r.carrier,
			SignalStatus:  r.d.SignalStatus,
		}
		prompt, perr := markergate.RenderNudgePrompt(cfg)
		r.warn("marker-gate", perr)
		should, serr := markergate.ShouldNudgeOutcome(cfg)
		r.warn("marker-gate", serr)
		if !should {
			break
		}
		r.say("==> required marker missing — resuming the session once with a nudge")
		resumed = true
		if err := r.resume(prompt); err != nil {
			return resumed, err
		}
	}
	return resumed, nil
}

// backstop keeps a clean exit from going unreported. A non-zero exit
// propagates untouched: forcing zero would turn a retryable transient failure
// into a terminal blocked run (issue #593).
func (r *boxRun) backstop(recoveryAttempted bool) error {
	if r.st.rc != 0 || r.st.outcomeLine != "" {
		return nil
	}
	r.say("==> driver produced no SPINDRIFT_OUTCOME line — emitting synthetic backstop")
	cfg, err := r.backstopConfig(recoveryAttempted)
	if err != nil {
		return phaseErr("outcome-backstop", err)
	}
	line, err := r.capture(func(w io.Writer) error { return r.d.Backstop(cfg, w) })
	if err != nil {
		return phaseErr("outcome-backstop", err)
	}
	r.say("%s", line)
	r.st.outcomeLine = line
	r.st.viaBackstop = true
	return nil
}

// prIntentNudge covers a read-only Box that reached status=ready without the
// SPINDRIFT_PR_INTENT marker, which leaves hostMediateDraftPR nothing to relay
// (issues #2045, #2036, #2511, #2978). An advise-only kind never opens a PR
// (issue #3906).
func (r *boxRun) prIntentNudge() error {
	for attempts := 1; attempts <= prIntentNudgeCap; attempts++ {
		if r.st.rc != 0 || r.adviseOnly || !r.relay {
			return nil
		}
		cfg := markergate.NudgeConfig{
			Marker:              markergate.MarkerPRIntent,
			Nonce:               r.env.RunNonce,
			OriginalOutcomeLine: r.st.outcomeLine,
			LogPath:             r.st.streamLog,
			SignalCarrier:       r.carrier,
			SignalStatus:        r.d.SignalStatus,
		}
		prompt, perr := markergate.RenderNudgePrompt(cfg)
		r.warn("marker-gate", perr)
		should, serr := markergate.ShouldNudgePRIntent(cfg)
		r.warn("marker-gate", serr)
		if !should {
			return nil
		}

		original := r.st.outcomeLine
		r.say("==> PR-intent marker missing — resuming the session once with a nudge")
		if err := r.resume(prompt); err != nil {
			return err
		}

		// Both logs were reassigned by the resume, so the scans below see the
		// resumed pass's own output.
		res, rerr := markergate.Resolve(markergate.ResolveConfig{
			Attempts:                 attempts,
			LogPath:                  r.st.streamLog,
			Nonce:                    r.env.RunNonce,
			ResumedOutcomeLine:       r.st.outcomeLine,
			ResumedDriverTextLogPath: r.st.textLog,
			OriginalOutcomeLine:      original,
			OutcomeViaBackstop:       r.st.viaBackstop,
			ResumeExitCode:           r.st.rc,
			SignalCarrier:            r.carrier,
			SignalStatus:             r.d.SignalStatus,
		})
		r.warn("marker-gate", rerr)

		// A crash in this best-effort nudge must not undo a terminal
		// backstop-declared ready run (issues #593, #2448).
		if res.ForceExitZero {
			r.say("==> PR-intent nudge resume failed (rc=%d) after a backstop-declared ready outcome — staying terminal (issue #593, #2448)", r.st.rc)
			r.st.rc = 0
		}
		if op := trimNewlines(res.OpLine); op != "" {
			r.say("%s", op)
		}
		// The line must stay emitted exactly once: reprint the original only
		// when the resume shadowed it with a near-miss, and leave a genuine
		// resumed verdict alone (issue #2448).
		if r.st.outcomeLine == "" {
			r.st.outcomeLine = original
			if restore := trimNewlines(res.OutcomeLine); restore != "" {
				r.say("==> resumed pass did not repeat the original SPINDRIFT_OUTCOME line — restoring it")
				r.say("%s", restore)
			}
		}
	}
	return nil
}

// demoteAlreadyResolved stops a commit-carrying branch behind a
// status=already-resolved claim from skipping review, CI and the merge gate
// while still closing the issue (issue #4016). Unguarded by the exit code: a
// crashed run's claim would close too.
func (r *boxRun) demoteAlreadyResolved() error {
	if r.adviseOnly || r.st.outcomeLine == "" {
		return nil
	}
	cfg, err := r.backstopConfig(false)
	if err != nil {
		return phaseErr("outcome-demotion", err)
	}
	line, err := r.capture(func(w io.Writer) error { return r.d.Demote(cfg, r.st.outcomeLine, w) })
	if err != nil {
		return phaseErr("outcome-demotion", err)
	}
	if line != "" {
		r.say("%s", line)
		r.st.outcomeLine = line
	}
	return nil
}

// bundleOut is the harness-owned code-out (ADR 0033, issues #1808, #2082). An
// advise-only kind never cuts the branch it would resolve.
func (r *boxRun) bundleOut() error {
	if r.adviseOnly || !r.needsBox {
		return nil
	}
	if r.in.WorkDir == "" || r.env.Branch == "" || r.in.OutboxDir == "" {
		return phaseErr("bundle-out", fmt.Errorf("work dir, BRANCH, and outbox dir are all required"))
	}
	err := r.d.BundleOut(bundleout.Config{
		Repo:             r.in.WorkDir,
		Base:             "origin/" + r.env.BaseBranch,
		Branch:           r.env.Branch,
		OutboxDir:        r.in.OutboxDir,
		Issue:            r.env.IssueNumber,
		PriorOutcomeLine: r.st.outcomeLine,
	}, r.d.Stdout)
	if err != nil {
		return phaseErr("bundle-out", err)
	}
	return nil
}

// backstopConfig reads the operator rebase/backoff knobs only here: the
// entrypoint dereferenced them only inside emit_outcome_backstop, so a
// malformed value is fatal only on a run that reaches the verb.
func (r *boxRun) backstopConfig(recoveryAttempted bool) (outcomebackstop.Config, error) {
	var knobs [3]int
	for i, name := range []string{"MAX_REBASE_ATTEMPTS", "TRANSIENT_BACKOFF_SECS", "HOLD_JITTER_SECS"} {
		n, err := strconv.Atoi(r.d.Getenv(name))
		if err != nil {
			return outcomebackstop.Config{}, fmt.Errorf("%s: %w", name, err)
		}
		knobs[i] = n
	}
	if r.in.WorkDir == "" || r.env.Branch == "" {
		return outcomebackstop.Config{}, fmt.Errorf("work dir and BRANCH are required")
	}
	return outcomebackstop.Config{
		Repo:               r.in.WorkDir,
		Issue:              r.env.DispatchKey,
		Branch:             r.env.Branch,
		Base:               "origin/" + r.env.BaseBranch,
		Kind:               r.kind,
		HostMediatedRemote: r.env.HostMediatedRemote,
		OutboxRelayCapable: r.env.OutboxRelayCapable,
		WriteEnabled:       r.env.BoxWriteEnabled,
		RecoveryAttempted:  recoveryAttempted,
		MaxAttempts:        knobs[0],
		Backoff:            time.Duration(knobs[1]) * time.Second,
		Jitter:             time.Duration(knobs[2]) * time.Second,
		Clock:              retry.RealClock(),
		RunStateFilePath:   runStateFile,
	}, nil
}

// capture mirrors bash's `$(...)`: the verb's whole stdout with trailing
// newlines trimmed.
func (r *boxRun) capture(fn func(io.Writer) error) (string, error) {
	var buf bytes.Buffer
	err := fn(&buf)
	return trimNewlines(buf.String()), err
}

func trimNewlines(s string) string { return strings.TrimRight(s, "\n") }

// resume runs one corrective orchestrator pass against the pinned session and
// refreshes the state the way run_driver_in_env did.
func (r *boxRun) resume(prompt string) error {
	handoff, driverName, err := r.strippedHandoff()
	if err != nil {
		return phaseErr("resume handoff", err)
	}
	d, err := driver.New(driverName)
	if err != nil {
		return phaseErr("resume", err)
	}

	promptFile, err := os.CreateTemp("", "box-prompt-")
	if err != nil {
		return phaseErr("resume", err)
	}
	defer os.Remove(promptFile.Name())
	// `$(...)` trimmed the prompt's trailing newlines before bash wrote it.
	if _, err := promptFile.WriteString(trimNewlines(prompt)); err != nil {
		promptFile.Close()
		return phaseErr("resume", err)
	}
	if err := promptFile.Close(); err != nil {
		return phaseErr("resume", err)
	}

	// The stream log outlives the call: the PR-intent gate scans it later.
	streamLog, err := os.CreateTemp("", "box-stream-")
	if err != nil {
		return phaseErr("resume", err)
	}
	streamLog.Close()

	argv := []string{
		"--handoff-file", handoff,
		"--prompt-file", promptFile.Name(),
		"--session-file", r.in.ResumeSessionFile,
		"--log-path", streamLog.Name(),
	}
	if r.needsBox {
		// Must match passmanifest.FileName.
		argv = append(argv, "--manifest-path", r.in.OutboxDir+"/manifest.json")
	}
	// `cmd || claude_rc=$?`: a zero exit leaves the earlier code in place.
	if rc := r.d.Orchestrate(argv); rc != 0 {
		r.st.rc = rc
	}

	text, err := d.ResultText(streamLog.Name())
	if err != nil {
		return phaseErr("resume extract", err)
	}
	stripped := outcome.StripResultText(text)
	textLog, err := os.CreateTemp("", "box-text-")
	if err != nil {
		return phaseErr("resume extract", err)
	}
	if _, err := textLog.WriteString(stripped); err != nil {
		textLog.Close()
		return phaseErr("resume extract", err)
	}
	if err := textLog.Close(); err != nil {
		return phaseErr("resume extract", err)
	}

	r.st.outcomeLine = outcome.ExtractOutcomeLine(stripped)
	r.st.streamLog = streamLog.Name()
	r.st.textLog = textLog.Name()
	if r.st.outcomeLine != "" {
		r.say("%s", r.st.outcomeLine)
	}
	return nil
}

// strippedHandoff writes a throwaway copy of the shared handoff with
// ReviewPromptFile cleared, so the resume stays a narrow single pass instead of
// re-entering the full review loop (issues #2065, #2975). Left on disk: this
// Box exits after one run.
func (r *boxRun) strippedHandoff() (path, driverName string, err error) {
	raw, err := os.ReadFile(r.in.HandoffFile)
	if err != nil {
		return "", "", err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", "", fmt.Errorf("parse handoff file %s: %w", r.in.HandoffFile, err)
	}
	if v, ok := fields["Driver"]; ok {
		if err := json.Unmarshal(v, &driverName); err != nil {
			return "", "", fmt.Errorf("handoff file %s: Driver: %w", r.in.HandoffFile, err)
		}
	}
	fields["ReviewPromptFile"] = json.RawMessage(`""`)

	f, err := os.CreateTemp("", "box-handoff-")
	if err != nil {
		return "", "", err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fields); err != nil {
		f.Close()
		return "", "", err
	}
	return f.Name(), driverName, f.Close()
}
