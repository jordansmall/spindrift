// Package readonlyguards renders and installs the runtime read-only guards
// named by lib/prompt-contract.nix's forbiddenMarkers registry, decoded via
// promptassembly.ForbiddenMarkerRow.
//
// Every rejection message a guard prints comes verbatim from a row's
// RuntimeMessage field, never a string baked into this package's Go source --
// the registry is the one place a rejection message's wording lives.
// RuntimeMessage is deliberately distinct from the row's Message field, which is
// written for promptassembly.Validate's prompt-time check and would be
// nonsensical printed by a runtime shim.
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

// The Enforce values Install switches on -- an allowlist, so a row this package
// doesn't recognize is skipped rather than silently mis-rendered.
const (
	enforceCommandShim = "command-shim"
	enforceGitHook     = "git-hook"
	enforcePromptOnly  = "prompt-only"
)

// kindGhAPIMutation is the one Kind whose shim rendering differs from a plain
// substring-subcommand match: rather than rejecting the subcommand outright, it
// scans the invocation's arguments for a mutating HTTP method flag.
const kindGhAPIMutation = "gh-api-mutation"

// hookNames are the git hook filenames Install writes identical rendered content
// to: pre-push covers the client-side push path a working checkout takes,
// pre-receive covers a bare/decoy repo used as a push target. Which one actually
// fires depends on how the caller wires RepoDir, so both are installed
// unconditionally.
var hookNames = []string{"pre-push", "pre-receive"}

// Config is everything Install needs to render and install every runtime
// guard a set of forbiddenMarkers rows describes.
type Config struct {
	// RepoDir is the repository whose hooks directory (see gitHooksDir) receives
	// the rendered git-hook content. Only required when rows contains at least
	// one Enforce == "git-hook" row.
	RepoDir string
	// ExtraRepoDirs names additional repositories that receive the identical
	// rendered git-hook content -- additive and optional, never a substitute for
	// RepoDir. Production installs into both a throwaway decoy repo (RepoDir,
	// where origin's pushurl is repointed) and $WORK_DIR itself: only a plain
	// origin push resolves through the repointed pushurl, so a push to an
	// explicit URL or non-origin remote goes around the decoy entirely and needs
	// $WORK_DIR's own hook to catch it.
	ExtraRepoDirs []string
	// ShimDir is the directory command-shim rows install into: one shim script
	// per argv0 group, plus a sibling ".real-<argv0>" file recording that argv0's
	// real, resolved binary path. Deliberately caller-chosen, never derived from
	// RepoDir, so the shim script never shows up as an untracked file under `git
	// status`/`git add -A` in the repo it is guarding. Only required when rows
	// contains at least one Enforce == "command-shim" row.
	ShimDir string
	// SkipGitHook makes Install treat every git-hook row as absent: no error even
	// when RepoDir is empty, no hook installed, Result.HookInstalled stays false.
	// Command-shim rows are still processed. It is set for a read-only Box whose
	// descriptor leaves both OutboxRelayCapable and HostMediatedRemote false,
	// i.e. whose hand-off is a real `git push` that a local hook would break;
	// the command-shim guard carries no such risk and still installs. No
	// currently-registered backend takes this branch — it exists for a future
	// one lacking outbox-relay capability.
	SkipGitHook bool
	// RealBinary resolves argv0's real, absolute binary path. Called once per
	// command-shim argv0 group, and necessarily BEFORE a caller prepends ShimDir
	// to PATH. Nil defaults to exec.LookPath(argv0).
	RealBinary func(argv0 string) (string, error)
}

// Result reports what Install actually installed, for a caller that wants to log
// or act on it (e.g. prepending ShimDir to PATH).
type Result struct {
	// Shims lists the argv0 names a shim was installed for, sorted. A group
	// whose argv0 has no resolvable real binary is skipped, not listed here.
	Shims []string
	// HookInstalled reports whether any git-hook row was rendered and installed.
	HookInstalled bool
}

// Install groups rows by Enforce and renders/installs each group's runtime
// guard:
//
//   - "command-shim" rows are grouped by their Marker's first word (argv0) and
//     rendered into one shim script per group.
//   - "git-hook" rows are rendered into one hook body, installed as both
//     pre-push and pre-receive.
//   - "prompt-only" rows produce no runtime artifact.
//
// A short log of what was installed goes to out, which may be nil to discard it.
// Install never panics; every failure is returned as a wrapped error.
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

// filterRows returns the subset of rows whose Enforce equals enforce, in their
// original relative order.
func filterRows(rows []promptassembly.ForbiddenMarkerRow, enforce string) []promptassembly.ForbiddenMarkerRow {
	var out []promptassembly.ForbiddenMarkerRow
	for _, row := range rows {
		if row.Enforce == enforce {
			out = append(out, row)
		}
	}
	return out
}

// argv0Of returns marker's first whitespace-delimited word -- the binary name a
// command-shim row's guard is grouped and installed under.
func argv0Of(marker string) string {
	fields := strings.Fields(marker)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// groupByArgv0 buckets rows by argv0Of(row.Marker), preserving each bucket's
// original relative order.
func groupByArgv0(rows []promptassembly.ForbiddenMarkerRow) map[string][]promptassembly.ForbiddenMarkerRow {
	groups := make(map[string][]promptassembly.ForbiddenMarkerRow)
	for _, row := range rows {
		argv0 := argv0Of(row.Marker)
		groups[argv0] = append(groups[argv0], row)
	}
	return groups
}

// installCommandShims groups rows by argv0, renders one shim script per group,
// and installs each under shimDir alongside its ".real-<argv0>" file. Returns
// the installed argv0 names in sorted order.
//
// A group whose argv0 has no resolvable real binary is skipped rather than
// treated as fatal: not every Box's image bakes every registry-named binary, and
// a command absent from PATH can never be invoked, so there is nothing to guard.
// Hard-failing would take down every other row's guard along with it.
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

// renderShimScript renders one POSIX-sh shim for argv0 guarding every row in
// rows, execing through to the real binary (read from the sibling
// ".real-<argv0>" file) for anything not rejected.
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
// against the shim's positional parameters -- "gh pr create" becomes a check on
// $1="pr" && $2="create". Returns "" when marker names only argv0, in which case
// the caller's guard applies unconditionally.
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

// renderSubstringGuard renders an unconditional reject-and-exit block
// guarded by cond (or always, when cond is "").
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

// renderMutationGuard renders the gh-api-mutation scan: entered only when cond
// matches (e.g. `$1 = "api"`), it scans "$@" for a mutating -X/--method flag
// (case-insensitive POST/PATCH/PUT/DELETE) and rejects with message only then --
// everything else, including a plain read with no method flag, falls through
// untouched.
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

// gitHooksDir returns the hooks directory git itself would consult for repoDir:
// repoDir/.git/hooks for a normal working copy, repoDir/hooks otherwise, since a
// bare repository has no .git subdirectory and repoDir itself is the git
// directory. Getting this wrong writes a hook file git never reads, silently
// leaving the guard absent while Result.HookInstalled still reports true.
func gitHooksDir(repoDir string) string {
	if info, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil && info.IsDir() {
		return filepath.Join(repoDir, ".git", "hooks")
	}
	return filepath.Join(repoDir, "hooks")
}

// installGitHook renders one hook body from hookRows and installs it identically
// as every name in hookNames, under repoDir's hooks directory and every
// extraRepoDirs entry's -- see Config.ExtraRepoDirs for why both matter.
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

// renderGitHookScript renders a POSIX-sh git hook body that unconditionally
// rejects the invocation, printing every row's Message. git invokes a
// pre-push/pre-receive hook with ref data on stdin, not a distinguishing
// argument, so there is no way to select between rows the way a command shim's
// positional parameters do: every applicable row fires together.
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

// shQuote renders s as a single-quoted POSIX-sh string literal, safe to splice
// into generated script source regardless of s's content. A literal single quote
// is closed out, escaped, and reopened via the standard '"'"' trick.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
