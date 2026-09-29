// Package gitexec holds the one git-invocation convention ledger and butler
// both need: building a "git" *exec.Cmd against a caller-supplied leading
// args slice, and wrapping an Output() error with any stderr it captured.
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
