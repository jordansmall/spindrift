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
	"strconv"
	"strings"
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/daemon"
)

// inputDocument mirrors the nix-rendered Launcher input document (ADR 0020).
// cmd/launcher/inputdoc.go is the source of truth for the shape; it cannot be
// imported here (a different package main), so this is a minimal local copy
// of the two fields this binary reads.
type inputDocument struct {
	Settings  map[string]string `json:"settings"`
	Artifacts map[string]string `json:"artifacts"`
}

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

// loadInputDocument reads and parses the Launcher input document at path.
func loadInputDocument(path string) (*inputDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input document %s: %w", path, err)
	}
	var doc inputDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse input document %s: %w", path, err)
	}
	return &doc, nil
}

// lookupKnob resolves one schema knob: ambient env first, then the
// document's settings (keyed by env var name — lib/mkHarness.nix's
// documentSettings), then nothing (found=false). When the document also
// carries a value and the ambient env wins anyway, it prints a provenance
// warning to stderr (mirroring cmd/launcher/inputdoc.go's
// warnAmbientKnobEnv) — otherwise nothing records which value actually drove
// the run, and a stale exported override silently wins (ADR 0020).
func lookupKnob(doc *inputDocument, envVar string, stderr io.Writer) (string, bool) {
	if v := os.Getenv(envVar); v != "" {
		if doc != nil {
			if docVal, ok := doc.Settings[envVar]; ok && docVal != "" {
				fmt.Fprintf(stderr, "%s=%s set in environment — knob env overrides are deprecated; use the --input document's settings.%s\n", envVar, v, envVar)
			}
		}
		return v, true
	}
	if doc != nil {
		if v, ok := doc.Settings[envVar]; ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// resolveKnob wraps lookupKnob for knobs that must have a value: the
// default lives in the schema and travels in the document, so a knob absent
// from both is a configuration error, not a silent default.
func resolveKnob(doc *inputDocument, envVar string, stderr io.Writer) (string, error) {
	if v, ok := lookupKnob(doc, envVar, stderr); ok {
		return v, nil
	}
	return "", fmt.Errorf("daemon: no value for %s (not in environment or --input document settings)", envVar)
}

// resolveKnobOptional wraps lookupKnob for knobs whose schema default is
// itself the empty string — absent-from-both is the normal case, not a
// configuration error — while still keeping lookupKnob's ambient-env-wins
// provenance warning.
func resolveKnobOptional(doc *inputDocument, envVar string, stderr io.Writer) string {
	v, _ := lookupKnob(doc, envVar, stderr)
	return v
}

// parseIntKnob parses raw as a base-10 integer no smaller than min, both
// callers' shared shape: resolveKnob can hand back an ambient env override
// of anything, so a malformed or out-of-range knob must fail startup here
// with a clear diagnostic rather than reaching daemon.Loop's own halt, which
// is meant for a genuine programming error, not an operator's mistyped env
// var. label names the constraint in the error text (e.g. "positive
// integer"); it must read correctly next to both "got %q" (unparsable) and
// "got %d" (out of range).
func parseIntKnob(name, label, raw string, min int) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a %s, got %q", name, label, raw)
	}
	if n < min {
		return 0, fmt.Errorf("%s must be a %s, got %d", name, label, n)
	}
	return n, nil
}

// parseSlots turns MAX_PARALLEL's resolved string value into the daemon's
// pool size. lib/env-schema.nix declares it intKind = "positive".
func parseSlots(raw string) (int, error) {
	return parseIntKnob("MAX_PARALLEL", "positive integer", raw, 1)
}

// parseResearchReservation turns RESEARCH_RESERVATION's resolved string
// value into the daemon's research slot floor. lib/env-schema.nix declares
// it intKind = "nonneg" and bounds it at MAX_PARALLEL; the upper bound is
// this knob's own, since parseIntKnob only ever enforces a lower one.
func parseResearchReservation(raw string, slots int) (int, error) {
	n, err := parseIntKnob("RESEARCH_RESERVATION", "non-negative integer", raw, 0)
	if err != nil {
		return 0, err
	}
	if n > slots {
		return 0, fmt.Errorf("RESEARCH_RESERVATION (%d) must not exceed MAX_PARALLEL (%d)", n, slots)
	}
	return n, nil
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

// daemonIdleFloor and daemonIdleCap are the shipped defaults for the
// pool-wide idle backoff; the backoff's mechanics live in backoff.go and
// the operator-facing writeup is in docs/reference.md's daemon exit-code
// section. These values are a defensible first cut, not a tuned final
// answer (final values were out of scope for the issue that added them);
// expect to revisit them against a real unattended run — retuning them
// also means updating TestIdleBackoffCapsAtShippedDefaults
// (cmd/launcher/internal/daemon/backoff_test.go), which spells this pair
// out by hand since it can't import them from package main. Making either
// configurable (per-kind backoff) is a later ticket (loop.go's Config
// doc).
const (
	daemonIdleFloor = 5 * time.Minute
	daemonIdleCap   = 30 * time.Minute
)

// daemonFailureBackoff, daemonBreakerThreshold and daemonBreakerWindow are
// a defensible first cut, not a tuned final answer (final values are out
// of scope for the issue that added them) — expect to revisit them against
// a real unattended run. A systemic fault (an expired token, a forge
// outage) fails every slot's child immediately, so the pool crosses
// daemonBreakerThreshold within about one backoff at the default 3 slots,
// and after (daemonBreakerThreshold-1) backoffs — about four minutes — at
// MAX_PARALLEL=1, where the pool-wide count is one slot's own retries. A
// lone slot reaching the threshold by itself is deliberate rather than a
// gap: a failed child's issue has already left the ready queue (dispatch
// claims it by label swap), so consecutive unclassified failures read as
// systemic rather than as one bad issue retried, and a 1-slot pool has no
// sibling still doing useful work for a spared breaker to protect (see
// TestBreakerDefaults_TripReachableAtOneSlot).
const (
	daemonFailureBackoff   = 1 * time.Minute
	daemonBreakerThreshold = 5
	daemonBreakerWindow    = 15 * time.Minute
)

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

// handleStopSignal waits for a single signal on sig, then cancels ctx and
// calls forward once. Loop only checks ctx.Err() between iterations and
// always waits out a started child (daemon.Loop's doc), so cancelling here
// already gets "stop starting new work, let anything running drain".
// Forwarding SIGTERM on top makes the running child itself start draining
// right away instead of only being noticed once it exits on its own — the
// same gesture as dogfood.sh's request_stop and cmd/launcher/main.go's
// notifyStopSignal. It is never a kill: the child chooses to drain.
func handleStopSignal(sig <-chan os.Signal, cancel context.CancelFunc, forward func()) {
	<-sig
	cancel()
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

// mainRun holds everything main() does: argv parse, input document load,
// knob resolution, repo root, signal wiring, and daemon.Loop, returning the
// exit code rather than calling os.Exit so tests can drive it repeatedly
// with different argv (mirrors cmd/launcher/driver-exec/main.go's mainRun).
func mainRun(argv []string, stdout, stderr io.Writer) int {
	args, err := parseArgs(argv)
	if err != nil {
		return fail(stderr, err)
	}

	doc, err := loadInputDocument(args.InputPath)
	if err != nil {
		return fail(stderr, err)
	}

	appAttr, err := resolveKnob(doc, "DAEMON_APP", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	baseBranch, err := resolveKnob(doc, "BASE_BRANCH", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	maxParallelRaw, err := resolveKnob(doc, "MAX_PARALLEL", stderr)
	if err != nil {
		return fail(stderr, err)
	}
	slots, err := parseSlots(maxParallelRaw)
	if err != nil {
		return fail(stderr, err)
	}

	// The schema validates DAEMON_AWAKE_WINDOW at Nix eval time, but an
	// ambient env override (lookupKnob above) bypasses that entirely, so
	// this runtime parse is the actual guarantee.
	awakeRaw := resolveKnobOptional(doc, "DAEMON_AWAKE_WINDOW", stderr)
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
		selfAttr, err = resolveKnob(doc, "DAEMON_SELF_APP", stderr)
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
		researchReservationRaw, err := resolveKnob(doc, "RESEARCH_RESERVATION", stderr)
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := newHostRunner(hostRunnerConfig{
		repoPath:   repoPath,
		appAttr:    appAttr,
		baseBranch: baseBranch,
		selfAttr:   selfAttr,
		nixSystem:  nixSystem,
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go handleStopSignal(sig, cancel, r.forwardStop)

	// After the signal wiring, so an operator's Ctrl-C during the preflight
	// is honoured; before Loop, so a refusal happens before any slot, any
	// claim, any Box. After AcquireCheckoutLock above, so a second daemon
	// against the same checkout is still refused by the lock's own cheaper
	// path rather than after a full doctor run.
	if reason := startupPreflight(ctx, r, em); reason != "" {
		fmt.Fprintf(stderr, "daemon: %s\n", reason)
		if isOperatorStop(reason) {
			return 0
		}
		return exitPreflightFailed
	}

	cfg := daemon.Config{
		Kinds:               args.Kinds,
		ResearchReservation: reservation,
		IdleFloor:           daemonIdleFloor,
		IdleCap:             daemonIdleCap,
		Slots:               slots,
		SelfProgram:         selfProgram,
		FailureBackoff:      daemonFailureBackoff,
		BreakerThreshold:    daemonBreakerThreshold,
		BreakerWindow:       daemonBreakerWindow,
		Awake:               awake,
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
