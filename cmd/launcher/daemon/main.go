// Command daemon drives daemon.Loop against a real operator checkout: it
// resolves each iteration's revision with git and runs each child with nix
// (issue #3538). It is a separate binary, not a launcher subcommand, because
// it cannot exec the launcher store path it was built against — that path is
// precisely the stale one a rebuild exists to replace — so it always shells
// out to `nix run` with the app attribute baked in (see runner.go).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/inputdoc"
)

// parsedArgs is the result of parsing argv: `--input <path>` plus an
// optional positional kind-set selector (dispatch|research, default both —
// see parseArgs).
type parsedArgs struct {
	InputPath string
	Kinds     []daemon.Kind
}

// parseArgs parses `daemon --input <path> [dispatch|research]`. With no
// positional verb the daemon draws from both kinds off one pool (issue
// #3541) — an operator stops having to choose between advancing work and
// enriching the backlog. `dispatch` alone keeps work-only operation, which
// is how an operator who has not created the research labels on their
// target repo runs the daemon; `research` alone restricts it to advise-only
// research.
func parseArgs(args []string) (parsedArgs, error) {
	var inputPath string
	var havePath bool
	var positional []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--input" {
			if i+1 >= len(args) {
				return parsedArgs{}, fmt.Errorf("flag --input requires a value")
			}
			inputPath = args[i+1]
			havePath = true
			i++
			continue
		}
		positional = append(positional, args[i])
	}
	if !havePath {
		return parsedArgs{}, fmt.Errorf("flag --input is required")
	}
	if len(positional) > 1 {
		return parsedArgs{}, fmt.Errorf("unexpected extra arguments: %v", positional[1:])
	}
	var kindArg string
	if len(positional) == 1 {
		kindArg = positional[0]
	}
	kinds, err := daemon.ParseKinds(kindArg)
	if err != nil {
		return parsedArgs{}, err
	}
	return parsedArgs{InputPath: inputPath, Kinds: kinds}, nil
}

// settingsKeys returns doc's settings keys, sorted, so the warning loop and
// the runner's stripped-key list both read from one source and come out in
// a fixed order for an operator — a Go map's own iteration order is random,
// which would otherwise reshuffle the warnings between runs. A nil doc or a
// nil Settings map (the JSON shape when the document carries no "settings"
// key at all) yields an empty slice rather than panicking.
func settingsKeys(doc *inputdoc.Document) []string {
	if doc == nil {
		return nil
	}
	keys := make([]string, 0, len(doc.Settings))
	for k := range doc.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// warnStrippedChildEnv prints one stderr line per key in keys that is
// actually set in the daemon's own environment (matching
// inputdoc.Document.Lookup's own "set" test: non-empty os.Getenv, so an
// exported-but-empty knob is not "set" here either). This is distinct from
// Lookup's deprecation warning: that one flags the daemon itself still
// honouring an ambient override; this one flags a key that a child will
// never see at all, because childEnv strips it before exec.
func warnStrippedChildEnv(keys []string, stderr io.Writer) {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			// Printing v mirrors inputdoc.Document.Lookup, which prints a
			// value too. These keys are whatever the --input document's
			// settings carry — inputdoc.Load validates none of them against
			// the schema — but that document is an operator-supplied trusted
			// input written by lib/mkHarness.nix's documentSettings, which
			// draws only on flakeOption schema entries, and
			// lib/env-schema.nix marks no entry both flakeOption and
			// secret. So no credential reaches this line.
			fmt.Fprintf(stderr, "%s=%s set in environment — not forwarded to children; use the --input document's settings.%s\n", key, v, key)
		}
	}
}

// parseSlots turns MAX_PARALLEL's resolved string value into the daemon's
// pool size. lib/env-schema.nix declares it intKind = "positive".
func parseSlots(raw string) (int, error) {
	return inputdoc.ParseInt("MAX_PARALLEL", "positive integer", raw, 1)
}

// parseResearchReservation turns RESEARCH_RESERVATION's resolved string
// value into the daemon's research slot floor. lib/env-schema.nix declares
// it intKind = "nonneg" and bounds it at MAX_PARALLEL; the upper bound is
// this knob's own, since inputdoc.ParseInt only ever enforces a lower one.
func parseResearchReservation(raw string, slots int) (int, error) {
	n, err := inputdoc.ParseInt("RESEARCH_RESERVATION", "non-negative integer", raw, 0)
	if err != nil {
		return 0, err
	}
	if n > slots {
		return 0, fmt.Errorf("RESEARCH_RESERVATION (%d) must not exceed MAX_PARALLEL (%d)", n, slots)
	}
	return n, nil
}

// parseIdleFloor turns DAEMON_IDLE_FLOOR's resolved string value into the
// pool-wide idle backoff's starting wait (backoff.go).
func parseIdleFloor(raw string) (time.Duration, error) {
	return inputdoc.ParseDuration("DAEMON_IDLE_FLOOR", "positive duration", raw, time.Nanosecond)
}

// parseIdleCap turns DAEMON_IDLE_CAP's resolved string value into the idle
// backoff's ceiling, and rejects a cap below floor: the backoff doubles up
// from DAEMON_IDLE_FLOOR (backoff.go), so a cap under it would make the
// wait shrink partway through a jammed kind's doubling instead of climbing.
func parseIdleCap(raw string, floor time.Duration) (time.Duration, error) {
	idleCap, err := inputdoc.ParseDuration("DAEMON_IDLE_CAP", "positive duration", raw, time.Nanosecond)
	if err != nil {
		return 0, err
	}
	if idleCap < floor {
		return 0, fmt.Errorf("DAEMON_IDLE_CAP (%v) must not be less than DAEMON_IDLE_FLOOR (%v)", idleCap, floor)
	}
	return idleCap, nil
}

// parseFailureBackoff turns DAEMON_FAILURE_BACKOFF's resolved string value
// into the per-slot wait after an unclassified child failure. Unlike the
// idle and breaker knobs, zero is legal here (retry immediately), so this
// passes inputdoc.ParseDuration a min of 0 rather than the one-nanosecond
// min that rejects "0s" for the others.
func parseFailureBackoff(raw string) (time.Duration, error) {
	return inputdoc.ParseDuration("DAEMON_FAILURE_BACKOFF", "non-negative duration", raw, 0)
}

// parseBreakerThreshold turns DAEMON_BREAKER_THRESHOLD's resolved string
// value into the circuit breaker's trip count. lib/env-schema.nix declares
// it intKind = "positive".
func parseBreakerThreshold(raw string) (int, error) {
	return inputdoc.ParseInt("DAEMON_BREAKER_THRESHOLD", "positive integer", raw, 1)
}

// parseBreakerWindow turns DAEMON_BREAKER_WINDOW's resolved string value
// into the circuit breaker's trailing window.
func parseBreakerWindow(raw string) (time.Duration, error) {
	return inputdoc.ParseDuration("DAEMON_BREAKER_WINDOW", "positive duration", raw, time.Nanosecond)
}

// nixSystemDouble maps Go's GOOS/GOARCH to the nix system double the self
// check's flake attribute path needs (daemon.SelfSpec.System). It takes both
// as parameters, rather than reading runtime.GOOS/runtime.GOARCH itself, so
// the mapping is testable without build tags. An unmapped pair returns an
// error naming it rather than guessing: a wrong double would evaluate a
// system that does not exist and report a phantom self-change.
func nixSystemDouble(goos, goarch string) (string, error) {
	unmapped := func() error {
		return fmt.Errorf("daemon: no nix system double for GOOS/GOARCH %s/%s", goos, goarch)
	}
	var arch string
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", unmapped()
	}
	// The OS half needs no translation — nix spells these two exactly as Go
	// does — but it still needs screening, or an unmapped GOOS would sail
	// through the join below as a double no flake output exists for.
	if goos != "linux" && goos != "darwin" {
		return "", unmapped()
	}
	return arch + "-" + goos, nil
}

// gitRevParse runs `git rev-parse <flag>` in dir and returns its trimmed
// stdout, folding git's own stderr into the error on failure. Shared by
// repoRoot and gitDir so the exec+stderr-capture plumbing exists once.
func gitRevParse(dir, flag string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", flag)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// git's own stderr, not just the bare error: the likeliest cause is
		// a `safe.directory` refusal, and without this the fail-fast
		// diagnostic reduces to "exit status 128" (same shape as
		// hostRunner.ResolveRevision's fetch error).
		return "", fmt.Errorf("not a git checkout: %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// repoRoot resolves the git checkout root containing dir via `git rev-parse
// --show-toplevel`, run in dir. Failing fast here — rather than letting a
// subdirectory-relative repoPath reach the first child — turns a
// misconfigured working directory into a startup error instead of a
// git+file:// flakeref pointing at a non-root that only breaks the first
// child invocation.
func repoRoot(dir string) (string, error) {
	return gitRevParse(dir, "--show-toplevel")
}

// gitDir resolves dir's checkout git dir via `git rev-parse
// --absolute-git-dir`, run in dir. The checkout lock (issue #3543) lands
// there rather than under repoRoot: the daemon's contract is that it never
// mutates the operator's working tree (docs/reference.md), and
// --absolute-git-dir is also per-checkout for a linked `git worktree`,
// which is exactly the granularity "one daemon per checkout" needs.
func gitDir(dir string) (string, error) {
	return gitRevParse(dir, "--absolute-git-dir")
}

// fail prints one daemon diagnostic and yields mainRun's error exit code, so
// each startup step below stays a single guarded line.
func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "daemon: %s\n", err)
	return 1
}

// cmdStatus implements `daemon status`: read-only, so unlike the rest of
// mainRun it needs only the git dir — never the --input document, a knob,
// or the repo root. An operator querying a checkout has no work to
// configure, so requiring any of those would be a needless barrier to
// asking "what is running".
func cmdStatus(wd string, stdout, stderr io.Writer) int {
	gitDirPath, err := gitDir(wd)
	if err != nil {
		return fail(stderr, err)
	}
	report, err := daemon.ReadStatus(gitDirPath)
	if err != nil {
		// ReadStatus's contract: on error the report still carries
		// whatever the lock probe established, so an operator learns a
		// daemon holds the checkout even though a garbage status file
		// makes the exit code 1.
		fmt.Fprintln(stderr, summarizeStatus(report))
		return fail(stderr, err)
	}

	// stdout is the machine-readable answer, one JSON object plus a
	// newline: this binary's existing contract is "stdout is the machine
	// stream only" (the durable event-JSON-lines stream, elsewhere in this
	// file; a child's own stdout/stderr and every human-facing message go
	// to stderr instead), and status keeps that line rather than carving
	// an exception for itself.
	data, err := json.Marshal(report)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "%s\n", data)

	fmt.Fprintln(stderr, summarizeStatus(report))

	// 0 whenever an answer was produced, including "no daemon running":
	// the JSON's `live` field is the answer a scripting caller reads, not
	// the exit code, and this binary's own exit-code taxonomy
	// (exitCodeFor) already spends 1 on a genuine failure — reusing it
	// here for "nothing is running" would conflate the two.
	return 0
}

// summarizeSlot renders one SlotStatus compactly for summarizeStatus's
// stderr line: idle slots need only their number, a busy slot names its
// kind and, when the child has announced any, the issues it is in flight
// on.
func summarizeSlot(s daemon.SlotStatus) string {
	if !s.Busy {
		return fmt.Sprintf("%d:idle", s.Slot)
	}
	if len(s.Issues) == 0 {
		return fmt.Sprintf("%d:busy(%s)", s.Slot, s.Kind)
	}
	return fmt.Sprintf("%d:busy(%s #%s)", s.Slot, s.Kind, strings.Join(s.Issues, ",#"))
}

// summarizeStatus renders cmdStatus's one human sentence for stderr. It
// covers all four cases ReadStatus's doc distinguishes, checked in this
// order because the lock, not the status file, is the liveness truth: a
// held lock always wins over what the (possibly stale or absent) status
// file says, and only an unheld lock lets a present file mean "stale".
func summarizeStatus(report daemon.StatusReport) string {
	switch {
	case report.Live:
		status := report.Status
		slots := make([]string, 0, len(status.Slots))
		for _, s := range status.Slots {
			slots = append(slots, summarizeSlot(s))
		}
		return fmt.Sprintf("daemon: live, state=%s, pid=%d, slots=[%s]", status.State, status.Pid, strings.Join(slots, " "))
	case report.LockHeld && report.Status == nil:
		return fmt.Sprintf("daemon: a daemon holds this checkout but has not published its status yet (%s)", report.Holder)
	case report.LockHeld:
		return fmt.Sprintf("daemon: a daemon holds this checkout, but the published status file is a predecessor's leftover (pid=%d, state=%s), not this holder's (%s)", report.Status.Pid, report.Status.State, report.Holder)
	case report.Stale:
		return fmt.Sprintf("daemon: stale — no daemon is running; last published state=%s at %s", report.Status.State, report.Status.Time)
	default:
		return "daemon: no daemon has run in this checkout"
	}
}

// hostClock is the production daemon.Clock: Now is time.Now, Sleep waits d
// or returns early on ctx cancellation, so an operator stop during the idle
// wait is honoured immediately rather than after the full interval.
type hostClock struct{}

func (hostClock) Now() time.Time { return time.Now() }

func (hostClock) Sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// isOperatorStop reports whether reason (daemon.Loop's return value) reflects
// an operator-requested stop rather than a real failure: either the loop's
// own ctx was cancelled (a signal arrived between iterations, no child
// running) or the running child drained and reported exit 7 / "signalled-stop"
// (a forwarded SIGTERM reached it mid-run — see runner.go's forwardStop).
// Anything else — a resolve/run-child error or a Halt-mapped exit like
// host-tainted or config-invalid — is a real failure.
func isOperatorStop(reason string) bool {
	return strings.HasPrefix(reason, "context-cancelled") || reason == "outcome: signalled-stop"
}

// handleStopSignals waits for the first two signals on sig and forwards
// each: the first stops the daemon from filling any more slots (cancel) and
// forwards a SIGTERM so anything already running starts draining right away
// instead of only being noticed once it exits on its own; the second
// forwards again, which hostRunner.forwardStop's own count turns into the
// escalation (issue #3521's child launcher aborts a drain on its second
// signal). The daemon implements no drain or reap of its own — Loop only
// checks ctx.Err() between iterations and always waits out a started child
// (daemon.Loop's doc), and it is daemon.Loop's own wg.Wait() that waits the
// children out, here and after escalation alike. The kind of signal never
// matters, only first versus second — same contract as
// cmd/launcher/main.go's relaySignals/notifyStopSignal. A third and later
// signal is a no-op: this function returns after the second and nothing
// else ever reads sig again.
//
// quit lets a caller unpark this goroutine when no second signal ever
// arrives — mainRun is driven repeatedly under test, and without a way out
// each call would leak a goroutine blocked on <-sig forever. signal.Stop is
// deliberately not called: Go's own handler stays installed, so the third
// signal above is swallowed in the buffer rather than killing the daemon
// outright.
func handleStopSignals(sig <-chan os.Signal, quit <-chan struct{}, cancel context.CancelFunc, forward func(), em *daemon.Emitter) {
	select {
	case <-sig:
	case <-quit:
		return
	}
	em.Emit(daemon.Event{Event: "shutdown", Reason: daemon.ShutdownDrain})
	cancel()
	forward()

	select {
	case <-sig:
	case <-quit:
		return
	}
	em.Emit(daemon.Event{Event: "shutdown", Reason: daemon.ShutdownEscalate})
	forward()
}

// preflightRunner is the slice of the runner startupPreflight needs — a
// narrow interface distinct from daemon.Runner (the *loop's* seam), so a
// test can drive startupPreflight with a small fake without daemon.Runner
// growing a RunDoctor method the loop itself never calls. *hostRunner
// already satisfies it.
type preflightRunner interface {
	ResolveRevision(ctx context.Context) (string, error)
	RunDoctor(ctx context.Context, revision string) (int, error)
}

// startupPreflight runs `doctor` exactly once, before the pool exists, and
// reports why the daemon must not start — "" means it may. It always emits
// one "preflight" event on every path — pass, refusal, seam failure, or an
// operator's Ctrl-C — so a reader of the event stream can tell "ran and was
// healthy" apart from "never ran"; every path but a pass also emits a
// "halt" event carrying the same operator-facing reason returned here.
func startupPreflight(ctx context.Context, r preflightRunner, em *daemon.Emitter) string {
	// Resolve at the same freshly fetched tip the first child will run at,
	// so the preflight validates the build about to actually run rather
	// than the operator's possibly-stale working tree.
	revision, err := r.ResolveRevision(ctx)
	// refuseCancelled reports an operator's Ctrl-C during the preflight. Its
	// reason deliberately carries no HaltPreflightPrefix: mainRun's
	// isOperatorStop tells a stop (exit 0) from a refusal (exit 11) by
	// matching "context-cancelled" as a *prefix*, so putting anything in
	// front of it would displace that match and turn a Ctrl-C into a refusal.
	refuseCancelled := func() string {
		return preflightRefusal(em, revision, "doctor-cancelled", "context-cancelled: "+ctx.Err().Error())
	}
	// Cancellation is checked against ctx rather than against err: a seam
	// whose child was signal-killed can return any shape at all (including
	// a nil error), and an operator's Ctrl-C must read as a clean stop
	// however it surfaced.
	if ctx.Err() != nil {
		return refuseCancelled()
	}
	if err != nil {
		return preflightRefusal(em, revision, "doctor-seam-error", daemon.HaltPreflightPrefix+"resolve-revision: "+err.Error())
	}

	exit, err := r.RunDoctor(ctx, revision)
	if ctx.Err() != nil {
		return refuseCancelled()
	}
	if err != nil {
		return preflightRefusal(em, revision, "doctor-seam-error", daemon.HaltPreflightPrefix+"run-doctor: "+err.Error())
	}

	v := daemon.ClassifyPreflight(exit)
	em.Emit(daemon.Event{Event: "preflight", Revision: revision, Exit: &exit, Outcome: v.Outcome})
	if v.Healthy {
		return ""
	}
	reason := v.HaltReason()
	em.Emit(daemon.Event{Event: "halt", Reason: reason})
	return reason
}

// preflightRefusal emits the event pair every startupPreflight path that
// never got a doctor verdict emits — the "preflight" event proving doctor
// was attempted, then the "halt" event — and returns reason for mainRun.
// Neither carries an `exit` field, there being no exit code to report, and
// outcome stays "doctor-"-prefixed like ClassifyPreflight's own labels so a
// preflight outcome can never be read as a child_finish one.
func preflightRefusal(em *daemon.Emitter, revision, outcome, reason string) string {
	em.Emit(daemon.Event{Event: "preflight", Revision: revision, Outcome: outcome, Reason: reason})
	em.Emit(daemon.Event{Event: "halt", Reason: reason})
	return reason
}

// publishHaltedStatus writes a StateHalted status naming reason directly to
// sw, for a caller with no pool yet to snapshot through — the preflight
// refusal below, before daemon.Loop (and therefore any pool) ever exists. A
// write failure is reported to stderr and otherwise ignored, matching
// pool.publish's own advisory-only handling.
func publishHaltedStatus(stderr io.Writer, sw *daemon.StatusWriter, kinds []daemon.Kind, reason string) {
	if err := sw.Write(daemon.Status{Kinds: kinds, State: daemon.StateHalted, Reason: reason}); err != nil {
		fmt.Fprintf(stderr, "daemon: status file write failed: %v\n", err)
	}
}

// mainRun holds everything main() does: argv parse, input document load,
// knob resolution, repo root, signal wiring, and daemon.Loop, returning the
// exit code rather than calling os.Exit so tests can drive it repeatedly
// with different argv (mirrors cmd/launcher/driver-exec/main.go's mainRun).
func mainRun(argv []string, stdout, stderr io.Writer) int {
	// `status` is dispatched here, ahead of parseArgs, not folded into it:
	// parseArgs's one positional slot is the kind selector (dispatch |
	// research, see its own doc), and overloading that same slot with a
	// verb would make `daemon status dispatch` parse as a kind selector of
	// "status dispatch" rather than the status verb it plainly reads as.
	if len(argv) > 0 && argv[0] == "status" {
		if len(argv) > 1 {
			return fail(stderr, fmt.Errorf("status takes no arguments, got: %v", argv[1:]))
		}
		wd, err := os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
		return cmdStatus(wd, stdout, stderr)
	}

	args, err := parseArgs(argv)
	if err != nil {
		return fail(stderr, err)
	}

	doc, err := inputdoc.Load(args.InputPath)
	if err != nil {
		return fail(stderr, err)
	}

	// Computed once here, right after the document loads (the earliest
	// point the key set is known) and reused below for the runner config,
	// so the warning and the actual strip act on the same list.
	strippedKeys := settingsKeys(doc)
	warnStrippedChildEnv(strippedKeys, stderr)

	appAttr, err := doc.Resolve("DAEMON_APP", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	baseBranch, err := doc.Resolve("BASE_BRANCH", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	maxParallelRaw, err := doc.Resolve("MAX_PARALLEL", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	slots, err := parseSlots(maxParallelRaw)
	if err != nil {
		return fail(stderr, err)
	}

	// The five backoff/breaker tuning knobs, resolved and validated here —
	// before startupPreflight, before any slot fills, before any Box runs —
	// so a bad value refuses the start cleanly rather than surfacing as a
	// daemon.Loop halt mid-run.
	idleFloorRaw, err := doc.Resolve("DAEMON_IDLE_FLOOR", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	idleFloor, err := parseIdleFloor(idleFloorRaw)
	if err != nil {
		return fail(stderr, err)
	}
	idleCapRaw, err := doc.Resolve("DAEMON_IDLE_CAP", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	idleCap, err := parseIdleCap(idleCapRaw, idleFloor)
	if err != nil {
		return fail(stderr, err)
	}
	failureBackoffRaw, err := doc.Resolve("DAEMON_FAILURE_BACKOFF", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	failureBackoff, err := parseFailureBackoff(failureBackoffRaw)
	if err != nil {
		return fail(stderr, err)
	}
	breakerThresholdRaw, err := doc.Resolve("DAEMON_BREAKER_THRESHOLD", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	breakerThreshold, err := parseBreakerThreshold(breakerThresholdRaw)
	if err != nil {
		return fail(stderr, err)
	}
	breakerWindowRaw, err := doc.Resolve("DAEMON_BREAKER_WINDOW", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	breakerWindow, err := parseBreakerWindow(breakerWindowRaw)
	if err != nil {
		return fail(stderr, err)
	}

	// The schema validates DAEMON_AWAKE_WINDOW at Nix eval time, but an
	// ambient env override (Lookup above) bypasses that entirely, so
	// this runtime parse is the actual guarantee.
	awakeRaw := doc.ResolveOptional("DAEMON_AWAKE_WINDOW", stderr)
	awake, err := daemon.ParseWindow(awakeRaw)
	if err != nil {
		return fail(stderr, err)
	}

	// SPINDRIFT_DAEMON_PROGRAM is exported by the generated wrapper
	// (lib/mkHarness.nix's daemonWrapper) as this daemon's own store path,
	// read at runtime because a derivation cannot interpolate its own store
	// path into its own text. A daemon started any other way (e.g. `go run`
	// during development) cannot know its own build, so it must not guess:
	// the self check stays off, but visibly so — a safety property silently
	// off is worse than one an operator can see is off.
	selfProgram := os.Getenv("SPINDRIFT_DAEMON_PROGRAM")
	var selfAttr, nixSystem string
	if selfProgram == "" {
		fmt.Fprintln(stderr, "daemon: SPINDRIFT_DAEMON_PROGRAM is unset, so the self-change check is disabled (not started through the generated wrapper)")
	} else {
		selfAttr, err = doc.Resolve("DAEMON_SELF_APP", stderr)
		if err != nil {
			return fail(stderr, err)
		}
		nixSystem, err = nixSystemDouble(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return fail(stderr, err)
		}
	}

	// RESEARCH_RESERVATION is inert for a single-kind daemon (both the
	// schema doc and daemon.Config say so), so it is resolved and validated
	// only when the positional verb actually put both kinds in play. A
	// work-only operator (no research labels created yet) must not be
	// failed at startup by a reservation value that happens to exceed their
	// MAX_PARALLEL — the knob simply does not apply to their run.
	var reservation int
	if len(args.Kinds) > 1 {
		researchReservationRaw, err := doc.Resolve("RESEARCH_RESERVATION", stderr)
		if err != nil {
			return fail(stderr, err)
		}
		reservation, err = parseResearchReservation(researchReservationRaw, slots)
		if err != nil {
			return fail(stderr, err)
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	repoPath, err := repoRoot(wd)
	if err != nil {
		return fail(stderr, err)
	}

	clk := hostClock{}
	em := daemon.NewEmitter(stdout, clk.Now)

	gitDirPath, err := gitDir(wd)
	if err != nil {
		return fail(stderr, err)
	}
	lock, err := daemon.AcquireCheckoutLock(gitDirPath, args.Kinds)
	if err != nil {
		// Report both to the operator (stderr, via fail below) and to the
		// durable event stream, so a reader of either sees the same halt.
		em.Emit(daemon.Event{Event: "halt", Reason: daemon.HaltInstanceLockPrefix + err.Error()})
		return fail(stderr, err)
	}
	// No cleanup on the crash path: the kernel drops the flock when the
	// holder dies (including SIGKILL), so a deferred Release here only
	// needs to cover the ordinary return path.
	defer func() { _ = lock.Release() }()

	// Built only now, after the lock acquire above succeeded: the status
	// file belongs to the daemon that actually holds the checkout, and a
	// refused second instance (the branch above) must never stomp the
	// live holder's file. There is no matching cleanup on the ordinary
	// return path below, and that is deliberate, not a missing defer: the
	// last state this writer publishes is StateHalted with its Reason, and
	// a reader who finds the lock unheld (ReadStatus) already reports the
	// file stale — its contents are then a last-known record of how this
	// run ended, not a lie, so deleting it on exit would only destroy
	// information a stale read is designed to surface.
	statusWriter := daemon.NewStatusWriter(gitDirPath, clk.Now)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := newHostRunner(hostRunnerConfig{
		repoPath:   repoPath,
		appAttr:    appAttr,
		baseBranch: baseBranch,
		selfAttr:   selfAttr,
		nixSystem:  nixSystem,
		// Snapshotted once here, not per-child: nothing between mainRun's
		// entry and this line calls os.Setenv, so it's still a startup capture.
		env:   os.Environ(),
		knobs: strippedKeys,
	})

	// Buffered at 2, not 1, so a signal isn't dropped for want of room
	// between receives — see notifyStopSignal's buffer-of-2 reasoning.
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	quit := make(chan struct{})
	defer close(quit)
	go handleStopSignals(sig, quit, cancel, r.forwardStop, em)

	// After the signal wiring, so an operator's Ctrl-C during the preflight
	// is honoured; before Loop, so a refusal happens before any slot, any
	// claim, any Box. After AcquireCheckoutLock above, so a second daemon
	// against the same checkout is still refused by the lock's own cheaper
	// path rather than after a full doctor run.
	if reason := startupPreflight(ctx, r, em); reason != "" {
		fmt.Fprintf(stderr, "daemon: %s\n", reason)
		publishHaltedStatus(stderr, statusWriter, args.Kinds, reason)
		if isOperatorStop(reason) {
			return 0
		}
		return exitPreflightFailed
	}

	cfg := daemon.Config{
		Kinds:               args.Kinds,
		ResearchReservation: reservation,
		IdleFloor:           idleFloor,
		IdleCap:             idleCap,
		Slots:               slots,
		SelfProgram:         selfProgram,
		FailureBackoff:      failureBackoff,
		BreakerThreshold:    breakerThreshold,
		BreakerWindow:       breakerWindow,
		Awake:               awake,
		Status:              statusWriter,
	}

	reason := daemon.Loop(ctx, cfg, r, em, clk)

	return exitCodeFor(reason)
}

// exitSelfChanged is the daemon's own exit code for the one halt an
// operator may want to act on automatically: its build changed at the
// fetched tip. It is deliberately distinct from every other code this
// binary returns (0 clean stop, 1 anything else) so a service unit can
// restart on it alone — the daemon never re-execs itself, so composing
// this exit with a restart policy is how an operator opts into
// self-update, by choice rather than by default. It sits outside the
// 0-7 band the *child* launcher's exit codes occupy (Interpret,
// cmd/launcher/internal/daemon/outcome.go) so the two taxonomies cannot
// be confused when both appear in one log.
const exitSelfChanged = 10

// exitPreflightFailed is the daemon's own exit code for a refused start: the
// startup doctor preflight found a Required-tier failure (missing triage
// labels, an invalid config) or could not run at all. It is 11, not a
// pass-through of doctor's own 1/2/3/4 (a different table where the same
// integers mean something else) nor of the *child* launcher's 0-7 band
// (Interpret) — sharing either would let a reader misattribute this halt to
// the wrong process's contract. Unlike exitSelfChanged (10), which an
// operator deliberately composes with a restart policy, a restart cannot
// clear this one: nothing the daemon does fixes a missing label or an
// undersized VM, so a supervisor must not treat this code as retryable.
const exitPreflightFailed = 11

// exitCodeFor maps daemon.Loop's halt reason to this process's exit code.
func exitCodeFor(reason string) int {
	if isOperatorStop(reason) {
		return 0
	}
	if strings.HasPrefix(reason, daemon.HaltSelfChanged+":") {
		return exitSelfChanged
	}
	// A plain non-zero exit: the event stream already carries the specific
	// halt reason as structured JSON, so stderr/exit code need only say
	// "this was not a clean stop" for a process supervisor to act on.
	return 1
}

func main() {
	os.Exit(mainRun(os.Args[1:], os.Stdout, os.Stderr))
}
