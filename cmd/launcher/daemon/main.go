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
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/stopsignal"
	"spindrift.dev/launcher/internal/tokenrefresh"
)

// installStopSignal is a package-level test seam, like the launcher's own
// (cmd/launcher/main.go) — a test drives the latch through fake stop/abort
// channels instead of registering a real signal handler or sending a real
// SIGTERM/SIGINT to the test binary.
var installStopSignal = stopsignal.Notify

// inputFlag: mainRun's status-verb check skips leading pairs of it.
const inputFlag = "--input"

// handoffFlag names the descriptor an in-place restart predecessor left the
// handoff document on. mainRun strips it before anything else parses argv, and
// successorArgs never replays it.
const handoffFlag = "--handoff-fd"

// parsedArgs is the result of parsing argv: `--input <path>` plus an
// optional positional kind-set selector (dispatch|research|butler|recover,
// default every kind — see parseArgs) and an optional
// `--feature-branch <branch>`.
type parsedArgs struct {
	InputPath        string
	Kinds            []daemon.Kind
	ExplicitSelector bool   // true when argv named a kind selector; false for the bare "every kind" default (see gateKinds)
	FeatureBranch    string // empty when --feature-branch is absent; see hostRunner.featureBranch
	CheckOnly        bool   // --check-only: validate input and config, exit 0 or 1 with the startup diagnostic (see mainRun)
	RestartInPlace   bool   // --restart-in-place: exec the prebuilt daemon on a self-change, keeping children running (ADR 0063)
}

// parseArgs parses `daemon --input <path> [--feature-branch <branch>]
// [--check-only] [--restart-in-place] [dispatch|research|butler|recover]` —
// --feature-branch, --check-only and --restart-in-place may appear before or
// after the positional kind selector. With
// no positional verb the daemon draws from every kind off one pool (issue
// #3541, #3878) — an operator stops having to choose between advancing work,
// enriching the backlog, and butler upkeep. `dispatch` alone keeps work-only
// operation, which is how an operator who has not created the research
// labels on their target repo runs the daemon; `research` alone restricts it
// to advise-only research; `butler` alone restricts it to the chore;
// `recover` alone restricts it to queue-mode `spindrift recover` (issue
// #4656). The bare default's butler and recover are provisional until
// gateKinds, since parseArgs has no document to resolve BUTLER_CHORES,
// CODE_FORGE or BOX_FORGE_AND_ISSUE_ACCESS against.
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

// errNoOutboxRelay is why a recover kind is disabled: no read-only Box's
// outbox bundle is relayed, so there is nothing for it to land.
var errNoOutboxRelay = fmt.Errorf("no outbox bundle is relayed (recover needs CODE_FORGE %s with BOX_FORGE_AND_ISSUE_ACCESS=read-only)", strings.Join(backend.OutboxRelayBackends(), " or "))

// gateKnobs are the resolved knob values gateKinds judges each kind's
// Enablement row by.
type gateKnobs struct {
	chores               chore.Knobs
	codeForge, boxAccess string
}

// gateKinds drops from a bare invocation's kinds every kind whose descriptor
// Enablement row is unmet, so a bare daemon without Chores runs the other
// kinds rather than being refused by startupPreflight's `doctor --butler`,
// and one whose Box relays no outbox bundle runs without recover. A kind
// named explicitly fails startup instead of running nothing. A butler that
// survives this gate has the rest of its config validated by that same
// `doctor --butler` (issue #3920).
// Without an EnabledByChores kind among kinds the three knobs are never
// parsed, so a dispatch- or research-only daemon never fails on butler config
// it does not use. With one among kinds but knobs.chores.Chores empty, chore.Load
// itself short-circuits before touching BUTLER_EVERY or
// BUTLER_CHORE_CLASSES, so only a chore.Load error with at least one Chore
// enabled fails startup.
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
			if !backend.RelaysOutbox(knobs.codeForge, knobs.boxAccess) {
				disabled = fmt.Errorf("CODE_FORGE=%s with BOX_FORGE_AND_ISSUE_ACCESS=%s: %w", knobs.codeForge, knobs.boxAccess, errNoOutboxRelay)
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

// doctorPreflightFlags returns the kind flags doctor's preflight gets, from
// each kind's Preflight row. kinds must be post-gateKinds, so a kind
// gateKinds dropped never refuses startup over its own config. A
// PreflightWhenNamed kind is promoted only when the operator named it: the
// bare selector lists every kind, and a repo that never set one up (say,
// without the research labels) must still start.
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

// warnStrippedChildEnv prints one stderr line per key in keys that is
// actually set in the daemon's own environment (matching
// inputdoc.Document.Lookup's own "set" test: non-empty os.Getenv, so an
// exported-but-empty knob is not "set" here either). withoutKeys
// (internal/daemon/command.go) strips every such key before exec, so no
// child sees it; the daemon ignores it too unless it is an
// inputdoc.IsDaemonOnly key: the daemon reads those through
// inputdoc.Document.Lookup (TestDaemonOnlyKnobs_MatchesDocLookupSites pins
// that), which lets an ambient value still win (ADR 0020) and prints its own
// deprecation warning. Every other stripped knob resolves document-first via
// childKnob, as a child sees it (issue #4623).
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
			scope := "ignored by the daemon and its children"
			if inputdoc.IsDaemonOnly(key) {
				scope = "not forwarded to children"
			}
			fmt.Fprintf(stderr, "%s=%s set in environment — %s; use the --input document's settings.%s\n", key, v, scope, key)
		}
	}
}

// resolveDriverEnv applies the baked-Driver-wins rule (ADR 0064, issue #5050)
// to the daemon itself, printing the mismatch warning once. Children and the
// doctor preflight run through the wrapper, which re-sources harness.env and
// restores DRIVER, so stripping it cannot work; instead it returns the
// DriverWarnedEnv entry to append to their env, telling each launcher this
// value was already warned about. No mismatch, or an already-warned one,
// returns nil.
func resolveDriverEnv(doc *inputdoc.Document, stderr io.Writer) []string {
	var baked string
	if doc != nil {
		baked = doc.Artifacts["DRIVER"]
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

// validateSignalCarrier is the daemon-side counterpart of the launcher's own
// validateChoice (cmd/launcher/flags.go), whose wording it mirrors: the two
// are separate package mains, so the generated schemaFlags table is out of
// reach here and the choices are spelled out instead. They mirror
// lib/env-schema.nix's signalCarrier.choices, itself pinned against drift by
// nix/checks/schema-drift.nix — change that list and change this one. An
// empty value is valid and means unset: the knob is env-only (never in the
// --input document's settings), so an unset knob stays out of the child
// environment and the child takes the schema default.
func validateSignalCarrier(raw string) error {
	if raw == "" || raw == "log" || raw == "socket" {
		return nil
	}
	return fmt.Errorf("BOX_SIGNAL_CARRIER=%q is not valid; must be log or socket", raw)
}

// nixSystemDouble maps Go's GOOS/GOARCH to the nix system double the self
// check's flake attribute path needs (daemon.SelfSpec.System). It takes both
// as parameters, rather than reading runtime.GOOS/runtime.GOARCH itself, so
// the mapping is testable without build tags. An unmapped pair returns an
// error naming it rather than guessing: a wrong double would evaluate a
// system that does not exist and report a phantom self-change.
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
		// hostRunner.fetchRevision's fetch error).
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
	// the exit code, and fail (like daemon.Halt's ExitCode) already spends
	// 1 on a genuine failure — reusing it here for "nothing is running"
	// would conflate the two.
	return 0
}

// summarizeSlot renders one SlotStatus compactly for summarizeStatus's
// stderr line: idle slots need only their number, a busy slot names its
// kind and, when the child has announced any, the issues it is in flight
// on — or, for a butler slot, the Chore its child last boxed instead of an
// issue.
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

// summarizeStatus renders cmdStatus's one human sentence for stderr. It
// covers the cases ReadStatus's doc distinguishes, checked in this
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

// announceStop watches the shared stopsignal relay's stop/abort latch
// (installStopSignal) and re-derives the pool's own Stop/Abort pair from it,
// closing each in turn rather than handing daemon.Config the relay's
// channels directly. The extra hop exists for ordering: it emits the
// daemon's "shutdown" event — ShutdownDrain on the first latch,
// ShutdownEscalate on the second — before closing the corresponding
// returned channel, so the pool's own Stop watcher (which turns a closed
// Stop into the "halt" event) never observes the halt ahead of the
// shutdown an operator's event-stream reading expects it to follow.
//
// cancel ends the startup preflight's own context on the first latch: the
// pool that would otherwise watch Stop does not exist yet during
// startupPreflight, so nothing else would honour an operator's Ctrl-C
// there.
//
// quit lets a caller unpark this goroutine when no second signal ever
// arrives — mainRun is driven repeatedly under test, and without a way out
// each call would leak a goroutine parked on <-stop forever.
func announceStop(stop, abort <-chan struct{}, quit <-chan struct{}, cancel context.CancelFunc, em *daemon.Emitter) (poolStop, poolAbort <-chan struct{}) {
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	go func() {
		select {
		case <-stop:
		case <-quit:
			return
		}
		em.Emit(daemon.Event{Event: "shutdown", Reason: daemon.ShutdownDrain})
		cancel()
		close(stopCh)

		select {
		case <-abort:
		case <-quit:
			return
		}
		em.Emit(daemon.Event{Event: "shutdown", Reason: daemon.ShutdownEscalate})
		close(abortCh)
	}()
	return stopCh, abortCh
}

// preflightRunner is the slice of the runner startupPreflight needs — a
// narrow interface distinct from daemon.Runner (the *loop's* seam), so a
// test can drive startupPreflight with a small fake without daemon.Runner
// growing a RunDoctor method the loop itself never calls. *hostRunner
// already satisfies it.
//
// fetchRevision, not ResolveTip: the preflight only needs the revision to
// pin doctor to, never the self-eval half ResolveTip also runs when
// SPINDRIFT_DAEMON_PROGRAM is set. Naming the unexported fetch-only method
// here (main.go and runner.go share package main) makes that self-eval
// unreachable from the preflight by type, not by convention: a self-eval
// failure is a per-slot backoff under the self-build halt grammar, never
// grounds to refuse startup outright (issue #3625 review finding).
type preflightRunner interface {
	fetchRevision(ctx context.Context) (string, error)
	RunDoctor(ctx context.Context, revision string) (exit int, labelsLine string, err error)
}

// startupPreflight runs `doctor` exactly once, before the pool exists, and
// reports whether the daemon may start: the zero Halt (class HaltNone)
// means it may. It always emits one "preflight" event on every path —
// pass, refusal, seam failure, or an operator's Ctrl-C — so a reader of
// the event stream can tell "ran and was healthy" apart from "never ran".
// It does not itself emit the "halt" event a refusal implies — mainRun's
// finish helper does, after routing the returned Halt through ExitCode.
func startupPreflight(ctx context.Context, r preflightRunner, em *daemon.Emitter) daemon.Halt {
	// Resolve at the same freshly fetched tip the first child will run at,
	// so the preflight validates the build about to actually run rather
	// than the operator's possibly-stale working tree. fetchRevision only —
	// never ResolveTip's self-eval half — so a self-check failure can never
	// hard-refuse the preflight; see preflightRunner's doc.
	revision, err := r.fetchRevision(ctx)
	// refuseCancelled reports an operator's Ctrl-C during the preflight: a
	// clean stop, not a refusal — HaltOperatorStop's ExitCode is 0 by
	// class, so there is nothing left to distinguish by string layout.
	refuseCancelled := func() daemon.Halt {
		return preflightRefusal(em, revision, "doctor-cancelled", daemon.Halt{Class: daemon.HaltOperatorStop, Detail: ctx.Err().Error()})
	}
	// Cancellation is checked against ctx rather than against err: a seam
	// whose child was signal-killed can return any shape at all (including
	// a nil error), and an operator's Ctrl-C must read as a clean stop
	// however it surfaced.
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
	if v.Outcome == daemon.OutcomeRequiredLabelsMissing && labelsLine != "" {
		v.Detail = labelsLine
	}
	em.Emit(daemon.Event{Event: "preflight", Revision: revision, Exit: &exit, Outcome: v.Outcome})
	return v.Halt()
}

// preflightRefusal emits the "preflight" event proving doctor was
// attempted (the "halt" event that used to follow it here now comes from
// mainRun's finish helper instead) and returns h for startupPreflight.
// It never carries an `exit` field, there being no exit code to report, and
// outcome stays "doctor-"-prefixed like ClassifyPreflight's own labels so a
// preflight outcome can never be read as a child_finish one.
func preflightRefusal(em *daemon.Emitter, revision, outcome string, h daemon.Halt) daemon.Halt {
	em.Emit(daemon.Event{Event: "preflight", Revision: revision, Outcome: outcome, Reason: h.String()})
	return h
}

// finish is mainRun's one path from a pre-loop Halt to a return value: emit
// the halt event, print "daemon: " + h.Diagnostic() to stderr, publish the
// halted status, and derive the exit code. That order is load-bearing: a merged
// stdout+stderr stream must read halt event, diagnostic, then any
// Emitter.PublishHalted write-failure warning (when em's errW is that same
// stderr, as NewEmitter(stdout, stderr, ...) wires it), byte-identical to the
// output before 580296cf. The per-class text lives in Halt.Diagnostic, so no
// caller can print text that disagrees with h. sw == nil is
// deliberate, not a missing argument: it is the instance-lock refusal,
// where the status file belongs to the daemon that actually holds the
// checkout, and a refused second instance must never stomp the live
// holder's file (daemon.NewStatusWriter is not even constructed until
// after the lock acquire below succeeds).
//
// An in-loop halt never comes through here for the emit/publish half:
// pool.halt (and invalidConfig, via Emitter.PublishHalted) have already
// emitted the halt event and published the status by the time Loop returns
// its Halt, so mainRun ends on h.ExitCode() alone for that path — that
// asymmetry is intentional, not a gap to "fix" by double-emitting.
func finish(stderr io.Writer, em *daemon.Emitter, sw *daemon.StatusWriter, kinds []daemon.Kind, h daemon.Halt) int {
	em.Emit(h.Event())
	fmt.Fprintf(stderr, "daemon: %s\n", h.Diagnostic())
	em.PublishHalted(sw, kinds, h.String())
	return h.ExitCode()
}

// mainRun holds everything main() does: argv parse, input document load,
// knob resolution, repo root, signal wiring, and daemon.Loop, returning the
// exit code rather than calling os.Exit so tests can drive it repeatedly
// with different argv (mirrors cmd/launcher/driver-exec/main.go's mainRun).
func mainRun(argv []string, stdout, stderr io.Writer) int {
	// Ahead of the status verb and parseArgs: a successor must take over the
	// lock and the running children before anything below can fail.
	//
	// A successor latches stop signals before that: until Notify runs, Go's
	// own SIGTERM handler is fatal, and a stop arriving from the first
	// instruction of main must survive until the pool can act on it. A fresh
	// start installs the latch later, so Ctrl-C during input parsing still
	// kills it.
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
	// No cleanup on the crash path: the kernel drops the flock when the
	// holder dies (including SIGKILL), so a deferred Release only needs to
	// cover the ordinary return path. After an exec the successor holds the
	// lock through its dup.
	defer func() {
		if lock != nil && !handedOff {
			_ = lock.Release()
		}
	}()
	// Runs before the release above: inherited children the pool never claimed
	// must not be orphaned, nor the lock dropped while they still run. Reading
	// stopSig and abortSig here, not at registration, keeps the stop an
	// operator sends during the wait reaching those children.
	defer func() {
		hs := slices.Collect(maps.Values(inherited))
		if r != nil {
			hs = r.takeUnclaimed()
		}
		waitOut(hs, stopSig, abortSig)
	}()

	// tryRollBack hands a resumed image's children back to the build it came
	// from, so a build that cannot start does not cost the operator the children
	// it adopted. It reports whether the exec happened.
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

	// `status` is dispatched here, ahead of parseArgs, not folded into it:
	// parseArgs's one positional slot is the kind selector (dispatch |
	// research, see its own doc), and overloading that same slot with a
	// verb would make `daemon status dispatch` parse as a kind selector of
	// "status dispatch" rather than the status verb it plainly reads as.
	//
	// Leading --input pairs are skipped: the `nix run .#daemon` wrapper
	// prepends `--input <doc>` to every argv, and status never loads it.
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

	// Computed once here, right after the document loads (the earliest
	// point the key set is known) and reused below for the runner config,
	// so the warning and the actual strip act on the same list.
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

	// The schema validates DAEMON_AWAKE_WINDOW at Nix eval time, but an
	// ambient env value (used when the document does not carry the key)
	// bypasses that entirely, so this runtime parse is the actual guarantee.
	awake, err := daemon.ParseWindow(shared.awakeWindow)
	if err != nil {
		return refuse(err)
	}

	gatedKinds, err := gateKinds(args.Kinds, args.ExplicitSelector, gateKnobs{
		chores:    chore.Knobs{Chores: shared.butlerChores, Every: shared.butlerEvery, Classes: shared.butlerChoreClasses},
		codeForge: shared.codeForge,
		boxAccess: shared.boxAccess,
	})
	if err != nil {
		return refuse(err)
	}
	args.Kinds = gatedKinds
	doctorFlags, err := doctorPreflightFlags(args.Kinds, args.ExplicitSelector)
	if err != nil {
		return refuse(err)
	}

	// SPINDRIFT_DAEMON_PROGRAM is exported by the generated wrapper
	// (lib/mkHarness.nix's daemonWrapper) as this daemon's own store path,
	// read at runtime because a derivation cannot interpolate its own store
	// path into its own text. A daemon started any other way (e.g. `go run`
	// during development) cannot know its own build, so it must not guess:
	// the self check stays off, but visibly so — a safety property silently
	// off is worse than one an operator can see is off.
	selfProgram := os.Getenv("SPINDRIFT_DAEMON_PROGRAM")
	if selfProgram == "" {
		fmt.Fprintln(stderr, "daemon: SPINDRIFT_DAEMON_PROGRAM is unset, so the self-change check is disabled (not started through the generated wrapper)")
	}

	// The daemon's own knobs, resolved and validated here — before
	// startupPreflight, before any slot fills, before any Box runs — so a bad
	// value refuses the start cleanly rather than surfacing as a daemon.Loop
	// halt mid-run. RESEARCH_RESERVATION is inert for a single-kind daemon
	// (both the schema doc and daemon.Config say so), so it is resolved and
	// validated only when the positional verb actually put more than one kind
	// in play: a work-only operator (no research labels created yet) must not
	// be failed at startup by a reservation value that happens to exceed
	// their MAX_PARALLEL.
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

	// BOX_SIGNAL_CARRIER is env-only (absent from the --input document's
	// settings map), so it is read with os.Getenv rather than doc.Resolve —
	// Resolve would fail on a legitimately-unset knob.
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

	// Stops before the lock and preflight: a freshly built successor
	// smoke-tests itself this way beside the live daemon, which holds the lock.
	// Keep it ahead of the demand build and token watcher too; no test can
	// observe the watcher's goroutine.
	if args.CheckOnly {
		fmt.Fprintln(stderr, "daemon: check-only: input and config are valid")
		return 0
	}

	// Built from the daemon's own resolved knobs, per kind row, once the
	// butler gate has settled which kinds are in play.
	demand, demandMissing := buildDemandSources(doc, args.Kinds)
	// The github Demand probe runs gh in this process, so keep its token fresh
	// the way a child's bootstrap does: an expired token fails every probe,
	// and probe failures feed the breaker.
	if f := childKnob(doc, "GH_TOKEN_REFRESH_FILE", os.Getenv("GH_TOKEN_REFRESH_FILE")); f != "" {
		go tokenrefresh.Watch(f, tokenrefresh.Interval, nil, func(v string) error {
			return os.Setenv("GH_TOKEN", v)
		})
	}

	wd, err := os.Getwd()
	if err != nil {
		return refuse(err)
	}
	repoPath, err := repoRoot(wd)
	if err != nil {
		return refuse(err)
	}

	clk := hostClock{}
	em := daemon.NewEmitter(stdout, stderr, clk.Now)

	gitDirPath, err := gitDir(wd)
	if err != nil {
		return refuse(err)
	}
	if res != nil {
		err = lock.Claim(gitDirPath)
	} else {
		lock, err = daemon.AcquireCheckoutLock(gitDirPath, args.Kinds)
	}
	if err != nil {
		// finish keeps the pre-580296cf halt-event-then-diagnostic order. sw
		// nil: no status file of ours exists yet.
		return finish(stderr, em, nil, args.Kinds, daemon.Halt{Class: daemon.HaltInstanceLock, Detail: err.Error()})
	}

	// Lock-first, like the status writer below: a refused second daemon must
	// not append to or rotate the holder's Events file. Before
	// warnUnprobedKinds, whose events belong in the durable copy too.
	em.TeeEvents(daemon.NewEventsFile(gitDirPath))

	// Recorded here, once the successor holds the lock and the Events file, so
	// an exec that failed in the predecessor never leaves a handoff behind.
	if res != nil {
		em.Emit(handoffEvent(res.handoff))
	}

	// After the lock: a refused second daemon schedules nothing, so it must
	// not announce a fallback scheduler.
	warnUnprobedKinds(demandMissing, stderr, em)

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

	r, err = newHostRunner(hostRunnerConfig{
		repoPath:      repoPath,
		appAttr:       appAttr,
		baseBranch:    baseBranch,
		featureBranch: args.FeatureBranch,
		selfAttr:      selfAttr,
		nixSystem:     nixSystem,
		// Snapshotted once here, not per-child: nothing between mainRun's
		// entry and this line calls os.Setenv, so it's still a startup capture.
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

	// preflightCtx exists only for startupPreflight, which runs before the
	// pool (and its own Stop watcher) exist: announceStop cancels it on the
	// first latch so an operator's Ctrl-C during the preflight is still
	// honoured, and the defer covers the ordinary return path where no
	// signal ever arrives.
	preflightCtx, cancelPreflight := context.WithCancel(context.Background())
	defer cancelPreflight()

	if stopCleanup == nil {
		stopSig, abortSig, stopCleanup = installStopSignal()
		defer stopCleanup()
	}
	quit := make(chan struct{})
	defer close(quit)
	poolStop, poolAbort := announceStop(stopSig, abortSig, quit, cancelPreflight, em)

	// After the signal wiring, so an operator's Ctrl-C during the preflight
	// is honoured; before Loop, so a refusal happens before any slot, any
	// claim, any Box. After the checkout lock is taken above (acquired, or
	// inherited from the predecessor on a resume), so a second daemon against
	// the same checkout is still refused by the lock's own cheaper path
	// rather than after a full doctor run.
	//
	// A resumed image that was stopped during its preflight still runs the
	// pool: with Stop closed it halts and drains the inherited children
	// instead of abandoning them.
	if h := startupPreflight(preflightCtx, r, em); h.Class != daemon.HaltNone && (res == nil || h.Class != daemon.HaltOperatorStop) {
		// Before finish, so a rolled-back image leaves no halt event behind.
		if tryRollBack(h.Diagnostic()) {
			return 0
		}
		// finish keeps the pre-580296cf halt-event-then-diagnostic order.
		return finish(stderr, em, statusWriter, args.Kinds, h)
	}

	cfg := daemon.Config{
		Kinds:               args.Kinds,
		ResearchReservation: reservation,
		IdleFloor:           idleFloor,
		IdleCap:             idleCap,
		ProbeIntervals:      probeIntervals(demand, probeOverride),
		Trackers:            trackers(demand, doc),
		RepoURL:             repoURL(doc),
		Slots:               slots,
		SelfProgram:         selfProgram,
		FailureBackoff:      failureBackoff,
		BreakerThreshold:    breakerThreshold,
		BreakerWindow:       breakerWindow,
		Awake:               awake,
		Status:              statusWriter,
		Stop:                poolStop,
		Abort:               poolAbort,
		RestartInPlace:      args.RestartInPlace,
	}
	if res != nil {
		cfg.Handoff = &res.handoff
		if res.handoff.RolledBack {
			cfg.SelfUpdate = daemon.SelfUpdate{Phase: daemon.SelfUpdateBlocked, Revision: res.handoff.Revision, Reason: res.handoff.Reason}
		}
	}

	// Every pool-config check runs here, fresh starts included, so each
	// config-invalid halt from this binary goes through finish (which also
	// prints the stderr line); Loop's invalidConfig is reached only by
	// embedders and tests. A resumed image whose config cannot take the
	// handoff (a handed-off kind the new build no longer runs, MAX_PARALLEL
	// below a handed-off slot) refuses like a failed preflight: before any
	// halt event, so a rolled-back image leaves none, and before rootSelf.
	// With no rollback it halts config-invalid and the deferred wait-out
	// drains the inherited children.
	if err := cfg.Validate(); err != nil {
		h := daemon.Halt{Class: daemon.HaltInvalidConfig, Detail: err.Error()}
		if tryRollBack(h.Diagnostic()) {
			return 0
		}
		return finish(stderr, em, statusWriter, args.Kinds, h)
	}

	// After the preflight and config check, so a refused start never replaces
	// the rollback target's root. A fresh context: a stop latched meanwhile
	// cancels preflightCtx, and the roots must still end up right for the next
	// image.
	rootCtx, cancelRoot := context.WithTimeout(context.Background(), selfRootTimeout)
	r.rootSelf(rootCtx, selfProgram, stderr)
	cancelRoot()

	var code int
	code, handedOff = superviseLoop(cfg, r, em, clk, lock, selfProgram)
	return code
}

// superviseLoop runs the pool and, each time it commits an in-place restart,
// execs the prebuilt daemon with the children kept running. handedOff reports
// that the exec happened (only a test seam returns from one), so the caller
// must leave the lock for the successor. A stop latched before the exec, or an
// exec that fails, re-enters the pool with the same handoff document: the
// first drains, the second keeps supervising under a blocked self-update.
func superviseLoop(cfg daemon.Config, r *hostRunner, em *daemon.Emitter, clk daemon.Clock, lock *daemon.CheckoutLock, selfProgram string) (code int, handedOff bool) {
	for {
		// The context is never cancelled in production — cfg.Stop/cfg.Abort are
		// what stop the pool now. A hard-cancel context.Background() here
		// (rather than a CancelFunc threaded through and left uncalled) keeps
		// that fact visible at the call site; the only reason Loop still takes
		// a context at all is for tests and any future embedder (issue #3626).
		h := daemon.Loop(context.Background(), cfg, r, em, clk)
		if h.Handoff == nil {
			return h.ExitCode(), false
		}
		cfg.Handoff = h.Handoff
		err := r.execSuccessor(*h.Handoff, lock.File(), cfg.Stop, selfProgram)
		switch {
		case err == nil:
			return 0, true
		case errors.Is(err, errStopBeforeExec):
		default:
			cfg.SelfUpdate = daemon.SelfUpdate{Phase: daemon.SelfUpdateBlocked, Revision: r.prebuiltRevision(), Reason: "exec: " + err.Error()}
			em.Emit(cfg.SelfUpdate.Event())
		}
	}
}

func main() {
	os.Exit(mainRun(os.Args[1:], os.Stdout, os.Stderr))
}

// successorArgs is the operator's own argv with every --input and
// --handoff-fd pair removed.
// The `nix run .#daemon` wrapper prepends `--input <doc>` pointing at the
// running daemon's document; the successor's wrapper supplies its own, and
// parseArgs lets a later --input win, so replaying the old one would make the
// smoke test validate the old document instead of the new build's. A resumed
// image's own --handoff-fd names a descriptor it has already consumed.
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
		return daemon.Event{Event: "self_update", Phase: string(daemon.SelfUpdateRolledBack), Revision: h.Revision, Reason: h.Reason, FromPath: h.FromPath, ToPath: h.ToPath, Slots: slots}
	}
	return daemon.Event{Event: "self_update", Phase: string(daemon.SelfUpdateHandoff), Revision: h.Revision, FromPath: h.FromPath, ToPath: h.ToPath, Slots: slots}
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
	// SIG_IGN survives it. The stop latch already covers the latter two;
	// SIGHUP is never notified, so put back its default unless the
	// predecessor's was ignored to begin with.
	if err := restoreSIGHUP(h.SIGHUPIgnored); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: restore SIGHUP: %v\n", err)
	}
	return res, nil
}

// errInPlaceUnsupported is returned wherever the exec path is unavailable;
// restart_other.go's stubs and mainRun's refusal share it.
var errInPlaceUnsupported = errors.New("in-place restart is supported only on linux amd64/arm64")
