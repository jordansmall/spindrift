// Package gitexec holds git-invocation conventions shared across packages:
// building a "git" *exec.Cmd against a caller-supplied leading args slice,
// wrapping an Output() error with any stderr it captured, and the config pairs
// that keep scratch clones free of background maintenance.
package gitexec

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Cmd builds a git command from gitArgs (leading options like -c
// credential.helper=...) followed by args, built from a fresh copy of
// gitArgs so the caller's slice is never mutated. GIT_TERMINAL_PROMPT=0 so a
// missing credential fails fast instead of hanging the caller.
func Cmd(gitArgs []string, args ...string) *exec.Cmd {
	full := append(append([]string{}, gitArgs...), args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

var noAutoMaintenance = [][2]string{{"gc.auto", "0"}, {"maintenance.auto", "false"}}

// NoAutoMaintenanceConfig returns the key/value config pairs that keep a
// detached auto gc or maintenance run from outliving a scratch clone and
// racing its cleanup, for callers that persist them with `git config`. The
// slice is fresh each call.
func NoAutoMaintenanceConfig() [][2]string {
	return append([][2]string{}, noAutoMaintenance...)
}

// NoAutoMaintenance returns those pairs as flag key=value arguments, each
// preceded by flag ("-c" per invocation, "--config" to persist at clone time).
// The slice is fresh each call, so callers may append to it.
func NoAutoMaintenance(flag string) []string {
	args := make([]string, 0, 2*len(noAutoMaintenance))
	for _, kv := range noAutoMaintenance {
		args = append(args, flag, kv[0]+"="+kv[1])
	}
	return args
}

// GuardedArgs returns the argv for a git command run in dir with auto gc and
// maintenance off for that invocation: -C dir, the guard, then args. The slice
// is fresh each call.
func GuardedArgs(dir string, args ...string) []string {
	return append(append([]string{"-C", dir}, NoAutoMaintenance("-c")...), args...)
}

// OutputErr wraps err from a git command run with Output(), keeping the
// stderr Output() captured on *exec.ExitError. prefix names the calling
// package (e.g. "ledger", "butler") so the error reads "<prefix>: <msg>:
// <err>: <stderr>", or "<prefix>: <msg>: <err>" when stderr is empty.
// stderr goes in unredacted: a remote git command can echo a tokened URL
// there, so run forge.RedactURLCredentials over its stderr instead of using
// this helper.
func OutputErr(prefix string, err error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("%s: %s: %w: %s", prefix, msg, err, bytes.TrimSpace(exitErr.Stderr))
	}
	return fmt.Errorf("%s: %s: %w", prefix, msg, err)
}
