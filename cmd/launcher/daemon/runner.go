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
// git, RunChild shells out to nix. It is the only place in this binary that
// touches either.
type hostRunner struct {
	repoPath   string
	appAttr    string
	baseBranch string

	mu    sync.Mutex
	child *os.Process // the currently running child, for signal forwarding; nil when idle
}

func newHostRunner(repoPath, appAttr, baseBranch string) *hostRunner {
	return &hostRunner{repoPath: repoPath, appAttr: appAttr, baseBranch: baseBranch}
}

// ResolveRevision shells out to git fetch + rev-parse via CommandContext, not
// Command, so a cancelled ctx (SIGINT/SIGTERM with no child yet to forward
// to) tears the fetch down instead of hanging the daemon until SIGKILL
// (issue #3538).
func (r *hostRunner) ResolveRevision(ctx context.Context) (string, error) {
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

func (r *hostRunner) RunChild(ctx context.Context, kind daemon.Kind, revision string) (daemon.ChildResult, error) {
	argv, err := daemon.ChildCommand(daemon.ChildSpec{
		RepoPath: r.repoPath,
		AppAttr:  r.appAttr,
		Revision: revision,
		Kind:     kind,
	})
	if err != nil {
		return daemon.ChildResult{}, err
	}

	cmd := runnerExecCommand(argv[0], argv[1:]...)
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
	r.child = cmd.Process
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.child = nil
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

// forwardStop sends SIGTERM to the currently running child, if any, so it
// drains its in-flight Boxes rather than being abandoned — the same gesture
// as dogfood.sh's request_stop and cmd/launcher/main.go's notifyStopSignal.
// It never kills the child; a no-op when none is running.
func (r *hostRunner) forwardStop() {
	r.mu.Lock()
	child := r.child
	r.mu.Unlock()
	if child != nil {
		_ = child.Signal(syscall.SIGTERM)
	}
}
