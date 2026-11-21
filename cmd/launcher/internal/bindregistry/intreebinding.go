package bindregistry

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
)

// intreebindingTempFilePattern is shared by ApplyInTreeBinding's temp-file
// write and sweepOrphanedTempFiles's glob so the two cannot drift.
const intreebindingTempFilePattern = ".intreebinding-*"

// InTreeBindings returns every Strategy of all whose registry pin lives in a
// tracked config file rather than an env var (ADR 0044), in Strategies order:
// npm's .npmrc, yarn berry's .yarnrc.yml (issue #2856), pnpm's
// pnpm-workspace.yaml (issue #2855), and nuget's nuget.config spellings.
// A row naming only DiscoveryConfigPaths is admitted too; BoundInTreeRow picks
// the spelling it binds.
func InTreeBindings(all []ecosystem.Strategy) []ecosystem.Strategy {
	var rows []ecosystem.Strategy
	for _, row := range all {
		// A RepoAwareHomeConfig rewrite (cargo, issue #3201) does not compose
		// with an in-tree one: once one swaps the index host away, the other
		// has nothing to key off. Cargo keeps InTreeConfigPath only for
		// host-side discovery.
		if len(row.ConfigPaths()) > 0 && row.RepoAwareHomeConfig == nil {
			rows = append(rows, row)
		}
	}
	return rows
}

// discoversInTreeConfig reports whether row has no fixed InTreeConfigPath and
// instead names DiscoveryConfigPaths spellings for BoundInTreeRow to choose from.
func discoversInTreeConfig(row ecosystem.Strategy) bool {
	return row.InTreeConfigPath == "" && len(row.DiscoveryConfigPaths) > 0
}

// BoundInTreeRow returns row with InTreeConfigPath naming the file Apply
// rewrites. A row with a fixed InTreeConfigPath is returned as is; one with
// DiscoveryConfigPaths binds the first spelling that is a regular file in
// repoDir under exactly that name, or the first spelling when none is, so a
// not-found message still names a path.
func BoundInTreeRow(repoDir string, row ecosystem.Strategy) ecosystem.Strategy {
	if !discoversInTreeConfig(row) {
		return row
	}
	row.InTreeConfigPath = row.DiscoveryConfigPaths[0]
	for _, p := range row.DiscoveryConfigPaths {
		if regularFileSpelled(repoDir, p) {
			row.InTreeConfigPath = p
			break
		}
	}
	return row
}

// regularFileSpelled reports whether rel's file name exists in its directory
// spelled exactly so. On a case-insensitive filesystem (macOS) os.Stat alone
// matches any case variant, which would bind a spelling git does not track.
func regularFileSpelled(repoDir, rel string) bool {
	full := filepath.Join(repoDir, rel)
	entries, err := os.ReadDir(filepath.Dir(full))
	if err != nil {
		return false
	}
	base := filepath.Base(full)
	if !slices.ContainsFunc(entries, func(e os.DirEntry) bool { return e.Name() == base }) {
		return false
	}
	info, err := os.Stat(full)
	return err == nil && info.Mode().IsRegular()
}

// isTracked reports whether relPath is git-tracked in the repo at repoDir
// (skip-worktree only applies to a tracked path). Only a failure to run git
// returns err, so "git not found" never reads as "confirmed untracked".
func isTracked(repoDir, relPath string) (bool, error) {
	cmd := exec.Command("git", "-C", repoDir, "ls-files", "--error-unmatch", "--", relPath)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false, nil
	}
	return false, err
}

// ApplyOutcome classifies why ApplyInTreeBinding did or did not rewrite
// configPath. A registry proxy manifest (ADR 0045) with no route upstream
// host and an unreachable manifest endpoint (issue #3082) suppress the rewrite
// earlier, in the verb layer, so they have no value here.
type ApplyOutcome int

const (
	// The zero value pairs only with a non-nil error.
	_ ApplyOutcome = iota
	// ApplyMissing: configPath doesn't exist (ENOENT).
	ApplyMissing
	// ApplyNotRegular: configPath is a directory, fifo, or device, symlinked
	// or not (issue #2933's `[ -f ]` parity guard).
	ApplyNotRegular
	// ApplyUntracked: git doesn't track configPath, so `update-index
	// --skip-worktree` was never attempted; git would reject it anyway.
	ApplyUntracked
	// ApplySkipWorktreeSet: the bit was already set, so content was never
	// re-checked. Distinct from ApplyNoopContent because a crash between
	// tagging and rewriting (issue #2932) leaves the bit set over unrewritten
	// content.
	ApplySkipWorktreeSet
	// ApplyNoopContent: the bit was clear and the content already doesn't
	// mention upstreamHost, so there is nothing to rewrite or converge.
	ApplyNoopContent
	// ApplyApplied: the content rewrite plus skip-worktree tag, or (the
	// defensive converge case) just the tag over dirty content that already
	// lacks upstreamHost. The converge case cannot tell that content apart
	// from an unrelated dirty edit; see ApplyInTreeBinding (issue #3024 gap 2).
	ApplyApplied
)

func (a ApplyOutcome) String() string {
	switch a {
	case ApplyMissing:
		return "missing"
	case ApplyNotRegular:
		return "not-regular"
	case ApplyUntracked:
		return "untracked"
	case ApplySkipWorktreeSet:
		return "skip-worktree-set"
	case ApplyNoopContent:
		return "noop-content"
	case ApplyApplied:
		return "applied"
	default:
		return "unknown"
	}
}

// HostRewrite is one route's upstream-host-to-local-Forwarder-URL pair.
// ApplyInTreeBinding applies every entry in a single content pass, so a config
// naming several routes' hosts gets all of them rewritten (issue #3142).
type HostRewrite struct {
	UpstreamHost string
	LocalURL     string
}

// FoldHost is the key in-tree binding compares upstream hosts on. Hostnames
// compare case-insensitively (RFC 4343 folds ASCII only). It keeps any port,
// unlike registryvocab.HostKey: "registry.example.com" and
// "registry.example.com:8443" are distinct rewrites here.
func FoldHost(host string) string {
	return asciiLower(host)
}

// asciiLower lowercases ASCII A-Z only. Because it never changes byte length, a
// caller can match on the lowered copy and splice into the original by index.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// indexHostFold returns the first index >= from where needle occurs in folded
// and ends at a host boundary (EOF or a URL/quote/whitespace/YAML-flow
// terminator), or -1. ':' is deliberately not a terminator: a portless route
// must not match "host:8081", nor "host.mirror.net" (issue #5311). folded and
// needle must already be asciiLower'd.
func indexHostFold(folded, needle string, from int) int {
	for from <= len(folded) {
		i := strings.Index(folded[from:], needle)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(needle)
		if end == len(folded) {
			return i
		}
		switch folded[end] {
		case '/', '?', '#', '"', '\'', '}', ']', ',', ' ', '\t', '\n', '\r', '\f', '\v':
			return i
		}
		from = i + 1
	}
	return -1
}

// replaceAllFold is strings.ReplaceAll with ASCII case-insensitive matching,
// accepting only matches that end at a host boundary (see indexHostFold).
// Bytes outside a match stay untouched. Relies on asciiLower preserving length
// so folded indexes align with s; old must be non-empty.
func replaceAllFold(s, old, repl string) string {
	foldedS, foldedOld := asciiLower(s), asciiLower(old)
	var out strings.Builder
	last := 0
	for {
		i := indexHostFold(foldedS, foldedOld, last)
		if i < 0 {
			break
		}
		out.WriteString(s[last:i])
		out.WriteString(repl)
		last = i + len(old)
	}
	out.WriteString(s[last:])
	return out.String()
}

// ApplyInTreeBinding rewrites row's in-tree config file, when it exists and is
// git-tracked, so references to any rewrites' UpstreamHost point at that
// entry's LocalURL. Such tracked config would otherwise beat an env override,
// or says what one cannot (a per-scope .npmrc registry, .yarnrc.yml's
// npmScopes, pnpm-workspace.yaml's registries; ADR 0044).
func ApplyInTreeBinding(repoDir string, row ecosystem.Strategy, rewrites []HostRewrite) (ApplyOutcome, error) {
	if row.InTreeConfigPath == "" {
		return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with %s row that has no bound config path; resolve it with BoundInTreeRow first", row.Name)
	}
	// Contract guards: the verb layer already drops hostless and shared-host
	// routes (issue #3142). Errors, not outcomes, so a violation never reads
	// as "config not found" (issue #3082).
	if len(rewrites) == 0 {
		return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with no rewrites for %s", row.InTreeConfigPath)
	}
	seenHosts := make(map[string]bool, len(rewrites))
	for _, rw := range rewrites {
		if rw.UpstreamHost == "" {
			return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with an empty UpstreamHost in rewrites for %s", row.InTreeConfigPath)
		}
		if rw.LocalURL == "" {
			return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with an empty LocalURL for UpstreamHost %q in rewrites for %s", rw.UpstreamHost, row.InTreeConfigPath)
		}
		key := FoldHost(rw.UpstreamHost)
		if seenHosts[key] {
			return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with duplicate UpstreamHost %q in rewrites for %s", rw.UpstreamHost, row.InTreeConfigPath)
		}
		seenHosts[key] = true
	}

	configPath := filepath.Join(repoDir, row.InTreeConfigPath)

	// Never run Apply and Revert concurrently for one repo: npm, yarn, and pnpm
	// share the repo-root configDir, so one sweep would delete another's
	// in-flight temp file.
	sweepOrphanedTempFiles(filepath.Dir(configPath))

	// configPath may be a symlink (git mode 120000); following it makes a
	// dangling link ENOENT, and IsRegular no-ops on a directory, fifo, or
	// device, whose read hangs or OOMs (issue #2933). Hard-erroring on a
	// symlink, as #2932 first did, aborted every Strategy in the call.
	info, statErr := os.Stat(configPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return ApplyMissing, nil
		}
		return 0, statErr
	}
	if !info.Mode().IsRegular() {
		return ApplyNotRegular, nil
	}

	tracked, err := isTracked(repoDir, row.InTreeConfigPath)
	if err != nil {
		return 0, err
	}
	if !tracked {
		return ApplyUntracked, nil
	}

	// Check the bit before the content: appliedness must converge the same way
	// RevertInTreeBinding's bit-then-dirty check does, not from content alone.
	skipSet, err := skipWorktreeBitSet(repoDir, row.InTreeConfigPath)
	if err != nil {
		return 0, err
	}
	// A set bit means a prior Apply completed. A crash between tag and rewrite
	// leaves it set over unrewritten content, which every later Apply skips (issue
	// #2932, #3024 gap 1); the caller closes that window: box reverts on any
	// nonzero exit, and each dispatch clones fresh.
	if skipSet {
		return ApplySkipWorktreeSet, nil
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		return 0, err
	}

	// Match only scheme-qualified forms: a bare-host match would tag a
	// `.npmrc` whose only mention is a protocol-relative `//host/:_authToken=`
	// line and log a rewrite with no content change. Every rewrite's host is
	// checked, not just the first (#3142).
	contentStr := string(content)
	foldedContent := asciiLower(contentStr)
	anyHostPresent := false
	for _, rw := range rewrites {
		host := FoldHost(rw.UpstreamHost)
		if indexHostFold(foldedContent, "https://"+host, 0) >= 0 || indexHostFold(foldedContent, "http://"+host, 0) >= 0 {
			anyHostPresent = true
			break
		}
	}
	if !anyHostPresent {
		// Bit clear, no host left: nothing needed rewriting, or something
		// other than Apply dirtied the file; converge by tagging. An unrelated
		// dirty edit reads identically and reports ApplyApplied, accepted
		// because telling them apart needs an API change (issue #3024 gap 2).
		dirty, dirtyErr := workingTreeDirty(repoDir, row.InTreeConfigPath)
		if dirtyErr != nil {
			return 0, dirtyErr
		}
		if !dirty {
			return ApplyNoopContent, nil
		}
		if err := exec.Command("git", "-C", repoDir, "update-index", "--skip-worktree", "--", row.InTreeConfigPath).Run(); err != nil {
			return 0, err
		}
		return ApplyApplied, nil
	}

	// Longest host first, on a copy to keep the caller's order: a safeguard
	// beside the host boundary for prefix-overlapping hosts, and it keeps a
	// shorter host out of an already-rewritten LocalURL.
	ordered := append([]HostRewrite(nil), rewrites...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return len(ordered[i].UpstreamHost) > len(ordered[j].UpstreamHost)
	})

	// Either scheme may appear, including "sparse+https://HOST/...". Matching
	// is literal apart from ASCII case folding (issue #3706).
	rewritten := contentStr
	for _, rw := range ordered {
		rewritten = replaceAllFold(rewritten, "https://"+rw.UpstreamHost, rw.LocalURL)
		rewritten = replaceAllFold(rewritten, "http://"+rw.UpstreamHost, rw.LocalURL)
	}

	// The InTreeRewrite hook runs only here, not on the no-host-left path, so
	// a convergent second Apply does not re-run it over rewritten content; and
	// before the tag, so a hook error leaves the bit clear.
	if row.InTreeRewrite != nil {
		localURLs := make([]string, len(rewrites))
		for i, rw := range rewrites {
			localURLs[i] = rw.LocalURL
		}
		var err error
		if rewritten, err = row.InTreeRewrite(rewritten, localURLs); err != nil {
			return 0, err
		}
	}

	// Tag before writing: a path git refuses to tag (exit 128, unmerged) then
	// fails before the proxy URL lands in a file Revert cannot clean up
	// (issue #2932). The tag also hides the rewrite from `git status`.
	if err := exec.Command("git", "-C", repoDir, "update-index", "--skip-worktree", "--", row.InTreeConfigPath).Run(); err != nil {
		return 0, err
	}

	// Temp file plus rename: os.WriteFile would write through a symlink,
	// possibly outside repoDir; os.Rename replaces the entry itself. Same
	// directory keeps the rename atomic.
	tmp, err := os.CreateTemp(filepath.Dir(configPath), intreebindingTempFilePattern)
	if err != nil {
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", row.InTreeConfigPath).Run()
		return 0, err
	}
	tmpPath := tmp.Name()
	writeErr := func() error {
		defer tmp.Close()
		if _, err := tmp.Write([]byte(rewritten)); err != nil {
			return err
		}
		return tmp.Chmod(info.Mode().Perm())
	}()
	if writeErr != nil {
		_ = os.Remove(tmpPath)
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", row.InTreeConfigPath).Run()
		return 0, writeErr
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		// Best-effort: undo the bit so a write failure cannot leave "bit set,
		// content never rewritten" for a later Apply to read as already-applied.
		_ = os.Remove(tmpPath)
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", row.InTreeConfigPath).Run()
		return 0, err
	}

	return ApplyApplied, nil
}

// skipWorktreeBitSet reports whether relPath's skip-worktree bit is set, via
// `git ls-files -v`'s "S " prefix.
func skipWorktreeBitSet(repoDir, relPath string) (bool, error) {
	out, err := exec.Command("git", "-C", repoDir, "ls-files", "-v", "--", relPath).Output()
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(string(out), "S "), nil
}

// workingTreeDirty reports whether relPath's working-tree content differs from
// the index (not HEAD), via `git diff --quiet`: exit 1 is dirty, any other
// nonzero exit a real error.
func workingTreeDirty(repoDir, relPath string) (bool, error) {
	err := exec.Command("git", "-C", repoDir, "diff", "--quiet", "--", relPath).Run()
	if err == nil {
		return false, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

// sweepOrphanedTempFiles removes leftover intreebindingTempFilePattern files
// from configDir (issue #3027): a hard kill between os.CreateTemp and
// os.Rename runs no Go cleanup, and the untracked orphan would be folded into
// the next `git add -A`.
func sweepOrphanedTempFiles(configDir string) {
	// Unconditional, not crash-detecting: retries can strand several orphans,
	// and a directory with none stays a silent no-op. Glob only fails on
	// metacharacters, which the fixed /work paths never carry.
	matches, _ := filepath.Glob(filepath.Join(configDir, intreebindingTempFilePattern))
	for _, m := range matches {
		// .intreebinding-* is a reserved name: a tracked file so named would
		// show as a deletion (a per-match `git ls-files` guard costs a
		// subprocess each). A failed remove must not fail the call.
		_ = os.Remove(m)
	}
}

// RevertInTreeBinding undoes ApplyInTreeBinding's rewrite with no sentinel of
// its own: appliedness comes from the skip-worktree bit and working-tree
// content, never from cross-call state (issue #2932 AC1/AC2). Every no-op case
// (missing, untracked, already reverted, never applied) reports reverted=false
// with a nil error, indistinguishably.
func RevertInTreeBinding(repoDir string, row ecosystem.Strategy) (reverted bool, err error) {
	configPath := filepath.Join(repoDir, row.InTreeConfigPath)

	// Ahead of the early returns, so a config gone missing or untracked after
	// a crash still sheds its orphan.
	sweepOrphanedTempFiles(filepath.Dir(configPath))

	if _, statErr := os.Stat(configPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		return false, statErr
	}

	tracked, err := isTracked(repoDir, row.InTreeConfigPath)
	if err != nil {
		return false, err
	}
	if !tracked {
		return false, nil
	}

	skipSet, err := skipWorktreeBitSet(repoDir, row.InTreeConfigPath)
	if err != nil {
		return false, err
	}
	if skipSet {
		if err := exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", row.InTreeConfigPath).Run(); err != nil {
			return false, err
		}
		// Unconditional: cheap, and converges without a second dirty check.
		if err := exec.Command("git", "-C", repoDir, "checkout", "--", row.InTreeConfigPath).Run(); err != nil {
			return false, err
		}
		return true, nil
	}

	// Bit clear but content may be dirty from something other than Apply;
	// treated as applied, so an unrelated dirty edit is discarded too (issue
	// #3024 gap 2).
	dirty, err := workingTreeDirty(repoDir, row.InTreeConfigPath)
	if err != nil {
		return false, err
	}
	if !dirty {
		return false, nil
	}

	if err := exec.Command("git", "-C", repoDir, "checkout", "--", row.InTreeConfigPath).Run(); err != nil {
		return false, err
	}
	return true, nil
}

// RevertInTreeBindings reverts every Strategy in rows, continuing past a
// per-Strategy error, and reports whether any failed. Each outcome is narrated
// to w; who prefixes a failure line. A DiscoveryConfigPaths row is reverted at each spelling whose
// skip-worktree bit is set, not a re-resolved one: the bit marks the bound
// spelling even if the agent since created an earlier one. An unmarked
// spelling is left alone, its dirty content being the agent's.
func RevertInTreeBindings(workDir string, rows []ecosystem.Strategy, who string, w io.Writer) (failed bool) {
	for _, row := range rows {
		targets, probed, err := revertTargets(workDir, row)
		if err != nil {
			fmt.Fprintln(w, who+": revert in-tree "+probed+":", err)
			failed = true
			continue
		}
		for _, target := range targets {
			reverted, err := RevertInTreeBinding(workDir, target)
			if err != nil {
				fmt.Fprintln(w, who+": revert in-tree "+target.InTreeConfigPath+":", err)
				failed = true
				continue
			}
			if reverted {
				fmt.Fprintln(w, "==> in-tree "+target.Name+" config "+target.InTreeConfigPath+" restored and un-hidden from git")
			}
		}
	}
	return failed
}

// revertTargets returns the rows to revert for row: row itself when it names a
// fixed InTreeConfigPath, else one row per DiscoveryConfigPaths spelling whose
// skip-worktree bit is set. On a probe error it also returns the spelling probed.
func revertTargets(workDir string, row ecosystem.Strategy) (targets []ecosystem.Strategy, probed string, err error) {
	if !discoversInTreeConfig(row) {
		return []ecosystem.Strategy{row}, "", nil
	}
	for _, p := range row.DiscoveryConfigPaths {
		set, err := skipWorktreeBitSet(workDir, p)
		if err != nil {
			return nil, p, err
		}
		if set {
			row.InTreeConfigPath = p
			targets = append(targets, row)
		}
	}
	return targets, "", nil
}
