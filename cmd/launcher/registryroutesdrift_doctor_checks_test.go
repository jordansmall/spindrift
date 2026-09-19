package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
)

// The check runs on the tempdir itself, not on a parent: t.TempDir() lives
// outside any git checkout, so no walk up can find a ".git" entry.
func TestGitCheckoutRoot_NoGitAnywhere(t *testing.T) {
	dir := t.TempDir()
	if got := gitCheckoutRoot(dir); got != "" {
		t.Errorf("gitCheckoutRoot(%q) = %q, want \"\"", dir, got)
	}
}

func TestGitCheckoutRoot_DirWithGitIsItself(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitCheckoutRoot(dir); got != dir {
		t.Errorf("gitCheckoutRoot(%q) = %q, want %q", dir, got, dir)
	}
}

// Doctor run from a subdirectory of a checkout must still read the whole
// checkout, so the lookup walks up to the ".git" entry.
func TestGitCheckoutRoot_NestedSubdirWalksUpToRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitCheckoutRoot(nested); got != root {
		t.Errorf("gitCheckoutRoot(%q) = %q, want %q", nested, got, root)
	}
}

// Drift is only meaningful alongside a routes file, so an unset
// registryProxyRoutesFile excludes the row from the report, the slice-1 gate
// pattern (issue #3144 slice 2). The drift seams are stubbed to the state
// that does yield the row, so the routes-file gate is the only thing left
// that can suppress it.
func TestDoctorCheckSets_UnsetRoutesFileExcludesRegistryRouteDriftRow(t *testing.T) {
	withDriftRepoDir(t, t.TempDir())
	withDriftMatchingRemote(t)

	c := minimalValidConfig()
	_, report := doctorCheckSets(c)
	assertNoDriftRow(t, report, "when registryProxyRoutesFile is unset")
}

// The production seam behind registryRouteDriftRepoDirFn is os.Getwd.
func withDriftRepoDir(t *testing.T, dir string) {
	t.Helper()
	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })
}

// The stubbed remote matches minimalValidConfig's identity (codeForge:
// "github", repoSlug: "owner/repo"), so content tests that do not care about
// identity resolution get a matching checkout without shelling out to git.
// The production seam is `git remote get-url origin`.
func withDriftMatchingRemote(t *testing.T) {
	t.Helper()
	orig := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = orig })
}

// assertNoDriftRow scans report for the registry-route-drift row and fails
// naming the condition that should have excluded it, the shared assertion
// behind the doctorCheckSets-level gating tests. condition completes "want it
// excluded ...", so each caller carries its own leading preposition.
func assertNoDriftRow(t *testing.T, report []doctor.Check, condition string) {
	t.Helper()
	for _, ch := range report {
		if ch.Name == registryRouteDriftCheckName {
			t.Errorf("doctorCheckSets(c) report contains %q, want it excluded %s", ch.Name, condition)
		}
	}
}

func TestRegistryRouteDriftCheckForRoutes_UncoveredHostFailsNamingHostAndRemedy(t *testing.T) {
	repoDir := t.TempDir()
	npmrc := "registry=https://uncovered.example.com/\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}
	withDriftRepoDir(t, repoDir)
	withDriftMatchingRemote(t)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_UNCOVERED" }
`)

	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)
	checks := registryRouteDriftCheckForRoutes(c, routes)
	if len(checks) != 1 {
		t.Fatalf("registryRouteDriftCheckForRoutes() returned %d rows, want 1", len(checks))
	}
	ch := checks[0]
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want an error naming the uncovered host")
	}
	if !strings.Contains(err.Error(), "uncovered.example.com") {
		t.Errorf("Probe() error %q must name the uncovered host", err.Error())
	}
	if !strings.Contains(ch.Remedy, "spindrift registry discover") {
		t.Errorf("Remedy %q must name `spindrift registry discover`", ch.Remedy)
	}
	if !strings.Contains(ch.Remedy, "--force") {
		t.Errorf("Remedy %q must name `--force`", ch.Remedy)
	}
	if !strings.Contains(ch.Remedy, "discarding hand edits") {
		t.Errorf("Remedy %q must warn that --force discards hand edits", ch.Remedy)
	}
}

func TestRegistryRouteDriftCheckForRoutes_FullyCoveredRepoPasses(t *testing.T) {
	repoDir := t.TempDir()
	npmrc := "registry=https://covered.example.com/\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}
	withDriftRepoDir(t, repoDir)
	withDriftMatchingRemote(t)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "covered.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_COVERED" }
`)

	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)
	checks := registryRouteDriftCheckForRoutes(c, routes)
	if len(checks) != 1 {
		t.Fatalf("registryRouteDriftCheckForRoutes() returned %d rows, want 1", len(checks))
	}
	if _, err := checks[0].Probe(); err != nil {
		t.Errorf("Probe() unexpected error for a fully covered repo: %v", err)
	}

	results := doctor.RunChecks(checks)
	var buf bytes.Buffer
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "ok: registry-route-drift (no drift)") {
		t.Errorf("want %q in ReportResults output, got:\n%s", "ok: registry-route-drift (no drift)", out)
	}
}

// Pins issue #3405: a route in ADR 0048 block grammar still parses through
// registryroutes.Parse. The len(checks) == 1 assertion plus a passing Probe
// together prove the migrated grammar reached the drift check rather than
// degrading down the parse-error path, which returns zero rows.
func TestRegistryRouteDriftCheckForRoutes_BlockGrammarRouteParsesAndReportsNoDrift(t *testing.T) {
	repoDir := t.TempDir()
	npmrc := "registry=https://covered.example.com/\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}
	withDriftRepoDir(t, repoDir)
	withDriftMatchingRemote(t)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "covered.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_BLOCK_GRAMMAR" }

[routes.ecosystems.go]
path = "/go-modules"

[routes.ecosystems.cargo]
registries = ["internal"]
`)

	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)
	checks := registryRouteDriftCheckForRoutes(c, routes)
	if len(checks) != 1 {
		t.Fatalf("registryRouteDriftCheckForRoutes() returned %d rows, want 1", len(checks))
	}
	if _, err := checks[0].Probe(); err != nil {
		t.Errorf("Probe() unexpected error for a fully covered repo with a block-grammar route: %v", err)
	}

	results := doctor.RunChecks(checks)
	var buf bytes.Buffer
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "ok: registry-route-drift (no drift)") {
		t.Errorf("want %q in ReportResults output, got:\n%s", "ok: registry-route-drift (no drift)", out)
	}
}

// With no checkout available, drift has nothing to compare the routes file
// against, so the row is skipped entirely rather than failing or passing.
func TestRegistryRouteDriftCheckForRoutes_NoCheckoutAvailable_ReturnsNil(t *testing.T) {
	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_NO_CHECKOUT" }
`)
	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)

	if got := registryRouteDriftCheckForRoutes(c, routes); got != nil {
		t.Errorf("registryRouteDriftCheckForRoutes() = %#v, want nil when no repo checkout is available", got)
	}
}

// The seam reports no error but resolves no checkout (repoDir == ""), the
// gitCheckoutRoot case where the walk reached the filesystem root. No
// checkout available is not the same as an error resolving one, but both
// must suppress the row.
func TestRegistryRouteDriftCheckForRoutes_EmptyRepoDirReturnsNil(t *testing.T) {
	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return "", nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_EMPTY_REPO_DIR" }
`)
	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)

	if got := registryRouteDriftCheckForRoutes(c, routes); got != nil {
		t.Errorf("registryRouteDriftCheckForRoutes() = %#v, want nil when no checkout is resolved", got)
	}
}

// doctorCheckSets defers to the existing registry-proxy-routes row
// (checks.go), which already reports this same read failure, rather than
// reporting a duplicate error over the identical cause. The drift seams are
// stubbed to the state that does yield the row, so only the read failure can
// suppress it.
func TestDoctorCheckSets_UnreadableRoutesFileExcludesRegistryRouteDriftRow(t *testing.T) {
	withDriftRepoDir(t, t.TempDir())
	withDriftMatchingRemote(t)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = filepath.Join(t.TempDir(), "does-not-exist.toml")

	_, report := doctorCheckSets(c)
	assertNoDriftRow(t, report, "for an unreadable routes file")
}

// The parse-failure counterpart of the unreadable case above: the aggregate
// registry-proxy-routes row already reports it, so the drift row stays out.
// The drift seams are stubbed to the state that does yield the row, so only
// the parse failure can suppress it.
func TestDoctorCheckSets_UnparsableRoutesFileExcludesRegistryRouteDriftRow(t *testing.T) {
	withDriftRepoDir(t, t.TempDir())
	withDriftMatchingRemote(t)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `not valid toml [[[`)

	_, report := doctorCheckSets(c)
	assertNoDriftRow(t, report, "for an unparsable routes file")
}

// An indeterminate probe is distinct from a genuine drift finding, so a
// registrydiscover.Extract error wraps doctor.ErrDegraded instead of reading
// as an ordinary "no route covers it" result.
func TestRegistryRouteDriftCheckFor_ExtractErrorDegradesProbe(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	malformed := "not valid toml [[["
	if err := os.WriteFile(filepath.Join(repoDir, ".cargo", "config.toml"), []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}

	ch := registryRouteDriftCheckFor(repoDir, nil)
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want an error for malformed .cargo/config.toml")
	}
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() error %v must wrap doctor.ErrDegraded", err)
	}
	if strings.Contains(err.Error(), "no route covers it") {
		t.Errorf("Probe() error %q must not read as an ordinary drift finding", err.Error())
	}
}

// AC2's counterpart to
// TestBwrapCapabilityChecks_CgroupDelegationRendersAdvisoryNotMissing in
// bwrap_doctor_checks_test.go. The row's Advisory Tier drives the
// "advisory:" framing, so a genuine drift finding carries a bare Probe
// error, not a doctor.ErrDegraded wrap.
func TestRegistryRouteDriftCheckFor_UncoveredHostRendersAdvisoryNotMissing(t *testing.T) {
	repoDir := t.TempDir()
	npmrc := "registry=https://uncovered.example.com/\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	routesFile := writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_ADVISORY_RENDER" }
`)
	routes := mustLoadRoutes(t, routesFile)

	ch := registryRouteDriftCheckFor(repoDir, routes)
	_, probeErr := ch.Probe()
	if probeErr == nil {
		t.Fatal("Probe() succeeded, want an error for the uncovered host")
	}
	if errors.Is(probeErr, doctor.ErrDegraded) {
		t.Errorf("Probe() error %v must not wrap doctor.ErrDegraded for a genuine drift finding", probeErr)
	}

	results := doctor.RunChecks([]doctor.Check{ch})
	var buf bytes.Buffer
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "advisory: registry-route-drift") {
		t.Errorf("want advisory: framing for a failing registry-route-drift row, got:\n%s", out)
	}
	if strings.Contains(out, "MISSING: registry-route-drift") {
		t.Errorf("want no MISSING: framing for registry-route-drift, got:\n%s", out)
	}
}

// Pins that drift is host coverage only. The fixture declares two registries
// on one Artifactory host at different index paths, and the single route
// covers that host while declaring no path, so differing paths under a
// covered host are not a drift category (ADR 0047, issue #3262).
func TestRegistryRouteDriftCheckFor_DifferingPathsOnCoveredHostIsNotDrift(t *testing.T) {
	repoDir := t.TempDir()
	writeTwoRegistryCargoFixture(t, repoDir, "artifactory.example.com")

	routesFile := writeRoutesFile(t, `
[[routes]]
match-host = "artifactory.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_PATH_NOT_CATEGORY" }
`)
	routes := mustLoadRoutes(t, routesFile)

	ch := registryRouteDriftCheckFor(repoDir, routes)
	if _, err := ch.Probe(); err != nil {
		t.Errorf("Probe() = %v, want no drift: the host is covered, so differing declared paths on it are not drift", err)
	}
}

// The companion half of the "path drift is not a category" pin above:
// absence of a path in the finding text matters as much as presence of the
// host.
func TestRegistryRouteDriftCheckFor_UncoveredHostFailureIsSilentAboutPath(t *testing.T) {
	repoDir := t.TempDir()
	npmrc := "registry=https://uncovered.example.com/some/path/\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	routesFile := writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_UNCOVERED_PATH_SILENT" }
`)
	routes := mustLoadRoutes(t, routesFile)

	ch := registryRouteDriftCheckFor(repoDir, routes)
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want an error naming the uncovered host")
	}
	if !strings.Contains(err.Error(), "uncovered.example.com") {
		t.Errorf("Probe() error %q must name the uncovered host", err.Error())
	}
	if strings.Contains(err.Error(), "/some/path/") {
		t.Errorf("Probe() error %q must not name the declared path", err.Error())
	}
}

// The Consumer-flake-versus-Target-repo mismatch issue #3144 flagged.
// Without this identity gate the row reports the enclosing checkout's own
// drift as if it were the Target repo's, a false all-clear or false failure
// whenever the two roles differ.
func TestRegistryRouteDriftCheckForRoutes_NonTargetCheckoutReturnsNil(t *testing.T) {
	repoDir := t.TempDir()
	mustRunGit(t, repoDir, "init")
	mustRunGit(t, repoDir, "remote", "add", "origin", "git@github.com:other/elsewhere.git")
	npmrc := "registry=https://uncovered.example.com/\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}
	withDriftRepoDir(t, repoDir)

	c := minimalValidConfig() // codeForge: "github", repoSlug: "owner/repo"
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_NON_TARGET" }
`)
	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)

	if got := registryRouteDriftCheckForRoutes(c, routes); got != nil {
		t.Errorf("registryRouteDriftCheckForRoutes() = %#v, want nil when the checkout's origin remote does not match the configured Target repo", got)
	}
}

// Under CODE_FORGE=local the drift row must come from the Accumulation
// repo's baseBranch snapshot, never the cwd checkout. The failing stub and
// the t.Chdir into an unrelated directory both exist so a fallback to the
// cwd-checkout branch fails loudly instead of passing from the wrong source.
// Follows TestBuildRegistryProxyRoutes_HostRooted_Local_DerivesFromAccumulationRepo.
func TestRegistryRouteDriftCheckForRoutes_LocalForgeReadsFromAccumulationRepo(t *testing.T) {
	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) {
		// t.Error, not t.Fatal: the subtests below call this stub on their own
		// goroutines, where FailNow on the parent t is documented misuse.
		// Returning an error still starves the cwd-checkout path of a repo dir.
		t.Error("registryRouteDriftRepoDirFn called under CODE_FORGE=local; the local path must derive from the Accumulation repo, never a cwd checkout")
		return "", errors.New("registryRouteDriftRepoDirFn must not be called under CODE_FORGE=local")
	}
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })
	t.Chdir(t.TempDir())

	t.Run("drift", func(t *testing.T) {
		accumRepo := mustLocalAccumulationRepo(t, "registry=https://uncovered.example.com/\n")
		c := minimalValidLocalConfigForRoutes(accumRepo)
		c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_LOCAL_UNCOVERED" }
`)
		routes := mustLoadRoutes(t, c.registryProxyRoutesFile)

		checks := registryRouteDriftCheckForRoutes(c, routes)
		if len(checks) != 1 {
			t.Fatalf("registryRouteDriftCheckForRoutes() returned %d rows, want 1", len(checks))
		}
		_, err := checks[0].Probe()
		if err == nil {
			t.Fatal("Probe() succeeded, want an error naming the uncovered host")
		}
		if !strings.Contains(err.Error(), "uncovered.example.com") {
			t.Errorf("Probe() error %q must name the uncovered host", err.Error())
		}
	})

	t.Run("no drift", func(t *testing.T) {
		accumRepo := mustLocalAccumulationRepo(t, "registry=https://covered.example.com/\n")
		c := minimalValidLocalConfigForRoutes(accumRepo)
		c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "covered.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_LOCAL_COVERED" }
`)
		routes := mustLoadRoutes(t, c.registryProxyRoutesFile)

		checks := registryRouteDriftCheckForRoutes(c, routes)
		if len(checks) != 1 {
			t.Fatalf("registryRouteDriftCheckForRoutes() returned %d rows, want 1", len(checks))
		}
		if _, err := checks[0].Probe(); err != nil {
			t.Errorf("Probe() unexpected error for a fully covered Accumulation repo: %v", err)
		}
	})
}

// AC2: a codeForgeAccumulationRepoDir that does not exist degrades to the
// same skipped row the cwd-checkout path produces with no checkout
// available, never a false "no drift".
func TestRegistryRouteDriftCheckForRoutes_LocalForgeMissingAccumulationRepo_ReturnsNil(t *testing.T) {
	c := minimalValidLocalConfigForRoutes(filepath.Join(t.TempDir(), "does-not-exist.git"))
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_LOCAL_MISSING_ACCUM" }
`)
	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)

	if got := registryRouteDriftCheckForRoutes(c, routes); got != nil {
		t.Errorf("registryRouteDriftCheckForRoutes() = %#v, want nil when the Accumulation repo does not exist", got)
	}
}

// AC2's other half: a reachable Accumulation repo whose baseBranch names a
// ref it does not have also skips the row, rather than failing with a git
// error where a checkout-availability skip belongs.
func TestRegistryRouteDriftCheckForRoutes_LocalForgeUnresolvableRef_ReturnsNil(t *testing.T) {
	accumRepo := mustLocalAccumulationRepo(t, "registry=https://uncovered.example.com/\n")
	c := minimalValidLocalConfigForRoutes(accumRepo)
	c.baseBranch = "does-not-exist"
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_LOCAL_BAD_REF" }
`)
	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)

	if got := registryRouteDriftCheckForRoutes(c, routes); got != nil {
		t.Errorf("registryRouteDriftCheckForRoutes() = %#v, want nil when baseBranch names a ref the Accumulation repo does not have", got)
	}
}

func TestDoctorCheckSets_WiresRegistryRouteDriftRow(t *testing.T) {
	withDriftRepoDir(t, t.TempDir())
	withDriftMatchingRemote(t)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_WIRING" }
`)
	_, checks := doctorCheckSets(c)
	checkByName(t, checks, registryRouteDriftCheckName)
}

// A real checkout, not a stub, so checkoutIsTargetRepo's tests exercise the
// actual `git remote get-url origin` seam.
func newGitCheckoutWithRemote(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()
	mustRunGit(t, dir, "init")
	if remoteURL != "" {
		mustRunGit(t, dir, "remote", "add", "origin", remoteURL)
	}
	return dir
}

func TestCheckoutIsTargetRepo_GithubMatch(t *testing.T) {
	c := minimalValidConfig() // codeForge: "github", repoSlug: "owner/repo"

	for _, remoteURL := range []string{
		"git@github.com:owner/repo.git",
		"https://github.com/owner/repo.git",
	} {
		t.Run(remoteURL, func(t *testing.T) {
			dir := newGitCheckoutWithRemote(t, remoteURL)
			if !checkoutIsTargetRepo(dir, c) {
				t.Errorf("checkoutIsTargetRepo(%q, c) = false, want true for matching remote %q", dir, remoteURL)
			}
		})
	}
}

// GitHub owner/repo slugs are case-insensitive.
func TestCheckoutIsTargetRepo_GithubMatch_CaseInsensitiveSlug(t *testing.T) {
	c := minimalValidConfig() // repoSlug: "owner/repo"
	dir := newGitCheckoutWithRemote(t, "git@github.com:Owner/Repo.git")
	if !checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = false, want true for a slug differing only in case", dir)
	}
}

func TestCheckoutIsTargetRepo_GithubMismatch(t *testing.T) {
	c := minimalValidConfig() // repoSlug: "owner/repo"
	dir := newGitCheckoutWithRemote(t, "git@github.com:other/elsewhere.git")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false for a remote naming a different repo", dir)
	}
}

func TestCheckoutIsTargetRepo_GitMatch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/team/proj.git"

	for _, remoteURL := range []string{
		"https://git.example.com/team/proj.git",
		"https://git.example.com/team/proj", // no trailing ".git", must still match
	} {
		t.Run(remoteURL, func(t *testing.T) {
			dir := newGitCheckoutWithRemote(t, remoteURL)
			if !checkoutIsTargetRepo(dir, c) {
				t.Errorf("checkoutIsTargetRepo(%q, c) = false, want true for matching remote %q", dir, remoteURL)
			}
		})
	}
}

func TestCheckoutIsTargetRepo_GitMismatch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/team/proj.git"
	dir := newGitCheckoutWithRemote(t, "https://git.example.com/team/other.git")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false for a mismatched codeForge=git remote", dir)
	}
}

// The two remotes name the same repo in different URL forms (scp-like ssh
// versus ssh://), so the raw-normalized compare fails and the match must
// come from the gitremote.ParseHostSlug fallback.
func TestCheckoutIsTargetRepo_GitMatch_DifferentRemoteForms(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "ssh://git@host/owner/repo.git"
	dir := newGitCheckoutWithRemote(t, "git@host:owner/repo.git")
	if !checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = false, want true for equivalent scp-like/ssh:// remote forms", dir)
	}
}

// Pins the non-empty-parse guard on the ParseHostSlug fallback. Two
// different local-path remotes both parse to ("", ""), and without the guard
// an EqualFold("", "") && EqualFold("", "") comparison falsely matches them.
func TestCheckoutIsTargetRepo_GitLocalPathMismatch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "/srv/git/target.git"
	dir := newGitCheckoutWithRemote(t, "/srv/git/consumer.git")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false for two different local-path remotes that both parse to (\"\", \"\")", dir)
	}
}

func TestCheckoutIsTargetRepo_ForgejoMatch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.repoSlug = "owner/repo"
	dir := newGitCheckoutWithRemote(t, "git@codeberg.org:owner/repo.git")
	if !checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = false, want true for a matching forgejo host+slug", dir)
	}
}

// The forgejo branch compares both host and slug case-insensitively.
func TestCheckoutIsTargetRepo_ForgejoMatch_CaseInsensitive(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.repoSlug = "owner/repo"
	dir := newGitCheckoutWithRemote(t, "git@CODEBERG.ORG:Owner/Repo.git")
	if !checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = false, want true for a forgejo host+slug differing only in case", dir)
	}
}

// The slug matches here, so only the host difference can reject.
func TestCheckoutIsTargetRepo_ForgejoHostMismatch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.repoSlug = "owner/repo"
	dir := newGitCheckoutWithRemote(t, "git@git.example.com:owner/repo.git")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false for a remote on a different forgejo host", dir)
	}
}

func TestCheckoutIsTargetRepo_NoOriginRemote(t *testing.T) {
	c := minimalValidConfig()
	dir := newGitCheckoutWithRemote(t, "")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false with no origin remote", dir)
	}
}

// A codeForge with no remote-based Target identity never matches, even when
// the checkout does have an origin remote.
func TestCheckoutIsTargetRepo_LocalCodeForgeNeverMatches(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	dir := newGitCheckoutWithRemote(t, "git@github.com:owner/repo.git")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false for codeForge=local", dir)
	}
}
