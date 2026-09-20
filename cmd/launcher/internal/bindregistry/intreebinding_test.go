package bindregistry

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// Order is asserted, not just membership: the verb's apply/revert loop
// iterates InTreeBindings() in whatever order it returns, so npm/yarn/pnpm
// must match ecosystem.Table's own order (issue #3180, rows from issue
// #2933). cargo is absent by design (issue #3201), pinned by
// TestInTreeBindings_ExcludesCargo below.
func TestInTreeBindingTableHasExpectedRows(t *testing.T) {
	want := []struct {
		name       string
		configPath string
	}{
		{name: "npm", configPath: ".npmrc"},
		{name: "yarn", configPath: ".yarnrc.yml"},
		{name: "pnpm", configPath: "pnpm-workspace.yaml"},
	}

	got := InTreeBindings()
	if len(got) != len(want) {
		t.Fatalf("InTreeBindings() has %d rows, want exactly %d: %+v", len(got), len(want), got)
	}
	for i, tc := range want {
		if got[i].Name != tc.name || got[i].InTreeConfigPath != tc.configPath {
			t.Errorf("row %d = {Name: %q, InTreeConfigPath: %q}, want {Name: %q, InTreeConfigPath: %q}",
				i, got[i].Name, got[i].InTreeConfigPath, tc.name, tc.configPath)
		}
	}
}

// Cargo's ecosystem.Table row keeps a non-empty InTreeConfigPath for
// host-side discovery but carries a non-nil RepoAwareHomeConfig, so
// InTreeBindings' filter must exclude it (issue #3201). A regression would
// dual-write the in-tree rewrite and cargo's source-replacement home config,
// which its 1:1 URL to source-name rule cannot tolerate.
func TestInTreeBindings_ExcludesCargo(t *testing.T) {
	for _, row := range InTreeBindings() {
		if row.Name == "cargo" {
			t.Fatalf("InTreeBindings() = %+v, want it to never include the cargo row", InTreeBindings())
		}
	}
}

// gitQuietArgs is the leading `git` argument list every helper here builds on.
// Git forks a detached `git gc --auto` / `git maintenance run --auto` once a
// commit crosses the loose-object threshold, and it can still be repacking
// when t.TempDir()'s RemoveAll runs, failing the test with "directory not
// empty" on .git — the same race commit 31f9696d closed in driver-exec's own
// git helper.
func gitQuietArgs(dir string) []string {
	return []string{"-C", dir, "-c", "gc.auto=0", "-c", "maintenance.auto=false"}
}

// These tests use a single local repo dir: isTracked is purely local, so no
// bare repo, clone, or push is needed.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append(gitQuietArgs(dir), args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")

	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-m", "initial")

	untracked := filepath.Join(dir, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestIsTrackedReportsTrueForTrackedFile(t *testing.T) {
	dir := newTestRepo(t)

	tracked, err := isTracked(dir, "tracked.txt")
	if err != nil {
		t.Fatalf("isTracked: %v", err)
	}
	if !tracked {
		t.Error("isTracked(tracked.txt) = false, want true")
	}
}

func TestIsTrackedReportsFalseForUntrackedFile(t *testing.T) {
	dir := newTestRepo(t)

	tracked, err := isTracked(dir, "untracked.txt")
	if err != nil {
		t.Fatalf("isTracked: %v", err)
	}
	if tracked {
		t.Error("isTracked(untracked.txt) = true, want false")
	}
}

func TestIsTrackedReportsFalseForMissingFile(t *testing.T) {
	dir := newTestRepo(t)

	tracked, err := isTracked(dir, "does-not-exist.txt")
	if err != nil {
		t.Fatalf("isTracked: %v", err)
	}
	if tracked {
		t.Error("isTracked(does-not-exist.txt) = true, want false")
	}
}

func writeConfig(t *testing.T, dir, relPath, content string, tracked bool) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if tracked {
		runGit(t, dir, "add", relPath)
		runGit(t, dir, "commit", "-m", "add "+relPath)
	}
}

// skipWorktreeSet reports whether relPath's skip-worktree bit is set, which
// `git ls-files -v` marks with an "S " prefix.
func skipWorktreeSet(t *testing.T, dir, relPath string) bool {
	t.Helper()
	cmd := exec.Command("git", append(gitQuietArgs(dir), "ls-files", "-v", "--", relPath)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files -v: %v: %s", err, out)
	}
	return strings.HasPrefix(string(out), "S ")
}

var cargoBinding = ecosystem.Row{Name: "cargo", InTreeConfigPath: ".cargo/config.toml"}
var npmBinding = ecosystem.Row{Name: "npm", InTreeConfigPath: ".npmrc"}
var yarnBinding = ecosystem.Row{Name: "yarn", InTreeConfigPath: ".yarnrc.yml"}
var pnpmBinding = ecosystem.Row{Name: "pnpm", InTreeConfigPath: "pnpm-workspace.yaml"}

func TestApplyInTreeBindingRewritesTrackedFileBothSchemes(t *testing.T) {
	dir := newTestRepo(t)
	content := "[source.crates-io]\nreplace-with = \"proxy\"\n\n[source.proxy]\nregistry = \"sparse+https://upstream.example/index/\"\n\n[registries.proxy]\nindex = \"http://upstream.example/other/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Errorf("reason = %v, want %v", reason, ApplyApplied)
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewritten content still mentions upstream.example: %s", got)
	}
	if !strings.Contains(string(got), "sparse+http://127.0.0.1:27182/index/") {
		t.Errorf("rewritten content missing expected sparse https rewrite: %s", got)
	}
	if !strings.Contains(string(got), "http://127.0.0.1:27182/other/") {
		t.Errorf("rewritten content missing expected http rewrite: %s", got)
	}
	if !skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit not set after apply")
	}
}

// These hosts contain characters that were sed metacharacters in the bash
// phase this engine replaced, so that phase needed per-host escaping.
// strings.ReplaceAll needs none.
func TestApplyInTreeBindingEscaping(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		content string
		want    string
	}{
		{
			name:    "sed_special_character_host",
			host:    "registry.corp#1.example",
			content: "registry = \"https://registry.corp#1.example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		{
			name:    "asterisk_host",
			host:    "registry*.example",
			content: "registry = \"https://registry*.example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		{
			name:    "bracket_character_class_host",
			host:    "registry[1].example",
			content: "registry = \"https://registry[1].example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		{
			name:    "caret_dollar_anchor_host",
			host:    "registry^end$.example",
			content: "registry = \"https://registry^end$.example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		{
			name:    "backslash_host",
			host:    "registry\\.example",
			content: "registry = \"https://registry\\.example/index/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\n",
		},
		// The host's dots must stay literal, not a regexp "any character".
		// The decoy line is the same length with the dot positions replaced
		// by other letters, so a wildcard rewrite would match it too. Only
		// the exact reg.stry.example line may be rewritten.
		{
			name:    "dot_does_not_match_arbitrary_character",
			host:    "reg.stry.example",
			content: "registry = \"https://reg.stry.example/index/\"\nother = \"https://regXstryYexample/decoy/\"\n",
			want:    "registry = \"http://127.0.0.1:27182/index/\"\nother = \"https://regXstryYexample/decoy/\"\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newTestRepo(t)
			writeConfig(t, dir, cargoBinding.InTreeConfigPath, tc.content, true)

			reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: tc.host, LocalURL: "http://127.0.0.1:27182"}})
			if err != nil {
				t.Fatalf("ApplyInTreeBinding: %v", err)
			}
			if reason != ApplyApplied {
				t.Errorf("reason = %v, want %v", reason, ApplyApplied)
			}

			got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("rewritten content = %q, want %q", got, tc.want)
			}
		})
	}
}

// Each case proves the generic engine ApplyInTreeBinding already uses for
// cargo's TOML also rewrites npm's, yarn berry's, and pnpm's own syntax on
// both schemes, with no ecosystem-specific code (issue #2933; these were
// entrypoint.sh's phase_npm_intree_binding_apply and its yarn berry and
// pnpm workspace siblings).
func TestApplyInTreeBindingRewritesNonCargoEcosystemShapes(t *testing.T) {
	cases := []struct {
		name    string
		binding ecosystem.Row
		content string
		want    string
	}{
		{
			// An INI-like scoped entry (`@scope:registry=`) and an
			// unscoped entry (`registry=`), one on each scheme.
			name:    "npm",
			binding: npmBinding,
			content: "@mycorp:registry=https://upstream.example/\nregistry=http://upstream.example/\n",
			want:    "@mycorp:registry=http://127.0.0.1:27182/\nregistry=http://127.0.0.1:27182/\n",
		},
		{
			// The top-level npmRegistryServer key and a per-scope
			// npmScopes.<scope>.npmRegistryServer entry (issue #2856), one
			// on each scheme. This is plain text substitution against real
			// YAML syntax, not a YAML-aware parse.
			name:    "yarn",
			binding: yarnBinding,
			content: "npmRegistryServer: \"https://upstream.example\"\nnpmScopes:\n  mycorp:\n    npmRegistryServer: \"http://upstream.example\"\n",
			want:    "npmRegistryServer: \"http://127.0.0.1:27182\"\nnpmScopes:\n  mycorp:\n    npmRegistryServer: \"http://127.0.0.1:27182\"\n",
		},
		{
			// The registries map keyed by URL, one entry per scheme, plus a
			// URL embedding the scheme behind a prefix. That last entry
			// exercises the same substring property cargo's
			// "sparse+https://" URL proves, using a synthetic
			// "mirror+https://" since pnpm has no sparse-index concept.
			name:    "pnpm",
			binding: pnpmBinding,
			content: "packages:\n  - \"packages/*\"\nregistries:\n  \"https://upstream.example/\": {scopes: [\"@mycorp\"]}\n  \"http://upstream.example/other/\": {scopes: [\"@other\"]}\n  mirrorRegistry: \"mirror+https://upstream.example/mirror/\"\n",
			want:    "packages:\n  - \"packages/*\"\nregistries:\n  \"http://127.0.0.1:27182/\": {scopes: [\"@mycorp\"]}\n  \"http://127.0.0.1:27182/other/\": {scopes: [\"@other\"]}\n  mirrorRegistry: \"mirror+http://127.0.0.1:27182/mirror/\"\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newTestRepo(t)
			writeConfig(t, dir, tc.binding.InTreeConfigPath, tc.content, true)

			reason, err := ApplyInTreeBinding(dir, tc.binding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
			if err != nil {
				t.Fatalf("ApplyInTreeBinding: %v", err)
			}
			if reason != ApplyApplied {
				t.Errorf("reason = %v, want %v", reason, ApplyApplied)
			}

			got, err := os.ReadFile(filepath.Join(dir, tc.binding.InTreeConfigPath))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("rewritten content = %q, want %q", got, tc.want)
			}
		})
	}
}

// Issue #3142's multi-route loop: a config naming both routes' upstream
// hosts gets both rewritten, each to its own local URL, in one Apply call,
// not just the first rewrite in the slice.
func TestApplyInTreeBindingRewritesEachRouteToItsOwnLocalURL(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry-a = \"https://host-a.example/index/\"\nregistry-b = \"https://host-b.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	rewrites := []HostRewrite{
		{UpstreamHost: "host-a.example", LocalURL: "http://127.0.0.1:27182/r0"},
		{UpstreamHost: "host-b.example", LocalURL: "http://127.0.0.1:27182/r1"},
	}
	reason, err := ApplyInTreeBinding(dir, cargoBinding, rewrites)
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Errorf("reason = %v, want %v", reason, ApplyApplied)
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	want := "registry-a = \"http://127.0.0.1:27182/r0/index/\"\nregistry-b = \"http://127.0.0.1:27182/r1/index/\"\n"
	if string(got) != want {
		t.Errorf("rewritten content = %q, want %q", got, want)
	}
}

// The other half of the multi-route loop: content mentions only host-b while
// rewrites lists both, and only host-b's occurrence is rewritten.
func TestApplyInTreeBindingRewritesOnlyHostBWhenBothInRewriteList(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry-b = \"https://host-b.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	rewrites := []HostRewrite{
		{UpstreamHost: "host-a.example", LocalURL: "http://127.0.0.1:27182/r0"},
		{UpstreamHost: "host-b.example", LocalURL: "http://127.0.0.1:27182/r1"},
	}
	reason, err := ApplyInTreeBinding(dir, cargoBinding, rewrites)
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Errorf("reason = %v, want %v", reason, ApplyApplied)
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	want := "registry-b = \"http://127.0.0.1:27182/r1/index/\"\n"
	if string(got) != want {
		t.Errorf("rewritten content = %q, want %q", got, want)
	}
}

// The multi-route content-match check: when content mentions neither
// rewrite's host in either scheme, the call must no-op exactly as the
// single-rewrite case does.
func TestApplyInTreeBindingNoopWhenNeitherRouteHostPresent(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://some-other-host.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	rewrites := []HostRewrite{
		{UpstreamHost: "host-a.example", LocalURL: "http://127.0.0.1:27182/r0"},
		{UpstreamHost: "host-b.example", LocalURL: "http://127.0.0.1:27182/r1"},
	}
	reason, err := ApplyInTreeBinding(dir, cargoBinding, rewrites)
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyNoopContent {
		t.Errorf("reason = %v, want %v", reason, ApplyNoopContent)
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content changed unexpectedly: got %q, want %q", got, content)
	}
}

// Issue #3142's overlap defect: "registry.example.com" and
// "registry.example.com:8443" share a prefix, so a rewrite loop running in
// caller order, shorter host first, would replace the bare host inside the
// longer host's own URL and corrupt it. The rewrites below are listed
// shorter-host-first deliberately, to prove Apply reorders by host length.
func TestApplyInTreeBindingOverlappingHostsRewriteLongestFirst(t *testing.T) {
	dir := newTestRepo(t)
	content := "short = \"https://registry.example.com/index/\"\nlong = \"https://registry.example.com:8443/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	rewrites := []HostRewrite{
		{UpstreamHost: "registry.example.com", LocalURL: "http://127.0.0.1:27182/r0"},
		{UpstreamHost: "registry.example.com:8443", LocalURL: "http://127.0.0.1:27182/r1"},
	}
	reason, err := ApplyInTreeBinding(dir, cargoBinding, rewrites)
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Errorf("reason = %v, want %v", reason, ApplyApplied)
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	want := "short = \"http://127.0.0.1:27182/r0/index/\"\nlong = \"http://127.0.0.1:27182/r1/index/\"\n"
	if string(got) != want {
		t.Errorf("rewritten content = %q, want %q", got, want)
	}
}

// The verb layer already checks the manifest for at least one route upstream
// host before calling in, so an empty rewrites slice here is a contract
// violation, not one of ApplyOutcome's operator-facing no-op cases (issue
// #3142).
func TestApplyInTreeBindingErrorsOnEmptyRewrites(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	reason, err := ApplyInTreeBinding(dir, cargoBinding, nil)
	if err == nil {
		t.Fatal("ApplyInTreeBinding: err = nil, want non-nil (empty rewrites is an internal-consistency violation)")
	}
	if reason == ApplyMissing {
		t.Errorf("reason = %v, want anything but ApplyMissing (a nil-error-shaped outcome for a real error)", reason)
	}
}

// Neither ApplyInTreeBinding nor RevertInTreeBinding may run `git
// update-index --skip-worktree` (or `checkout`) against a path git does not
// track: git rejects the former, and the latter's meaning is undefined for
// an untracked path.
func TestInTreeBindingUntrackedFileTolerance(t *testing.T) {
	content := "registry = \"https://upstream.example/index/\"\n"

	cases := []struct {
		name string
		run  func(t *testing.T, dir string) (actedOn bool)
	}{
		{
			name: "apply",
			run: func(t *testing.T, dir string) bool {
				reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
				if err != nil {
					t.Fatalf("ApplyInTreeBinding: %v", err)
				}
				if reason != ApplyUntracked {
					t.Errorf("reason = %v, want %v", reason, ApplyUntracked)
				}
				return reason == ApplyApplied
			},
		},
		{
			name: "revert",
			run: func(t *testing.T, dir string) bool {
				reverted, err := RevertInTreeBinding(dir, cargoBinding)
				if err != nil {
					t.Fatalf("RevertInTreeBinding: %v", err)
				}
				return reverted
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newTestRepo(t)
			writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, false)

			if acted := tc.run(t, dir); acted {
				t.Errorf("%s: acted on untracked file, want no-op", tc.name)
			}

			got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != content {
				t.Errorf("%s: untracked file was modified: got %q, want %q", tc.name, got, content)
			}
		})
	}
}

func TestApplyInTreeBindingNoopOnMissingFile(t *testing.T) {
	dir := newTestRepo(t)

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyMissing {
		t.Errorf("reason = %v, want %v", reason, ApplyMissing)
	}
}

func TestApplyInTreeBindingNoopWhenHostAbsent(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://some-other-host.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyNoopContent {
		t.Errorf("reason = %v, want %v", reason, ApplyNoopContent)
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content changed unexpectedly: got %q, want %q", got, content)
	}
	if skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit set, want unset")
	}
}

func TestApplyInTreeBindingIdempotentOnSecondCall(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	reason1, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("first ApplyInTreeBinding: %v", err)
	}
	if reason1 != ApplyApplied {
		t.Fatalf("first call: reason = %v, want %v", reason1, ApplyApplied)
	}

	afterFirst, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}

	reason2, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("second ApplyInTreeBinding: %v", err)
	}
	// The second call finds the skip-worktree bit already set from the first,
	// so it reports ApplySkipWorktreeSet. Issue #2932 requires that outcome
	// distinct from ApplyNoopContent.
	if reason2 != ApplySkipWorktreeSet {
		t.Errorf("second call: reason = %v, want %v (idempotent no-op)", reason2, ApplySkipWorktreeSet)
	}

	afterSecond, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("content changed on second call: before %q, after %q", afterFirst, afterSecond)
	}
	if !skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit not set after second (no-op) call, want still set")
	}
}

func TestApplyInTreeBindingConvergesAfterCrashBetweenPhases(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, original, true)

	// Simulate Apply's rewrite landing but the process dying before the
	// skip-worktree bit got set: content rewritten and dirty against the
	// index, bit clear.
	rewritten := "registry = \"http://127.0.0.1:27182/index/\"\n"
	if err := os.WriteFile(filepath.Join(dir, cargoBinding.InTreeConfigPath), []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Errorf("reason = %v, want %v", reason, ApplyApplied)
	}

	if !skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit not set after converge, want set")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != rewritten {
		t.Errorf("content = %q, want unchanged %q", got, rewritten)
	}
}

// Issue #3024's gap 1, pinned as accepted risk rather than desired behavior:
// once anything sets the skip-worktree bit without rewriting content, Apply's
// bit-first check takes that as proof a prior Apply completed and never looks
// at content again, permanently. The caller-side mitigation lives in
// agent/entrypoint.sh's intree_binding_apply, not here.
func TestApplyInTreeBindingStaysNonConvergentWhenBitSetBeforeWrite(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, original, true)

	// Set the bit directly, bypassing ApplyInTreeBinding, so content stays
	// exactly as committed. The crash-between-phases test above does the
	// opposite: content rewritten, bit clear.
	runGit(t, dir, "update-index", "--skip-worktree", "--", cargoBinding.InTreeConfigPath)

	rewrites := []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}}

	reason1, err := ApplyInTreeBinding(dir, cargoBinding, rewrites)
	if err != nil {
		t.Fatalf("first ApplyInTreeBinding: %v", err)
	}
	if reason1 != ApplySkipWorktreeSet {
		t.Fatalf("first call: reason = %v, want %v", reason1, ApplySkipWorktreeSet)
	}

	got1, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got1) != original {
		t.Errorf("first call: content = %q, want untouched %q", got1, original)
	}

	// The non-convergence is permanent, not a one-shot miss on the first
	// observation of the bit.
	reason2, err := ApplyInTreeBinding(dir, cargoBinding, rewrites)
	if err != nil {
		t.Fatalf("second ApplyInTreeBinding: %v", err)
	}
	if reason2 != ApplySkipWorktreeSet {
		t.Errorf("second call: reason = %v, want %v", reason2, ApplySkipWorktreeSet)
	}

	got2, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != original {
		t.Errorf("second call: content = %q, want still untouched %q", got2, original)
	}
}

// Issue #3024's gap 2, pinned as accepted risk: workingTreeDirty only says
// something changed configPath since the index, not that Apply's own rewrite
// did. A config dirtied for an unrelated reason reads as "a crashed Apply
// already rewrote this" and gets skip-worktree-tagged, hiding the unrelated
// edit from git status without ever rewriting it.
func TestApplyInTreeBindingTagsUnrelatedDirtyConfig(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, original, true)

	// Dirty the working tree with an edit that never mentions
	// upstream.example: not a partial or crashed rewrite, just an unrelated
	// change with the bit still clear.
	unrelated := "registry = \"https://unrelated.example/index/\"\n"
	if err := os.WriteFile(filepath.Join(dir, cargoBinding.InTreeConfigPath), []byte(unrelated), 0o644); err != nil {
		t.Fatal(err)
	}

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Errorf("reason = %v, want %v", reason, ApplyApplied)
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != unrelated {
		t.Errorf("content = %q, want unrelated edit left untouched %q", got, unrelated)
	}

	if !skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit not set, want set despite Apply never touching content")
	}
}

// gitOutput returns stdout only, unlike runGit, because callers here want a
// value back (a branch name, say) rather than a combined-output message.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append(gitQuietArgs(dir), args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// newUnmergedTestRepo builds a repo where relPath is genuinely unmerged
// (UU), the state a pre-work rebase can leave a config file in (issue
// #2932). Unlike newTestRepo, it needs a real three-commit history so `git
// merge` conflicts rather than fast-forwards.
func newUnmergedTestRepo(t *testing.T, relPath string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")

	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(content string) {
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("registry = \"https://upstream.example/index/\"\n")
	runGit(t, dir, "add", relPath)
	runGit(t, dir, "commit", "-m", "base")
	base := strings.TrimSpace(gitOutput(t, dir, "symbolic-ref", "--short", "HEAD"))

	runGit(t, dir, "checkout", "-b", "feature")
	write("registry = \"https://upstream.example/other/\"\n")
	runGit(t, dir, "add", relPath)
	runGit(t, dir, "commit", "-m", "feature")

	runGit(t, dir, "checkout", base)
	write("registry = \"https://upstream.example/index2/\"\n")
	runGit(t, dir, "add", relPath)
	runGit(t, dir, "commit", "-m", "base2")

	// A conflicting merge is the point of this fixture, so a nonzero exit
	// here is the expected outcome, not a setup failure.
	if err := exec.Command("git", append(gitQuietArgs(dir), "merge", "feature")...).Run(); err == nil {
		t.Fatal("git merge feature: succeeded, want a conflict")
	}

	// Confirm the fixture really reproduces an unmerged path, the state `git
	// update-index --skip-worktree` rejects with exit 128 (issue #2932), and
	// not some other kind of dirty working tree.
	status, err := exec.Command("git", append(gitQuietArgs(dir), "status", "--porcelain", "--", relPath)...).Output()
	if err != nil || !strings.HasPrefix(string(status), "UU ") {
		t.Fatalf("git status --porcelain %s = %q, err %v; want \"UU \" (unmerged)", relPath, status, err)
	}

	return dir
}

// Issue #2932: on an unmerged config path `git update-index --skip-worktree`
// fails with exit 128, and the old write-then-tag order had already rewritten
// content by then, landing the proxy URL in a tracked unmerged file that
// RevertInTreeBinding cannot clean up either (`git checkout --` refuses an
// unmerged path). Apply must fail without ever touching content.
func TestApplyInTreeBindingDoesNotRewriteContentWhenSkipWorktreeFails(t *testing.T) {
	dir := newUnmergedTestRepo(t, cargoBinding.InTreeConfigPath)

	before, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err == nil {
		t.Fatal("ApplyInTreeBinding: err = nil, want non-nil (skip-worktree must fail on an unmerged path)")
	}

	after, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("content rewritten despite update-index failure: before %q, after %q", before, after)
	}
}

// The other half of issue #2932's tag-then-write order: update-index
// succeeds, then the content write fails. The parent directory is made
// read-only, not the config file: post-#2933 the rewrite stages into a temp
// file and renames it over configPath, and both are directory-entry
// operations. Apply's compensating `--no-skip-worktree` must undo the bit.
func TestApplyInTreeBindingUnsetsBitWhenWriteFileFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permission bits don't block writes, so the WriteFile failure can't be simulated")
	}

	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, original, true)

	configFile := filepath.Join(dir, cargoBinding.InTreeConfigPath)
	configDir := filepath.Dir(configFile)
	if err := os.Chmod(configDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(configDir, 0o755)
	})

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err == nil {
		t.Fatal("ApplyInTreeBinding: err = nil, want non-nil (temp-file write must fail against a read-only parent directory)")
	}
	if reason == ApplyApplied {
		t.Error("reason = ApplyApplied, want anything else")
	}

	if skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit still set after write failure, want unset (compensating --no-skip-worktree should have run)")
	}

	if err := os.Chmod(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("content = %q, want unchanged %q", got, original)
	}
}

func TestRevertInTreeBindingRestoresAfterApply(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, original, true)

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Fatalf("ApplyInTreeBinding: reason = %v, want %v", reason, ApplyApplied)
	}

	reverted, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if !reverted {
		t.Error("reverted = false, want true")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("reverted content = %q, want %q", got, original)
	}
	if skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit still set after revert")
	}
}

func TestRevertInTreeBindingSecondCallIsNoop(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, original, true)

	if _, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}}); err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}

	reverted1, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("first RevertInTreeBinding: %v", err)
	}
	if !reverted1 {
		t.Fatal("first call: reverted = false, want true")
	}

	afterFirst, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}

	reverted2, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("second RevertInTreeBinding: %v", err)
	}
	if reverted2 {
		t.Error("second call: reverted = true, want false (idempotent no-op)")
	}

	afterSecond, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("content changed on second revert: before %q, after %q", afterFirst, afterSecond)
	}
	if skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit set after second revert, want unset")
	}
}

func TestRevertInTreeBindingNoopOnNeverApplied(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	reverted, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if reverted {
		t.Error("reverted = true, want false")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content changed unexpectedly: got %q, want %q", got, content)
	}
}

func TestRevertInTreeBindingRestoresAfterCrashBetweenPhases(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, original, true)

	// Simulate Apply's rewrite landing but the process dying before the
	// skip-worktree bit got set: content dirty, bit clear.
	rewritten := "registry = \"http://127.0.0.1:27182/index/\"\n"
	if err := os.WriteFile(filepath.Join(dir, cargoBinding.InTreeConfigPath), []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}

	reverted, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if !reverted {
		t.Error("reverted = false, want true")
	}

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("reverted content = %q, want %q", got, original)
	}
}

// Issue #2932/#2933's symlink-escape hazard: bash's `sed -i` reads through a
// symlink but writes by renaming a temp file over the path, replacing the
// symlink's directory entry instead of writing through it. Apply must match:
// a tracked symlink config path (git tracks symlinks as blob mode 120000)
// becomes a plain file, and its old target is never written through.
func TestApplyInTreeBindingReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := newTestRepo(t)

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside-config.toml")
	sentinel := "registry = \"https://upstream.example/index/\"\n"
	if err := os.WriteFile(outsideFile, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(dir, cargoBinding.InTreeConfigPath)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", cargoBinding.InTreeConfigPath)
	runGit(t, dir, "commit", "-m", "add symlinked config")

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Errorf("reason = %v, want %v", reason, ApplyApplied)
	}

	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		t.Error("config path is still a symlink after apply, want a plain regular file")
	}

	got, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewritten content still mentions upstream.example: %s", got)
	}
	if !strings.Contains(string(got), "http://127.0.0.1:27182/index/") {
		t.Errorf("rewritten content missing expected rewrite: %s", got)
	}

	outsideGot, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(outsideGot) != sentinel {
		t.Errorf("outside file was modified through the symlink: got %q, want %q", outsideGot, sentinel)
	}
}

// Bash `[ -f ]` parity: it is false for a symlink whose target is missing,
// so the bash phase functions silently skipped it rather than erroring.
// os.Stat on a dangling symlink returns ENOENT, so this falls into the same
// not-exist branch a missing file takes, needing no dedicated check.
func TestApplyInTreeBindingNoopsOnDanglingSymlink(t *testing.T) {
	dir := newTestRepo(t)

	linkPath := filepath.Join(dir, cargoBinding.InTreeConfigPath)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// git tracks the symlink blob (mode 120000) itself, whether or not the
	// target it points at exists on disk.
	if err := os.Symlink(filepath.Join(dir, "does-not-exist.toml"), linkPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", cargoBinding.InTreeConfigPath)
	runGit(t, dir, "commit", "-m", "add dangling symlink config")

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyMissing {
		t.Errorf("reason = %v, want %v", reason, ApplyMissing)
	}
	if skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit set on a dangling-symlink no-op")
	}
}

// The other bash `[ -f ]` parity case: it is false for a symlink resolving
// to a directory, so Apply must no-op rather than error or attempt a read.
func TestApplyInTreeBindingNoopsOnSymlinkToDirectory(t *testing.T) {
	dir := newTestRepo(t)

	targetDir := filepath.Join(dir, "some-dir")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(dir, cargoBinding.InTreeConfigPath)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, linkPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", cargoBinding.InTreeConfigPath)
	runGit(t, dir, "commit", "-m", "add directory-symlink config")

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyNotRegular {
		t.Errorf("reason = %v, want %v", reason, ApplyNotRegular)
	}
	if skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
		t.Error("skip-worktree bit set on a symlink-to-directory no-op")
	}
}

// Bash `[ -f ]` is false for any non-regular file, not just a directory, so
// a tracked symlink to a fifo must no-op instead of reaching os.ReadFile,
// which blocks forever on a fifo with no writer (issue #2933). The call runs
// in a goroutine under a short timeout so a regression fails fast instead of
// hanging the suite; the leaked goroutine dies with the test process.
func TestApplyInTreeBindingNoopsOnSymlinkToFifo(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("named pipes need syscall.Mkfifo, which this repo's other syscall-dependent tests gate to linux only")
	}

	dir := newTestRepo(t)

	fifoPath := filepath.Join(dir, "some.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	linkPath := filepath.Join(dir, cargoBinding.InTreeConfigPath)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifoPath, linkPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", cargoBinding.InTreeConfigPath)
	runGit(t, dir, "commit", "-m", "add fifo-symlink config")

	type result struct {
		reason ApplyOutcome
		err    error
	}
	done := make(chan result, 1)
	go func() {
		reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
		done <- result{reason: reason, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("ApplyInTreeBinding: %v", res.err)
		}
		if res.reason != ApplyNotRegular {
			t.Errorf("reason = %v, want %v", res.reason, ApplyNotRegular)
		}
		if skipWorktreeSet(t, dir, cargoBinding.InTreeConfigPath) {
			t.Error("skip-worktree bit set on a symlink-to-fifo no-op")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ApplyInTreeBinding hung reading a fifo with no writer -- the non-regular-file guard regressed (issue #2933)")
	}
}

// Issue #3082: an empty upstreamHost is an internal-consistency violation,
// since the verb layer already checks the manifest's route upstream host
// before calling in. It must surface as a real error, not render as
// ApplyMissing ("config not found") for a file that may be perfectly fine.
func TestApplyInTreeBindingErrorsOnEmptyUpstreamHost(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "", LocalURL: "http://127.0.0.1:27182"}})
	if err == nil {
		t.Fatal("ApplyInTreeBinding: err = nil, want non-nil (empty upstreamHost is an internal-consistency violation)")
	}
	if reason == ApplyMissing {
		t.Errorf("reason = %v, want anything but ApplyMissing (a nil-error-shaped outcome for a real error)", reason)
	}
}

// Issue #3142's duplicate-host defect: two rewrites naming the same
// UpstreamHost with different LocalURLs cannot be told apart by host-only
// matching, so whichever ReplaceAll runs first consumes every occurrence and
// silently sends the second route's traffic to the first route's prefix. The
// verb layer filters duplicates, so one reaching here violates the contract.
func TestApplyInTreeBindingErrorsOnDuplicateUpstreamHost(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	rewrites := []HostRewrite{
		{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182/r0"},
		{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182/r1"},
	}
	reason, err := ApplyInTreeBinding(dir, cargoBinding, rewrites)
	if err == nil {
		t.Fatal("ApplyInTreeBinding: err = nil, want non-nil (duplicate UpstreamHost is an internal-consistency violation)")
	}
	if reason == ApplyMissing {
		t.Errorf("reason = %v, want anything but ApplyMissing (a nil-error-shaped outcome for a real error)", reason)
	}
}

// An empty LocalURL would silently blank every matched URL rather than route
// it anywhere, so it must error the same way an empty UpstreamHost does.
func TestApplyInTreeBindingErrorsOnEmptyLocalURL(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: ""}})
	if err == nil {
		t.Fatal("ApplyInTreeBinding: err = nil, want non-nil (empty LocalURL is an internal-consistency violation)")
	}
	if reason == ApplyMissing {
		t.Errorf("reason = %v, want anything but ApplyMissing (a nil-error-shaped outcome for a real error)", reason)
	}
}

func TestRevertInTreeBindingNoopOnMissingFile(t *testing.T) {
	dir := newTestRepo(t)

	reverted, err := RevertInTreeBinding(dir, cargoBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if reverted {
		t.Error("reverted = true, want false")
	}
}

// writeOrphan strands a temp file the way a hard kill in the
// CreateTemp-to-Rename window would, without killing a real process.
func writeOrphan(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertOrphanGone takes context because each caller pins a different early
// return, which a bare "orphan still exists" would not name.
func assertOrphanGone(t *testing.T, path, context string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("orphan %s not removed by %s (err=%v)", path, context, err)
	}
}

// A Box crashing across more than one retry strands more than one orphan in
// the same directory (issue #3027), and a single glob-and-remove pass must
// clear all of them, not just the first match.
func TestSweepOrphanedTempFilesRemovesMultipleOrphans(t *testing.T) {
	dir := t.TempDir()
	var orphans []string
	for _, name := range []string{".intreebinding-aaa", ".intreebinding-bbb", ".intreebinding-ccc"} {
		orphans = append(orphans, writeOrphan(t, dir, name))
	}

	sweepOrphanedTempFiles(dir)

	for _, p := range orphans {
		assertOrphanGone(t, p, "sweepOrphanedTempFiles")
	}
}

func TestSweepOrphanedTempFilesNoopWhenNoneExist(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	sweepOrphanedTempFiles(dir)

	if _, err := os.Stat(other); err != nil {
		t.Errorf("sweep over an orphan-free directory touched an unrelated file: %v", err)
	}
}

// The glob boundary: ".intreebindingrc" shares the "intreebinding" prefix
// but has no "-" separator, so it must not match ".intreebinding-*", and the
// config file itself must never be swept regardless of name.
func TestSweepOrphanedTempFilesLeavesUnrelatedFilesAndConfigAlone(t *testing.T) {
	dir := t.TempDir()
	orphan := writeOrphan(t, dir, ".intreebinding-crashed")
	config := filepath.Join(dir, ".npmrc")
	unrelatedDotfile := filepath.Join(dir, ".intreebindingrc")

	for _, p := range []string{config, unrelatedDotfile} {
		if err := os.WriteFile(p, []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sweepOrphanedTempFiles(dir)

	assertOrphanGone(t, orphan, "sweepOrphanedTempFiles")
	for _, p := range []string{config, unrelatedDotfile} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("sweep removed unrelated file %s: %v", p, err)
		}
	}
}

// cargoBinding is deliberate (issue #3201 excludes it from InTreeBindings,
// but it is the only fixture row whose config lives in a subdirectory,
// .cargo/): the sweep must target configPath's own directory, not always
// repoDir. The sweep must also leave Apply's own temp file alone, created
// moments later in the same directory.
func TestApplyInTreeBindingSweepsOrphanInConfigSubdirectory(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, content, true)

	configDir := filepath.Join(dir, filepath.Dir(cargoBinding.InTreeConfigPath))
	orphan := writeOrphan(t, configDir, ".intreebinding-crashed")

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Fatalf("reason = %v, want %v", reason, ApplyApplied)
	}

	assertOrphanGone(t, orphan, "ApplyInTreeBinding")

	got, err := os.ReadFile(filepath.Join(dir, cargoBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewrite did not land alongside orphan removal: %q", got)
	}
}

// Revert has its own sweep call, independent of Apply's.
func TestRevertInTreeBindingRemovesOrphanTempFile(t *testing.T) {
	dir := newTestRepo(t)
	original := "registry = \"https://upstream.example/index/\"\n"
	writeConfig(t, dir, npmBinding.InTreeConfigPath, original, true)

	if _, err := ApplyInTreeBinding(dir, npmBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}}); err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}

	orphan := writeOrphan(t, dir, ".intreebinding-crashed")

	reverted, err := RevertInTreeBinding(dir, npmBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if !reverted {
		t.Error("reverted = false, want true")
	}

	assertOrphanGone(t, orphan, "RevertInTreeBinding")
}

// This test and its untracked sibling below pin the sweep running before the
// os.Stat/isTracked early returns: otherwise a config that goes missing or
// untracked after a crash strands its orphan forever (issue #3027). It uses
// npmBinding because that config path is the repo root itself (.npmrc),
// which exists even when the config does not, unlike cargoBinding's .cargo/.
func TestApplyInTreeBindingRemovesOrphanOnMissingConfig(t *testing.T) {
	dir := newTestRepo(t)

	orphan := writeOrphan(t, dir, ".intreebinding-crashed")

	reason, err := ApplyInTreeBinding(dir, npmBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyMissing {
		t.Fatalf("reason = %v, want %v", reason, ApplyMissing)
	}

	assertOrphanGone(t, orphan, "the ApplyMissing early return")
}

func TestApplyInTreeBindingRemovesOrphanOnUntrackedConfig(t *testing.T) {
	dir := newTestRepo(t)
	writeConfig(t, dir, cargoBinding.InTreeConfigPath, "registry = \"https://upstream.example/index/\"\n", false)

	configDir := filepath.Join(dir, filepath.Dir(cargoBinding.InTreeConfigPath))
	orphan := writeOrphan(t, configDir, ".intreebinding-crashed")

	reason, err := ApplyInTreeBinding(dir, cargoBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyUntracked {
		t.Fatalf("reason = %v, want %v", reason, ApplyUntracked)
	}

	assertOrphanGone(t, orphan, "the ApplyUntracked early return")
}

// Mirrors the Apply-side pair above for Revert's own ENOENT early return,
// with npmBinding for the same existing-directory reason.
func TestRevertInTreeBindingRemovesOrphanOnMissingConfig(t *testing.T) {
	dir := newTestRepo(t)

	orphan := writeOrphan(t, dir, ".intreebinding-crashed")

	reverted, err := RevertInTreeBinding(dir, npmBinding)
	if err != nil {
		t.Fatalf("RevertInTreeBinding: %v", err)
	}
	if reverted {
		t.Error("reverted = true, want false")
	}

	assertOrphanGone(t, orphan, "RevertInTreeBinding's missing-config early return")
}

// Issue #3027's actual crash state: Apply tags the bit before it writes, so
// a hard kill in the CreateTemp-to-Rename window leaves the bit set, content
// un-rewritten, and an orphan behind. The retry then hits
// ApplySkipWorktreeSet, an early return above the sweep in source order, so
// this is the one case pinning "sweep before skipWorktreeBitSet".
func TestApplyInTreeBindingRemovesOrphanWhenSkipWorktreeAlreadySet(t *testing.T) {
	dir := newTestRepo(t)
	writeConfig(t, dir, npmBinding.InTreeConfigPath, "registry=https://upstream.example/index/\n", true)
	runGit(t, dir, "update-index", "--skip-worktree", "--", npmBinding.InTreeConfigPath)

	orphan := writeOrphan(t, dir, ".intreebinding-crashed")

	reason, err := ApplyInTreeBinding(dir, npmBinding, []HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:27182"}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplySkipWorktreeSet {
		t.Fatalf("reason = %v, want %v", reason, ApplySkipWorktreeSet)
	}

	assertOrphanGone(t, orphan, "the ApplySkipWorktreeSet early return")
}

// Issue #3259: for a host-rooted route ecosystem.RouteLocalURL yields a
// LocalURL with no trailing slash, so Apply's plain scheme-plus-host
// substring replace leaves whatever path followed the upstream host
// untouched, and the registry's real upstream path survives with no
// route-aware code here. This pins existing behavior, not new behavior.
func TestApplyInTreeBindingPreservesFullUpstreamPathForHostRootedRoute(t *testing.T) {
	dir := newTestRepo(t)
	content := "registry=https://registry.example.com/artifactory/api/npm/npm-local/\n"
	writeConfig(t, dir, npmBinding.InTreeConfigPath, content, true)

	route := registrymanifest.Route{Prefix: "r0", UpstreamHost: "registry.example.com"}
	port := 27182
	localURL := ecosystem.RouteLocalURL(route.Prefix, port)

	reason, err := ApplyInTreeBinding(dir, npmBinding, []HostRewrite{{UpstreamHost: route.UpstreamHost, LocalURL: localURL}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyApplied {
		t.Fatalf("reason = %v, want %v", reason, ApplyApplied)
	}

	got, err := os.ReadFile(filepath.Join(dir, npmBinding.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	want := "registry=http://127.0.0.1:27182/r0/artifactory/api/npm/npm-local/\n"
	if string(got) != want {
		t.Errorf("rewritten content = %q, want %q (full upstream path preserved, not just the bare route root)", got, want)
	}
}

// AC3's missing-config outcome (ApplyMissing, no rewrite) in the host-rooted
// context: the LocalURL here carries a real route prefix built via
// ecosystem.RouteLocalURL, not the bare-port LocalURL every other
// ApplyMissing test in this file uses, so this is not a duplicate of
// TestApplyInTreeBindingNoopOnMissingFile under another name.
func TestApplyInTreeBindingMissingConfigUnderHostRootedRoute(t *testing.T) {
	dir := newTestRepo(t)

	route := registrymanifest.Route{Prefix: "r0", UpstreamHost: "registry.example.com"}
	localURL := ecosystem.RouteLocalURL(route.Prefix, 27182)

	reason, err := ApplyInTreeBinding(dir, npmBinding, []HostRewrite{{UpstreamHost: route.UpstreamHost, LocalURL: localURL}})
	if err != nil {
		t.Fatalf("ApplyInTreeBinding: %v", err)
	}
	if reason != ApplyMissing {
		t.Errorf("reason = %v, want %v", reason, ApplyMissing)
	}
}
