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
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/connectorsetup"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/driver/bundle" // the bundled Drivers; its init registers the rest
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/exitcode"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/launcherchecks"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/stopsignal"
)

// installStopSignal is a test seam, like cmd/launcher/main.go's: tests drive
// the latch through fake stop/abort channels instead of real signals.
var installStopSignal = stopsignal.Notify

// inputFlag: mainRun's status-verb check skips leading pairs of it.
const inputFlag = "--input"

// handoffFlag names the descriptor an in-place restart predecessor left the
// handoff document on. mainRun strips it before anything else parses argv, and
// successorArgs never replays it.
const handoffFlag = "--handoff-fd"

// parsedArgs is parsed argv; see parseArgs.
type parsedArgs struct {
	InputPath        string
	Kinds            []daemon.Kind
	ExplicitSelector bool   // true when argv named a kind selector; false for the bare "every kind" default (see gateKinds)
	FeatureBranch    string // empty when --feature-branch is absent; see hostRunner.featureBranch
	CheckOnly        bool   // --check-only: validate input and config, exit 0 or 1 with the startup diagnostic (see mainRun)
	RestartInPlace   bool   // --restart-in-place: exec the prebuilt daemon on a self-change, keeping children running (ADR 0063)
}

// parseArgs parses `daemon --input <path> [--feature-branch <branch>]
// [--check-only] [--restart-in-place] [dispatch|research|butler|recover]`,
// flags in any order. No selector draws every kind off one pool (issues
// #3541, #3878); `dispatch` alone is how an operator without the research
// labels runs it; `recover` is queue-mode `spindrift recover` (#4656). The
// bare default's butler and recover stay provisional until gateKinds, which
// has the document to resolve their knobs against.
func parseArgs(args []string) (parsedArgs, error) {
	var inputPath string
	var havePath bool
	var featureBranch string
	var checkOnly, restartInPlace bool
	var positional []string
	for i := 0; i < len(args); i++ {
		if args[i] == inputFlag {
			if i+1 >= len(args) {
				return parsedArgs{}, fmt.Errorf("flag %s requires a value", inputFlag)
			}
			inputPath = args[i+1]
			havePath = true
			i++
			continue
		}
		if args[i] == "--check-only" {
			checkOnly = true
			continue
		}
		if args[i] == "--restart-in-place" {
			restartInPlace = true
			continue
		}
		if args[i] == "--feature-branch" {
			if i+1 >= len(args) || args[i+1] == "" {
				return parsedArgs{}, fmt.Errorf("flag --feature-branch requires a non-empty value")
			}
			v := args[i+1]
			// A forgotten value must not swallow the kind selector or the
			// next flag as the branch name.
			if _, ok := dispatchkind.ByVerb(v); ok || strings.HasPrefix(v, "-") {
				return parsedArgs{}, fmt.Errorf("flag --feature-branch requires a branch name, got %q", v)
			}
			featureBranch = v
			i++
			continue
		}
		positional = append(positional, args[i])
	}
	if !havePath {
		return parsedArgs{}, fmt.Errorf("flag %s is required", inputFlag)
	}
	if len(positional) > 1 {
		return parsedArgs{}, fmt.Errorf("unexpected extra arguments: %v", positional[1:])
	}
	var kindArg string
	explicitSelector := len(positional) == 1
	if explicitSelector {
		kindArg = positional[0]
	}
	kinds, err := daemon.ParseKinds(kindArg)
	if err != nil {
		return parsedArgs{}, err
	}
	return parsedArgs{InputPath: inputPath, Kinds: kinds, ExplicitSelector: explicitSelector, FeatureBranch: featureBranch, CheckOnly: checkOnly, RestartInPlace: restartInPlace}, nil
}

// errNoOutboxRelay is why a recover kind is disabled: the forge is not a
// pull-request forge, so no stranded bundle awaits landing as a PR.
var errNoOutboxRelay = fmt.Errorf("no pull-request outbox to recover (recover needs CODE_FORGE %s)", strings.Join(backend.OutboxRelayBackends(), " or "))

// gateKnobs are the resolved knob values gateKinds judges each kind's
// Enablement row by.
type gateKnobs struct {
	chores    chore.Knobs
	codeForge string
}

// gateKinds drops from a bare invocation every kind whose Enablement row is
// unmet, so a daemon without Chores runs the other kinds instead of being
// refused by `doctor --butler`, and one without a PR outbox runs without
// recover; an explicitly named kind fails startup instead. A surviving
// butler's config is validated by that doctor run (issue #3920). Chores are
// loaded lazily, so a daemon with no EnabledByChores kind never fails on
// butler config it does not use. chore.Load returns early with no Chores set,
// so only a daemon with a Chore enabled fails on bad BUTLER_EVERY or
// BUTLER_CHORE_CLASSES.
func gateKinds(kinds []daemon.Kind, explicitSelector bool, knobs gateKnobs) ([]daemon.Kind, error) {
	loadChores := sync.OnceValues(func() ([]chore.Chore, error) { return chore.Load(knobs.chores) })
	kept := make([]daemon.Kind, 0, len(kinds))
	for _, k := range kinds {
		d, ok := dispatchkind.ByVerb(string(k))
		if !ok {
			return nil, fmt.Errorf("unknown kind %q", k)
		}
		var disabled error
		switch d.Enablement {
		case dispatchkind.EnabledAlways:
		case dispatchkind.EnabledByChores:
			resolved, err := loadChores()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", d.Verb, err)
			}
			if len(resolved) == 0 {
				disabled = chore.ErrNoChores
			}
		case dispatchkind.EnabledByOutboxRelay:
			if !backend.RelaysOutbox(knobs.codeForge) {
				disabled = fmt.Errorf("CODE_FORGE=%s: %w", knobs.codeForge, errNoOutboxRelay)
			}
		default:
			return nil, fmt.Errorf("%s: unknown enablement %d", d.Verb, d.Enablement)
		}
		switch {
		case disabled == nil:
			kept = append(kept, k)
		case explicitSelector:
			return nil, fmt.Errorf("%s selected but %w", d.Verb, disabled)
		}
	}
	return kept, nil
}

// doctorPreflightFlags returns doctor's preflight kind flags from each kind's
// Preflight row. kinds must be post-gateKinds, so a dropped kind never
// refuses startup over its own config. A PreflightWhenNamed kind is promoted
// only when named, so a repo without, say, the research labels still starts
// under the bare selector.
func doctorPreflightFlags(kinds []daemon.Kind, explicitSelector bool) ([]string, error) {
	var flags []string
	for _, k := range kinds {
		d, ok := dispatchkind.ByVerb(string(k))
		if !ok {
			return nil, fmt.Errorf("unknown kind %q", k)
		}
		if f := d.Preflight.Flag(explicitSelector); f != "" {
			flags = append(flags, f)
		}
	}
	return flags, nil
}

// strippedKeys returns the keys of doc's settings that count (Document.Setting),
// sorted so warnings come out in a stable order. It is the child strip list: a
// key whose value does not count is left for the child to inherit from the
// ambient env. A nil doc or nil Settings yields an empty slice.
func strippedKeys(doc *inputdoc.Document) []string {
	if doc == nil {
		return nil
	}
	keys := make([]string, 0, len(doc.Settings))
	for k := range doc.Settings {
		if _, ok := doc.Setting(k); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// warnStrippedChildEnv warns for each of keys set (non-empty, as
// inputdoc.Document.Lookup tests it) in the daemon's own environment.
// withoutKeys (internal/daemon/command.go) strips each before exec, so no
// child sees it. The daemon ignores it too, except an inputdoc.IsDaemonOnly key, which it reads through
// Document.Lookup so an ambient value still wins (ADR 0020;
// TestDaemonOnlyKnobs_MatchesDocLookupSites). Every other knob resolves
// document-first via childKnob, as a child sees it (issue #4623).
func warnStrippedChildEnv(keys []string, stderr io.Writer) {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			// Printing v is safe: the keys come from mkHarness's
			// documentSettings, which draws only on flakeOption entries, and
			// lib/env-schema.nix marks none both flakeOption and secret.
			scope := "ignored by the daemon and its children"
			if inputdoc.IsDaemonOnly(key) {
				scope = "not forwarded to children"
			}
			fmt.Fprintf(stderr, "%s=%s set in environment — %s; use the --input document's settings.%s\n", key, v, scope, key)
		}
	}
}

// resolveDriverEnv applies the baked-Driver-wins rule (ADR 0064, issue #5050)
// to the daemon, printing the mismatch warning once. Children and the doctor
// preflight run through the wrapper, which re-sources harness.env and
// restores DRIVER, so stripping it cannot work; instead this returns a
// DriverWarnedEnv entry for their envs so they don't warn again, or nil when
// there is nothing to warn about.
func resolveDriverEnv(doc *inputdoc.Document, stderr io.Writer) []string {
	var baked string
	if doc != nil {
		baked = doc.Artifacts.Shared.Driver.String()
	}
	ambient := os.Getenv("DRIVER")
	_, warning := inputdoc.ResolveDriver(baked, ambient, inputdoc.DriverWarned())
	if warning == "" {
		return nil
	}
	fmt.Fprint(stderr, warning)
	return []string{inputdoc.DriverWarnedEnv + "=" + inputdoc.DriverMarker(ambient, baked)}
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
// pool-wide idle backoff's starting wait (internal/daemon/backoff.go).
func parseIdleFloor(raw string) (time.Duration, error) {
	return inputdoc.ParseDuration("DAEMON_IDLE_FLOOR", "positive duration", raw, time.Nanosecond)
}

// parseIdleCap turns DAEMON_IDLE_CAP's resolved string value into the idle
// backoff's ceiling, and rejects a cap below floor: the backoff doubles up
// from DAEMON_IDLE_FLOOR (internal/daemon/backoff.go), so a cap under it would make the
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
// idle and breaker knobs, zero is legal (retry immediately).
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

// validateSignalCarrier mirrors the launcher's validateChoice
// (cmd/launcher/flags.go). Empty means unset: the knob is env-only, so the
// child gets no --box-signal-carrier flag and takes the schema default.
func validateSignalCarrier(raw string) error {
	e, _ := inputdoc.SchemaFlag("BOX_SIGNAL_CARRIER")
	if raw == "" || slices.Contains(e.Choices, raw) {
		return nil
	}
	return fmt.Errorf("BOX_SIGNAL_CARRIER=%q is not valid; must be %s", raw, launcherchecks.JoinOxford(e.Choices))
}

// validateBoxAccess refuses a BOX_FORGE_AND_ISSUE_ACCESS value the schema
// removed (issue #6901) at daemon start, so every child does not fail on it
// instead. Any other value is the child launcher's own validateChoice to judge.
func validateBoxAccess(raw string) error {
	if msg, removed := inputdoc.RemovedChoice("BOX_FORGE_AND_ISSUE_ACCESS", raw); removed {
		return errors.New(msg)
	}
	return nil
}

// nixSystemDouble maps GOOS/GOARCH to the nix system double for
// daemon.SelfSpec.System. It takes both as parameters, not runtime.GOOS and
// runtime.GOARCH, so it is testable without build tags. An unmapped pair
// errors rather than guessing: a wrong double would evaluate a nonexistent
// system and report a phantom self-change.
func nixSystemDouble(goos, goarch string) (string, error) {
	unmapped := func() error {
		return fmt.Errorf("no nix system double for GOOS/GOARCH %s/%s", goos, goarch)
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
	// nix spells these two OSes as Go does, but others must still be refused.
	if goos != "linux" && goos != "darwin" {
		return "", unmapped()
	}
	return arch + "-" + goos, nil
}

// gitRevParse runs `git rev-parse <flag>` in dir and returns its trimmed
// stdout, folding git's own stderr into the error on failure.
func gitRevParse(dir, flag string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", flag)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// The likeliest cause is a `safe.directory` refusal, which the bare
		// error reduces to "exit status 128".
		return "", fmt.Errorf("not a git checkout: %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// repoRoot resolves the checkout root containing dir, so a subdirectory
// working directory fails at startup rather than as a git+file:// flakeref at
// a non-root that only breaks the first child.
func repoRoot(dir string) (string, error) {
	return gitRevParse(dir, "--show-toplevel")
}

// gitDir resolves dir's absolute git dir. The checkout lock (issue #3543)
// lives there, not under repoRoot: the daemon never mutates the operator's
// working tree, and the git dir is per-checkout even for a linked worktree,
// the granularity "one daemon per checkout" needs.
func gitDir(dir string) (string, error) {
	return gitRevParse(dir, "--absolute-git-dir")
}

// fail prints one daemon diagnostic and yields mainRun's error exit code, so
// each startup step below stays a single guarded line.
func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "daemon: %s\n", err)
	return 1
}

// cmdStatus implements `daemon status`. It needs only the git dir, never the
// --input document, a knob, or the repo root, so asking "what is running"
// takes no configuration.
func cmdStatus(wd string, stdout, stderr io.Writer) int {
	gitDirPath, err := gitDir(wd)
	if err != nil {
		return fail(stderr, err)
	}
	report, err := daemon.ReadStatus(gitDirPath)
	if err != nil {
		// On error the report still carries the lock probe's result, so
		// the operator learns a daemon holds the checkout.
		fmt.Fprintln(stderr, summarizeStatus(report))
		return fail(stderr, err)
	}

	// This binary's stdout is the machine stream only; humans read stderr.
	data, err := json.Marshal(report)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "%s\n", data)

	fmt.Fprintln(stderr, summarizeStatus(report))

	// 0 even for "no daemon running": scripts read the JSON's `live` field,
	// and exit 1 is reserved for a genuine failure.
	return 0
}

// summarizeSlot renders one SlotStatus compactly for summarizeStatus.
func summarizeSlot(s daemon.SlotStatus) string {
	if !s.Busy {
		return fmt.Sprintf("%d:idle", s.Slot)
	}
	if s.Chore != "" {
		return fmt.Sprintf("%d:busy(%s %s)", s.Slot, s.Kind, s.Chore)
	}
	if len(s.Issues) == 0 {
		return fmt.Sprintf("%d:busy(%s)", s.Slot, s.Kind)
	}
	return fmt.Sprintf("%d:busy(%s #%s)", s.Slot, s.Kind, strings.Join(s.Issues, ",#"))
}

// summarizeStatus renders cmdStatus's one human sentence for stderr. The
// lock, not the status file, is the liveness truth, so a held lock wins
// over whatever the file says and only an unheld one makes a file "stale".
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
	case report.LockHeld && report.HolderPidGone:
		return fmt.Sprintf("daemon: a daemon holds this checkout and is still starting; its lock line and the published status file are both a predecessor's leftover (pid=%d, state=%s)", report.Status.Pid, report.Status.State)
	case report.LockHeld:
		return fmt.Sprintf("daemon: a daemon holds this checkout, but the published status file is a predecessor's leftover (pid=%d, state=%s), not this holder's (%s)", report.Status.Pid, report.Status.State, report.Holder)
	case report.Stale:
		return fmt.Sprintf("daemon: stale — no daemon is running; last published state=%s at %s", report.Status.State, report.Status.Time)
	default:
		return "daemon: no daemon has run in this checkout"
	}
}

// hostClock is the production daemon.Clock. Sleep returns early on ctx
// cancellation, so an operator stop during the idle wait is honoured at once.
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

// announceStop re-derives the pool's Stop/Abort pair from the stopsignal
// latch rather than handing daemon.Config the relay's channels, so it can
// emit each "shutdown" event before closing the matching channel: the pool
// turns a closed Stop into "halt", which must follow the shutdown event.
// cancel ends startupPreflight's context on the first latch, since no pool
// exists yet to honour Ctrl-C there. quit unparks the goroutine when no
// second signal arrives, so repeated test calls don't leak it.
func announceStop(stop, abort <-chan struct{}, quit <-chan struct{}, cancel context.CancelFunc, em *daemon.Emitter) (poolStop, poolAbort <-chan struct{}) {
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	go func() {
		select {
		case <-stop:
		case <-quit:
			return
		}
		em.Emit(daemon.Event{Event: report.EventShutdown, Reason: daemon.ShutdownDrain})
		cancel()
		close(stopCh)

		select {
		case <-abort:
		case <-quit:
			return
		}
		em.Emit(daemon.Event{Event: report.EventShutdown, Reason: daemon.ShutdownEscalate})
		close(abortCh)
	}()
	return stopCh, abortCh
}

// preflightRunner is the slice of *hostRunner startupPreflight needs, kept
// apart from daemon.Runner so the loop's seam never grows RunDoctor.
// fetchRevision, not ResolveTip, makes the self-eval unreachable from the
// preflight by type: a self-eval failure is a per-slot backoff, never
// grounds to refuse startup (issue #3625).
type preflightRunner interface {
	fetchRevision(ctx context.Context) (string, error)
	RunDoctor(ctx context.Context, revision string) (exit int, labelsLine string, err error)
}

// startupPreflight runs `doctor` once, before the pool exists; the zero Halt
// means the daemon may start. It emits exactly one "preflight" event on
// every path, so the stream tells "ran and was healthy" from "never ran".
// finish, not this, emits the "halt" event a refusal implies.
func startupPreflight(ctx context.Context, r preflightRunner, em *daemon.Emitter) daemon.Halt {
	// The freshly fetched tip the first child will run, not the operator's
	// possibly-stale working tree.
	revision, err := r.fetchRevision(ctx)
	// An operator's Ctrl-C during the preflight is a clean stop, not a refusal.
	refuseCancelled := func() daemon.Halt {
		return preflightRefusal(em, revision, "doctor-cancelled", daemon.Halt{Class: daemon.HaltOperatorStop, Detail: ctx.Err().Error()})
	}
	// Checked against ctx, not err: a signal-killed child can surface any
	// shape, including a nil error.
	if ctx.Err() != nil {
		return refuseCancelled()
	}
	if err != nil {
		// Its own outcome label, so the event stream tells a gone branch
		// apart from a seam failure; the class stays HaltPreflight here.
		var gone *daemon.FeatureBranchGoneError
		if errors.As(err, &gone) {
			return preflightRefusal(em, revision, "doctor-feature-branch-gone", daemon.Halt{Class: daemon.HaltPreflight, Detail: "resolve-revision: " + err.Error()})
		}
		return preflightRefusal(em, revision, "doctor-seam-error", daemon.Halt{Class: daemon.HaltPreflight, Detail: "resolve-revision: " + err.Error()})
	}

	exit, labelsLine, err := r.RunDoctor(ctx, revision)
	if ctx.Err() != nil {
		return refuseCancelled()
	}
	if err != nil {
		return preflightRefusal(em, revision, "doctor-seam-error", daemon.Halt{Class: daemon.HaltPreflight, Detail: "run-doctor: " + err.Error()})
	}

	v := daemon.ClassifyPreflight(exit)
	// Exit 4 can come from the triage, butler, research or Filer label sets;
	// doctor's own line is the only place that names which.
	if exit == int(exitcode.DoctorRequiredLabelsMissing) && labelsLine != "" {
		v.Detail = labelsLine
	}
	em.Emit(daemon.Event{Event: report.EventPreflight, Revision: revision, Exit: &exit, Outcome: v.Outcome})
	return v.Halt()
}

// preflightRefusal emits the exit-less "preflight" event proving doctor was
// attempted and returns h. outcome stays "doctor-"-prefixed, like
// ClassifyPreflight's labels, so it can never be read as a child_finish one.
func preflightRefusal(em *daemon.Emitter, revision, outcome string, h daemon.Halt) daemon.Halt {
	em.Emit(daemon.Event{Event: report.EventPreflight, Revision: revision, Outcome: outcome, Reason: h.String()})
	return h
}

// finish is mainRun's one path from a pre-loop Halt to an exit code. The
// order is load-bearing: a merged stdout+stderr stream must read halt event,
// diagnostic, then any PublishHalted write-failure warning. sw == nil is the
// instance-lock refusal, which must never stomp the live holder's status
// file. An in-loop halt does not come through here: the pool has already
// emitted and published it, so mainRun uses h.ExitCode() alone.
func finish(stderr io.Writer, em *daemon.Emitter, sw *daemon.StatusWriter, kinds []daemon.Kind, h daemon.Halt) int {
	em.Emit(h.Event())
	fmt.Fprintf(stderr, "daemon: %s\n", h.Diagnostic())
	em.PublishHalted(sw, kinds, h.String())
	return h.ExitCode()
}

// mainRun is main() returning an exit code instead of calling os.Exit, so
// tests can drive it repeatedly.
func mainRun(argv []string, stdout, stderr io.Writer) int {
	// A successor must take over the lock and running children before
	// anything below can fail, and latch stop signals before that: until
	// Notify runs, Go's SIGTERM handler is fatal. A fresh start latches later,
	// so Ctrl-C during input parsing still kills it.
	var (
		stopSig, abortSig <-chan struct{}
		stopCleanup       func()
	)
	argv, handoffFD, resuming, err := splitHandoffFlag(argv)
	if err != nil {
		return fail(stderr, err)
	}
	if resuming {
		stopSig, abortSig, stopCleanup = installStopSignal()
		defer stopCleanup()
	}
	var (
		lock      *daemon.CheckoutLock
		inherited map[int]*hostChildHandle
		r         *hostRunner
		handedOff bool
		res       *resumed
	)
	if resuming {
		if res, err = resumeFromHandoff(handoffFD); err != nil {
			return fail(stderr, err)
		}
		lock, inherited = res.lock, res.inherited
	}
	// The kernel drops the flock if the holder dies, so this covers only the
	// ordinary return. After an exec the successor holds it through its dup.
	defer func() {
		if lock != nil && !handedOff {
			_ = lock.Release()
		}
	}()
	// Runs before the release above, so unclaimed inherited children are not
	// orphaned. stopSig and abortSig are read here, not at registration, so a
	// stop sent during the wait still reaches them.
	defer func() {
		hs := slices.Collect(maps.Values(inherited))
		if r != nil {
			hs = r.takeUnclaimed()
		}
		waitOut(hs, stopSig, abortSig)
	}()

	// tryRollBack hands a resumed image's children back to the build it came
	// from, so a build that cannot start does not lose them. It reports whether
	// the exec happened.
	tryRollBack := func(reason string) bool {
		if res == nil {
			return false
		}
		if err := res.rollBack(stopSig, successorArgs(argv), reason); err != nil {
			fmt.Fprintf(stderr, "daemon: no rollback: %v\n", err)
			return false
		}
		fmt.Fprintf(stderr, "daemon: %s; rolled back to %s\n", reason, res.handoff.FromPath)
		// Only a test seam returns from the exec: the children now belong to
		// the build rolled back to, as do the lock and the descriptors.
		clear(inherited)
		if r != nil {
			r.forgetHandles()
		}
		handedOff = true
		return true
	}
	// refuse ends a startup that failed: rolled back where possible, else a
	// plain failure, after which the wait-out above drains the children.
	refuse := func(err error) int {
		if tryRollBack(err.Error()) {
			return 0
		}
		return fail(stderr, err)
	}

	// `status` is a verb, not a kind selector, so it is dispatched ahead of
	// parseArgs. Leading --input pairs are skipped: the `nix run .#daemon`
	// wrapper prepends one to every argv.
	rest := argv
	for len(rest) >= 2 && rest[0] == inputFlag {
		rest = rest[2:]
	}
	if len(rest) > 0 && rest[0] == "status" {
		if len(rest) > 1 {
			return fail(stderr, fmt.Errorf("status takes no arguments, got: %v", rest[1:]))
		}
		wd, err := os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
		return cmdStatus(wd, stdout, stderr)
	}

	args, err := parseArgs(argv)
	if err != nil {
		return refuse(err)
	}

	if args.RestartInPlace && !inPlaceRestartSupported {
		return refuse(fmt.Errorf("--restart-in-place: %w", errInPlaceUnsupported))
	}

	doc, err := inputdoc.Load(args.InputPath)
	if err != nil {
		return refuse(err)
	}
	driverRows, err := driverrows.Parse(doc.Drivers, bundle.Drivers())
	if err != nil {
		return refuse(err)
	}
	if err := backendrows.Use(doc.Backends, func(k string) string { return backendRowSetting(doc, stderr, k) }); err != nil {
		return refuse(err)
	}

	// Reused for the runner config, so the warning and the strip agree.
	stripped := strippedKeys(doc)
	warnStrippedChildEnv(stripped, stderr)
	driverEnv := resolveDriverEnv(doc, stderr)

	shared, err := resolveChildSharedKnobs(doc)
	if err != nil {
		return refuse(err)
	}
	baseBranch := shared.baseBranch
	slots, err := parseSlots(shared.maxParallel)
	if err != nil {
		return refuse(err)
	}

	// An ambient env value bypasses the schema's eval-time validation, so this
	// parse is the real check.
	awake, err := daemon.ParseWindow(shared.awakeWindow)
	if err != nil {
		return refuse(err)
	}

	if err := validateBoxAccess(shared.boxAccess); err != nil {
		return refuse(err)
	}
	gatedKinds, err := gateKinds(args.Kinds, args.ExplicitSelector, gateKnobs{
		chores:    chore.Knobs{Chores: shared.butlerChores, Every: shared.butlerEvery, Classes: shared.butlerChoreClasses},
		codeForge: shared.codeForge,
	})
	if err != nil {
		return refuse(err)
	}
	args.Kinds = gatedKinds
	doctorFlags, err := doctorPreflightFlags(args.Kinds, args.ExplicitSelector)
	if err != nil {
		return refuse(err)
	}

	// The generated wrapper (lib/mkHarness.nix's daemonWrapper) exports this
	// daemon's own store path, which a derivation cannot bake into itself.
	// Started any other way, the daemon cannot know its build, so the self
	// check stays off, visibly.
	selfProgram := os.Getenv("SPINDRIFT_DAEMON_PROGRAM")
	if selfProgram == "" {
		fmt.Fprintln(stderr, "daemon: SPINDRIFT_DAEMON_PROGRAM is unset, so the self-change check is disabled (not started through the generated wrapper)")
	}

	// The daemon's own knobs are validated before any Box runs, so a bad value
	// refuses the start rather than halting mid-run. RESEARCH_RESERVATION is inert for a single kind,
	// so it is checked only with several, lest a work-only operator fail on
	// a reservation above their MAX_PARALLEL.
	withSelf, multiKind := selfProgram != "", len(args.Kinds) > 1
	own, err := resolveDaemonOnlyKnobs(doc, stderr, withSelf, multiKind)
	if err != nil {
		return refuse(err)
	}
	appAttr := own.app
	idleFloor, err := parseIdleFloor(own.idleFloor)
	if err != nil {
		return refuse(err)
	}
	idleCap, err := parseIdleCap(own.idleCap, idleFloor)
	if err != nil {
		return refuse(err)
	}
	probeOverride, err := parseProbeInterval(own.probeInterval)
	if err != nil {
		return refuse(err)
	}
	failureBackoff, err := parseFailureBackoff(own.failureBackoff)
	if err != nil {
		return refuse(err)
	}
	breakerThreshold, err := parseBreakerThreshold(own.breakerThreshold)
	if err != nil {
		return refuse(err)
	}
	breakerWindow, err := parseBreakerWindow(own.breakerWindow)
	if err != nil {
		return refuse(err)
	}

	// Env-only, so os.Getenv: doc.Resolve would fail on a legitimately unset
	// knob. ChildCommand/DoctorCommand forward it to children as a flag.
	if err := validateSignalCarrier(os.Getenv("BOX_SIGNAL_CARRIER")); err != nil {
		return refuse(err)
	}

	var selfAttr, nixSystem string
	if withSelf {
		selfAttr = own.selfApp
		nixSystem, err = nixSystemDouble(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return refuse(err)
		}
	}

	var reservation int
	if multiKind {
		reservation, err = parseResearchReservation(own.researchReservation, slots)
		if err != nil {
			return refuse(err)
		}
	}

	// A Connector-routed tracker row's probe reads its credential from the
	// runtime's source. Built ahead of the check-only return so a successor's
	// smoke test also catches a missing or failing credential. Nothing above
	// loads a Connector, so the sandbox it installs is in place for every load.
	pwd, err := os.Getwd()
	if err != nil {
		return refuse(err)
	}
	connIn := connectorInput(doc, args.Kinds, pwd)
	connIn.Drivers = driverRows
	connectors, err := newConnectorRuntime(connIn, connectorsetup.Options{
		RequireCredential: true, RefreshEvery: connectorCredentialRefresh,
		OnRefreshError: func(env string, err error) {
			fmt.Fprintf(stderr, "daemon: connector credential %s refresh failed, keeping the previous value: %v\n", env, err)
		},
	})
	if err != nil {
		return refuse(err)
	}
	defer func() { _ = connectors.Close() }()

	// Before the lock and preflight: a freshly built successor smoke-tests
	// itself this way beside the live daemon. Keep it ahead of the demand
	// build too.
	if args.CheckOnly {
		fmt.Fprintln(stderr, "daemon: check-only: input and config are valid")
		return 0
	}

	demand, demandMissing := buildDemandSources(connectors, doc, args.Kinds)

	repoPath, err := repoRoot(pwd)
	if err != nil {
		return refuse(err)
	}

	clk := hostClock{}
	em := daemon.NewEmitter(stdout, stderr, clk.Now)

	gitDirPath, err := gitDir(pwd)
	if err != nil {
		return refuse(err)
	}
	if res != nil {
		err = lock.Claim(gitDirPath)
	} else {
		lock, err = daemon.AcquireCheckoutLock(gitDirPath, args.Kinds)
	}
	if err != nil {
		// sw nil: the status file is the lock holder's, not ours.
		return finish(stderr, em, nil, args.Kinds, daemon.Halt{Class: daemon.HaltInstanceLock, Detail: err.Error()})
	}

	// After the lock, so a refused second daemon never touches the holder's
	// Events file; before warnUnprobedKinds, whose events belong in it.
	em.TeeEvents(daemon.NewEventsFile(gitDirPath))

	// Only now, so an exec that failed in the predecessor records no handoff.
	if res != nil {
		em.Emit(handoffEvent(res.handoff))
	}

	// After the lock: a refused second daemon must not announce a fallback
	// scheduler.
	warnUnprobedKinds(demandMissing, stderr, em)

	// After the lock, so a refused second instance never stomps the holder's
	// file. Deliberately never deleted on exit: with the lock unheld,
	// ReadStatus reports it stale, as a record of how this run ended.
	statusWriter := daemon.NewStatusWriter(gitDirPath, clk.Now)

	// After the lock and status writer so a bad route halts through finish;
	// before the runner and preflight so no Box starts against a route the
	// tracker rejects. A resumed image rolls back instead, like the other
	// pre-pool refusals below.
	if err := probeRoute(demand, args.Kinds, issueRoute(doc), stderr); err != nil {
		h := daemon.Halt{Class: daemon.HaltInvalidConfig, Detail: err.Error()}
		if tryRollBack(h.Diagnostic()) {
			return 0
		}
		return finish(stderr, em, statusWriter, args.Kinds, h)
	}

	r, err = newHostRunner(hostRunnerConfig{
		repoPath:      repoPath,
		appAttr:       appAttr,
		baseBranch:    baseBranch,
		featureBranch: args.FeatureBranch,
		selfAttr:      selfAttr,
		nixSystem:     nixSystem,
		// A child inherits the Consumer's `<env>_CMD`, SECRET_CMD and
		// GH_TOKEN_REFRESH_FILE and resolves its own credential sources; the
		// daemon exports no row credential.
		env:         append(os.Environ(), driverEnv...),
		knobs:       stripped,
		doctorFlags: doctorFlags,
		demand:      demand,
		stateDir:    gitDirPath,
		selfArgs:    successorArgs(argv),
		inherited:   inherited,
	})
	if err != nil {
		return refuse(err)
	}

	// Before startupPreflight, so the split is visible even if it halts.
	if args.FeatureBranch != "" {
		fmt.Fprintf(stderr, "daemon: tracking %s; children target %s\n", baseBranch, args.FeatureBranch)
	}

	// announceStop cancels this on the first latch, since startupPreflight
	// runs before the pool's own Stop watcher exists.
	preflightCtx, cancelPreflight := context.WithCancel(context.Background())
	defer cancelPreflight()

	if stopCleanup == nil {
		stopSig, abortSig, stopCleanup = installStopSignal()
		defer stopCleanup()
	}
	quit := make(chan struct{})
	defer close(quit)
	poolStop, poolAbort := announceStop(stopSig, abortSig, quit, cancelPreflight, em)

	// After the signal wiring, so Ctrl-C is honoured; after the lock, so a
	// second daemon is refused cheaply; before Loop, so a refusal precedes any
	// claim. A resumed image stopped during its preflight still runs the pool,
	// which with Stop closed drains the inherited children.
	if h := startupPreflight(preflightCtx, r, em); h.Class != daemon.HaltNone && (res == nil || h.Class != daemon.HaltOperatorStop) {
		// Before finish, so a rolled-back image leaves no halt event behind.
		if tryRollBack(h.Diagnostic()) {
			return 0
		}
		return finish(stderr, em, statusWriter, args.Kinds, h)
	}

	cfg := daemon.Config{
		Kinds:                  args.Kinds,
		Reservation:            reservation,
		IdleFloor:              idleFloor,
		IdleCap:                idleCap,
		ProbeIntervals:         probeIntervals(demand, probeOverride),
		Trackers:               trackers(demand, doc),
		RepoURL:                repoURL(doc),
		Slots:                  slots,
		LateMergeSweepInterval: settle.LateMergeSweepInterval,
		SelfProgram:            selfProgram,
		FailureBackoff:         failureBackoff,
		BreakerThreshold:       breakerThreshold,
		BreakerWindow:          breakerWindow,
		Awake:                  awake,
		Status:                 statusWriter,
		Stop:                   poolStop,
		Abort:                  poolAbort,
		RestartInPlace:         args.RestartInPlace,
	}
	if res != nil {
		cfg.Handoff = &res.handoff
		if res.handoff.RolledBack {
			cfg.SelfUpdate = daemon.SelfUpdate{Phase: daemon.SelfUpdateBlocked, Revision: res.handoff.Revision, Reason: res.handoff.Reason}
		}
	}

	// Validated here so every config-invalid halt from this binary goes
	// through finish (the route probe above is the one earlier such halt);
	// Loop's invalidConfig is reached only by embedders and tests. A resumed
	// image whose config cannot take the handoff (a dropped kind,
	// MAX_PARALLEL below a handed-off slot) refuses like a failed preflight:
	// before any halt event and before rootSelf. Without a rollback, the
	// deferred wait-out drains the inherited children.
	if err := cfg.Validate(); err != nil {
		h := daemon.Halt{Class: daemon.HaltInvalidConfig, Detail: err.Error()}
		if tryRollBack(h.Diagnostic()) {
			return 0
		}
		return finish(stderr, em, statusWriter, args.Kinds, h)
	}

	// After the checks, so a refused start never replaces the rollback
	// target's root. A fresh context: a latched stop cancels preflightCtx, and
	// the roots must still end up right for the next image.
	rootCtx, cancelRoot := context.WithTimeout(context.Background(), selfRootTimeout)
	r.rootSelf(rootCtx, selfProgram, stderr)
	cancelRoot()

	var code int
	code, handedOff = superviseLoop(cfg, stopSig, r, em, clk, lock, selfProgram)
	return code
}

// superviseLoop runs the pool and, each time it commits an in-place restart,
// execs the prebuilt daemon with the children kept running. handedOff reports
// that the exec happened (only a test seam returns from one), so the caller
// must leave the lock for the successor. A stop latched before the exec, or an
// exec that fails, re-enters the pool with the same handoff document: the
// first drains, the second keeps supervising under a blocked self-update.
// stopSig is the raw stop latch the pre-exec check reads: cfg.Stop closes only
// after the shutdown event is emitted, which a stalled consumer can delay.
func superviseLoop(cfg daemon.Config, stopSig <-chan struct{}, r *hostRunner, em *daemon.Emitter, clk daemon.Clock, lock *daemon.CheckoutLock, selfProgram string) (code int, handedOff bool) {
	for {
		// Never cancelled in production: cfg.Stop/cfg.Abort stop the pool. Loop
		// takes a context only for tests and embedders (issue #3626).
		h := daemon.Loop(context.Background(), cfg, r, em, clk)
		if h.Handoff == nil {
			return h.ExitCode(), false
		}
		cfg.Handoff = h.Handoff
		err := r.execSuccessor(*h.Handoff, lock.File(), stopSig, selfProgram)
		switch {
		case err == nil:
			return 0, true
		case errors.Is(err, errStopBeforeExec):
			// Re-entering at once is safe: Loop restarts outside SelfUpdateWaiting,
			// so it cannot recommit before a fresh prebuild, and it drains once the
			// latched stop reaches cfg.Stop.
		default:
			cfg.SelfUpdate = daemon.SelfUpdate{Phase: daemon.SelfUpdateBlocked, Revision: r.prebuiltRevision(), Reason: "exec: " + err.Error()}
			em.Emit(cfg.SelfUpdate.Event())
		}
	}
}

func main() {
	os.Exit(mainRun(os.Args[1:], os.Stdout, os.Stderr))
}

// successorArgs is the operator's argv without --input and --handoff-fd
// pairs. The successor's wrapper supplies its own --input, and a replayed old
// one would win, making the smoke test validate the old document. A
// --handoff-fd names a descriptor already consumed.
func successorArgs(args []string) []string {
	return dropFlagPairs(args, inputFlag, handoffFlag)
}

// dropFlagPairs is args without every `flag value` pair for the given flags.
// A trailing flag with no value is kept, for the parser to reject.
func dropFlagPairs(args []string, flags ...string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if slices.Contains(flags, args[i]) && i+1 < len(args) {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// handoffEvent is the self_update event an in-place restart records, built
// from the document the predecessor wrote.
func handoffEvent(h daemon.Handoff) daemon.Event {
	var slots []int
	for _, s := range h.Slots {
		slots = append(slots, s.Slot)
	}
	// Literals, since the handoff's extra fields (FromPath, ToPath, Revision,
	// Slots) are the ones the event-stream-parity check reads from it.
	if h.RolledBack {
		return daemon.Event{Event: report.EventSelfUpdate, Phase: string(daemon.SelfUpdateRolledBack), Revision: h.Revision, Reason: h.Reason, FromPath: h.FromPath, ToPath: h.ToPath, Slots: slots}
	}
	return daemon.Event{Event: report.EventSelfUpdate, Phase: string(daemon.SelfUpdateHandoff), Revision: h.Revision, FromPath: h.FromPath, ToPath: h.ToPath, Slots: slots}
}

// resumed is what a successor image takes over from its predecessor's exec.
type resumed struct {
	handoff daemon.Handoff
	lock    *daemon.CheckoutLock
	// inherited are the children that validated, by report fd. One that did
	// not is left for the pool's InheritChild to report.
	inherited map[int]*hostChildHandle
}

// rollbackReasonMax caps, in bytes, the refusal reason a rollback records: the
// handoff document must fit one pipe's capacity, and a longer one would turn
// the rollback into drain-and-exit.
const rollbackReasonMax = 2000

// capRollbackReason keeps the head of reason, where a diagnostic leads with
// its cause, cutting on a rune boundary so the result is valid UTF-8.
func capRollbackReason(reason string) string {
	if len(reason) <= rollbackReasonMax {
		return reason
	}
	return strings.ToValidUTF8(reason[:rollbackReasonMax], "") + "..."
}

// rollBack execs back to the build this image was resumed from, handing it
// the same lock and the children this image re-attached, and recording reason
// as the refusal. It never rolls back a rolled-back image, so a pair of builds
// that both refuse to start cannot hand the children to each other forever. A
// nil return means the exec happened (only a test seam returns).
func (res *resumed) rollBack(stop <-chan struct{}, selfArgs []string, reason string) error {
	h := res.handoff
	switch {
	case h.RolledBack:
		return errors.New("this image is itself a rollback")
	case h.Stop:
		return errStopBeforeExec
	case h.V < daemon.HandoffRollbackVersion:
		return fmt.Errorf("the previous daemon wrote handoff version %d, which cannot read a version-%d document", h.V, daemon.HandoffVersion)
	case h.FromPath == "":
		return errors.New("no previous daemon")
	}
	if err := validStorePath("previous daemon", h.FromPath); err != nil {
		return err
	}
	// Dropped slots were never adopted, so their descriptors are not ours to
	// pass on.
	h.Slots = slices.DeleteFunc(slices.Clone(h.Slots), func(s daemon.HandoffSlot) bool {
		return res.inherited[s.Child.ReportFD] == nil
	})
	h.RolledBack, h.Reason = true, capRollbackReason(reason)
	h.FromPath, h.ToPath = h.ToPath, h.FromPath
	// Not the runner's env: a rollback can run before the host runner exists,
	// and the target's wrapper rebuilds its own environment.
	return execHandoff(h, res.lock.File(), stop, os.Environ(), selfArgs)
}

// splitHandoffFlag strips every --handoff-fd pair from argv, returning the
// last value and whether the flag was present at all: the one place mainRun
// learns it is a successor.
func splitHandoffFlag(argv []string) (rest []string, raw string, found bool, err error) {
	for i := 0; i < len(argv); i++ {
		if argv[i] != handoffFlag {
			continue
		}
		if i+1 >= len(argv) {
			return nil, "", false, fmt.Errorf("flag %s requires a value", handoffFlag)
		}
		raw, found = argv[i+1], true
		i++
	}
	return dropFlagPairs(argv, handoffFlag), raw, found, nil
}

// resumeFromHandoff takes over what the predecessor handed down through the
// descriptor named by raw — the instance lock and the running children —
// before anything can fail on the input document and leave them unattended.
// Any error is a startup failure; a child that does not validate is not.
func resumeFromHandoff(raw string) (*resumed, error) {
	fd, err := strconv.Atoi(raw)
	if err != nil || fd <= syscall.Stderr {
		return nil, fmt.Errorf("flag %s: %q is not a descriptor", handoffFlag, raw)
	}
	f := os.NewFile(uintptr(fd), "handoff document")
	h, err := daemon.DecodeHandoff(f)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	lock, err := daemon.InheritCheckoutLock(h.LockFD)
	if err != nil {
		return nil, err
	}
	res := &resumed{handoff: h, lock: lock, inherited: make(map[int]*hostChildHandle, len(h.Slots))}
	for _, s := range h.Slots {
		c, err := inheritHandle(s.Child)
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemon: drop inherited slot %d: %v\n", s.Slot, err)
			continue
		}
		res.inherited[s.Child.ReportFD] = c
	}
	// The predecessor ignored SIGHUP, SIGTERM and SIGINT across the exec, and
	// SIG_IGN survives exec. The stop latch covers the latter two; SIGHUP gets
	// its default back unless it was ignored to begin with.
	if err := restoreSIGHUP(h.SIGHUPIgnored); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: restore SIGHUP: %v\n", err)
	}
	return res, nil
}

// errInPlaceUnsupported is returned wherever the exec path is unavailable;
// restart_other.go's stubs and mainRun's refusal share it.
var errInPlaceUnsupported = errors.New("in-place restart is supported only on linux amd64/arm64")
