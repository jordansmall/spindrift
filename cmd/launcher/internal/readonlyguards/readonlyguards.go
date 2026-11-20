// Package readonlyguards renders and installs the runtime read-only git-push
// guard named by lib/prompt-contract.nix's forbiddenMarkers registry (issues
// #2464, #2499, #2509). The hook prints a row's RuntimeMessage verbatim; the
// row's Message field is written for promptassembly.Validate's prompt-time
// check and reads as nonsense at runtime (issue #2509 Finding 2).
package readonlyguards

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/promptassembly"
)

// enforceGitHook is the one Enforce value Install acts on; a row with any
// other value (prompt-only today) installs nothing.
const enforceGitHook = "git-hook"

// hookNames are the git hook filenames Install writes identical content to:
// pre-push covers a working checkout's client-side push, pre-receive covers
// a bare or decoy repo used as a push target. Which one fires depends on how
// the caller wires RepoDir, so Install writes both unconditionally.
var hookNames = []string{"pre-push", "pre-receive"}

// Config is what Install needs to render and install the guards a set of
// forbiddenMarkers rows describes.
type Config struct {
	// RepoDir is the git repository whose hooks directory receives the
	// git-hook content. Required only when rows contains a git-hook row.
	RepoDir string
	// ExtraRepoDirs names additional repositories that receive the identical
	// hook content, additive to RepoDir and never a substitute for it. A
	// decoy repo only sees a plain origin push, since only that resolves
	// through origin's repointed pushurl; a push to an explicit URL or a
	// non-origin remote needs $WORK_DIR's own hook (issue #2509 Finding 1).
	ExtraRepoDirs []string
}

// Result reports what Install installed, for a caller that wants to log it
// or act on it.
type Result struct {
	// HookInstalled reports whether any git-hook row was installed.
	HookInstalled bool
}

// Install renders one hook body and installs it as both pre-push and
// pre-receive for git-hook rows, and nothing for other rows. It logs what it
// installed to out, which may be nil to discard the log.
func Install(rows []promptassembly.ForbiddenMarkerRow, cfg Config, out io.Writer) (Result, error) {
	if out == nil {
		out = io.Discard
	}
	var result Result

	if hookRows := filterRows(rows, enforceGitHook); len(hookRows) > 0 {
		if cfg.RepoDir == "" {
			return result, fmt.Errorf("readonlyguards: install git-hook guard: RepoDir is empty")
		}
		if err := installGitHook(hookRows, cfg.RepoDir, cfg.ExtraRepoDirs, out); err != nil {
			return result, err
		}
		result.HookInstalled = true
	}

	return result, nil
}
func filterRows(rows []promptassembly.ForbiddenMarkerRow, enforce string) []promptassembly.ForbiddenMarkerRow {
	var out []promptassembly.ForbiddenMarkerRow
	for _, row := range rows {
		if row.Enforce == enforce {
			out = append(out, row)
		}
	}
	return out
}

// gitHooksDir returns the hooks directory git consults for repoDir:
// repoDir/.git/hooks for a normal working copy, repoDir/hooks otherwise,
// since a bare repo has no .git subdirectory and is itself the git
// directory. Getting this wrong writes a hook git never reads, leaving the
// guard absent while Result.HookInstalled still reports true.
func gitHooksDir(repoDir string) string {
	if info, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil && info.IsDir() {
		return filepath.Join(repoDir, ".git", "hooks")
	}
	return filepath.Join(repoDir, "hooks")
}

// HookPaths lists the files Install writes the git-hook guard to in repoDir,
// so a caller can assert they are in place.
func HookPaths(repoDir string) []string {
	hooksDir := gitHooksDir(repoDir)
	paths := make([]string, 0, len(hookNames))
	for _, name := range hookNames {
		paths = append(paths, filepath.Join(hooksDir, name))
	}
	return paths
}

// installGitHook renders one hook body and installs it under every name in
// hookNames, in repoDir's hooks directory and in each extraRepoDirs entry's.
// See Config.ExtraRepoDirs for why both destinations matter.
func installGitHook(hookRows []promptassembly.ForbiddenMarkerRow, repoDir string, extraRepoDirs []string, out io.Writer) error {
	script := renderGitHookScript(hookRows)

	dirs := make([]string, 0, 1+len(extraRepoDirs))
	dirs = append(dirs, repoDir)
	dirs = append(dirs, extraRepoDirs...)

	for _, dir := range dirs {
		hooksDir := gitHooksDir(dir)
		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			return fmt.Errorf("readonlyguards: mkdir hooks dir %s: %w", hooksDir, err)
		}

		for _, name := range hookNames {
			hookPath := filepath.Join(hooksDir, name)
			if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
				return fmt.Errorf("readonlyguards: write hook %s: %w", hookPath, err)
			}
		}

		fmt.Fprintf(out, "readonlyguards: installed git-hook guard at %s (guarding %d row(s))\n", hooksDir, len(hookRows))
	}

	return nil
}

// renderGitHookScript renders a POSIX-sh hook body that rejects the
// invocation unconditionally, printing every row's message. git invokes a
// pre-push or pre-receive hook with ref data on stdin and no distinguishing
// argument, so every guarded row fires together; today there is exactly one
// (forbidden-git-push).
func renderGitHookScript(hookRows []promptassembly.ForbiddenMarkerRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\n")
	fmt.Fprintf(&b, "# readonlyguards: generated read-only git hook (issue #2509) --\n")
	fmt.Fprintf(&b, "# rejects this git operation locally. Rendered from the\n")
	fmt.Fprintf(&b, "# forbiddenMarkers registry; do not edit by hand.\n")
	for _, row := range hookRows {
		fmt.Fprintf(&b, "printf '%%s\\n' %s >&2\n", shQuote(row.RuntimeMessage))
	}
	fmt.Fprintf(&b, "exit 1\n")
	return b.String()
}

// shQuote renders s as a single-quoted POSIX-sh literal, safe to splice into
// generated script source whatever s contains. A literal single quote is
// closed out, escaped, and reopened via the standard '"'"' trick.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
