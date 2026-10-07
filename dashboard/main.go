// Command dashboard is a read-only web view of a Daemon's published status
// file (ADR 0060). It reads only that file and never takes the Daemon's lock.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const defaultListen = "127.0.0.1:8099"

// runHooks lets tests observe the bound address and end the server; nil
// means serve until SIGINT/SIGTERM.
type runHooks struct {
	ready chan<- net.Addr
	stop  <-chan struct{}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr, nil))
}

func run(args []string, stderr io.Writer, hooks *runHooks) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	checkout := fs.String("checkout", "", "checkout the Daemon runs in (default: git root of the working directory)")
	listen := fs.String("listen", defaultListen, "address to listen on")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	statusPath, err := resolveStatusPath(*checkout)
	if err != nil {
		fmt.Fprintf(stderr, "dashboard: %v\n", err)
		return 1
	}

	// Bind before announcing so a taken port fails here, naming the address.
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "dashboard: listen %s: %s\n", *listen, listenReason(err))
		return 1
	}
	defer ln.Close()
	addr := ln.Addr().String()
	fmt.Fprintf(stderr, "dashboard: serving http://%s (unauthenticated: anyone who can reach this address can read the Daemon's state)\n", addr)
	if hooks != nil && hooks.ready != nil {
		hooks.ready <- ln.Addr()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if hooks != nil && hooks.stop != nil {
		go func() {
			select {
			case <-hooks.stop:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	srv := &http.Server{Handler: newServer(statusPath), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		fmt.Fprintf(stderr, "dashboard: serve: %v\n", err)
		return 1
	case <-ctx.Done():
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	// Best effort: the process exits next either way, and a timeout only means
	// a slow client was cut off.
	_ = srv.Shutdown(shutdownCtx)
	return 0
}

// listenReason unwraps a net.Listen error to its cause, so the message reads
// "address already in use" rather than repeating the op and address.
func listenReason(err error) string {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Err.Error()
	}
	return err.Error()
}

func resolveStatusPath(checkout string) (string, error) {
	if checkout == "" {
		root, err := gitRevParse(".", "--show-toplevel")
		if err != nil {
			return "", fmt.Errorf("no --checkout given and the working directory is not in a git checkout: %w", err)
		}
		checkout = root
	}
	return statusPathFor(checkout)
}

// statusPathFor resolves checkout's git dir the way the daemon does, so a
// linked worktree finds its own status file rather than the main one's.
func statusPathFor(checkout string) (string, error) {
	dir, err := gitRevParse(checkout, "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("%s is not a git checkout: %w", checkout, err)
	}
	return filepath.Join(dir, statusFileName), nil
}

func gitRevParse(dir, flag string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", flag).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
