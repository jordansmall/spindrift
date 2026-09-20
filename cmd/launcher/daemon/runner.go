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

// hostRunner is the production daemon.Runner: ResolveRevision shells out to
// git, RunChild and SelfPath shell out to nix. It is the only place in this
// binary that touches either.
type hostRunner struct {
	repoPath   string
	appAttr    string
	baseBranch string
	selfAttr   string
	nixSystem  string

	mu       sync.Mutex
	children map[int]*os.Process // slot -> currently running child, for signal forwarding; empty when idle

	// fetchMu serializes ResolveRevision across Slots goroutines sharing one
	// repoPath: concurrent `git fetch` calls race the refs/remotes/origin/*
	// ref lock the instant the remote tip actually moves, and even past that,
	// one call's fetch+rev-parse can interleave with another's on the single
	// FETCH_HEAD file fetch writes and rev-parse reads (issue #3539). Not the
	// same mutex as mu: mu guards children and forwardStop takes it while a
	// child is running, so sharing it here would block a SIGTERM fan-out
	// behind an in-flight fetch.
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
}

func newHostRunner(cfg hostRunnerConfig) *hostRunner {
	return &hostRunner{
		repoPath:   cfg.repoPath,
		appAttr:    cfg.appAttr,
		baseBranch: cfg.baseBranch,
		selfAttr:   cfg.selfAttr,
		nixSystem:  cfg.nixSystem,
		children:   make(map[int]*os.Process),
	}
}

// ResolveRevision shells out to git fetch + rev-parse via CommandContext, not
// Command, so a cancelled ctx (SIGINT/SIGTERM with no child yet to forward
// to) tears the fetch down instead of hanging the daemon until SIGKILL
// (issue #3538).
func (r *hostRunner) ResolveRevision(ctx context.Context) (string, error) {
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

// SelfPath evaluates the daemon attribute's store path at revision via
// `nix eval`, shelled out with a context-aware exec so a cancelled ctx tears
// the evaluation down instead of hanging the daemon until SIGKILL — same
// reasoning as ResolveRevision above.
//
// It does not take r.fetchMu: that mutex only serializes the FETCH_HEAD race
// between concurrent git fetch/rev-parse pairs (see its doc above), and nix
// eval touches neither, so sharing it here would only add latency behind an
// unrelated slot's in-flight fetch.
func (r *hostRunner) SelfPath(ctx context.Context, revision string) (string, error) {
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

func (r *hostRunner) RunChild(ctx context.Context, req daemon.ChildRequest) (daemon.ChildResult, error) {
	argv, err := daemon.ChildCommand(daemon.ChildSpec{
		RepoPath: r.repoPath,
		AppAttr:  r.appAttr,
		Revision: req.Revision,
		Kind:     req.Kind,
	})
	if err != nil {
		return daemon.ChildResult{}, err
	}

	cmd := runnerExecCommand(argv[0], argv[1:]...)
	// A terminal Ctrl-C delivers SIGINT to the whole foreground process
	// group (daemon, nix run, launcher); the daemon only treats SIGTERM as
	// a drain request (see forwardStop below), so without this the group
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

	r.mu.Lock()
	r.children[req.Slot] = cmd.Process
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.children, req.Slot)
		r.mu.Unlock()
	}()

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
	argv, err := daemon.DoctorCommand(daemon.DoctorSpec{RepoPath: r.repoPath, AppAttr: r.appAttr, Revision: revision})
	if err != nil {
		return 0, err
	}

	cmd := runnerDoctorCommand(ctx, argv[0], argv[1:]...)
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

// forwardStop sends SIGTERM to every currently running child, if any, so
// each drains its in-flight Boxes rather than being abandoned — the same
// gesture as dogfood.sh's request_stop and cmd/launcher/main.go's
// notifyStopSignal. It never kills a child; a no-op when none is running.
func (r *hostRunner) forwardStop() {
	r.mu.Lock()
	children := make([]*os.Process, 0, len(r.children))
	for _, child := range r.children {
		children = append(children, child)
	}
	r.mu.Unlock()
	for _, child := range children {
		_ = child.Signal(syscall.SIGTERM)
	}
}
