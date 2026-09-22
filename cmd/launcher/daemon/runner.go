package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"spindrift.dev/launcher/internal/daemon"
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

	// fetchMu serializes ResolveRevision across Slots goroutines sharing one
	// repoPath: concurrent `git fetch` calls race the refs/remotes/origin/*
	// ref lock the instant the remote tip actually moves, and even past that,
	// one call's fetch+rev-parse can interleave with another's on the single
	// FETCH_HEAD file fetch writes and rev-parse reads (issue #3539).
	fetchMu sync.Mutex
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

// fetchRevision shells out to git fetch + rev-parse via CommandContext, not
// Command, so a cancelled ctx (SIGINT/SIGTERM with no child yet to forward
// to) tears the fetch down instead of hanging the daemon until SIGKILL
// (issue #3538).
func (r *hostRunner) fetchRevision(ctx context.Context) (string, error) {
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()

	const remote = "origin" // nothing varies this yet; inline until a caller needs it (issue #3538 review)
	fetch := exec.CommandContext(ctx, "git", "-C", r.repoPath, "fetch", remote, r.baseBranch)
	var stderr bytes.Buffer
	fetch.Stderr = &stderr
	if err := fetch.Run(); err != nil {
		return "", fmt.Errorf("git fetch %s %s: %w: %s", remote, r.baseBranch, err, strings.TrimSpace(stderr.String()))
	}
	// FETCH_HEAD, not the local baseBranch ref: a bare `fetch` never moves
	// any local branch, and resolving the local ref instead would silently
	// pin a stale tip whenever the operator's checkout lags origin.
	out, err := exec.CommandContext(ctx, "git", "-C", r.repoPath, "rev-parse", "FETCH_HEAD").Output()
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

// runnerEvalCommand is SelfPath's exec seam: a test overrides it the same
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

// stopSignalSequence is the ordered pair of signal kinds forwarded on the
// first and second stop request: SIGTERM, then SIGINT. Two different
// standard signals can't coalesce — the kernel keeps a separate pending
// bit per signal number — whereas two SIGTERMs sent back-to-back collapse
// into one pending signal if the child hasn't drained the first yet. The
// kind is irrelevant to the child: main.go's relaySignals (#3521) counts
// first-vs-second only, on either SIGTERM or SIGINT. A third request has no
// third kind and forwards nothing — see forwardStop's bound.
var stopSignalSequence = [...]os.Signal{syscall.SIGTERM, syscall.SIGINT}

// evalSelfPath evaluates the daemon attribute's store path at revision via
// `nix eval`, shelled out with a context-aware exec so a cancelled ctx tears
// the evaluation down instead of hanging the daemon until SIGKILL — same
// reasoning as fetchRevision above.
//
// It does not take r.fetchMu: that mutex only serializes the FETCH_HEAD race
// between concurrent git fetch/rev-parse pairs (see its doc above), and nix
// eval touches neither, so sharing it here would only add latency behind an
// unrelated slot's in-flight fetch.
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

// ResolveTip fetches the current revision and, when the self check is
// configured (selfAttr != ""), evaluates the daemon's own build at that
// revision in the same call — daemon.Runner's two halves of one tip
// resolution. r.selfAttr == "" means the self check is off (see
// main.go's SPINDRIFT_DAEMON_PROGRAM handling): ResolveTip then skips the
// eval outright and returns a Tip with an empty SelfPath, rather than
// evaluating anything to throw away.
//
// Moved is always false in this slice: the "did the tip move since the
// last one this runner handed out" baseline lands in a later slice.
func (r *hostRunner) ResolveTip(ctx context.Context) (daemon.Tip, error) {
	revision, err := r.fetchRevision(ctx)
	if err != nil {
		return daemon.Tip{}, err
	}
	if r.selfAttr == "" {
		return daemon.Tip{Revision: revision}, nil
	}
	path, err := r.evalSelfPath(ctx, revision)
	if err != nil {
		return daemon.Tip{Revision: revision}, &daemon.SelfEvalError{Err: err}
	}
	return daemon.Tip{Revision: revision, SelfPath: path}, nil
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
	cmd.Env = childCmd.Env
	// A terminal Ctrl-C delivers SIGINT to the whole foreground process
	// group (daemon, nix run, launcher); the daemon only treats SIGTERM as
	// a drain request (see forwardSignals below), so without this the group
	// signal would kill the child outright and abandon a running Box
	// mid-flight — exactly what issue #3538 forbids. Same reasoning as
	// internal/runner/nixrealize.go's background `nix build` fork; see
	// "Background realize process isolation" in docs/reference.md.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Stdout is scanned line-by-line for announce lines (there is no
	// machine-readable channel for them — daemon.ParseAnnouncedIssue's own
	// doc), so it cannot also go straight to os.Stderr via cmd.Stdout; each
	// line is re-emitted below instead, which keeps it "combined output on
	// stderr" without giving up the scan.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return daemon.ChildResult{}, fmt.Errorf("daemon: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return daemon.ChildResult{}, fmt.Errorf("daemon: start child: %w", err)
	}

	stopForwarding := forwardSignals(cmd.Process, req.Stop, req.Abort)
	defer stopForwarding()

	var issues []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(stdout)
	// Raising bufio's 64 KiB default line cap keeps the scan parsing lines
	// a child can legitimately write (a single announce — or stray — line
	// can exceed the default), but the cap is still a cap. What actually
	// keeps an over-long line from wedging the daemon is the drain below:
	// once Scan stops, nothing reads the pipe the child is still writing
	// into, and cmd.Wait() would block forever.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(os.Stderr, line)
		if issue, ok := daemon.ParseAnnouncedIssue(line); ok && !seen[issue] {
			seen[issue] = true
			issues = append(issues, issue)
			// Fires as the announce line is read, not after the child
			// exits: ChildResult.Issues below only arrives post-exit, so
			// it can never name the issue a slot has in flight right now
			// (ChildRequest.OnIssue's own doc, issue #3545).
			if req.OnIssue != nil {
				req.OnIssue(issue)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: scan child stdout: %s\n", err)
		// Drain whatever's left before Wait: the child may still be writing,
		// and Wait never returns while it blocks on an unread pipe. These
		// bytes are relayed but never parsed, so a Box announced past the
		// cap gets no `box` event; report a failed drain rather than
		// compounding that loss with a silent one.
		if _, err := io.Copy(os.Stderr, stdout); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: drain child stdout: %s\n", err)
		}
	}

	waitErr := cmd.Wait()
	if waitErr == nil {
		return daemon.ChildResult{Exit: 0, Issues: issues}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		// A non-zero exit is the child's own outcome, not a seam failure —
		// daemon.Loop's Interpret is what turns it into a verdict.
		return daemon.ChildResult{Exit: exitErr.ExitCode(), Issues: issues}, nil
	}
	// Issues travel with the error too: the child may have announced Boxes
	// before a non-ExitError wait failure, and those Boxes are real work
	// already in flight — dropping them here would lose them from the stream.
	return daemon.ChildResult{Issues: issues}, fmt.Errorf("daemon: wait child: %w", waitErr)
}

// runnerDoctorCommand is RunDoctor's exec seam: a test overrides it to skip
// nix and run a scripted `/bin/sh -c ...` in its place — the same gesture
// runnerExecCommand and runnerEvalCommand already make for RunChild and
// SelfPath.
var runnerDoctorCommand = exec.CommandContext

// RunDoctor shells out to the pinned child's "doctor" subcommand as the
// daemon's own startup preflight. It uses runnerDoctorCommand (context-aware)
// so a cancelled ctx (SIGINT/SIGTERM during the preflight, before any Box is
// running) tears the child down instead of hanging until SIGKILL — same
// reasoning as SelfPath/ResolveRevision above.
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
	// operator reads which row failed and its remedy. Unlike RunChild there
	// are no announce lines to scan, so no pipe/scanner is needed here.
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
