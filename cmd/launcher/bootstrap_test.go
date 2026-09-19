package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/settle"
)

// bootstrap surfaces a validation error without constructing a runner, forge
// client, or dispatch factory.
func TestBootstrap_PropagatesValidateError(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	lc, err := bootstrap(true, dispatchKindWork, false)

	if lc != nil {
		t.Errorf("bootstrap() launch context = %+v, want nil on validate error", lc)
	}
	if err == nil || !strings.Contains(err.Error(), "REPO_SLUG") {
		t.Fatalf("bootstrap() error = %v, want a REPO_SLUG validation error", err)
	}
}

// bootstrap() wraps a validate(c) failure so errors.Is(err, errConfigInvalid)
// holds (issue #2568 slice 1), letting a caller tell a config-validation
// failure from any other bootstrap failure. validate(c) itself is unchanged:
// TestBootstrap_PropagatesValidateError still asserts the raw REPO_SLUG text.
func TestBootstrap_ValidateError_WrapsErrConfigInvalid(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	_, err := bootstrap(true, dispatchKindWork, false)

	if !errors.Is(err, errConfigInvalid) {
		t.Fatalf("bootstrap() error = %v, want errors.Is(err, errConfigInvalid) = true", err)
	}
}

// bootstrap()'s wrapping of validate(seedConfig) preserves the Remedy text
// (issue #2886). This picks the driver-credentials row because its Remedy
// differs from its own Probe error text (see
// TestValidate_RequiredKnobFailure_IncludesRemedy in main_test.go).
func TestBootstrap_PropagatesRemedyText_WrapsErrConfigInvalid(t *testing.T) {
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("DRIVER", "")

	_, err := bootstrap(true, dispatchKindWork, false)

	if err == nil {
		t.Fatal("bootstrap() = nil error, want a driver-credentials validation error")
	}
	if !errors.Is(err, errConfigInvalid) {
		t.Fatalf("bootstrap() error = %v, want errors.Is(err, errConfigInvalid) = true", err)
	}
	// The row's Remedy is a fixed string, so minimalValidConfig() resolves the
	// same text the env above makes bootstrap() fail on.
	wantRemedy := checkByName(t, launcherRequiredKnobChecks(minimalValidConfig()), "driver-credentials").Remedy
	if !strings.Contains(err.Error(), "\nremedy: "+wantRemedy) {
		t.Errorf("bootstrap() error = %q, want it to contain remedy line %q", err.Error(), wantRemedy)
	}
}

// The retirement gate (registry-proxy-routes row, checks.go) refuses
// REGISTRY_PROXY_CREDENTIAL_ENV unconditionally, even with
// REGISTRY_PROXY_UPSTREAM_URL unset. That case was the carve-out issue #2850
// used to exempt; it no longer exists, so any one of the five retired knobs
// aborts the launch on its own.
func TestBootstrap_RetiredRegistryProxyCredentialEnv_NoUpstreamURL_WrapsErrConfigInvalid(t *testing.T) {
	stubExecutableOnPath(t, "pasta")
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	issuesDir := t.TempDir()
	issueFile := `---
title: Some issue
state: untriaged
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(issuesDir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Setenv("REGISTRY_PROXY_CREDENTIAL_ENV", "SPINDRIFT_TEST_REGISTRY_PROXY_CRED_DOES_NOT_EXIST")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err == nil {
		t.Fatal("bootstrap() = nil error, want an error: REGISTRY_PROXY_CREDENTIAL_ENV is retired regardless of REGISTRY_PROXY_UPSTREAM_URL")
	}
	if lc != nil {
		t.Fatalf("bootstrap() on error = %+v, want nil launch context", lc)
	}
	if !errors.Is(err, errConfigInvalid) {
		t.Fatalf("bootstrap() error = %v, want errors.Is(err, errConfigInvalid) = true", err)
	}
	if !strings.Contains(err.Error(), "REGISTRY_PROXY_CREDENTIAL_ENV") {
		t.Errorf("bootstrap() error = %q, must name REGISTRY_PROXY_CREDENTIAL_ENV", err.Error())
	}
}

// The retirement gate (registry-proxy-routes row, checks.go) fires as a
// launch-gate failure through bootstrap() when only
// REGISTRY_PROXY_UPSTREAM_URL is set, before any Box runs.
func TestBootstrap_RegistryProxyUpstreamURLAlone_WrapsErrConfigInvalid(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("REGISTRY_PROXY_UPSTREAM_URL", "https://registry.example.com/artifactory/api/cargo/crates/index/")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err == nil {
		t.Fatal("bootstrap() = nil error, want an error: REGISTRY_PROXY_UPSTREAM_URL is retired")
	}
	if lc != nil {
		t.Fatalf("bootstrap() on error = %+v, want nil launch context", lc)
	}
	if !errors.Is(err, errConfigInvalid) {
		t.Fatalf("bootstrap() error = %v, want errors.Is(err, errConfigInvalid) = true", err)
	}
	// The remedy stanza names the host the retired URL pointed at, never the
	// URL itself: a route matches a host and derives the paths it serves, so
	// there is no key left for that base path to migrate into (ADR 0047,
	// issue #3261).
	if !strings.Contains(err.Error(), `match-host = "registry.example.com"`) {
		t.Errorf("bootstrap() error = %q, must name the host derived from the offending URL", err.Error())
	}
	if strings.Contains(err.Error(), "/artifactory/api/cargo/crates/index/") {
		t.Errorf("bootstrap() error = %q, must not carry the retired URL's base path into the remedy stanza", err.Error())
	}
}

// The non-matching entries around the matching one prove that host-matching,
// not "first entry wins", resolves the credential. internal/credresolver's own
// tests define a separate copy because that package cannot import this
// test-only const.
const multiEntryNetrc = `machine other.example.com
login someone
password wrong-entry

machine registry.example.com
login someone
password s3cr3t

machine yet-another.example.com
login someone
password also-wrong
`

// The least env a registry-proxy-routes test needs to reach the registry-proxy
// resolution step: local tracker and forge, bwrap runtime, so no test makes a
// real network call. The two retirement-gate tests above each pin a distinct
// env shape and so spell theirs out in full instead.
func setMinimalLocalBootstrapEnv(t *testing.T, repoPath string) {
	t.Helper()
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", t.TempDir())
}

// buildRegistryProxyRoutes' routes-file branch runs a route's credential
// through the same destructive credresolver.New(...).Resolve() the retired
// scalar knobs used (issue #3139): the value lands on
// lc.config.registryProxyRoutes and the source env var is unset afterward.
func TestBootstrap_RegistryProxyRoutesFile_EnvCredential_ResolvesAndUnsets(t *testing.T) {
	stubExecutableOnPath(t, "pasta")
	checkout := mustSeedableCheckout(t)
	setMinimalLocalBootstrapEnv(t, filepath.Join(t.TempDir(), "accum.git"))

	t.Setenv("SPINDRIFT_TEST_ROUTES_ENV_CRED", "s3cr3t")
	t.Setenv("REGISTRY_PROXY_ROUTES_FILE", writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-origin = "https://registry.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_ENV_CRED" }
`))
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want no error: a resolvable routes-file env credential must not be rejected", err)
	}
	t.Cleanup(lc.cleanup)

	if len(lc.config.registryProxyRoutes) != 1 {
		t.Fatalf("lc.config.registryProxyRoutes = %+v, want exactly 1 route", lc.config.registryProxyRoutes)
	}
	if got := lc.config.registryProxyRoutes[0].Credential; got != "s3cr3t" {
		t.Errorf("lc.config.registryProxyRoutes[0].Credential = %q, want %q", got, "s3cr3t")
	}
	if v := os.Getenv("SPINDRIFT_TEST_ROUTES_ENV_CRED"); v != "" {
		t.Errorf("source env var must be unset after resolution, still has value %q", v)
	}
}

// A route's auth-scheme and upstream-origin survive resolution onto
// lc.config.registryProxyRoutes, keeping the trailing-slash normalization
// registryroutes.Parse applies.
func TestBootstrap_RegistryProxyRoutesFile_AuthSchemeAndUpstreamOrigin_Preserved(t *testing.T) {
	stubExecutableOnPath(t, "pasta")
	checkout := mustSeedableCheckout(t)
	setMinimalLocalBootstrapEnv(t, filepath.Join(t.TempDir(), "accum.git"))

	t.Setenv("SPINDRIFT_TEST_ROUTES_BASIC_CRED", "s3cr3t")
	t.Setenv("REGISTRY_PROXY_ROUTES_FILE", writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-origin = "https://registry.example.com/"
auth-scheme = "basic"
credential = { env = "SPINDRIFT_TEST_ROUTES_BASIC_CRED" }
`))
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want no error: an auth-scheme + upstream-origin route must resolve", err)
	}
	t.Cleanup(lc.cleanup)

	if len(lc.config.registryProxyRoutes) != 1 {
		t.Fatalf("lc.config.registryProxyRoutes = %+v, want exactly 1 route", lc.config.registryProxyRoutes)
	}
	route := lc.config.registryProxyRoutes[0]
	if route.AuthScheme != "basic" {
		t.Errorf("route.AuthScheme = %q, want %q", route.AuthScheme, "basic")
	}
	if want := "https://registry.example.com"; route.Upstream != want {
		t.Errorf("route.Upstream = %q, want %q (trailing slash normalized away)", route.Upstream, want)
	}
}

// The routes file is the only surviving netrc credential path now the scalar
// knobs are retired, and the credential resolves by the route's own
// match-host. multiEntryNetrc puts a non-matching machine entry ahead of the
// matching one, so "first entry wins" would fail this.
func TestBootstrap_RegistryProxyRoutesFile_NetrcCredential_ResolvesByRouteHost(t *testing.T) {
	stubExecutableOnPath(t, "pasta")
	checkout := mustSeedableCheckout(t)
	setMinimalLocalBootstrapEnv(t, filepath.Join(t.TempDir(), "accum.git"))

	netrcPath := filepath.Join(t.TempDir(), "netrc")
	if err := os.WriteFile(netrcPath, []byte(multiEntryNetrc), 0o600); err != nil {
		t.Fatalf("failed to write temp netrc file: %v", err)
	}
	t.Setenv("REGISTRY_PROXY_ROUTES_FILE", writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-origin = "https://registry.example.com"
credential = { netrc = "`+netrcPath+`" }
`))
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want no error: a resolvable netrc-sourced route credential must not be rejected", err)
	}
	t.Cleanup(lc.cleanup)

	if len(lc.config.registryProxyRoutes) != 1 || lc.config.registryProxyRoutes[0].Credential != "s3cr3t" {
		t.Errorf("lc.config.registryProxyRoutes = %+v, want exactly 1 route with Credential %q", lc.config.registryProxyRoutes, "s3cr3t")
	}
}

// The cargo-credentials sibling of the netrc test above: the credential comes
// from the [registries.NAME] table matching the route's registry-name, not its
// match-host. An unrelated table sits ahead of the matching one so "first table
// wins" would fail, and the token differs from the netrc test's so a
// copy-paste between the two tests fails instead of passing silently.
func TestBootstrap_RegistryProxyRoutesFile_CargoCredential_ResolvesByRegistryName(t *testing.T) {
	stubExecutableOnPath(t, "pasta")
	checkout := mustSeedableCheckout(t)
	setMinimalLocalBootstrapEnv(t, filepath.Join(t.TempDir(), "accum.git"))

	const cargoCredentials = `[registries.other]
token = "wrong-token"

[registries.myreg]
token = "cargos3cr3t"
`
	credentialsPath := filepath.Join(t.TempDir(), "credentials.toml")
	if err := os.WriteFile(credentialsPath, []byte(cargoCredentials), 0o600); err != nil {
		t.Fatalf("failed to write temp credentials.toml file: %v", err)
	}
	t.Setenv("REGISTRY_PROXY_ROUTES_FILE", writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-origin = "https://registry.example.com"
credential = { cargo-credentials = "`+credentialsPath+`", registry-name = "myreg" }
`))
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want no error: a resolvable cargo-credentials-sourced route credential must not be rejected", err)
	}
	t.Cleanup(lc.cleanup)

	if len(lc.config.registryProxyRoutes) != 1 || lc.config.registryProxyRoutes[0].Credential != "cargos3cr3t" {
		t.Errorf("lc.config.registryProxyRoutes = %+v, want exactly 1 route with Credential %q", lc.config.registryProxyRoutes, "cargos3cr3t")
	}
}

// The registry proxy's documented off state survives becoming a routes builder
// (issue #3139): with REGISTRY_PROXY_ROUTES_FILE unset, bootstrap() succeeds
// with an empty route table and never touches credresolver.
func TestBootstrap_NoRoutesFile_EmptyRoutes(t *testing.T) {
	stubExecutableOnPath(t, "pasta")
	checkout := mustSeedableCheckout(t)
	setMinimalLocalBootstrapEnv(t, filepath.Join(t.TempDir(), "accum.git"))
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want no error with no routes file set", err)
	}
	t.Cleanup(lc.cleanup)

	if len(lc.config.registryProxyRoutes) != 0 {
		t.Errorf("lc.config.registryProxyRoutes = %+v, want empty", lc.config.registryProxyRoutes)
	}
}

// An unresolvable route credential aborts bootstrap() naming that route's
// match-host, wrapped in errConfigInvalid. It fails at the registry-proxy-routes
// row's Peek probe in checks.go, which validate(c) runs before
// buildRegistryProxyRoutes, so this asserts that Peek wrap, not
// resolveRegistryRoutesFromFile's Resolve wrap (covered in its own test).
func TestBootstrap_RegistryProxyRoutesFile_ResolveFailure_ChecksGateNamesRoute(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	setMinimalLocalBootstrapEnv(t, filepath.Join(t.TempDir(), "accum.git"))

	t.Setenv("REGISTRY_PROXY_ROUTES_FILE", writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_ROUTES_CRED_DOES_NOT_EXIST" }
`))
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err == nil {
		t.Fatal("bootstrap() = nil error, want an error: the route's env credential is unresolvable")
	}
	if lc != nil {
		t.Fatalf("bootstrap() on error = %+v, want nil launch context", lc)
	}
	if !errors.Is(err, errConfigInvalid) {
		t.Fatalf("bootstrap() error = %v, want errors.Is(err, errConfigInvalid) = true", err)
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("bootstrap() error = %q, must name the offending route's match-host", err.Error())
	}
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := runGit(dir, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// A throwaway checkout with one commit on "main", for use as the pwd argument
// to seedAccumulationRepoIfHostMediated.
func mustSeedableCheckout(t *testing.T) string {
	t.Helper()
	checkout := t.TempDir()
	mustRunGit(t, checkout, "init", "-b", "main")
	mustRunGit(t, checkout, "config", "user.email", "test@example.com")
	mustRunGit(t, checkout, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(checkout, "base.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, checkout, "add", "base.txt")
	mustRunGit(t, checkout, "commit", "-m", "base")
	return checkout
}

// Presence on disk is not enough: HEAD must resolve to a real ref with a
// commit, not the dangling ref git init --bare leaves behind (see
// SeedAccumulationRepo's symbolic-ref step), because cloning is what the
// acceptance criterion turns on.
func assertClonableAccumulationRepo(t *testing.T, repoPath, baseBranch string) {
	t.Helper()
	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf("Accumulation repo not created: %v", err)
	}
	if err := runGit(repoPath, "rev-parse", "--verify", "refs/heads/"+baseBranch); err != nil {
		t.Errorf("Accumulation repo has no %s ref: %v", baseBranch, err)
	}
	head, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("symbolic-ref HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(head)); got != "refs/heads/"+baseBranch {
		t.Errorf("Accumulation repo HEAD = %s, want refs/heads/%s", got, baseBranch)
	}
}

// seedAccumulationRepoIfHostMediated wires local.SeedAccumulationRepo (ADR
// 0033) against the config's resolved codeForgeAccumulationRepoDir and
// baseBranch. Seeding must happen before any Box runs (issue #1726): a
// defaulted but nonexistent path makes the /repo mount silently skip.
func TestSeedAccumulationRepoIfHostMediated_Local_SeedsFromPwd(t *testing.T) {
	checkout := mustSeedableCheckout(t)

	repoPath := filepath.Join(t.TempDir(), "accum.git")
	c := baseConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repoPath
	c.baseBranch = "main"

	lock, err := seedAccumulationRepoIfHostMediated(c, checkout)
	if err != nil {
		t.Fatalf("seedAccumulationRepoIfHostMediated: %v", err)
	}
	if lock == nil {
		t.Fatal("seedAccumulationRepoIfHostMediated lock = nil, want a held *local.AccumulationLock (issue #2441)")
	}
	t.Cleanup(func() { _ = lock.Release() })

	assertClonableAccumulationRepo(t, repoPath, "main")
}

// No seeding occurs for github/git (issue #1726). The nonexistent pwd would
// fail SeedAccumulationRepo's git push if it were invoked, so a nil error
// proves the no-op.
func TestSeedAccumulationRepoIfHostMediated_NonLocal_NoOp(t *testing.T) {
	c := baseConfig()
	c.codeForge = "github"

	lock, err := seedAccumulationRepoIfHostMediated(c, "/nonexistent/pwd")
	if err != nil {
		t.Errorf("seedAccumulationRepoIfHostMediated(CODE_FORGE=github) = %v, want nil (no-op)", err)
	}
	if lock != nil {
		t.Errorf("seedAccumulationRepoIfHostMediated(CODE_FORGE=github) lock = %v, want nil (no-op)", lock)
	}
}

// The research dispatch kind seeds too under CODE_FORGE=local while
// c.selfContained is false (issue #2439): research still clones and explores
// the repo in-box via agent/entrypoint.sh's clone_repo(), so it needs /repo
// mounted like work does. Only the selfContained sub-mode stays a no-op.
func TestSeedAccumulationRepoIfHostMediated_ResearchKind_SeedsFromPwd(t *testing.T) {
	checkout := mustSeedableCheckout(t)

	repoPath := filepath.Join(t.TempDir(), "accum.git")
	c := baseConfig()
	c.codeForge = "local"
	c.dispatchKind = dispatchKindResearch
	c.codeForgeAccumulationRepoDir = repoPath
	c.baseBranch = "main"

	lock, err := seedAccumulationRepoIfHostMediated(c, checkout)
	if err != nil {
		t.Fatalf("seedAccumulationRepoIfHostMediated: %v", err)
	}
	if lock == nil {
		t.Fatal("seedAccumulationRepoIfHostMediated lock = nil, want a held *local.AccumulationLock (issue #2441)")
	}
	t.Cleanup(func() { _ = lock.Release() })

	assertClonableAccumulationRepo(t, repoPath, "main")
}

// Self-contained research skips seeding even under CODE_FORGE=local: it never
// mounts /repo or clones anything, so seeding would only add a way to fail (a
// missing baseBranch in pwd) for a run that never uses the repo. The
// nonexistent pwd would fail SeedAccumulationRepo's git push if it were
// invoked, so a nil error proves the no-op.
func TestSeedAccumulationRepoIfHostMediated_ResearchSelfContained_NoOp(t *testing.T) {
	c := baseConfig()
	c.codeForge = "local"
	c.dispatchKind = dispatchKindResearch
	c.selfContained = true
	c.codeForgeAccumulationRepoDir = filepath.Join(t.TempDir(), "accum.git")
	c.baseBranch = "main"

	lock, err := seedAccumulationRepoIfHostMediated(c, "/nonexistent/pwd")
	if err != nil {
		t.Errorf("seedAccumulationRepoIfHostMediated(research kind, selfContained) = %v, want nil (no-op)", err)
	}
	if lock != nil {
		t.Errorf("seedAccumulationRepoIfHostMediated(research kind, selfContained) lock = %v, want nil (no-op)", lock)
	}
}

// The core regression test for issue #2441: a second call against the same
// repoPath, standing in for a concurrent spindrift process, must fail while
// the first call's lock is held rather than race SeedAccumulationRepo's
// seed+mount window. A third call after release succeeds, proving the lock is
// per-run and not permanent.
func TestSeedAccumulationRepoIfHostMediated_ConcurrentCallSameRepo_FailsUntilReleased(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")
	c := baseConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repoPath
	c.baseBranch = "main"

	firstLock, err := seedAccumulationRepoIfHostMediated(c, checkout)
	if err != nil {
		t.Fatalf("first seedAccumulationRepoIfHostMediated: %v", err)
	}
	if firstLock == nil {
		t.Fatal("first seedAccumulationRepoIfHostMediated lock = nil, want a held *local.AccumulationLock")
	}

	secondLock, err := seedAccumulationRepoIfHostMediated(c, checkout)
	if err == nil {
		t.Error("second seedAccumulationRepoIfHostMediated while first lock held = nil error, want contention error (issue #2441)")
	}
	if secondLock != nil {
		t.Errorf("second seedAccumulationRepoIfHostMediated while first lock held = %v, want nil lock on error", secondLock)
	}

	if err := firstLock.Release(); err != nil {
		t.Fatalf("firstLock.Release(): %v", err)
	}

	thirdLock, err := seedAccumulationRepoIfHostMediated(c, checkout)
	if err != nil {
		t.Fatalf("third seedAccumulationRepoIfHostMediated after release: %v", err)
	}
	if thirdLock == nil {
		t.Fatal("third seedAccumulationRepoIfHostMediated after release lock = nil, want a held *local.AccumulationLock")
	}
	t.Cleanup(func() { _ = thirdLock.Release() })
}

// Regression test for the seed-failure path's `_ = lock.Release()` in
// bootstrap.go: a push that fails after the lock is held must release it, or
// the repo stays wedged for the rest of the process. A nil returned lock does
// not prove that (a caller could nil it out without releasing), so this
// reacquires the flock directly against the same repoPath.
func TestSeedAccumulationRepoIfHostMediated_SeedFailure_ReleasesLock(t *testing.T) {
	checkout := t.TempDir()
	mustRunGit(t, checkout, "init", "-b", "main")
	// No commit: baseBranch has no ref yet, so SeedAccumulationRepo's push
	// fails after the lock is already acquired.

	repoPath := filepath.Join(t.TempDir(), "accum.git")
	c := baseConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repoPath
	c.baseBranch = "main"

	lock, err := seedAccumulationRepoIfHostMediated(c, checkout)
	if err == nil {
		t.Fatal("seedAccumulationRepoIfHostMediated with an empty checkout = nil error, want a seed failure")
	}
	if lock != nil {
		t.Fatalf("seedAccumulationRepoIfHostMediated on seed failure lock = %v, want nil", lock)
	}

	reacquired, err := local.AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireAccumulationLock after seed failure: %v, want the failed attempt's lock to have been released", err)
	}
	t.Cleanup(func() { _ = reacquired.Release() })
}

// Regression test for bootstrap's early-return window (issue #2441): every
// step after seedAccumulationRepoIfHostMediated hands back a held lock used to
// leak it on `return nil, err`. RUNTIME=bwrap keeps EnsureReady() a no-op, so
// BOX_FORGE_AND_ISSUE_ACCESS=read-only against the github tracker fails
// checkReadOnlyCapabilityGate offline, exactly inside that window.
func TestBootstrap_EarlyErrorAfterAccumLockAcquired_ReleasesLock(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-only")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err == nil {
		t.Fatal("bootstrap() with BOX_FORGE_AND_ISSUE_ACCESS=read-only against the github tracker = nil error, want checkReadOnlyCapabilityGate to reject it")
	}
	if lc != nil {
		t.Fatalf("bootstrap() on early error = %+v, want nil launch context", lc)
	}

	reacquired, err := local.AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireAccumulationLock after bootstrap's early error: %v, want the held lock to have been released", err)
	}
	t.Cleanup(func() { _ = reacquired.Release() })
}

// The load-bearing half of issue #2441: the accum lock stays held across a
// successful bootstrap() return and is released only by lc.cleanup(). Deleting
// cleanup's accumLock.Release() leaves every other test green, since nothing
// else calls lc.cleanup() and then reacquires, so this checks contention
// before cleanup and a free lock after.
func TestBootstrap_Success_HoldsAccumLockUntilCleanup(t *testing.T) {
	stubExecutableOnPath(t, "pasta")
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	issuesDir := t.TempDir()
	issueFile := `---
title: Some issue
state: untriaged
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(issuesDir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want a successful launch context", err)
	}
	if lc == nil {
		t.Fatal("bootstrap() launch context = nil, want a non-nil *launchContext on success")
	}

	if _, err := local.AcquireAccumulationLock(repoPath); err == nil {
		t.Error("AcquireAccumulationLock before lc.cleanup() = nil error, want contention (accum lock should still be held after a successful bootstrap())")
	}

	lc.cleanup()

	reacquired, err := local.AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireAccumulationLock after lc.cleanup(): %v, want the lock to have been released", err)
	}
	t.Cleanup(func() { _ = reacquired.Release() })
}

// A PATH that still resolves "git" (bootstrap's seeding shells out to it) and
// a stub "bwrap" (validate(c)'s doctor.RuntimeCheck LookPath probe runs even
// earlier), but excludes pasta, so checkBwrapPastaGate's LookPath("pasta")
// fails whether or not the real test runner has pasta installed.
func pathWithoutPasta(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not found on PATH: %v", err)
	}
	bwrapStubDir := t.TempDir()
	bwrapStub := filepath.Join(bwrapStubDir, "bwrap")
	if err := os.WriteFile(bwrapStub, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write stub bwrap: %v", err)
	}
	return bwrapStubDir + string(os.PathListSeparator) + filepath.Dir(git)
}

// The wiring test for issue #2666: bwrap_pasta_gate_test.go covers
// checkBwrapPastaGate directly, but every other RUNNER_KIND=bwrap bootstrap
// test stubs pasta onto PATH, so a bootstrap() that stopped calling the gate
// would leave them all green. Here pasta is absent, and bootstrap() itself
// must return an error naming pasta and PATH.
func TestBootstrap_BwrapDefaultNetworkModeMissingPasta_BlocksLaunch(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	issuesDir := t.TempDir()
	issueFile := `---
title: Some issue
state: untriaged
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(issuesDir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Chdir(checkout)
	// No stubExecutableOnPath(t, "pasta") here, unlike every other
	// RUNNER_KIND=bwrap test in this file: PATH holds only a stub bwrap and
	// git's directory, so pasta cannot resolve.
	t.Setenv("PATH", pathWithoutPasta(t))

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err == nil {
		t.Fatal("bootstrap() with RUNNER_KIND=bwrap, default NETWORK_MODE, and pasta absent from PATH = nil error, want checkBwrapPastaGate to block the launch")
	}
	if lc != nil {
		t.Fatalf("bootstrap() on a pasta-gate error = %+v, want nil launch context", lc)
	}
	if !strings.Contains(err.Error(), "pasta") {
		t.Errorf("bootstrap() error = %q, want it to mention pasta", err.Error())
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("bootstrap() error = %q, want it to mention PATH", err.Error())
	}
}

// runner.ValidateRuntime only probes presence with exec.LookPath, so any
// executable file satisfies it. The stub exits nonzero on every invocation,
// mimicking an OCI CLI failing `image inspect` against a missing image: the
// bwrap branch never runs it, so a selection wrongly routed to the OCI branch
// shows up as a readiness failure instead of passing either way.
func stubExecutableOnPath(t *testing.T, name string) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, name)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Runner selection keys off RUNNER_KIND, not a runtime-name comparison (issue
// #2538). RUNTIME=podman is what the old `c.runtime == "bwrap"` check read as
// "select OCI". bwrapAdapter's IsReady() never shells out, so a successful
// bootstrap() proves the bwrap branch was taken; the old comparison would run
// `podman image inspect` and fail. The stub podman only satisfies LookPath.
func TestBootstrap_RunnerKindBwrap_OverridesMismatchedRuntime(t *testing.T) {
	stubExecutableOnPath(t, "podman")
	stubExecutableOnPath(t, "pasta")
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	issuesDir := t.TempDir()
	issueFile := `---
title: Some issue
state: untriaged
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(issuesDir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "podman")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Chdir(checkout)

	lc, err := bootstrap(false, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() with RUNNER_KIND=bwrap and RUNTIME=podman = %v, want success (bwrap selected)", err)
	}
	if lc == nil {
		t.Fatal("bootstrap() launch context = nil, want a non-nil *launchContext on success")
	}
	t.Cleanup(lc.cleanup)
}

// The mirror of the test above: RUNNER_KIND=oci routes to the OCI adapter even
// with RUNTIME=bwrap. The stub bwrap keeps ValidateRuntime's LookPath (keyed
// off RUNTIME) from short-circuiting before selection runs, which would make
// this pass whichever branch was taken. Asserting the adapter's own "image
// absent" message, not just err != nil, is what pins the OCI branch.
func TestBootstrap_RunnerKindOCI_OverridesMatchingRuntime(t *testing.T) {
	stubExecutableOnPath(t, "bwrap")
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	issuesDir := t.TempDir()
	issueFile := `---
title: Some issue
state: untriaged
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(issuesDir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "oci")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Chdir(checkout)

	lc, err := bootstrap(false, dispatchKindWork, false)
	if err == nil || !strings.Contains(err.Error(), "image absent") {
		t.Fatalf("bootstrap() with RUNNER_KIND=oci and RUNTIME=bwrap = %v, want the OCI adapter's \"image absent\" readiness error", err)
	}
	if lc != nil {
		t.Fatalf("bootstrap() on readiness error = %+v, want nil launch context", lc)
	}
}

// researchLaunchStack is cmdConsole's research-kind mirror of bootstrap's
// work-kind wiring (issue #1708): the same newIssueTracker/newDispatchFactory/
// newSettle helpers with dispatchKindResearch applied, so it must return the
// agent-research label family and a ResearchSettle. The local tracker makes
// the label write observable from disk with no network dependency.
func TestResearchLaunchStack_WiresResearchLabelsAndSettle(t *testing.T) {
	issuesDir := t.TempDir()
	issueFile := `---
title: Some issue
state: untriaged
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(issuesDir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	c := baseConfig()
	c.issueTracker = "local"
	c.localIssuesDir = issuesDir
	dir := tempLogDir(t)
	lc := &launchContext{
		config:    c,
		pwd:       dir,
		runner:    nil,
		codeForge: forge.NewFake(),
	}

	it, f, s := researchLaunchStack(lc)
	t.Cleanup(f.Cleanup)

	if err := it.TransitionState("42", forge.Untriaged, forge.Dispatchable); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
	iss, err := it.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !containsLabel(iss.Labels, "agent-research") {
		t.Errorf("issue labels = %v, want agent-research", iss.Labels)
	}
	if f == nil {
		t.Fatal("researchLaunchStack factory = nil, want a research-kind *dispatch.Factory")
	}
	if _, ok := s.(*settle.ResearchSettle); !ok {
		t.Errorf("researchLaunchStack settle = %T, want *settle.ResearchSettle", s)
	}
}
