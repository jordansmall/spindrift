package butler

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Head resolves branch's tip in repo (a bare Accumulation repo) to a full
// commit sha.
func Head(repo, branch string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return "", outputErr(err, "resolve refs/heads/%s", branch)
	}
	return strings.TrimSpace(string(out)), nil
}

// TrackedFiles returns every path git tracks in commit's tree, in whatever
// order `git ls-tree` reports them (NextScope sorts its own copy, so callers
// needn't).
func TrackedFiles(repo, commit string) ([]string, error) {
	out, err := exec.Command("git", "-C", repo, "ls-tree", "-r", "--name-only", "-z", commit).Output()
	if err != nil {
		return nil, outputErr(err, "ls-tree %s", commit)
	}
	trimmed := bytes.Trim(out, "\x00")
	if len(trimmed) == 0 {
		return nil, nil
	}
	parts := bytes.Split(trimmed, []byte{0})
	files := make([]string, len(parts))
	for i, p := range parts {
		files[i] = string(p)
	}
	return files, nil
}

// outputErr wraps err from a git command run with Output(), keeping the
// stderr Output() captured on *exec.ExitError (same convention as
// ledger.Local's git wrapper).
func outputErr(err error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("butler: %s: %w: %s", msg, err, bytes.TrimSpace(exitErr.Stderr))
	}
	return fmt.Errorf("butler: %s: %w", msg, err)
}
