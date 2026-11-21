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

	// flightMu guards flight, the Moved baseline, the self-path memo and
	// prebuilt. The sole flight leader writes the baseline and memo before
	// ResolveTip clears flight, so the next leader's Lock is ordered after
	// those writes.
	flightMu sync.Mutex
	flight   *tipFlight

	// lastRevision/haveLastRevision are the Runner-global, not per-slot,
	// Tip.Moved baseline (issue #3625); the first resolution reports
	// Moved == false.
	lastRevision     string
	haveLastRevision bool

	// memoRevision/memoPath/haveMemo memoize the self path: one `nix eval`
	// per distinct tip however many slots ask. A single entry suffices since
	// a revision is never revisited.
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

// tipFlight is one ResolveTip resolution shared by every caller that
// arrived while it ran. tip/err may only be read after <-done.
type tipFlight struct {
	done chan struct{}
	tip  daemon.Tip
	err  error
}

// hostRunnerConfig is a struct, not positional strings, so a transposed
// pair like appAttr/selfAttr is visible at the call site.
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

// newHostRunner rejects a nil cfg.env: past here withoutKeys makes it
// indistinguishable from an empty environment, and it would exec every
// child with no PATH rather than inherit the parent's (#3692).
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

// runnerFetchCommand is fetchRevision's exec seam, so a test can skip a real
// remote, count invocations, or block inside the leader's call.
var runnerFetchCommand = exec.CommandContext

// fetchWaitDelay bounds how long fetchRevision waits on a git's output pipes
// once it exits or ctx is cancelled: a remote helper (git-remote-https/ssh)
// inherits stderr and can outlive a SIGKILLed git, which would otherwise keep
// Run/Output blocked (issue #5278). exec.ErrWaitDelay only reports on a zero
// exit, so fetch and ls-remote treat it as success; rev-parse does not, since
// its captured output may be truncated.
const fetchWaitDelay = time.Second

// lsRemoteNoMatchExit is `git ls-remote --exit-code`'s "no matching refs"
// status, distinct from auth or network failures.
const lsRemoteNoMatchExit = 2

// redactedStderr renders captured child stderr for embedding in an error;
// those errors reach the event stream and status file, so URL userinfo is
// redacted here, at the source.
func redactedStderr(b *bytes.Buffer) string {
	return forge.RedactURLCredentials(strings.TrimSpace(b.String()))
}

// fetchRevision fetches baseBranch and returns FETCH_HEAD, under ctx so a
// stop tears the fetch down instead of hanging (issue #3538). It has no lock:
// startupPreflight calls it strictly before Loop starts, and after that
// ResolveTip's single flight is the only caller, so two fetches never race
// on FETCH_HEAD (issue #3539). It also confirms r.featureBranch still exists
// on the remote (issue #3883).
func (r *hostRunner) fetchRevision(ctx context.Context) (string, error) {
	const remote = "origin"
	git := func(args ...string) *exec.Cmd {
		cmd := runnerFetchCommand(ctx, "git", append([]string{"-C", r.repoPath}, args...)...)
		cmd.WaitDelay = fetchWaitDelay
		return cmd
	}
	fetch := git("fetch", remote, r.baseBranch)
	var stderr bytes.Buffer
	fetch.Stderr = &stderr
	if err := fetch.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return "", fmt.Errorf("git fetch %s %s: %w: %s", remote, r.baseBranch, err, redactedStderr(&stderr))
	}
	// FETCH_HEAD, not the local ref: a bare `fetch` never moves a local
	// branch, so the local ref would pin a stale tip.
	out, err := git("rev-parse", "FETCH_HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse FETCH_HEAD: %w", err)
	}
	revision := strings.TrimSpace(string(out))

	if r.featureBranch != "" {
		ref := "refs/heads/" + r.featureBranch
		lsRemote := git("ls-remote", "--exit-code", remote, ref)
		var lsStderr bytes.Buffer
		lsRemote.Stderr = &lsStderr
		if err := lsRemote.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
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

// runnerExecCommand is StartChild's exec seam, so a test can run a scripted
// `/bin/sh -c ...` instead of nix.
var runnerExecCommand = exec.Command

// runnerEvalCommand is evalSelfPath's exec seam.
var runnerEvalCommand = exec.CommandContext

// runnerSelfExec is the in-place restart's exec seam, since a real exec never
// returns on success.
var runnerSelfExec = syscall.Exec

// runnerSignal is forwardSignals' send seam. OS delivery can't show a test
// the sequence: a fresh child has no handler yet, so the first signal can
// kill it before the second is sent.
var runnerSignal = func(p *os.Process, sig os.Signal) error { return p.Signal(sig) }

// evalSelfPath evaluates the daemon attribute's store path at revision under
// ctx. Its only caller is the sole flight leader, so it takes no lock.
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

// runnerFlightJoined fires each time a caller joins an in-flight
// resolution; nil in production. It lets a test synchronise on the
// coalescing instead of sleeping.
var runnerFlightJoined func()

// ResolveTip coalesces concurrent callers onto one leader's resolveTipOnce,
// each receiving its exact Tip/error. There is no TTL: once the flight
// clears the next caller starts a fresh one, so no slot gets a revision
// older than the moment it asked.
func (r *hostRunner) ResolveTip(ctx context.Context) (daemon.Tip, error) {
	r.flightMu.Lock()
	if flight := r.flight; flight != nil {
		r.flightMu.Unlock()
		if runnerFlightJoined != nil {
			runnerFlightJoined()
		}
		// A joiner whose own ctx died must not keep waiting on another's
		// fetch: that is the hang issue #3538 forbids.
		select {
		case <-flight.done:
			// A ctx.Err() here is the leader's cancellation, not this joiner's.
			return flight.tip, flight.err
		case <-ctx.Done():
			return daemon.Tip{}, ctx.Err()
		}
	}
	flight := &tipFlight{done: make(chan struct{})}
	r.flight = flight
	r.flightMu.Unlock()

	// Deferred so a panic still releases every joiner. r.flight = nil before
	// close(done): the window between the two only ever starts a fresh flight.
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

// resolveTipOnce does one leader's fetch and, when the self check is on
// (selfAttr != ""), the self eval, plus the Moved and memo bookkeeping.
// Only the sole flight leader runs it.
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
		// The memo stays untouched, so a later resolution at this revision
		// cannot serve a path it never got.
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
// ReadyIDs when it has one (the host outbox, which leaves out the inFlight
// keys), else from its tracker counter. A kind with no source is an error:
// the pool only asks for kinds probeIntervals derived from the same map.
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

// runnerPrebuildCommand is the exec seam for Prebuild's `nix build` and
// smoke test, apart from runnerEvalCommand so tests script them separately.
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
// input and config, so a run past this is a hang; unbounded, it would hold the
// pool in `building` instead of reaching `blocked`. A var so a test can shrink
// it.
var prebuildSmokeTimeout = 2 * time.Minute

// prebuildBuildTimeout bounds `nix eval` and `nix build` together: generous
// for a cold build, but finite so a hung nix reaches `blocked`. Killing the
// build loses no finished derivation. A var so a test can shrink it.
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

	// selfArgs lacks --input (successorArgs), so the successor's wrapper
	// supplies its own document. Dir stays empty so the wrapper finds
	// harness.env from the daemon's cwd, as the real restart will.
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
// check, so even a refused one cannot leak into a later child, but is never
// closed: a forged entry may name one this process holds for something else.
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

// waitOut waits out children no pool claimed, so exiting does not orphan
// them. Concurrently, since each Wait forwards stop and abort only while it
// runs.
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
	// Knob-stripped env (withoutKeys), not the raw environment. It already
	// sets SPINDRIFT_REPORT_FD=3; the pipe below makes that true.
	cmd.Env = childCmd.Env
	// Without its own group, a terminal Ctrl-C would reach the child twice:
	// the group SIGINT plus forwardSignals' SIGTERM. The child's
	// stopsignal.Relay counts deliveries, so it would escalate past the drain
	// to abort a running Box (issue #3538). See "Background realize process
	// isolation" in docs/reference.md.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The daemon's stdout is the event stream, so child output goes straight
	// to a file (the Child log, else the daemon's stderr) with no pipe: a
	// child can neither stall nor die of SIGPIPE when the daemon stops
	// reading. One O_APPEND log for both streams keeps write order. A log
	// that cannot be created never fails the Dispatch.
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
	// No stdin: in a background process group a tty read stops with SIGTTIN
	// and would hang the slot, so a `<SECRET>_CMD` must be non-interactive
	// under the daemon — unlock the vault before starting it (issue #3548).

	reportRead, reportWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("report pipe: %w", err)
	}
	// ExtraFiles[0] always lands at fd 3 in the child (daemon.ReportFD).
	cmd.ExtraFiles = []*os.File{reportWrite}

	if err := cmd.Start(); err != nil {
		_ = reportRead.Close()
		_ = reportWrite.Close()
		return nil, fmt.Errorf("start child: %w", err)
	}
	// While the parent holds a write end, Wait's read never sees EOF.
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

	readErr := readReports(h.reportRead, h.kind, h.onRecord, &h.reports)
	stopWatching()
	if errors.Is(readErr, os.ErrDeadlineExceeded) {
		if h.owner != nil {
			h.owner.register(h)
		}
		return daemon.ChildResult{}, daemon.ErrWaitAbandoned
	}
	// Before proc.Wait, so a straggling writer gets EPIPE instead of wedging.
	_ = h.reportRead.Close()

	state, err := h.proc.Wait()
	if err != nil {
		return daemon.ChildResult{}, fmt.Errorf("wait child: %w", err)
	}
	// A non-zero exit is the child's outcome, not a seam failure. ExitCode is
	// -1 for a signal death.
	return daemon.ChildResult{Exit: state.ExitCode()}, nil
}

// State reads the child's pid, its report pipe's fd and the read state a
// successor continues from. ReportFD stays -1 if the fd cannot be read.
func (h *hostChildHandle) State() daemon.ChildState {
	st := daemon.ChildState{
		PID:        h.proc.Pid,
		ReportFD:   -1,
		Partial:    slices.Clone(h.reports.partial),
		Discarding: h.reports.discarding,
	}
	if fd, err := fileDescriptor(h.reportRead); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: read report pipe fd: %v\n", err)
	} else {
		st.ReportFD = fd
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
// break any SetReadDeadline-based read on f that follows.
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

// readReports feeds each report line daemon.ParseRecord accepts to onRecord
// until a read fails, returning that error so a caller can tell a deadline
// from EOF. A deadline-cut fragment is stashed in st.partial for the next
// call; an over-long line's discard and the once-only reports also persist
// in st across calls. ReadSlice over a report.MaxLine buffer, not a Scanner
// or ReadString, bounds memory: an over-long line is discarded to its
// newline, never accumulated. report.MaxLine is the writer's cap too, so only
// a hostile or buggy child hits that path (issue #3627). Malformed lines are
// skipped and reported once; a kind mismatch has its own once-only report, so
// an earlier malformed line cannot hide an emitter regression.
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
			// May pass report.MaxLine, still bounded by fragment plus buffer.
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
			return err
		}
	}
}

// runnerDoctorCommand is RunDoctor's exec seam.
var runnerDoctorCommand = exec.CommandContext

// RunDoctor runs the pinned child's `doctor` as the startup preflight, under
// ctx so a stop tears it down. labelsLine is doctor's last
// doctor.ErrRequiredLabelsMissing line, or "", so the halt can name the
// missing labels.
func (r *hostRunner) RunDoctor(ctx context.Context, revision string) (exit int, labelsLine string, err error) {
	doctorCmd, err := daemon.DoctorCommand(daemon.DoctorSpec{RepoPath: r.repoPath, AppAttr: r.appAttr, Revision: revision, Env: r.env, Knobs: r.knobs, FeatureBranch: r.featureBranch, Flags: r.doctorFlags})
	if err != nil {
		return 0, "", err
	}

	cmd := runnerDoctorCommand(ctx, doctorCmd.Argv[0], doctorCmd.Argv[1:]...)
	cmd.Env = doctorCmd.Env
	// To stderr, never the event-stream stdout. One shared writer makes
	// os/exec copy through a single goroutine, so the scanner needs no lock.
	out := &markerLineWriter{w: os.Stderr, marker: doctor.ErrRequiredLabelsMissing.Error()}
	cmd.Stdout = out
	cmd.Stderr = out
	// No stdin, so doctor never waits on a create-labels prompt; no Setpgid,
	// so a Ctrl-C during the preflight kills it.

	waitErr := cmd.Run()
	if waitErr == nil {
		return 0, out.line(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		// A non-zero exit is the preflight's result for the caller to
		// classify; -1 means a signal ended it, so there is no verdict.
		if code := exitErr.ExitCode(); code >= 0 {
			return code, out.line(), nil
		}
		return 0, "", fmt.Errorf("doctor ended without an exit code: %w", waitErr)
	}
	return 0, "", waitErr
}

// runnerSweepCommand is SweepLateMerges's exec seam.
var runnerSweepCommand = exec.CommandContext

// SweepLateMerges shells out to the pinned launcher's hidden late-merge sweep
// verb (issue #7342), so the forge reads run outside every pool slot. Its
// output goes to the daemon's stderr (stdout is the JSON event stream), and a
// non-zero exit is an error carrying the exit — the caller only warns.
func (r *hostRunner) SweepLateMerges(ctx context.Context, revision string) error {
	sweepCmd, err := daemon.LateMergeSweepCommand(daemon.LateMergeSweepSpec{RepoPath: r.repoPath, AppAttr: r.appAttr, Revision: revision, Env: r.env, Knobs: r.knobs, FeatureBranch: r.featureBranch})
	if err != nil {
		return err
	}
	cmd := runnerSweepCommand(ctx, sweepCmd.Argv[0], sweepCmd.Argv[1:]...)
	cmd.Env = sweepCmd.Env
	// Own process group, as StartChild's: a terminal Ctrl-C halts the pool and
	// lets the sweep finish; only ctx cancel (hard abort, or a committed
	// in-place handoff) ends it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The default cancel kills only the direct PID, orphaning whatever the
	// sweep forked (a Connector subprocess); pgid == pid under Setpgid.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

// forwardSignals relays stop as SIGTERM and abort as SIGINT onto p for the
// life of one Wait. Two distinct signals, because two of the same kind can
// coalesce into one pending delivery, and the child counts deliveries
// (stopsignal.Relay, #3521). sent latches what was delivered, so a resumed
// Wait never repeats one; the goroutine is its only writer and stopForwarding
// joins it, so sequential calls need no lock. stopForwarding blocks until the
// goroutine is gone: otherwise an unscheduled goroutine could see stop closed
// too and signal a child that already exited.
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

// execHandoff replaces this image with h.ToPath, handing h over on a pipe
// named by --handoff-fd, for both the forward handoff and the rollback. Only
// dups of the lock and report pipes survive the exec, so on a failed exec
// this image carries on untouched. The caller sets h.FromPath, h.ToPath and
// h.Revision, which the successor reads. A nil return means the exec happened
// (only a test seam returns).
func execHandoff(h daemon.Handoff, lock *os.File, stop <-chan struct{}, env, selfArgs []string) error {
	select {
	case <-stop:
		return errStopBeforeExec
	default:
	}

	// Everything fallible but the exec is prepared before the stop signals are
	// ignored, so the window in which a stop is dropped is just the settle.
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
