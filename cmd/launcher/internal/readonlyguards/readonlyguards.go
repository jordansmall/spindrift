// Package readonlyguards renders and installs the runtime read-only guards
// named by lib/prompt-contract.nix's forbiddenMarkers registry (issues
// #2464, #2499, #2509). A guard prints a row's RuntimeMessage verbatim; the
// row's Message field is written for promptassembly.Validate's prompt-time
// check and reads as nonsense at runtime (issue #2509 Finding 2).
package readonlyguards

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/promptassembly"
)

// The Enforce values Install switches on, an allowlist mirroring
// promptassembly.Validate's forbiddenRows loop: a row with any other value
// is skipped rather than silently mis-rendered.
const (
	enforceCommandShim = "command-shim"
	enforceGitHook     = "git-hook"
	enforcePromptOnly  = "prompt-only"
)

// kindGhAPIMutation is the one Kind whose shim rendering differs from a
// plain subcommand match: it scans the invocation's arguments for a
// mutating HTTP method flag instead of rejecting the subcommand outright.
const kindGhAPIMutation = "gh-api-mutation"

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
	// ShimDir is the directory command-shim rows install into: one shim
	// script per argv0 group, plus a sibling ".real-<argv0>" file recording
	// that argv0's resolved binary path. Caller-chosen and never derived
	// from RepoDir, so the shim never shows up as an untracked file in the
	// repo it guards. Required only when rows contains a command-shim row.
	ShimDir string
	// SkipGitHook makes Install treat every git-hook row as absent: no error
	// on an empty RepoDir, no hook installed, HookInstalled stays false. A
	// read-only Box whose hand-off is a real push sets it, since blocking
	// that push would break its only hand-off; command-shim rows still
	// install (#2509). No backend takes this branch today (#2927).
	SkipGitHook bool
	// RealBinary resolves argv0's real, absolute binary path. Install calls
	// it before a caller prepends ShimDir to PATH, so the shim never
	// resolves itself. Defaults to exec.LookPath(argv0) when nil.
	RealBinary func(argv0 string) (string, error)
}

// Result reports what Install installed, for a caller that wants to log it
// or act on it, such as prepending ShimDir to PATH.
type Result struct {
	// Shims lists the argv0 names Install installed a command shim for, in
	// sorted order. A group whose argv0 has no resolvable real binary is
	// skipped and absent here.
	Shims []string
	// HookInstalled reports whether any git-hook row was installed.
	HookInstalled bool
}

// Install renders and installs each row group's guard: one shim script per
// argv0 for command-shim rows, one hook body installed as both pre-push and
// pre-receive for git-hook rows, and nothing for prompt-only rows. It logs
// what it installed to out, which may be nil to discard the log.
func Install(rows []promptassembly.ForbiddenMarkerRow, cfg Config, out io.Writer) (Result, error) {
	if out == nil {
		out = io.Discard
	}
	var result Result

	if hookRows := filterRows(rows, enforceGitHook); len(hookRows) > 0 && !cfg.SkipGitHook {
		if cfg.RepoDir == "" {
			return result, fmt.Errorf("readonlyguards: install git-hook guard: RepoDir is empty")
		}
		if err := installGitHook(hookRows, cfg.RepoDir, cfg.ExtraRepoDirs, out); err != nil {
			return result, err
		}
		result.HookInstalled = true
	}

	shimRows := filterRows(rows, enforceCommandShim)
	if len(shimRows) > 0 {
		if cfg.ShimDir == "" {
			return result, fmt.Errorf("readonlyguards: install command-shim guard: ShimDir is empty")
		}
		realBinary := cfg.RealBinary
		if realBinary == nil {
			realBinary = func(argv0 string) (string, error) {
				return exec.LookPath(argv0)
			}
		}
		argv0s, err := installCommandShims(shimRows, cfg.ShimDir, realBinary, out)
		if err != nil {
			return result, err
		}
		result.Shims = argv0s
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

// argv0Of returns marker's first word, the binary name a command-shim row's
// guard is grouped and installed under ("gh" out of "gh pr create").
func argv0Of(marker string) string {
	fields := strings.Fields(marker)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func groupByArgv0(rows []promptassembly.ForbiddenMarkerRow) map[string][]promptassembly.ForbiddenMarkerRow {
	groups := make(map[string][]promptassembly.ForbiddenMarkerRow)
	for _, row := range rows {
		argv0 := argv0Of(row.Marker)
		groups[argv0] = append(groups[argv0], row)
	}
	return groups
}

// installCommandShims renders one shim per argv0 group under shimDir and
// returns the installed argv0 names in sorted order. A group whose argv0 has
// no resolvable real binary is skipped rather than failing the install: not
// every image bakes every registry-named binary (fj only for a forgejo
// Consumer), and a command absent from PATH can never be invoked.
func installCommandShims(rows []promptassembly.ForbiddenMarkerRow, shimDir string, realBinary func(string) (string, error), out io.Writer) ([]string, error) {
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		return nil, fmt.Errorf("readonlyguards: mkdir shim dir %s: %w", shimDir, err)
	}

	groups := groupByArgv0(rows)
	argv0s := make([]string, 0, len(groups))
	for argv0 := range groups {
		argv0s = append(argv0s, argv0)
	}
	sort.Strings(argv0s)

	installed := make([]string, 0, len(argv0s))
	for _, argv0 := range argv0s {
		real, err := realBinary(argv0)
		if err != nil {
			fmt.Fprintf(out, "readonlyguards: skipping %q command-shim -- not found on PATH: %v\n", argv0, err)
			continue
		}

		realFile := filepath.Join(shimDir, ".real-"+argv0)
		if err := os.WriteFile(realFile, []byte(real), 0o644); err != nil {
			return nil, fmt.Errorf("readonlyguards: write %s: %w", realFile, err)
		}

		script := renderShimScript(argv0, groups[argv0])
		shimPath := filepath.Join(shimDir, argv0)
		if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil {
			return nil, fmt.Errorf("readonlyguards: write shim %s: %w", shimPath, err)
		}

		fmt.Fprintf(out, "readonlyguards: installed %q command-shim at %s (guarding %d subcommand(s))\n", argv0, shimPath, len(groups[argv0]))
		installed = append(installed, argv0)
	}

	return installed, nil
}

// renderShimScript renders one POSIX-sh shim for argv0, execing through to
// the real binary read from the sibling ".real-<argv0>" file for anything
// the rows do not reject.
func renderShimScript(argv0 string, rows []promptassembly.ForbiddenMarkerRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\n")
	fmt.Fprintf(&b, "# readonlyguards: generated read-only %q shim (issue #2509) --\n", argv0)
	fmt.Fprintf(&b, "# rejects guarded subcommands locally, naming the relay that replaces\n")
	fmt.Fprintf(&b, "# each one, instead of letting the real %s reach the network. Rendered\n", argv0)
	fmt.Fprintf(&b, "# from the forbiddenMarkers registry; do not edit by hand.\n")
	fmt.Fprintf(&b, "real_bin=\"$(cat \"$(dirname \"$0\")/.real-%s\")\"\n", argv0)

	for _, row := range rows {
		cond := subcommandCond(row.Marker)
		switch row.Kind {
		case kindGhAPIMutation:
			b.WriteString(renderMutationGuard(cond, row.RuntimeMessage))
		default:
			b.WriteString(renderSubstringGuard(cond, row.RuntimeMessage))
		}
	}

	fmt.Fprintf(&b, "exec \"$real_bin\" \"$@\"\n")
	return b.String()
}

// subcommandCond renders a POSIX-sh test matching marker's words after argv0
// against the shim's positional parameters. It returns "" when marker names
// only argv0, in which case the caller's guard applies unconditionally.
func subcommandCond(marker string) string {
	fields := strings.Fields(marker)
	if len(fields) <= 1 {
		return ""
	}
	parts := make([]string, 0, len(fields)-1)
	for i, word := range fields[1:] {
		parts = append(parts, fmt.Sprintf("[ \"$%d\" = %s ]", i+1, shQuote(word)))
	}
	return strings.Join(parts, " && ")
}

// renderSubstringGuard renders a reject-and-exit block guarded by cond, or
// an unconditional one when cond is "".
func renderSubstringGuard(cond, message string) string {
	var b strings.Builder
	if cond == "" {
		fmt.Fprintf(&b, "printf '%%s\\n' %s >&2\n", shQuote(message))
		fmt.Fprintf(&b, "exit 1\n")
		return b.String()
	}
	fmt.Fprintf(&b, "if %s; then\n", cond)
	fmt.Fprintf(&b, "  printf '%%s\\n' %s >&2\n", shQuote(message))
	fmt.Fprintf(&b, "  exit 1\n")
	fmt.Fprintf(&b, "fi\n")
	return b.String()
}

// renderMutationGuard renders the gh-api-mutation scan: entered only when
// cond matches, it scans "$@" for a mutating -X or --method flag
// (case-insensitive POST, PATCH, PUT, DELETE) and rejects only then, so a
// plain read with no method flag falls through untouched.
func renderMutationGuard(cond, message string) string {
	var b strings.Builder
	open := "if true; then\n"
	if cond != "" {
		open = fmt.Sprintf("if %s; then\n", cond)
	}
	b.WriteString(open)
	b.WriteString("  method=\"GET\"\n")
	b.WriteString("  prev=\"\"\n")
	b.WriteString("  for arg in \"$@\"; do\n")
	b.WriteString("    if [ \"$prev\" = \"-X\" ] || [ \"$prev\" = \"--method\" ]; then\n")
	b.WriteString("      method=\"$arg\"\n")
	b.WriteString("    fi\n")
	b.WriteString("    case \"$arg\" in\n")
	b.WriteString("      --method=*) method=\"${arg#--method=}\" ;;\n")
	b.WriteString("    esac\n")
	b.WriteString("    prev=\"$arg\"\n")
	b.WriteString("  done\n")
	b.WriteString("  case \"$method\" in\n")
	b.WriteString("    [Pp][Oo][Ss][Tt] | [Pp][Aa][Tt][Cc][Hh] | [Pp][Uu][Tt] | [Dd][Ee][Ll][Ee][Tt][Ee])\n")
	fmt.Fprintf(&b, "      printf '%%s\\n' %s >&2\n", shQuote(message))
	b.WriteString("      exit 1\n")
	b.WriteString("      ;;\n")
	b.WriteString("  esac\n")
	b.WriteString("fi\n")
	return b.String()
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
