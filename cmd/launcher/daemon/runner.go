package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/report"
)

// hostRunner is the production daemon.Runner: ResolveTip's fetch half shells
// out to git, and RunChild and ResolveTip's eval half shell out to nix. It
// is the only place in this binary that touches either.
type hostRunner struct {
	repoPath   string
	appAttr    string
	baseBranch string
	selfAttr   string
	nixSystem  string
	env        []string // the daemon's own environment, captured once (os.Environ()) so every child sees the same snapshot
	knobs      []string // keys of the Launcher input document's settings map, stripped from env before a child sees it

	// flightMu guards flight below, plus the Moved baseline and the
	// self-path memo fields further down: resolveTipOnce is the sole leader
	// while flight is non-nil, and it writes all of them before clearing
	// flight, so the next leader's Lock is ordered after that write by the
	// same lock/unlock pair (see ResolveTip). RunChild's signal forwarding
	// (forwardSignals) takes no lock of its own — it starts fresh per child
	// from req.Stop/req.Abort — so nothing else in this runner contends
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
	repoPath   string
	appAttr    string
	baseBranch string
	selfAttr   string
	nixSystem  string
	env        []string
	knobs      []string
}

// newHostRunner rejects a nil cfg.env. childEnv
// (internal/daemon/command.go) returns a non-nil slice either way, so past
// this constructor an uncaptured nil and an explicitly empty environment
// are indistinguishable — and the nil is never "inherit the parent", it
// execs every child with no PATH (#3692).
func newHostRunner(cfg hostRunnerConfig) (*hostRunner, error) {
	if cfg.env == nil {
		return nil, errors.New("host runner: no captured environment (hostRunnerConfig.env is nil) — children would exec with no PATH; pass os.Environ()")
	}
	return &hostRunner{
		repoPath:   cfg.repoPath,
		appAttr:    cfg.appAttr,
		baseBranch: cfg.baseBranch,
		selfAttr:   cfg.selfAttr,
		nixSystem:  cfg.nixSystem,
		env:        cfg.env,
		knobs:      cfg.knobs,
	}, nil
}

// runnerFetchCommand is ResolveTip's fetch-half exec seam: a test overrides
// it to skip a real git remote, the way runnerExecCommand/runnerEvalCommand
// already stand in for RunChild/the eval half below. Both the `git fetch`
// and the `git rev-parse` go through it, so a test can count invocations
// (three callers, one fetch) or block inside the first call to synchronise
// on the leader actually being in flight, rather than sleeping and hoping.
var runnerFetchCommand = exec.CommandContext

// fetchRevision shells out to git fetch + rev-parse via runnerFetchCommand
// (context-aware), so a cancelled ctx (SIGINT/SIGTERM with no child yet to
// forward to) tears the fetch down instead of hanging the daemon until
// SIGKILL (issue #3538). No longer serialized by a mutex of its own:
// startupPreflight (main.go) calls this directly, but strictly before Loop
// starts, and once Loop is running the single flight in ResolveTip is the
// only caller — that ordering is what keeps two fetches from ever racing on
// FETCH_HEAD (issue #3539).
func (r *hostRunner) fetchRevision(ctx context.Context) (string, error) {
	const remote = "origin" // nothing varies this yet; inline until a caller needs it (issue #3538 review)
	fetch := runnerFetchCommand(ctx, "git", "-C", r.repoPath, "fetch", remote, r.baseBranch)
	var stderr bytes.Buffer
	fetch.Stderr = &stderr
	if err := fetch.Run(); err != nil {
		return "", fmt.Errorf("git fetch %s %s: %w: %s", remote, r.baseBranch, err, strings.TrimSpace(stderr.String()))
	}
	// FETCH_HEAD, not the local baseBranch ref: a bare `fetch` never moves
	// any local branch, and resolving the local ref instead would silently
	// pin a stale tip whenever the operator's checkout lags origin.
	out, err := runnerFetchCommand(ctx, "git", "-C", r.repoPath, "rev-parse", "FETCH_HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse FETCH_HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runnerExecCommand is the exec seam: a test overrides it to skip nix and
// run a scripted `/bin/sh -c ...` in its place, since RunChild's own
// behaviour (exit code passthrough, issue-line scanning) doesn't depend on
// what the child actually is.
var runnerExecCommand = exec.Command

// runnerEvalCommand is evalSelfPath's exec seam: a test overrides it the same
// way runnerExecCommand is overridden, to skip nix and run a scripted
// `/bin/sh -c ...` in its place.
var runnerEvalCommand = exec.CommandContext

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
		return "", fmt.Errorf("nix eval %s: %w: %s", strings.Join(argv[1:], " "), err, strings.TrimSpace(stderr.String()))
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

func (r *hostRunner) RunChild(ctx context.Context, req daemon.ChildRequest) (daemon.ChildResult, error) {
	childCmd, err := daemon.ChildCommand(daemon.ChildSpec{
		RepoPath: r.repoPath,
		AppAttr:  r.appAttr,
		Revision: req.Revision,
		Kind:     req.Kind,
		Env:      r.env,
		Knobs:    r.knobs,
	})
	if err != nil {
		return daemon.ChildResult{}, err
	}

	cmd := runnerExecCommand(childCmd.Argv[0], childCmd.Argv[1:]...)
	// Knob-stripped env, not the raw process environment — see childEnv.
	// ChildCommand already set SPINDRIFT_REPORT_FD=3 in it; the pipe below
	// is what makes that promise true.
	cmd.Env = childCmd.Env
	// A terminal Ctrl-C delivers SIGINT to the whole foreground process
	// group (daemon, nix run, launcher); the daemon only treats SIGTERM as
	// a drain request (see forwardSignals below), so without this the group
	// signal would kill the child outright and abandon a running Box
	// mid-flight — exactly what issue #3538 forbids. Same reasoning as
	// internal/runner/nixrealize.go's background `nix build` fork; see
	// "Background realize process isolation" in docs/reference.md.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The child's stdout now reaches the daemon's stderr through the
	// descriptor itself, byte-for-byte, with no userspace copy: the daemon's
	// own stdout is the JSON-lines event stream, so the child's output (and
	// anything it itself sends to stderr) both land on the daemon's stderr,
	// same as RunDoctor below.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// cmd.Stdin left nil (os/exec gives the child /dev/null): the Setpgid
	// above leaves it in a background process group, where a tty read is
	// stopped with SIGTTIN, so forwarding the daemon's stdin would hang a
	// slot rather than reach an operator. dogfood.sh could offer a mid-loop
	// vault-unlock prompt only by keeping its launcher in the terminal's
	// foreground group; under the daemon a `<SECRET>_CMD` must instead be
	// non-interactive — unlock the vault before starting it (issue #3548).

	reportRead, reportWrite, err := os.Pipe()
	if err != nil {
		return daemon.ChildResult{}, fmt.Errorf("daemon: report pipe: %w", err)
	}
	// The write end is exec.Cmd.ExtraFiles' first (and only) entry, which
	// always lands at fd 3 in the child — daemon.ReportFD names that same
	// number, and ChildCommand's SPINDRIFT_REPORT_FD=3 is how the child
	// learns it.
	cmd.ExtraFiles = []*os.File{reportWrite}

	if err := cmd.Start(); err != nil {
		_ = reportRead.Close()
		_ = reportWrite.Close()
		return daemon.ChildResult{}, fmt.Errorf("daemon: start child: %w", err)
	}
	// The parent's copy of the write end must close right after Start: the
	// child has its own (inherited, distinct fd) copy, but as long as the
	// parent also holds one open, the read loop below never sees EOF even
	// after the child exits — Wait would then hang behind a read that never
	// returns.
	_ = reportWrite.Close()

	stopForwarding := forwardSignals(cmd.Process, req.Stop, req.Abort)
	defer stopForwarding()

	// Read to EOF (the normal case: the write end closes when the child
	// exits) before Wait, right here rather than on a separate goroutine —
	// there is no stdout goroutine any more, and this read is the only
	// thing standing between the child exiting and Wait returning.
	readReports(reportRead, req.OnRecord)
	// Close the read end before Wait, not after: a child still writing past
	// this point (there shouldn't be any, since readReports just hit EOF)
	// gets EPIPE instead of wedging the daemon on a pipe nobody drains.
	_ = reportRead.Close()

	waitErr := cmd.Wait()
	if waitErr == nil {
		return daemon.ChildResult{Exit: 0}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		// A non-zero exit is the child's own outcome, not a seam failure —
		// daemon.Loop's Interpret is what turns it into a verdict.
		return daemon.ChildResult{Exit: exitErr.ExitCode()}, nil
	}
	return daemon.ChildResult{}, fmt.Errorf("daemon: wait child: %w", waitErr)
}

// readReports reads report-protocol lines from r to EOF and, for each one
// daemon.ParseRecord accepts, calls onRecord (when non-nil). It uses a
// bufio.Reader's ReadSlice rather than a bufio.Scanner deliberately: a
// Scanner reintroduces exactly the line cap and drain-or-wedge problem this
// change deletes the stdout scan to get rid of, and unlike the ReadString
// this replaced, the reader's own fixed buffer size (report.MaxLine) is the
// bound — ReadSlice reports bufio.ErrBufferFull instead of growing the
// buffer, so a line longer than the cap is discarded up to its next newline
// (or EOF) rather than accumulated in memory. Only the first malformed line
// is reported, on os.Stderr, so a hostile or buggy child spamming bad lines
// can't spam the daemon's own log; every malformed line (first and later,
// including an over-long one) is otherwise skipped and parsing continues.
// report.MaxLine — rather than a local literal — is shared with the writer
// side (report.emit), so a legitimate child's line can never itself be over
// this cap; the over-long path below stays as the defence against a
// hostile/buggy child (issue #3627's review finding).
func readReports(r *os.File, onRecord func(daemon.Record)) {
	reader := bufio.NewReaderSize(r, report.MaxLine)
	reportedBad := false
	reportOnce := func(err error) {
		if !reportedBad {
			reportedBad = true
			fmt.Fprintf(os.Stderr, "daemon: read child report: %s\n", err)
		}
	}
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			reportOnce(fmt.Errorf("line exceeds %d bytes, discarding", report.MaxLine))
			// The line so far didn't fit; keep pulling from the same line
			// (never parsing the fragment already in hand — it isn't a
			// complete record) until a newline surfaces or the reader hits
			// EOF/another error, then resume the normal loop.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = reader.ReadSlice('\n')
			}
			if err != nil {
				return
			}
			continue
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		if len(line) != 0 {
			rec, ok, parseErr := daemon.ParseRecord(string(line))
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
			return
		}
	}
}

// runnerDoctorCommand is RunDoctor's exec seam: a test overrides it to skip
// nix and run a scripted `/bin/sh -c ...` in its place — the same gesture
// runnerExecCommand and runnerEvalCommand already make for RunChild and
// evalSelfPath.
var runnerDoctorCommand = exec.CommandContext

// RunDoctor shells out to the pinned child's "doctor" subcommand as the
// daemon's own startup preflight. It uses runnerDoctorCommand (context-aware)
// so a cancelled ctx (SIGINT/SIGTERM during the preflight, before any Box is
// running) tears the child down instead of hanging until SIGKILL — same
// reasoning as evalSelfPath/fetchRevision above.
func (r *hostRunner) RunDoctor(ctx context.Context, revision string) (int, error) {
	doctorCmd, err := daemon.DoctorCommand(daemon.DoctorSpec{RepoPath: r.repoPath, AppAttr: r.appAttr, Revision: revision, Env: r.env, Knobs: r.knobs})
	if err != nil {
		return 0, err
	}

	cmd := runnerDoctorCommand(ctx, doctorCmd.Argv[0], doctorCmd.Argv[1:]...)
	// Same knob-stripped env as RunChild above (see its comment).
	cmd.Env = doctorCmd.Env
	// Both stdout and stderr go straight to the daemon's own stderr: the
	// daemon's stdout is the JSON-lines event stream, so a doctor report
	// written there would corrupt it, and the report itself is where the
	// operator reads which row failed and its remedy. Unlike RunChild, the
	// preflight gets no report pipe and no SPINDRIFT_REPORT_FD (doctorCmd.Env
	// above never carries it — see DoctorCommand): it dispatches nothing, so
	// it has nothing to report.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// cmd.Stdin left nil (os/exec gives the child /dev/null) so doctor takes
	// its non-interactive path rather than sitting on a create-labels prompt
	// nobody in a daemon context can answer; no Setpgid either, unlike
	// RunChild — a Ctrl-C during the preflight should kill exactly it.

	waitErr := cmd.Run()
	if waitErr == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		// A non-zero exit is the preflight's own result, not a seam failure —
		// the caller classifies what the code means. ExitCode() -1 is not an
		// exit code at all: the child was ended by a signal (an operator's
		// Ctrl-C, or ctx tearing it down), so there is no doctor verdict to
		// classify and handing -1 to the caller would report it as one.
		if code := exitErr.ExitCode(); code >= 0 {
			return code, nil
		}
		return 0, fmt.Errorf("daemon: doctor ended without an exit code: %w", waitErr)
	}
	return 0, fmt.Errorf("daemon: run doctor: %w", waitErr)
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
// The returned stop func tears the goroutine down without sending anything
// once the child has already exited on its own (RunChild's deferred call);
// without it the goroutine would sit on stop/abort for the life of the
// daemon process instead of the life of its one child. It blocks until the
// goroutine is actually gone: closing done alone only *asks* it to stop, and
// a goroutine still unscheduled when RunChild returns would then reach its
// select with stop closed too and pick that case at random, signalling a
// child that has already exited.
func forwardSignals(p *os.Process, stop, abort <-chan struct{}) (stopForwarding func()) {
	done := make(chan struct{})
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		select {
		case <-stop:
			_ = runnerSignal(p, syscall.SIGTERM)
		case <-done:
			return
		}
		select {
		case <-abort:
			_ = runnerSignal(p, syscall.SIGINT)
		case <-done:
		}
	}()
	return func() {
		close(done)
		<-gone
	}
}
