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

// TestGitCheckoutRoot_NoGitAnywhere verifies gitCheckoutRoot returns "" for a
// directory with no ".git" entry in it (called directly on the tempdir, not
// relying on any parent -- t.TempDir() lives outside any git checkout).
func TestGitCheckoutRoot_NoGitAnywhere(t *testing.T) {
	dir := t.TempDir()
	if got := gitCheckoutRoot(dir); got != "" {
		t.Errorf("gitCheckoutRoot(%q) = %q, want \"\"", dir, got)
	}
}

// TestGitCheckoutRoot_DirWithGitIsItself verifies gitCheckoutRoot returns dir
// itself when dir directly contains a ".git" entry.
func TestGitCheckoutRoot_DirWithGitIsItself(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitCheckoutRoot(dir); got != dir {
		t.Errorf("gitCheckoutRoot(%q) = %q, want %q", dir, got, dir)
	}
}

// TestGitCheckoutRoot_NestedSubdirWalksUpToRoot verifies gitCheckoutRoot
// walks up from a subdirectory nested under the checkout root to find the
// ".git" entry -- doctor run from a subdirectory of a checkout must still
// read the whole checkout.
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

// TestRegistryRouteDriftCheck_UnsetFileReturnsNil verifies
// registryRouteDriftCheck returns nil -- the slice-1 gate pattern -- when
// c.registryProxyRoutesFile is unset: drift is only meaningful alongside a
// routes file (issue #3144 slice 2).
func TestRegistryRouteDriftCheck_UnsetFileReturnsNil(t *testing.T) {
	c := minimalValidConfig()
	if got := registryRouteDriftCheck(c); got != nil {
		t.Errorf("registryRouteDriftCheck() = %#v, want nil when registryProxyRoutesFile is unset", got)
	}
}

// withDriftRepoDir points registryRouteDriftRepoDirFn at dir for the
// duration of the test, restoring the original (production os.Getwd) seam
// afterward.
func withDriftRepoDir(t *testing.T, dir string) {
	t.Helper()
	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })
}

// withDriftMatchingRemote stubs registryRouteDriftOriginRemoteFn to report an
// origin remote matching minimalValidConfig's identity (codeForge: "github",
// repoSlug: "owner/repo") for the duration of the test, restoring the
// original (production `git remote get-url origin`) seam afterward. Content
// tests that don't care about identity resolution itself use this to stand
// in for a real matching checkout without shelling out to git.
func withDriftMatchingRemote(t *testing.T) {
	t.Helper()
	orig := registryRouteDriftOriginRemoteFn
	registryRouteDriftOriginRemoteFn = func(string) string { return "git@github.com:owner/repo.git" }
	t.Cleanup(func() { registryRouteDriftOriginRemoteFn = orig })
}

// TestRegistryRouteDriftCheck_UncoveredHostFailsNamingHostAndRemedy verifies
// a repo declaring a host no configured route covers produces a failing
// (advisory) drift row naming that host, with a remedy naming `spindrift
// registry discover`.
func TestRegistryRouteDriftCheck_UncoveredHostFailsNamingHostAndRemedy(t *testing.T) {
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

	checks := registryRouteDriftCheck(c)
	if len(checks) != 1 {
		t.Fatalf("registryRouteDriftCheck() returned %d rows, want 1", len(checks))
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

// TestRegistryRouteDriftCheck_FullyCoveredRepoPasses verifies a repo whose
// every declared host is already covered by a route produces a passing
// drift row.
func TestRegistryRouteDriftCheck_FullyCoveredRepoPasses(t *testing.T) {
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

	checks := registryRouteDriftCheck(c)
	if len(checks) != 1 {
		t.Fatalf("registryRouteDriftCheck() returned %d rows, want 1", len(checks))
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

// TestRegistryRouteDriftCheck_NoCheckoutAvailable_ReturnsNil verifies the
// row is skipped entirely -- not a failing or passing row -- when no repo
// checkout is available (registryRouteDriftRepoDirFn errors), since drift
// has nothing to compare the routes file against.
func TestRegistryRouteDriftCheck_NoCheckoutAvailable_ReturnsNil(t *testing.T) {
	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_NO_CHECKOUT" }
`)

	if got := registryRouteDriftCheck(c); got != nil {
		t.Errorf("registryRouteDriftCheck() = %#v, want nil when no repo checkout is available", got)
	}
}

// TestRegistryRouteDriftCheck_EmptyRepoDirReturnsNil verifies the row is
// skipped -- not a failing or passing row -- when the seam reports no error
// but resolves no checkout at all (repoDir == ""), the gitCheckoutRoot "walk
// reached the filesystem root" case: no checkout available is not the same
// as an error resolving one, but both must suppress the row.
func TestRegistryRouteDriftCheck_EmptyRepoDirReturnsNil(t *testing.T) {
	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) { return "", nil }
	t.Cleanup(func() { registryRouteDriftRepoDirFn = orig })

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_EMPTY_REPO_DIR" }
`)

	if got := registryRouteDriftCheck(c); got != nil {
		t.Errorf("registryRouteDriftCheck() = %#v, want nil when no checkout is resolved", got)
	}
}

// TestRegistryRouteDriftCheck_UnreadableRoutesFileReturnsNil verifies the
// row is skipped when c.registryProxyRoutesFile points at a file that can't
// be read -- deferring to the existing registry-proxy-routes row rather than
// this check surfacing its own read error.
func TestRegistryRouteDriftCheck_UnreadableRoutesFileReturnsNil(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = filepath.Join(t.TempDir(), "does-not-exist.toml")

	if got := registryRouteDriftCheck(c); got != nil {
		t.Errorf("registryRouteDriftCheck() = %#v, want nil for an unreadable routes file", got)
	}
}

// TestRegistryRouteDriftCheck_UnparsableRoutesFileReturnsNil verifies the
// row is skipped when c.registryProxyRoutesFile contains invalid TOML --
// deferring to the existing registry-proxy-routes row rather than this
// check surfacing its own parse error.
func TestRegistryRouteDriftCheck_UnparsableRoutesFileReturnsNil(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `not valid toml [[[`)

	if got := registryRouteDriftCheck(c); got != nil {
		t.Errorf("registryRouteDriftCheck() = %#v, want nil for an unparsable routes file", got)
	}
}

// TestRegistryRouteDriftCheckFor_ExtractErrorDegradesProbe verifies
// registryRouteDriftCheckFor's Probe wraps a registrydiscover.Extract error
// (here: malformed .cargo/config.toml) with doctor.ErrDegraded rather than
// reporting it as an ordinary "no route covers it" finding -- an
// indeterminate probe is distinct from a genuine drift finding.
// registryRouteDriftCheckFor is called directly with a fixture repoDir, the
// use its doc comment promises.
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

// TestRegistryRouteDriftCheckFor_UncoveredHostRendersAdvisoryNotMissing
// verifies AC2's registry-route-drift counterpart to
// TestBwrapCapabilityChecks_CgroupDelegationRendersAdvisoryNotMissing
// (bwrap_doctor_checks_test.go): a genuine drift finding ("repo names X; no
// route covers it") renders through doctor.ReportResults as "advisory:",
// never "MISSING:" -- the row's Advisory Tier drives that framing, so its
// Probe error is a bare error, not a doctor.ErrDegraded wrap.
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
	routes, err := loadRegistryRoutes(routesFile)
	if err != nil {
		t.Fatal(err)
	}

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

// TestRegistryRouteDriftCheckFor_DifferingPathsOnCoveredHostIsNotDrift pins
// that drift is host coverage only: the repo's .cargo/config.toml declares
// two registries on one Artifactory host at different index paths, but the
// one configured route covers that host (declaring no path at all), so the
// Probe must succeed -- differing paths under a covered host are not a
// drift category (ADR 0047, issue #3262).
func TestRegistryRouteDriftCheckFor_DifferingPathsOnCoveredHostIsNotDrift(t *testing.T) {
	repoDir := t.TempDir()
	writeTwoRegistryCargoFixture(t, repoDir, "artifactory.example.com")

	routesFile := writeRoutesFile(t, `
[[routes]]
match-host = "artifactory.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_PATH_NOT_CATEGORY" }
`)
	routes, err := loadRegistryRoutes(routesFile)
	if err != nil {
		t.Fatal(err)
	}

	ch := registryRouteDriftCheckFor(repoDir, routes)
	if _, err := ch.Probe(); err != nil {
		t.Errorf("Probe() = %v, want no drift: the host is covered, so differing declared paths on it are not drift", err)
	}
}

// TestRegistryRouteDriftCheckFor_UncoveredHostFailureIsSilentAboutPath
// verifies the uncovered-host finding names only the host, never the path
// the repo declared it at -- the companion half of the "path drift is not a
// category" pin above: absence of a path in the finding text matters just
// as much as presence of the host.
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
	routes, err := loadRegistryRoutes(routesFile)
	if err != nil {
		t.Fatal(err)
	}

	ch := registryRouteDriftCheckFor(repoDir, routes)
	_, err = ch.Probe()
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

// TestRegistryRouteDriftCheck_NonTargetCheckoutReturnsNil verifies the drift
// row is absent when the enclosing checkout is a real git repo but its
// origin remote does NOT match the configured Target repo (config
// codeForge=github repoSlug="owner/repo"; checkout remote points at
// "other/elsewhere") -- the Consumer-flake-vs-Target-repo mismatch the
// review finding (issue #3144) flagged: without this identity gate the row
// would report the checkout's own drift as if it were the Target repo's,
// a false all-clear (or false failure) whenever the two roles differ.
func TestRegistryRouteDriftCheck_NonTargetCheckoutReturnsNil(t *testing.T) {
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

	if got := registryRouteDriftCheck(c); got != nil {
		t.Errorf("registryRouteDriftCheck() = %#v, want nil when the checkout's origin remote does not match the configured Target repo", got)
	}
}

// TestRegistryRouteDriftCheck_LocalForgeReadsFromAccumulationRepo verifies
// that under CODE_FORGE=local the drift row is derived from the
// Accumulation repo's baseBranch snapshot, never the cwd checkout:
// registryRouteDriftRepoDirFn is stubbed to t.Fatal (the
// TestBuildRegistryProxyRoutes_HostRooted_Local_DerivesFromAccumulationRepo
// pattern) and t.Chdir moves the process into an unrelated directory, so
// the test fails loudly if the local path ever falls back to the
// cwd-checkout branch. Covers both a drift and a no-drift outcome, proving
// the same row shape (Remedy, SuccessMsg) is shared with the cwd-checkout
// path.
func TestRegistryRouteDriftCheck_LocalForgeReadsFromAccumulationRepo(t *testing.T) {
	orig := registryRouteDriftRepoDirFn
	registryRouteDriftRepoDirFn = func() (string, error) {
		// t.Error, not t.Fatal: the subtests below call this stub on their
		// own goroutines, where FailNow on the parent t is documented
		// misuse. Returning an error still starves the cwd-checkout path of
		// a repo dir, so a fallback shows up as this failure, not a passing
		// row built from the wrong source.
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

		checks := registryRouteDriftCheck(c)
		if len(checks) != 1 {
			t.Fatalf("registryRouteDriftCheck() returned %d rows, want 1", len(checks))
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

		checks := registryRouteDriftCheck(c)
		if len(checks) != 1 {
			t.Fatalf("registryRouteDriftCheck() returned %d rows, want 1", len(checks))
		}
		if _, err := checks[0].Probe(); err != nil {
			t.Errorf("Probe() unexpected error for a fully covered Accumulation repo: %v", err)
		}
	})
}

// TestRegistryRouteDriftCheck_LocalForgeMissingAccumulationRepo_ReturnsNil
// covers AC2: a codeForgeAccumulationRepoDir that doesn't exist degrades to
// the same nil row (skipped) the cwd-checkout path produces when no
// checkout is available -- never a false "no drift".
func TestRegistryRouteDriftCheck_LocalForgeMissingAccumulationRepo_ReturnsNil(t *testing.T) {
	c := minimalValidLocalConfigForRoutes(filepath.Join(t.TempDir(), "does-not-exist.git"))
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_LOCAL_MISSING_ACCUM" }
`)

	if got := registryRouteDriftCheck(c); got != nil {
		t.Errorf("registryRouteDriftCheck() = %#v, want nil when the Accumulation repo does not exist", got)
	}
}

// TestRegistryRouteDriftCheck_LocalForgeUnresolvableRef_ReturnsNil covers
// AC2's other half: a real, reachable Accumulation repo whose baseBranch
// names a ref it does not have also degrades to a nil row, rather than a
// failing row surfacing a git error where a checkout-availability skip
// belongs.
func TestRegistryRouteDriftCheck_LocalForgeUnresolvableRef_ReturnsNil(t *testing.T) {
	accumRepo := mustLocalAccumulationRepo(t, "registry=https://uncovered.example.com/\n")
	c := minimalValidLocalConfigForRoutes(accumRepo)
	c.baseBranch = "does-not-exist"
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_LOCAL_BAD_REF" }
`)

	if got := registryRouteDriftCheck(c); got != nil {
		t.Errorf("registryRouteDriftCheck() = %#v, want nil when baseBranch names a ref the Accumulation repo does not have", got)
	}
}

// TestDoctorReportChecks_WiresRegistryRouteDriftCheck verifies
// doctorReportChecks appends registryRouteDriftCheck(c)'s row: present when
// c.registryProxyRoutesFile is set (with a checkout available), absent when
// it's unset.
func TestDoctorReportChecks_WiresRegistryRouteDriftCheck(t *testing.T) {
	withDriftRepoDir(t, t.TempDir())
	withDriftMatchingRemote(t)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DRIFT_WIRING" }
`)
	checkByName(t, doctorReportChecks(c), "registry-route-drift")

	c = minimalValidConfig()
	for _, ch := range doctorReportChecks(c) {
		if ch.Name == "registry-route-drift" {
			t.Errorf("doctorReportChecks output contains %q when registryProxyRoutesFile is unset", ch.Name)
		}
	}
}

// newGitCheckoutWithRemote git-inits a fresh checkout in t.TempDir() and, when
// remoteURL is non-empty, adds it as the "origin" remote -- the fixture shape
// checkoutIsTargetRepo's tests need to exercise the real `git remote get-url
// origin` seam rather than stubbing it.
func newGitCheckoutWithRemote(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()
	mustRunGit(t, dir, "init")
	if remoteURL != "" {
		mustRunGit(t, dir, "remote", "add", "origin", remoteURL)
	}
	return dir
}

// TestCheckoutIsTargetRepo_GithubMatch verifies a codeForge=github config
// matches when the checkout's origin remote is a github.com URL whose slug
// equals c.repoSlug, in both the scp-like and https remote forms.
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

// TestCheckoutIsTargetRepo_GithubMatch_CaseInsensitiveSlug verifies a
// codeForge=github config matches when the remote's slug differs from
// c.repoSlug only in case -- GitHub owner/repo slugs are case-insensitive.
func TestCheckoutIsTargetRepo_GithubMatch_CaseInsensitiveSlug(t *testing.T) {
	c := minimalValidConfig() // repoSlug: "owner/repo"
	dir := newGitCheckoutWithRemote(t, "git@github.com:Owner/Repo.git")
	if !checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = false, want true for a slug differing only in case", dir)
	}
}

// TestCheckoutIsTargetRepo_GithubMismatch verifies a codeForge=github config
// does not match a github.com remote naming a different slug.
func TestCheckoutIsTargetRepo_GithubMismatch(t *testing.T) {
	c := minimalValidConfig() // repoSlug: "owner/repo"
	dir := newGitCheckoutWithRemote(t, "git@github.com:other/elsewhere.git")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false for a remote naming a different repo", dir)
	}
}

// TestCheckoutIsTargetRepo_GitMatch verifies a codeForge=git config matches
// when the remote equals c.codeForgeRemoteURL, with or without a trailing
// ".git" on either side of the comparison.
func TestCheckoutIsTargetRepo_GitMatch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/team/proj.git"

	for _, remoteURL := range []string{
		"https://git.example.com/team/proj.git",
		"https://git.example.com/team/proj", // no trailing ".git" -- must still match
	} {
		t.Run(remoteURL, func(t *testing.T) {
			dir := newGitCheckoutWithRemote(t, remoteURL)
			if !checkoutIsTargetRepo(dir, c) {
				t.Errorf("checkoutIsTargetRepo(%q, c) = false, want true for matching remote %q", dir, remoteURL)
			}
		})
	}
}

// TestCheckoutIsTargetRepo_GitMismatch verifies a codeForge=git config does
// not match a remote different from c.codeForgeRemoteURL.
func TestCheckoutIsTargetRepo_GitMismatch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/team/proj.git"
	dir := newGitCheckoutWithRemote(t, "https://git.example.com/team/other.git")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false for a mismatched codeForge=git remote", dir)
	}
}

// TestCheckoutIsTargetRepo_GitMatch_DifferentRemoteForms verifies a
// codeForge=git config matches when the remote and c.codeForgeRemoteURL name
// the same repo but spell it in different URL forms (scp-like ssh vs
// ssh://) -- the raw-normalized compare fails here, so the match must come
// from the gitremote.ParseHostSlug fallback.
func TestCheckoutIsTargetRepo_GitMatch_DifferentRemoteForms(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "ssh://git@host/owner/repo.git"
	dir := newGitCheckoutWithRemote(t, "git@host:owner/repo.git")
	if !checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = false, want true for equivalent scp-like/ssh:// remote forms", dir)
	}
}

// TestCheckoutIsTargetRepo_GitLocalPathMismatch pins the non-empty-parse
// guard on the ParseHostSlug fallback (both sides parse to ("", "")): an
// empty-equals-empty match must not identify the checkout. codeForge=git
// with two DIFFERENT local-path remotes both parse to ("", "") via
// gitremote.ParseHostSlug, and without the guard an EqualFold("", "") &&
// EqualFold("", "") comparison would falsely match them.
func TestCheckoutIsTargetRepo_GitLocalPathMismatch(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "/srv/git/target.git"
	dir := newGitCheckoutWithRemote(t, "/srv/git/consumer.git")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false for two different local-path remotes that both parse to (\"\", \"\")", dir)
	}
}

// TestCheckoutIsTargetRepo_ForgejoMatch verifies a codeForge=forgejo config
// matches when the remote's host equals c.forgejoBaseURL's host and its slug
// equals c.repoSlug.
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

// TestCheckoutIsTargetRepo_ForgejoMatch_CaseInsensitive verifies a
// codeForge=forgejo config matches when the remote's host and slug differ
// from c.forgejoBaseURL's host and c.repoSlug only in case -- the forgejo
// branch's host and slug compares are both case-insensitive.
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

// TestCheckoutIsTargetRepo_ForgejoHostMismatch verifies a codeForge=forgejo
// config does not match a remote on a different host than c.forgejoBaseURL,
// even with a matching slug.
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

// TestCheckoutIsTargetRepo_NoOriginRemote verifies a git-init'd checkout with
// no origin remote at all never matches -- there is nothing to compare
// against.
func TestCheckoutIsTargetRepo_NoOriginRemote(t *testing.T) {
	c := minimalValidConfig()
	dir := newGitCheckoutWithRemote(t, "")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false with no origin remote", dir)
	}
}

// TestCheckoutIsTargetRepo_LocalCodeForgeNeverMatches verifies codeForge
// values with no remote-based Target identity (e.g. "local") never match,
// even when the checkout has an origin remote.
func TestCheckoutIsTargetRepo_LocalCodeForgeNeverMatches(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "local"
	dir := newGitCheckoutWithRemote(t, "git@github.com:owner/repo.git")
	if checkoutIsTargetRepo(dir, c) {
		t.Errorf("checkoutIsTargetRepo(%q, c) = true, want false for codeForge=local", dir)
	}
}
