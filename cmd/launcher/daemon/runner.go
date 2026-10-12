package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/report"
)

// hostRunner is the production daemon.Runner: ResolveTip's fetch half shells
// out to git, and StartChild and ResolveTip's eval half shell out to nix. It
// is the only place in this binary that touches either.
type hostRunner struct {
	repoPath   string
	appAttr    string
	baseBranch string
	// featureBranch goes to children and the doctor preflight as
	// --base-branch; empty means off. It never replaces baseBranch:
	// ResolveTip still fetches, pins to, and self-checks baseBranch's tip.
	featureBranch string
	selfAttr      string
	nixSystem     string
	env           []string      // the daemon's own environment, captured once (os.Environ()) so every child sees the same snapshot
	knobs         []string      // keys of the Launcher input document's settings map, stripped from env before a child sees it
	doctorFlags   []string      // kind flags RunDoctor adds to doctor (see doctorPreflightFlags in main.go)
	demand        demandSources // per-kind tracker Demand counters; a kind absent here is exit-driven

	// stateDir holds the Prebuild GC root; selfArgs is the daemon's own argv
	// minus its wrapper-supplied --input, replayed (plus --check-only) to the
	// successor.
	stateDir string
	selfArgs []string

	// flightMu guards flight below, plus the Moved baseline and the
	// self-path memo fields further down: resolveTipOnce is the sole leader
	// while flight is non-nil, and it writes all of them before clearing
	// flight, so the next leader's Lock is ordered after that write by the
	// same lock/unlock pair (see ResolveTip). A child handle's signal forwarding
	// (forwardSignals) takes no lock of its own — it reads the handle's own
	// stop/abort and sent latch — so nothing else in this runner contends
	// with flightMu.
	flightMu sync.Mutex
	flight   *tipFlight

	// lastRevision/haveLastRevision are the Tip.Moved baseline (issue
	// #3625): Runner-global, not per-slot. haveLastRevision is false until
	// the first resolution ever completes, so that one always reports
	// Moved == false — no last one means nothing moved.
	lastRevision     string
	haveLastRevision bool

	// memoRevision/memoPath/haveMemo are the self-path memo: a single
	// entry, not a map, since git history moves forward and a revision is
	// never revisited — one `nix eval` per distinct tip however many slots
	// ask, with no unbounded growth over a long night. A self-eval failure
	// never writes here, so it can't poison a later resolution at the same
	// revision into serving a path it never got.
	memoRevision string
	memoPath     string
	haveMemo     bool

	// prebuilt is the tip of the last successful Prebuild: the daemon an
	// in-place restart may switch to. Guarded by flightMu.
	prebuilt daemon.Tip

	// handles are children whose report pipe this image holds but no pool
	// supervises: an abandoned Wait's handle, or a successor's inherited
	// one. Keyed by report fd. It keeps each *os.File referenced, since its
	// finalizer would close the descriptor before an exec could carry it on.
	handlesMu sync.Mutex
	handles   map[int]*hostChildHandle
}

// tipFlight is one in-flight (or just-finished, until its leader clears
// r.flight) ResolveTip resolution shared by every caller that arrived while
// it was running. done is closed once tip/err are set, which is what
// publishes them to joiners — they must only be read after <-done.
type tipFlight struct {
	done chan struct{}
	tip  daemon.Tip
	err  error
}

// hostRunnerConfig is everything one hostRunner needs, grouped into a
// struct (rather than five positional strings) so each argument is named at
// the call site: a transposed pair of same-typed fields — appAttr for
// selfAttr, say — is visible there instead of compiling silently.
type hostRunnerConfig struct {
	repoPath      string
	appAttr       string
	baseBranch    string
	featureBranch string
	selfAttr      string
	nixSystem     string
	env           []string
	knobs         []string
	doctorFlags   []string
	demand        demandSources
	stateDir      string
	selfArgs      []string
	// inherited seeds hostRunner.handles with a successor's inherited
	// children (see inheritHandle), keyed by report fd.
	inherited map[int]*hostChildHandle
}

// newHostRunner rejects a nil cfg.env. withoutKeys
// (internal/daemon/command.go) returns a non-nil slice either way, so past
// this constructor an uncaptured nil and an explicitly empty environment
// are indistinguishable — and the nil is never "inherit the parent", it
// execs every child with no PATH (#3692).
func newHostRunner(cfg hostRunnerConfig) (*hostRunner, error) {
	if cfg.env == nil {
		return nil, errors.New("host runner: no captured environment (hostRunnerConfig.env is nil) — children would exec with no PATH; pass os.Environ()")
	}
	r := &hostRunner{
		repoPath:      cfg.repoPath,
		appAttr:       cfg.appAttr,
		baseBranch:    cfg.baseBranch,
		featureBranch: cfg.featureBranch,
		selfAttr:      cfg.selfAttr,
		nixSystem:     cfg.nixSystem,
		env:           cfg.env,
		knobs:         cfg.knobs,
		doctorFlags:   cfg.doctorFlags,
		demand:        cfg.demand,
		stateDir:      cfg.stateDir,
		selfArgs:      cfg.selfArgs,
		handles:       make(map[int]*hostChildHandle, len(cfg.inherited)),
	}
	for fd, h := range cfg.inherited {
		h.owner = r
		r.handles[fd] = h
	}
	return r, nil
}

// runnerFetchCommand is ResolveTip's fetch-half exec seam: a test overrides
// it to skip a real git remote, the way runnerExecCommand/runnerEvalCommand
// already stand in for StartChild/the eval half below. Both the `git fetch`
// and the `git rev-parse` go through it, so a test can count invocations
// (three callers, one fetch) or block inside the first call to synchronise
// on the leader actually being in flight, rather than sleeping and hoping.
var runnerFetchCommand = exec.CommandContext

// lsRemoteNoMatchExit is the exit status `git ls-remote --exit-code` returns
// when the given ref does not exist on the remote (its "no matching refs"
// case), distinct from any other failure (auth, network) that ls-remote
// reports with a different code.
const lsRemoteNoMatchExit = 2

// redactedStderr renders captured child stderr for embedding in an error;
// those errors reach the event stream and status file, so URL userinfo is
// redacted here, at the source.
func redactedStderr(b *bytes.Buffer) string {
	return forge.RedactURLCredentials(strings.TrimSpace(b.String()))
}

// fetchRevision shells out to git fetch + rev-parse via runnerFetchCommand
// (context-aware), so a cancelled ctx (SIGINT/SIGTERM with no child yet to
// forward to) tears the fetch down instead of hanging the daemon until
// SIGKILL (issue #3538). No longer serialized by a mutex of its own:
// startupPreflight (main.go) calls this directly, but strictly before Loop
// starts, and once Loop is running the single flight in ResolveTip is the
// only caller — that ordering is what keeps two fetches from ever racing on
// FETCH_HEAD (issue #3539).
//
// When r.featureBranch is set, it also confirms that branch still exists on
// the remote (issue #3883); living here gives startupPreflight and
// ResolveTip's single flight the same one check.
func (r *hostRunner) fetchRevision(ctx context.Context) (string, error) {
	const remote = "origin" // nothing varies this yet; inline until a caller needs it (issue #3538 review)
	fetch := runnerFetchCommand(ctx, "git", "-C", r.repoPath, "fetch", remote, r.baseBranch)
	var stderr bytes.Buffer
	fetch.Stderr = &stderr
	if err := fetch.Run(); err != nil {
		return "", fmt.Errorf("git fetch %s %s: %w: %s", remote, r.baseBranch, err, redactedStderr(&stderr))
	}
	// FETCH_HEAD, not the local baseBranch ref: a bare `fetch` never moves
	// any local branch, and resolving the local ref instead would silently
	// pin a stale tip whenever the operator's checkout lags origin.
	out, err := runnerFetchCommand(ctx, "git", "-C", r.repoPath, "rev-parse", "FETCH_HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse FETCH_HEAD: %w", err)
	}
	revision := strings.TrimSpace(string(out))

	if r.featureBranch != "" {
		ref := "refs/heads/" + r.featureBranch
		lsRemote := runnerFetchCommand(ctx, "git", "-C", r.repoPath, "ls-remote", "--exit-code", remote, ref)
		var lsStderr bytes.Buffer
		lsRemote.Stderr = &lsStderr
		if err := lsRemote.Run(); err != nil {
			// Any failure but a definitive "no matching ref" stays an
			// ordinary error for the breaker.
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == lsRemoteNoMatchExit {
				return "", &daemon.FeatureBranchGoneError{Remote: remote, Branch: r.featureBranch}
			}
			return "", fmt.Errorf("git ls-remote %s %s: %w: %s", remote, ref, err, redactedStderr(&lsStderr))
		}
	}

	return revision, nil
}

// runnerExecCommand is the exec seam: a test overrides it to skip nix and
// run a scripted `/bin/sh -c ...` in its place, since a child handle's
// Wait behaviour (exit code passthrough, issue-line scanning) doesn't depend on
// what the child actually is.
var runnerExecCommand = exec.Command

// runnerEvalCommand is evalSelfPath's exec seam: a test overrides it the same
// way runnerExecCommand is overridden, to skip nix and run a scripted
// `/bin/sh -c ...` in its place.
var runnerEvalCommand = exec.CommandContext

// runnerSelfExec is the in-place restart's exec seam: a test overrides it, since a real
// exec never returns on success.
var runnerSelfExec = syscall.Exec

// runnerSignal is the signal seam: forwardSignals' goroutine sends through
// it rather than calling p.Signal directly. OS-level delivery can't show a
// test the *sequence* — a child started after Stop/Abort are already
// closed has installed no handler yet, so the first signal can kill it on
// the default disposition before the second is even sent, leaving no trace
// outside. Routing the send through one seam is the only way a test can
// assert "two sends, distinct kinds" instead of just "process died".
var runnerSignal = func(p *os.Process, sig os.Signal) error { return p.Signal(sig) }

// evalSelfPath evaluates the daemon attribute's store path at revision via
// `nix eval`, shelled out with a context-aware exec so a cancelled ctx tears
// the evaluation down instead of hanging the daemon until SIGKILL — same
// reasoning as fetchRevision above.
//
// It does not take r.flightMu itself: resolveTipOnce, the only caller, runs
// this as the sole flight leader (see flightMu's doc), so the flight already
// serializes it without a separate lock held across the eval.
func (r *hostRunner) evalSelfPath(ctx context.Context, revision string) (string, error) {
	argv, err := daemon.SelfCommand(daemon.SelfSpec{
		RepoPath: r.repoPath,
		SelfAttr: r.selfAttr,
		Revision: revision,
		System:   r.nixSystem,
	})
	if err != nil {
		return "", err
	}

	cmd := runnerEvalCommand(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("nix eval %s: %w: %s", strings.Join(argv[1:], " "), err, redactedStderr(&stderr))
	}
	return strings.TrimSpace(string(out)), nil
}

// runnerFlightJoined fires each time a caller joins an in-flight resolution
// instead of starting its own leader; nil in production. Unlike
// runnerExecCommand/runnerEvalCommand/runnerSignal above, this seam stands
// in for nothing real — it exists purely so a test can synchronise on the
// coalescing itself: wait for exactly two joins, then release the leader,
// rather than releasing it after a fixed sleep and hoping the joiners
// arrived first, which would be flaky by construction.
var runnerFlightJoined func()

// ResolveTip resolves the tip once per caller that isn't already covered by
// someone else's in-flight resolution: the first caller becomes the leader
// and does the work below via resolveTipOnce; every caller that arrives
// while that leader's flight is registered waits on it instead and
// receives its exact Tip/error, rather than doing its own fetch. There is
// no TTL — once a flight's done channel is closed and r.flight cleared, the
// next caller starts a fresh one, so no slot is ever handed a revision
// older than the moment it asked.
func (r *hostRunner) ResolveTip(ctx context.Context) (daemon.Tip, error) {
	r.flightMu.Lock()
	if flight := r.flight; flight != nil {
		r.flightMu.Unlock()
		if runnerFlightJoined != nil {
			runnerFlightJoined()
		}
		// A joiner selects on ctx.Done() rather than blocking on <-flight.done
		// unconditionally: a caller left waiting on someone else's fetch
		// after its own ctx died is exactly the hang issue #3538 forbids,
		// just relocated from "waiting on my own git process" to "waiting on
		// another goroutine's".
		select {
		case <-flight.done:
			// flight.tip/flight.err are the leader's own outcome: if err is
			// a ctx.Err(), it's the leader's ctx that was cancelled, not
			// this joiner's — a joiner's own cancellation is the case below.
			return flight.tip, flight.err
		case <-ctx.Done():
			return daemon.Tip{}, ctx.Err()
		}
	}
	flight := &tipFlight{done: make(chan struct{})}
	r.flight = flight
	r.flightMu.Unlock()

	// Deferred so a panic in resolveTipOnce still clears r.flight and closes
	// flight.done, releasing every joiner instead of stranding them on a
	// leader that never returns. r.flight = nil before close(done): the
	// window between the two only ever starts a fresh, legitimate flight.
	defer func() {
		r.flightMu.Lock()
		r.flight = nil
		r.flightMu.Unlock()
		close(flight.done)
	}()

	tip, err := r.resolveTipOnce(ctx)
	flight.tip, flight.err = tip, err
	return tip, err
}

// resolveTipOnce does one leader's fetch and, when the self check is
// configured (selfAttr != ""), the eval half — daemon.Runner's two halves of
// one tip resolution — plus the Moved-baseline and self-path-memo
// bookkeeping ResolveTip's flight wraps around it. Only one goroutine per
// hostRunner ever runs this at a time: ResolveTip only calls it while
// holding the sole registered flight.
//
// r.selfAttr == "" means the self check is off (see main.go's
// SPINDRIFT_DAEMON_PROGRAM handling): resolveTipOnce then skips the eval
// outright and returns a Tip with an empty SelfPath, rather than evaluating
// anything to throw away.
func (r *hostRunner) resolveTipOnce(ctx context.Context) (daemon.Tip, error) {
	revision, err := r.fetchRevision(ctx)
	if err != nil {
		return daemon.Tip{}, err
	}

	// The baseline updates as soon as the fetch half succeeds, before the
	// eval half runs, so a self-eval failure below never leaves it behind.
	r.flightMu.Lock()
	moved := r.haveLastRevision && revision != r.lastRevision
	r.lastRevision = revision
	r.haveLastRevision = true
	r.flightMu.Unlock()

	if r.selfAttr == "" {
		return daemon.Tip{Revision: revision, Moved: moved}, nil
	}

	r.flightMu.Lock()
	if r.haveMemo && r.memoRevision == revision {
		path := r.memoPath
		r.flightMu.Unlock()
		return daemon.Tip{Revision: revision, SelfPath: path, Moved: moved}, nil
	}
	r.flightMu.Unlock()

	path, err := r.evalSelfPath(ctx, revision)
	if err != nil {
		// The memo is left untouched: a failed eval must not poison a later
		// resolution at this same revision into serving a path it never got.
		return daemon.Tip{Revision: revision, Moved: moved}, &daemon.SelfEvalError{Err: err}
	}

	r.flightMu.Lock()
	r.memoRevision = revision
	r.memoPath = path
	r.haveMemo = true
	r.flightMu.Unlock()

	return daemon.Tip{Revision: revision, SelfPath: path, Moved: moved}, nil
}

// Demand counts a kind's ready issues in-process: by name from the source's
// ReadyIDs when it has one (the host outbox, which leaves out the inFlight keys
// the pool's own children hold), else from its tracker counter. A
// kind with no source is an error: the pool only asks for kinds named in
// Config.ProbeIntervals, which probeIntervals derives from the same map.
func (r *hostRunner) Demand(ctx context.Context, kind daemon.Kind, fresh bool, inFlight dispatchkey.Set) (daemon.Demand, error) {
	c, ok := r.demand[kind]
	if !ok {
		return daemon.Demand{}, fmt.Errorf("no demand source for kind %q", kind)
	}
	if named, ok := c.(idReader); ok {
		ids, err := named.ReadyIDs(inFlight)
		if err != nil {
			return daemon.Demand{}, err
		}
		return daemon.Demand{Ready: len(ids), IDs: ids}, nil
	}
	n, err := c.CountReady(fresh)
	if err != nil {
		return daemon.Demand{}, err
	}
	return daemon.Demand{Ready: n}, nil
}

// runnerPrebuildCommand is Prebuild's exec seam for its two stages, `nix
// build` and the successor smoke test, kept apart from runnerEvalCommand so a
// test can script them independently of the eval; tests tell them apart by
// the program name ("nix" or tip.SelfPath).
var runnerPrebuildCommand = exec.CommandContext

// prebuildGCRoot is the single GC-root name under the state directory that
// each prebuild overwrites.
const prebuildGCRoot = "spindrift-daemon.next"

// currentGCRoot roots the running build, or after a handoff the rollback
// target, until the successor's startup succeeds and replaces it with itself.
const currentGCRoot = "spindrift-daemon.current"

// selfRootTimeout bounds the startup rooting `nix build`, which only registers
// a path already in the store.
const selfRootTimeout = 2 * time.Minute

// storeRootOf is the store path holding program ("/nix/store/aaa-old/bin/daemon"
// -> "/nix/store/aaa-old"); ok is false for a program outside the store.
func storeRootOf(program string) (root string, ok bool) {
	if validStorePath("program", program) != nil {
		return "", false
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(program, "/nix/store/"), "/")
	if name == "" {
		return "", false
	}
	return "/nix/store/" + name, true
}

// rootSelf pins this image's own build at currentGCRoot once its startup has
// succeeded, then drops prebuildGCRoot: that link is now this build, the build a
// rollback refused, or stale. A failed rooting only warns and leaves
// prebuildGCRoot, which may be the only root of the running build.
func (r *hostRunner) rootSelf(ctx context.Context, program string, warn io.Writer) {
	root, ok := storeRootOf(program)
	if !ok {
		return
	}
	argv := daemon.SelfRootCommand(root, filepath.Join(r.stateDir, currentGCRoot))
	if _, err := prebuildRun(runnerPrebuildCommand(ctx, argv[0], argv[1:]...)); err != nil {
		fmt.Fprintf(warn, "daemon: warning: could not root %s: %v\n", root, err)
		return
	}
	if err := os.Remove(filepath.Join(r.stateDir, prebuildGCRoot)); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(warn, "daemon: warning: could not drop the %s root: %v\n", prebuildGCRoot, err)
	}
}

// prebuildStderrTail caps, in bytes, the child stderr embedded in a
// PrebuildError: a failed nix build's log can run to thousands of lines, and
// the error lands in the event stream and status file.
const prebuildStderrTail = 2000

// prebuildSmokeTimeout bounds the successor's --check-only run. It only loads
// input and config, so a run past this is a hang; unbounded, it would hold
// the pool in `building` forever instead of reaching `blocked`. A var so a
// test can shrink it.
var prebuildSmokeTimeout = 2 * time.Minute

// prebuildBuildTimeout bounds `nix eval` and `nix build` together. Generous,
// because a cold build can legitimately take long, but finite: a hung nix
// would otherwise hold the pool in `building` forever instead of reaching
// `blocked`. Killing the build loses no finished derivation; the store keeps
// them. A var so a test can shrink it.
var prebuildBuildTimeout = 60 * time.Minute

// prebuildWaitDelay is how long Prebuild waits for a step's output pipes after
// killing it: a wrapper's grandchild can hold a pipe open and would otherwise
// defeat the timeout.
const prebuildWaitDelay = 5 * time.Second

// prebuildStderr cuts on a rune boundary so the tail is still valid UTF-8.
func prebuildStderr(b *bytes.Buffer) string {
	s := redactedStderr(b)
	if len(s) > prebuildStderrTail {
		start := len(s) - prebuildStderrTail
		for start < len(s) && !utf8.RuneStart(s[start]) {
			start++
		}
		s = "..." + s[start:]
	}
	return s
}

// prebuildRun runs cmd capturing stdout and stderr; a failure carries the
// redacted stderr tail. It returns the trimmed stdout.
func prebuildRun(cmd *exec.Cmd) (string, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, prebuildStderr(&stderr))
	}
	return strings.TrimSpace(string(out)), nil
}

// annotateTimeout marks err as a timeout when child's own bound d expired.
// Testing parent too keeps a stop, or a deadline parent itself carries, from
// being reported as child's timeout.
func annotateTimeout(parent, child context.Context, d time.Duration, err error) error {
	if parent.Err() == nil && errors.Is(child.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s: %w", d, err)
	}
	return err
}

// Prebuild realises tip.SelfPath under a GC root in the state directory and
// smoke-tests it, so a self-change halts the pool only for a daemon that is
// known to build and to accept the operator's own arguments. The single
// root (prebuildGCRoot) is overwritten by each prebuild.
func (r *hostRunner) Prebuild(ctx context.Context, tip daemon.Tip) error {
	build := func(err error) error { return &daemon.PrebuildError{Stage: daemon.PrebuildStageBuild, Err: err} }

	drvArgv, err := daemon.SelfDrvCommand(daemon.SelfSpec{
		RepoPath: r.repoPath,
		SelfAttr: r.selfAttr,
		Revision: tip.Revision,
		System:   r.nixSystem,
	})
	if err != nil {
		return build(err)
	}
	buildCtx, cancelBuild := context.WithTimeout(ctx, prebuildBuildTimeout)
	defer cancelBuild()
	evalCmd := runnerEvalCommand(buildCtx, drvArgv[0], drvArgv[1:]...)
	evalCmd.WaitDelay = prebuildWaitDelay
	drvPath, err := prebuildRun(evalCmd)
	if err != nil {
		return build(fmt.Errorf("nix eval %s: %w", strings.Join(drvArgv[1:], " "), annotateTimeout(ctx, buildCtx, prebuildBuildTimeout, err)))
	}
	if drvPath == "" {
		return build(errors.New("nix eval printed no derivation path"))
	}

	buildArgv := daemon.SelfBuildCommand(drvPath, filepath.Join(r.stateDir, prebuildGCRoot))
	buildCmd := runnerPrebuildCommand(buildCtx, buildArgv[0], buildArgv[1:]...)
	buildCmd.WaitDelay = prebuildWaitDelay
	outPath, err := prebuildRun(buildCmd)
	if err != nil {
		return build(fmt.Errorf("nix build %s: %w", drvPath, annotateTimeout(ctx, buildCtx, prebuildBuildTimeout, err)))
	}
	if outPath == "" || !strings.HasPrefix(tip.SelfPath, outPath+"/") {
		return build(fmt.Errorf("nix build of %s produced %q, which does not hold %s", drvPath, outPath, tip.SelfPath))
	}

	// --input is dropped from selfArgs (see successorArgs): the successor's
	// own wrapper supplies the input document it will actually run with.
	// Dir stays empty so the wrapper finds harness.env from the daemon's cwd,
	// exactly as the real restart will.
	smokeArgs := append(slices.Clone(r.selfArgs), "--check-only")
	smokeCtx, cancel := context.WithTimeout(ctx, prebuildSmokeTimeout)
	defer cancel()
	cmd := runnerPrebuildCommand(smokeCtx, tip.SelfPath, smokeArgs...)
	cmd.Env = r.env
	cmd.WaitDelay = prebuildWaitDelay
	if _, err := prebuildRun(cmd); err != nil {
		err = annotateTimeout(ctx, smokeCtx, prebuildSmokeTimeout, err)
		return &daemon.PrebuildError{Stage: daemon.PrebuildStageSmokeTest, Err: fmt.Errorf("%s --check-only: %w", tip.SelfPath, err)}
	}
	r.flightMu.Lock()
	r.prebuilt = tip
	r.flightMu.Unlock()
	return nil
}

// InheritChild re-attaches a child an earlier image handed off, trusting
// nothing in st until it checks out. A standard stream is refused untouched;
// any other report descriptor is re-marked close-on-exec before any other
// check, so even a refused entry can never leak into a later child. A refused
// entry's descriptor is left open, not closed: a forged entry may name one
// this process holds for something else.
func (r *hostRunner) InheritChild(_ context.Context, req daemon.ChildRequest, st daemon.ChildState) (daemon.ChildHandle, error) {
	h := r.takeHandle(st)
	if h == nil {
		var err error
		if h, err = inheritHandle(st); err != nil {
			return nil, err
		}
	}
	h.kind = req.Kind
	h.onRecord = req.OnRecord
	h.stop = req.Stop
	h.abort = req.Abort
	h.owner = r
	return h, nil
}

// inheritHandle is InheritChild's validation: it turns st into a handle with no
// pool wiring yet (kind, record callback and stop channels are the caller's).
// A successor runs it before reading its input document, so a failure is
// reported later by the pool's own InheritChild on the same st.
func inheritHandle(st daemon.ChildState) (*hostChildHandle, error) {
	fd := st.ReportFD
	if fd <= syscall.Stderr {
		return nil, fmt.Errorf("report fd %d is a standard stream", fd)
	}
	if err := setCloseOnExec(fd, true); err != nil {
		return nil, fmt.Errorf("report fd %d: %w", fd, err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("report fd %d: %w", fd, err)
	}
	if uint32(stat.Mode)&syscall.S_IFMT != syscall.S_IFIFO {
		return nil, fmt.Errorf("report fd %d is not a pipe", fd)
	}
	if err := checkWaitableChild(st.PID); err != nil {
		return nil, err
	}
	// Non-blocking before os.NewFile: only then is the File pollable, which
	// the read deadline behind an abandoned Wait needs.
	if err := syscall.SetNonblock(fd, true); err != nil {
		return nil, fmt.Errorf("report fd %d: %w", fd, err)
	}
	proc, err := os.FindProcess(st.PID)
	if err != nil {
		return nil, fmt.Errorf("find child %d: %w", st.PID, err)
	}
	return &hostChildHandle{
		proc:       proc,
		reportRead: os.NewFile(uintptr(fd), "report pipe"),
		reports:    reportState{partial: slices.Clone(st.Partial), discarding: st.Discarding},
	}, nil
}

// takeHandle removes and returns the registered handle for st's report fd and
// pid, or nil. Reusing it keeps the one *os.File on an fd the poller already
// watches: a second os.NewFile on it would be unpollable.
func (r *hostRunner) takeHandle(st daemon.ChildState) *hostChildHandle {
	r.handlesMu.Lock()
	defer r.handlesMu.Unlock()
	h := r.handles[st.ReportFD]
	if h == nil || h.proc.Pid != st.PID {
		return nil
	}
	delete(r.handles, st.ReportFD)
	return h
}

// register records h as held by this image but supervised by no pool.
func (r *hostRunner) register(h *hostChildHandle) {
	fd, err := fileDescriptor(h.reportRead)
	if err != nil {
		// SyscallConn fails only on a closed file, so no descriptor is left
		// for the handles map to protect.
		fmt.Fprintf(os.Stderr, "daemon: register abandoned child %d: %v\n", h.proc.Pid, err)
		return
	}
	r.handlesMu.Lock()
	r.handles[fd] = h
	r.handlesMu.Unlock()
}

// takeUnclaimed removes and returns every handle no pool claimed.
func (r *hostRunner) takeUnclaimed() []*hostChildHandle {
	r.handlesMu.Lock()
	defer r.handlesMu.Unlock()
	hs := slices.Collect(maps.Values(r.handles))
	clear(r.handles)
	return hs
}

// waitOut waits out children no pool claimed (a resumed image's inherited
// children its pool never took, or a fresh image's abandoned Waits), so
// exiting does not orphan them. They are waited concurrently, since each Wait
// forwards stop and abort only while it runs: the daemon's own latch must
// reach them exactly as the pool's does its children.
func waitOut(hs []*hostChildHandle, stop, abort <-chan struct{}) {
	var wg sync.WaitGroup
	for _, h := range hs {
		h.stop, h.abort = stop, abort
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Wait(nil); err != nil {
				fmt.Fprintf(os.Stderr, "daemon: wait for inherited child %d: %v\n", h.proc.Pid, err)
			}
		}()
	}
	wg.Wait()
}

// waitidPID is waitid's P_PID idtype; the syscall package exports neither it
// nor waitid itself.
const waitidPID = 1

// checkWaitableChild reports whether pid is a child of this process, running
// or already exited and unreaped, without reaping it. wait4 refuses WNOWAIT,
// so this is a raw waitid.
func checkWaitableChild(pid int) error {
	var info [128]byte // siginfo_t; sized for the largest of the supported OSes
	_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, waitidPID, uintptr(pid),
		uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOHANG|syscall.WNOWAIT, 0, 0)
	switch errno {
	case 0:
		return nil
	case syscall.ECHILD:
		return fmt.Errorf("pid %d is not a child of this process", pid)
	default:
		return fmt.Errorf("check child %d: %w", pid, errno)
	}
}

func (r *hostRunner) StartChild(ctx context.Context, req daemon.ChildRequest) (daemon.ChildHandle, error) {
	childCmd, err := daemon.ChildCommand(daemon.ChildSpec{
		RepoPath:      r.repoPath,
		AppAttr:       r.appAttr,
		Revision:      req.Revision,
		Kind:          req.Kind,
		Env:           r.env,
		Knobs:         r.knobs,
		FeatureBranch: r.featureBranch,
	})
	if err != nil {
		return nil, err
	}

	cmd := runnerExecCommand(childCmd.Argv[0], childCmd.Argv[1:]...)
	// Knob-stripped env, not the raw process environment — see withoutKeys
	// (internal/daemon/command.go). ChildCommand already set
	// SPINDRIFT_REPORT_FD=3 in it; the pipe below is what makes that
	// promise true.
	cmd.Env = childCmd.Env
	// A terminal Ctrl-C delivers SIGINT to the whole foreground process
	// group (daemon, nix run, launcher). The daemon catches SIGINT and
	// SIGTERM alike (stopsignal.Notify) and forwardSignals below relays a
	// SIGTERM of its own, so without this one Ctrl-C would reach the child
	// twice: the group SIGINT plus the forwarded SIGTERM, distinct signal
	// numbers that can't coalesce (see forwardSignals). The child's
	// stopsignal.Relay counts deliveries, not kinds, so it would escalate
	// straight past the drain to abort a running Box mid-flight — exactly
	// what issue #3538 forbids. Same group isolation as
	// internal/runner/nixrealize.go's background `nix build` fork; see
	// "Background realize process isolation" in docs/reference.md.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The daemon's own stdout is the JSON-lines event stream, so the child's
	// stdout and stderr both go to a file descriptor, never through the
	// daemon: the Child log when one opens, else the daemon's stderr (same as
	// RunDoctor below). os/exec hands an *os.File to the child as-is, with no
	// pipe and no copy goroutine, so a child can neither stall nor die
	// (SIGPIPE) because the daemon stopped reading. The log is O_APPEND and
	// shared by both streams, which keeps their interleaving in write order.
	// A log that cannot be created never fails the Dispatch.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// The child holds its own copy of the Child log once started, so the
	// parent's closes when StartChild returns, on every path.
	var logFile *os.File
	defer func() {
		if logFile != nil {
			_ = logFile.Close()
		}
	}()
	if req.ChildLog != "" {
		f, err := openChildLog(filepath.Join(r.repoPath, req.ChildLog))
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemon: child log %s: %s\n", req.ChildLog, err)
		} else {
			logFile = f
			cmd.Stdout = f
			cmd.Stderr = f
		}
	}
	// cmd.Stdin left nil (os/exec gives the child /dev/null): the Setpgid
	// above leaves it in a background process group, where a tty read is
	// stopped with SIGTTIN, so forwarding the daemon's stdin would hang a
	// slot rather than reach an operator. dogfood.sh could offer a mid-loop
	// vault-unlock prompt only by keeping its launcher in the terminal's
	// foreground group; under the daemon a `<SECRET>_CMD` must instead be
	// non-interactive — unlock the vault before starting it (issue #3548).

	reportRead, reportWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("report pipe: %w", err)
	}
	// The write end is exec.Cmd.ExtraFiles' first (and only) entry, which
	// always lands at fd 3 in the child — daemon.ReportFD names that same
	// number, and ChildCommand's SPINDRIFT_REPORT_FD=3 is how the child
	// learns it.
	cmd.ExtraFiles = []*os.File{reportWrite}

	if err := cmd.Start(); err != nil {
		_ = reportRead.Close()
		_ = reportWrite.Close()
		return nil, fmt.Errorf("start child: %w", err)
	}
	// The parent's copy of the write end must close right after Start: the
	// child has its own (inherited, distinct fd) copy, but as long as the
	// parent also holds one open, the read loop in Wait never sees EOF even
	// after the child exits — Wait would then hang behind a read that never
	// returns.
	_ = reportWrite.Close()

	return &hostChildHandle{
		proc:       cmd.Process,
		reportRead: reportRead,
		kind:       req.Kind,
		onRecord:   req.OnRecord,
		stop:       req.Stop,
		abort:      req.Abort,
		owner:      r,
	}, nil
}

// hostChildHandle is a started host child: Wait reads its report pipe to EOF,
// then reaps it. A Wait can instead be abandoned (daemon.ChildHandle), which
// leaves the child running and the pipe open; a later Wait resumes the read.
// Abandon applies while the report pipe is open; a child closes it only on its
// way out.
type hostChildHandle struct {
	// proc, not an *exec.Cmd: a child a previous daemon image started has no
	// Cmd here, only a pid.
	proc       *os.Process
	reportRead *os.File
	kind       daemon.Kind
	onRecord   func(daemon.Record)
	stop       <-chan struct{}
	abort      <-chan struct{}
	// reports is the read state that must outlive one Wait: a resumed Wait
	// continues the line, discard and once-only warnings the abandoned one left.
	reports reportState
	// sent latches the signals already forwarded, so a resumed Wait never
	// resends one.
	sent signalsSent
	// owner registers the handle when a Wait is abandoned; nil registers
	// nowhere.
	owner *hostRunner
}

// reportState is readReports' state across calls on one pipe.
type reportState struct {
	// partial is a line fragment cut by a deadline, prefixed onto the next
	// call's first line. Copied: ReadSlice's slice dies with its reader.
	partial                       []byte
	discarding                    bool
	reportedBad, reportedMismatch bool
}

// signalsSent records which stop signals forwardSignals already delivered.
type signalsSent struct{ term, intr bool }

// setReadDeadline only logs a failure, so an abandoned Wait whose deadline
// did not take effect blocks until the child exits.
func setReadDeadline(f *os.File, t time.Time) {
	if err := f.SetReadDeadline(t); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: set report pipe read deadline: %v\n", err)
	}
}

func (h *hostChildHandle) Wait(abandon <-chan struct{}) (daemon.ChildResult, error) {
	stopForwarding := forwardSignals(h.proc, h.stop, h.abort, &h.sent)
	defer stopForwarding()

	// Abandoning interrupts the blocking pipe read with a read deadline in
	// the past rather than closing the pipe, so the child keeps its report
	// channel. A previous abandon left that deadline set; clear it first.
	setReadDeadline(h.reportRead, time.Time{})
	stopWatching := func() {}
	if abandon != nil {
		done := make(chan struct{})
		gone := make(chan struct{})
		go func() {
			defer close(gone)
			select {
			case <-abandon:
				setReadDeadline(h.reportRead, time.Now())
			case <-done:
			}
		}()
		stopWatching = func() {
			close(done)
			<-gone
		}
	}

	// Read to EOF (the normal case: the write end closes when the child
	// exits) before Wait, right here rather than on a separate goroutine —
	// there is no stdout goroutine any more, and this read is the only
	// thing standing between the child exiting and Wait returning.
	readErr := readReports(h.reportRead, h.kind, h.onRecord, &h.reports)
	stopWatching()
	if errors.Is(readErr, os.ErrDeadlineExceeded) {
		if h.owner != nil {
			h.owner.register(h)
		}
		return daemon.ChildResult{}, daemon.ErrWaitAbandoned
	}
	// Close the read end before Wait, not after: a child still writing past
	// this point (there shouldn't be any, since readReports just hit EOF)
	// gets EPIPE instead of wedging the daemon on a pipe nobody drains.
	_ = h.reportRead.Close()

	state, err := h.proc.Wait()
	if err != nil {
		return daemon.ChildResult{}, fmt.Errorf("wait child: %w", err)
	}
	// A non-zero exit is the child's own outcome, not a seam failure —
	// daemon.Loop's Interpret is what turns it into a verdict. ExitCode is -1
	// for a signal death.
	return daemon.ChildResult{Exit: state.ExitCode()}, nil
}

// State reads the child's pid, its report pipe's fd and the read state a
// successor continues from. The fd comes from SyscallConn rather than Fd():
// Fd switches the file to blocking mode, which would defeat the read-deadline
// abandon on a handle that is waited on again.
func (h *hostChildHandle) State() daemon.ChildState {
	st := daemon.ChildState{
		PID:        h.proc.Pid,
		ReportFD:   -1,
		Partial:    slices.Clone(h.reports.partial),
		Discarding: h.reports.discarding,
	}
	rc, err := h.reportRead.SyscallConn()
	if err == nil {
		err = rc.Control(func(fd uintptr) { st.ReportFD = int(fd) })
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: read report pipe fd: %v\n", err)
	}
	return st
}

// descriptorFlags returns fd's descriptor flags (F_GETFD).
func descriptorFlags(fd int) (uintptr, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFD), 0)
	if errno != 0 {
		return 0, errno
	}
	return flags, nil
}

// isCloseOnExec reports whether fd is marked close-on-exec.
func isCloseOnExec(fd int) (bool, error) {
	flags, err := descriptorFlags(fd)
	return flags&syscall.FD_CLOEXEC != 0, err
}

// setCloseOnExec sets or clears FD_CLOEXEC on fd, leaving its other
// descriptor flags as they were. Go opens every descriptor close-on-exec, so a
// descriptor survives an exec only if handed off here.
func setCloseOnExec(fd int, on bool) error {
	flags, err := descriptorFlags(fd)
	if err != nil {
		return err
	}
	next := flags &^ syscall.FD_CLOEXEC
	if on {
		next |= syscall.FD_CLOEXEC
	}
	if next == flags {
		return nil
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFD), next); errno != 0 {
		return errno
	}
	return nil
}

// fileDescriptor returns f's descriptor number. It reads it through
// SyscallConn, not f.Fd(): Fd switches the file to blocking mode, which would
// break the SetReadDeadline-based abandon if an exec fails and this image
// carries on.
func fileDescriptor(f *os.File) (int, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	fd := -1
	if err := rc.Control(func(u uintptr) { fd = int(u) }); err != nil {
		return 0, err
	}
	return fd, nil
}

// openChildLog opens (creating it and its parent directories) the Child log
// for appending.
func openChildLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// readReports reads report-protocol lines from r until a read fails (EOF is
// the normal end) and returns that error, so a caller can tell a read
// deadline (os.ErrDeadlineExceeded) from EOF. A line cut short by a deadline
// is not parsed: its bytes are stashed in st.partial and prefixed onto the next
// line a later call reads, and an over-long line being discarded stays
// discarded (st.discarding) across calls, as do the once-only warnings below.
// For each complete line daemon.ParseRecord accepts, it calls onRecord (when
// non-nil). It uses a bufio.Reader's ReadSlice rather than a bufio.Scanner
// deliberately: a Scanner reintroduces exactly the line cap and
// drain-or-wedge problem this change deletes the stdout scan to get rid of,
// and unlike the ReadString
// this replaced, the reader's own fixed buffer size (report.MaxLine) is the
// bound — ReadSlice reports bufio.ErrBufferFull instead of growing the
// buffer, so a line longer than the cap is discarded up to its next newline
// (or EOF) rather than accumulated in memory. Only the first malformed line
// is reported, on os.Stderr, so a hostile or buggy child spamming bad lines
// can't spam the daemon's own log; every malformed line (first and later,
// including an over-long one) is otherwise skipped and parsing continues.
// A kind-mismatched record (daemon.ErrKindMismatch) gets its own once-only
// report, so an earlier malformed line can't hide an emitter regression.
// report.MaxLine — rather than a local literal — is shared with the writer
// side (report.emit), so a legitimate child's line can never itself be over
// this cap; the over-long path below stays as the defence against a
// hostile/buggy child (issue #3627's review finding).
func readReports(r *os.File, kind daemon.Kind, onRecord func(daemon.Record), st *reportState) error {
	reader := bufio.NewReaderSize(r, report.MaxLine)
	reportOnce := func(err error) {
		reported := &st.reportedBad
		if errors.Is(err, daemon.ErrKindMismatch) {
			reported = &st.reportedMismatch
		}
		if !*reported {
			*reported = true
			fmt.Fprintf(os.Stderr, "daemon: read child report: %s\n", err)
		}
	}
	for {
		if st.discarding {
			// Keep pulling from the over-long line (never parsing the
			// fragment — it isn't a complete record) until a newline
			// surfaces or the reader hits EOF/another error.
			var err error
			for {
				if _, err = reader.ReadSlice('\n'); !errors.Is(err, bufio.ErrBufferFull) {
					break
				}
			}
			if err != nil {
				return err
			}
			st.discarding = false
			continue
		}
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			reportOnce(fmt.Errorf("line exceeds %d bytes, discarding", report.MaxLine))
			// A stashed fragment belongs to this over-long line; drop it.
			st.partial = nil
			st.discarding = true
			continue
		}
		if len(st.partial) != 0 {
			// Prefix and new bytes together may pass report.MaxLine; that
			// only happens for a hostile child, and the memory is still
			// bounded by one fragment plus one buffer.
			line = append(st.partial, line...)
			st.partial = nil
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			st.partial = bytes.Clone(line)
			return err
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		if len(line) != 0 {
			rec, ok, parseErr := daemon.ParseRecord(string(line), kind)
			switch {
			case parseErr != nil:
				reportOnce(parseErr)
			case ok && onRecord != nil:
				onRecord(rec)
			}
		}
		if err != nil {
			// EOF (the normal case, once the write end closes at child
			// exit) and any other read error both just end the loop: there
			// is nothing left to read either way.
			return err
		}
	}
}

// runnerDoctorCommand is RunDoctor's exec seam: a test overrides it to skip
// nix and run a scripted `/bin/sh -c ...` in its place — the same gesture
// runnerExecCommand and runnerEvalCommand already make for StartChild and
// evalSelfPath.
var runnerDoctorCommand = exec.CommandContext

// RunDoctor shells out to the pinned child's "doctor" subcommand as the
// daemon's own startup preflight. It uses runnerDoctorCommand (context-aware)
// so a cancelled ctx (SIGINT/SIGTERM during the preflight, before any Box is
// running) tears the child down instead of hanging until SIGKILL — same
// reasoning as evalSelfPath/fetchRevision above.
//
// labelsLine is the last doctor line carrying doctor.ErrRequiredLabelsMissing,
// "" when doctor printed none: the report still streams to stderr, but
// startupPreflight needs the line itself so its halt can name which labels
// are missing.
func (r *hostRunner) RunDoctor(ctx context.Context, revision string) (exit int, labelsLine string, err error) {
	doctorCmd, err := daemon.DoctorCommand(daemon.DoctorSpec{RepoPath: r.repoPath, AppAttr: r.appAttr, Revision: revision, Env: r.env, Knobs: r.knobs, FeatureBranch: r.featureBranch, Flags: r.doctorFlags})
	if err != nil {
		return 0, "", err
	}

	cmd := runnerDoctorCommand(ctx, doctorCmd.Argv[0], doctorCmd.Argv[1:]...)
	// Same knob-stripped env as StartChild above (see its comment).
	cmd.Env = doctorCmd.Env
	// Both stdout and stderr go straight to the daemon's own stderr: the
	// daemon's stdout is the JSON-lines event stream, so a doctor report
	// written there would corrupt it, and the report itself is where the
	// operator reads which row failed and its remedy. Unlike StartChild, the
	// preflight gets no report pipe and no SPINDRIFT_REPORT_FD (doctorCmd.Env
	// above never carries it — see DoctorCommand): it dispatches nothing, so
	// it has nothing to report.
	//
	// One shared writer for both streams: os/exec then copies through a
	// single goroutine, so the line scanner needs no lock.
	out := &markerLineWriter{w: os.Stderr, marker: doctor.ErrRequiredLabelsMissing.Error()}
	cmd.Stdout = out
	cmd.Stderr = out
	// cmd.Stdin left nil (os/exec gives the child /dev/null) so doctor takes
	// its non-interactive path rather than sitting on a create-labels prompt
	// nobody in a daemon context can answer; no Setpgid either, unlike
	// StartChild — a Ctrl-C during the preflight should kill exactly it.

	waitErr := cmd.Run()
	if waitErr == nil {
		return 0, out.line(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		// A non-zero exit is the preflight's own result, not a seam failure —
		// the caller classifies what the code means. ExitCode() -1 is not an
		// exit code at all: the child was ended by a signal (an operator's
		// Ctrl-C, or ctx tearing it down), so there is no doctor verdict to
		// classify and handing -1 to the caller would report it as one.
		if code := exitErr.ExitCode(); code >= 0 {
			return code, out.line(), nil
		}
		return 0, "", fmt.Errorf("doctor ended without an exit code: %w", waitErr)
	}
	return 0, "", waitErr
}

// maxMarkerLine bounds the partial line markerLineWriter holds, so a doctor
// that never prints a newline cannot grow the daemon's memory.
const maxMarkerLine = 4096

// markerLineWriter passes everything through to w while remembering the last
// line containing marker. Only the current partial line is buffered, capped
// at maxMarkerLine.
type markerLineWriter struct {
	w       io.Writer
	marker  string
	partial []byte
	last    string
}

func (m *markerLineWriter) Write(p []byte) (int, error) {
	n, err := m.w.Write(p)
	for _, b := range p {
		if b == '\n' {
			m.flush()
			continue
		}
		if len(m.partial) < maxMarkerLine {
			m.partial = append(m.partial, b)
		}
	}
	return n, err
}

func (m *markerLineWriter) flush() {
	if l := string(m.partial); strings.Contains(l, m.marker) {
		m.last = strings.TrimSpace(l)
	}
	m.partial = m.partial[:0]
}

// line returns the last marker line, counting an unterminated final one.
func (m *markerLineWriter) line() string {
	m.flush()
	return m.last
}

// forwardSignals starts one goroutine that watches stop and abort for the
// life of a single child and relays each onto p: SIGTERM when stop closes,
// SIGINT when abort closes. It replaces the old shared children
// map/counter/replay with per-child state that needs no lock: a child
// started after stop (and/or abort) is already closed observes the close
// immediately — a select on a closed channel fires at once — which is
// exactly the old replay loop's job, now structural rather than counted.
// nil stop/abort (a ChildRequest carrying no latch, e.g. a one-off doctor
// run) is safe: a select on a nil channel simply never fires.
//
// SIGTERM then SIGINT, never twice the same kind: two identical standard
// signals sent back-to-back can coalesce into one pending delivery (the
// kernel keeps one pending bit per signal number), so a SIGTERM escalation
// could be lost the same way; two distinct kinds can't coalesce. The child
// itself only counts deliveries, not kinds (stopsignal.Relay, #3521),
// so which signal arrives is the daemon's problem to get right, not the
// child's.
//
// sent latches what was already delivered, so a later call for the same
// child (a Wait resumed after an abandon) never repeats a kind. The goroutine
// is the only writer and the returned stop func joins it, so sequential calls
// need no lock.
//
// The returned stop func tears the goroutine down without sending anything
// once the child has already exited on its own (the handle's Wait defers it);
// without it the goroutine would sit on stop/abort for the life of the
// daemon process instead of the life of its one child. It blocks until the
// goroutine is actually gone: closing done alone only *asks* it to stop, and
// a goroutine still unscheduled when Wait returns would then reach its
// select with stop closed too and pick that case at random, signalling a
// child that has already exited.
func forwardSignals(p *os.Process, stop, abort <-chan struct{}, sent *signalsSent) (stopForwarding func()) {
	done := make(chan struct{})
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		if !sent.term {
			select {
			case <-stop:
				sent.term = true
				_ = runnerSignal(p, syscall.SIGTERM)
			case <-done:
				return
			}
		}
		if !sent.intr {
			select {
			case <-abort:
				sent.intr = true
				_ = runnerSignal(p, syscall.SIGINT)
			case <-done:
			}
		}
	}()
	return func() {
		close(done)
		<-gone
	}
}

// errStopBeforeExec is execSuccessor's refusal to exec once a stop is latched:
// the caller re-enters the pool, which halts and drains its children.
var errStopBeforeExec = errors.New("stop latched before exec")

// execTarget returns the prebuilt daemon an in-place restart may switch to. The
// path must be a clean store path and the one evaluated at the latest fetched
// base-branch tip (ResolveTip evaluates baseBranch even with featureBranch
// set), so a tip that moved on again, or whose eval failed, is not exec'd.
func (r *hostRunner) execTarget() (daemon.Tip, error) {
	r.flightMu.Lock()
	defer r.flightMu.Unlock()
	t, p := r.prebuilt, r.prebuilt.SelfPath
	pathErr := validStorePath("prebuilt daemon", p)
	switch {
	case p == "":
		return daemon.Tip{}, errors.New("no prebuilt daemon")
	case pathErr != nil:
		return daemon.Tip{}, pathErr
	case !r.haveMemo || !r.haveLastRevision || r.memoRevision != r.lastRevision:
		return daemon.Tip{}, fmt.Errorf("no self path evaluated at the latest fetched tip %q", r.lastRevision)
	case r.memoPath != p:
		return daemon.Tip{}, fmt.Errorf("prebuilt daemon %q is not the latest tip's %q", p, r.memoPath)
	}
	return t, nil
}

// prebuiltRevision is the tip revision of the last successful Prebuild, "" if none.
func (r *hostRunner) prebuiltRevision() string {
	r.flightMu.Lock()
	defer r.flightMu.Unlock()
	return r.prebuilt.Revision
}

// stopSignals are ignored across the exec window. A stop landing there must be
// dropped, not left pending: the Go runtime unblocks SIGTERM/SIGINT/SIGHUP
// before it installs handlers, so one pending at the exec would kill the
// successor with the default action before its main runs. SIG_IGN survives
// execve; handlers do not.
var stopSignals = []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

// signalSettle is how long execSuccessor waits after ignoring the stop
// signals, so a stop the Go runtime already took before the SIG_IGN drains
// through the os/signal relay into the stop latch and is seen by the re-check.
var signalSettle = 20 * time.Millisecond

// dupInheritable dups fd without close-on-exec; ForkLock keeps a concurrent
// fork from catching it half-made.
func dupInheritable(fd int) (int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	return syscall.Dup(fd)
}

// validStorePath refuses a program path an exec may not switch to: it must be
// clean and under /nix/store/. what names the program in the error.
func validStorePath(what, p string) error {
	switch {
	case filepath.Clean(p) != p:
		return fmt.Errorf("%s path %q is not clean", what, p)
	case !strings.HasPrefix(p, "/nix/store/"):
		return fmt.Errorf("%s path %q is not under /nix/store/", what, p)
	}
	return nil
}

// execSuccessor replaces this image with the prebuilt daemon, handing h to it
// on a pipe named by --handoff-fd. fromPath is this image's own program,
// recorded in the document for the successor. A nil return means the exec
// happened (only a test seam returns).
func (r *hostRunner) execSuccessor(h daemon.Handoff, lock *os.File, stop <-chan struct{}, fromPath string) error {
	target, err := r.execTarget()
	if err != nil {
		return err
	}
	if root, ok := storeRootOf(fromPath); ok {
		// The rollback target must survive a collection between now and the
		// successor's startup.
		got, err := os.Readlink(filepath.Join(r.stateDir, currentGCRoot))
		if err != nil {
			return fmt.Errorf("rollback target %s: read %s: %w", root, currentGCRoot, err)
		}
		if got != root {
			return fmt.Errorf("rollback target %s is not rooted at %s", root, currentGCRoot)
		}
	}
	h.FromPath, h.ToPath, h.Revision = fromPath, target.SelfPath, target.Revision
	if err := execHandoff(h, lock, stop, r.env, r.selfArgs); err != nil {
		return err
	}
	r.forgetHandles()
	return nil
}

// forgetHandles drops every child handle once an exec has handed the children
// on, so the deferred wait-out neither waits on nor signals them.
func (r *hostRunner) forgetHandles() {
	r.handlesMu.Lock()
	clear(r.handles)
	r.handlesMu.Unlock()
}

// execHandoff replaces this image with the program at h.ToPath, handing h to it
// on a pipe named by --handoff-fd; both directions of a restart (the forward
// handoff and the rollback) go through it, so it needs no runner. The lock and
// every slot's report pipe are dup'd into the document and the dups survive
// the exec; the originals stay close-on-exec and untouched, so on a failed
// exec this image carries on with h as it was. The caller sets h.FromPath,
// h.ToPath and h.Revision, which the successor reads. A nil return means the
// exec happened (only a test seam returns).
func execHandoff(h daemon.Handoff, lock *os.File, stop <-chan struct{}, env, selfArgs []string) error {
	select {
	case <-stop:
		return errStopBeforeExec
	default:
	}

	// Everything fallible but the exec is prepared before the stop signals are
	// ignored, so the window in which a stop is dropped stays as short as the
	// settle itself.
	hupIgnored, err := sigHUPIgnored()
	if err != nil {
		return fmt.Errorf("prepare handoff: %w", err)
	}
	var dups []int
	release := func() {
		for _, fd := range dups {
			_ = syscall.Close(fd)
		}
	}
	dup := func(src int) (int, error) {
		fd, err := dupInheritable(src)
		if err == nil {
			dups = append(dups, fd)
		}
		return fd, err
	}

	wire := h
	wire.SIGHUPIgnored = hupIgnored
	wire.Slots = slices.Clone(h.Slots)
	lockFD, err := fileDescriptor(lock)
	if err == nil {
		wire.LockFD, err = dup(lockFD)
	}
	for i := 0; err == nil && i < len(wire.Slots); i++ {
		wire.Slots[i].Child.ReportFD, err = dup(wire.Slots[i].Child.ReportFD)
	}
	var doc bytes.Buffer
	if err == nil {
		err = daemon.EncodeHandoff(&doc, wire)
	}
	var docFD int
	if err == nil {
		if docFD, err = handoffDocPipe(doc.Bytes()); err == nil {
			dups = append(dups, docFD)
		}
	}
	if err != nil {
		release()
		return fmt.Errorf("prepare handoff: %w", err)
	}

	restore, err := ignoreStopSignals()
	if err != nil {
		release()
		return fmt.Errorf("ignore stop signals: %w", err)
	}
	if signalSettle > 0 {
		time.Sleep(signalSettle)
	}
	select {
	case <-stop:
		restore()
		release()
		return errStopBeforeExec
	default:
	}

	argv := append([]string{h.ToPath}, selfArgs...)
	argv = append(argv, handoffFlag, strconv.Itoa(docFD))
	err = runnerSelfExec(h.ToPath, argv, env)
	restore()
	if err != nil {
		release()
		return err
	}
	return nil
}
