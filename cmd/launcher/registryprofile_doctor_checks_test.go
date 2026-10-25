package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/hostprofile"
	"spindrift.dev/launcher/internal/registrypathset"
)

// siblingProfile stands in for a shipped row with Siblings, which the real
// table does not carry yet.
var siblingProfile = hostprofile.Row{
	Name:       "fakehost",
	AuthScheme: "bearer",
	Siblings:   map[string][]string{"synthetic": {"../../sibling"}},
}

func matchFakeProfile(declaredURL string, _ http.Header) (hostprofile.Row, bool) {
	return siblingProfile, strings.Contains(declaredURL, "profiled.example.com")
}

// profileRow runs the production wiring against a synthetic Target repo
// declaring decls, with the routes file doc.
func profileRow(t *testing.T, decls, routesDoc string) doctor.Check {
	t.Helper()
	repoDir := t.TempDir()
	writeSyntheticRegistries(t, repoDir, decls)
	withDriftRepoDir(t, repoDir)
	withDriftMatchingRemote(t)
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, routesDoc)
	routes := mustLoadRoutes(t, c.registryProxyRoutesFile, driftRows)
	checks := registryRouteProfileCheckForRoutes(c, routes, driftRows)
	if len(checks) != 1 {
		t.Fatalf("registryRouteProfileCheckForRoutes() returned %d rows, want 1", len(checks))
	}
	return checks[0]
}

// fakeProfileRow is profileRow with matchFakeProfile in place of the real table.
func fakeProfileRow(t *testing.T, decls, routesDoc string) doctor.Check {
	t.Helper()
	repoDir := t.TempDir()
	writeSyntheticRegistries(t, repoDir, decls)
	routesFile := writeRoutesFile(t, routesDoc)
	routes := mustLoadRoutes(t, routesFile, driftRows)
	return registryRouteProfileCheckFor(targetRepoSource{dir: repoDir}, routes, driftRows, matchFakeProfile)
}

const profiledDecl = "reg = https://profiled.example.com/repo/index\n"

func TestRegistryRouteProfileCheck_MatchingRouteNoFinding(t *testing.T) {
	ch := profileRow(t, "reg = https://acme.jfrog.io/artifactory/api/cargo/x/index\n", `
[[routes]]
match-host = "acme.jfrog.io"
auth-scheme = "bearer"
credential = { env = "SPINDRIFT_TEST_PROFILE_MATCH" }
`)
	out, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() = %v, want no finding for a route matching its profile", err)
	}
	if got := ch.SuccessMsg(out); !strings.Contains(got, registryRouteProfileCheckName) {
		t.Errorf("SuccessMsg = %q, want it to name the row", got)
	}
}

func TestRegistryRouteProfileCheck_SchemeMismatchWarns(t *testing.T) {
	ch := profileRow(t, "reg = https://acme.jfrog.io/artifactory/api/cargo/x/index\n", `
[[routes]]
match-host = "acme.jfrog.io"
auth-scheme = "basic"
credential = { env = "SPINDRIFT_TEST_PROFILE_SCHEME" }
`)
	if ch.Tier != doctor.Advisory {
		t.Errorf("Tier = %v, want Advisory", ch.Tier)
	}
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want a scheme-departure warning")
	}
	for _, want := range []string{"acme.jfrog.io", "artifactory", "basic", "bearer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Probe() error %q must name %q", err.Error(), want)
		}
	}
	if errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("a finding is not a degraded probe: %v", err)
	}
	if got := doctor.FirstRequiredError(doctor.RunChecks([]doctor.Check{ch})); got != nil {
		t.Errorf("FirstRequiredError = %v, want nil: a profile departure never fails doctor", got)
	}
}

// A profile with no scheme (a public host) has none to depart from.
func TestRegistryRouteProfileCheck_PublicProfileHasNoSchemeToDepartFrom(t *testing.T) {
	ch := profileRow(t, "reg = https://crates.io/index\n", `
[[routes]]
match-host = "crates.io"
auth-scheme = "basic"
credential = { env = "SPINDRIFT_TEST_PROFILE_PUBLIC" }
`)
	if _, err := ch.Probe(); err != nil {
		t.Fatalf("Probe() = %v, want no finding against a scheme-less profile", err)
	}
}

func TestRegistryRouteProfileCheck_UnprofiledHostNoFinding(t *testing.T) {
	ch := profileRow(t, "reg = https://registry.example.com/repo/index\n", `
[[routes]]
match-host = "registry.example.com"
auth-scheme = "basic"
credential = { env = "SPINDRIFT_TEST_PROFILE_UNPROFILED" }
`)
	if _, err := ch.Probe(); err != nil {
		t.Fatalf("Probe() = %v, want no finding for an unprofiled host", err)
	}
}

func TestRegistryRouteProfileCheck_MissingSiblingPathWarns(t *testing.T) {
	ch := fakeProfileRow(t, profiledDecl, `
[[routes]]
match-host = "profiled.example.com"
credential = { env = "SPINDRIFT_TEST_PROFILE_SIBLING" }
`)
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want a missing-sibling warning")
	}
	for _, want := range []string{"profiled.example.com", "fakehost", "synthetic", "/sibling", "allow"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Probe() error %q must name %q", err.Error(), want)
		}
	}
}

func TestRegistryRouteProfileCheck_SiblingAdmittedViaAllowNoFinding(t *testing.T) {
	ch := fakeProfileRow(t, profiledDecl, `
[[routes]]
match-host = "profiled.example.com"
allow = ["/sibling"]
credential = { env = "SPINDRIFT_TEST_PROFILE_SIBLING_ALLOWED" }
`)
	if _, err := ch.Probe(); err != nil {
		t.Fatalf("Probe() = %v, want no finding once allow admits the sibling path", err)
	}
}

// A sibling for an ecosystem the route does not serve is not owed.
func TestRegistryRouteProfileCheck_SiblingOfUnservedEcosystemNoFinding(t *testing.T) {
	other := func(u string, h http.Header) (hostprofile.Row, bool) {
		row, ok := matchFakeProfile(u, h)
		row.Siblings = map[string][]string{"unserved": {"../../sibling"}}
		return row, ok
	}
	repoDir := t.TempDir()
	writeSyntheticRegistries(t, repoDir, profiledDecl)
	routes := mustLoadRoutes(t, writeRoutesFile(t, `
[[routes]]
match-host = "profiled.example.com"
credential = { env = "SPINDRIFT_TEST_PROFILE_UNSERVED" }
`), driftRows)
	if _, err := registryRouteProfileCheckFor(targetRepoSource{dir: repoDir}, routes, driftRows, other).Probe(); err != nil {
		t.Fatalf("Probe() = %v, want no finding for an unserved ecosystem's sibling", err)
	}
}

// The declared-URL-path signal must match on its own: the host fallback sees
// only "https://"+MatchHost, which carries no registry path, so a regression
// that drops the EnforcedSubtrees loop leaves this host unprofiled.
func TestRegistryRouteProfileCheck_DeclaredPathMatchesWithoutHostSignal(t *testing.T) {
	pathOnly := func(u string, _ http.Header) (hostprofile.Row, bool) {
		return siblingProfile, strings.Contains(u, "/artifactory/api/")
	}
	repoDir := t.TempDir()
	writeSyntheticRegistries(t, repoDir, "reg = https://plain.example.com/artifactory/api/cargo/x/index\n")
	routes := mustLoadRoutes(t, writeRoutesFile(t, `
[[routes]]
match-host = "plain.example.com"
auth-scheme = "basic"
credential = { env = "SPINDRIFT_TEST_PROFILE_PATH_ONLY" }
`), driftRows)
	_, err := registryRouteProfileCheckFor(targetRepoSource{dir: repoDir}, routes, driftRows, pathOnly).Probe()
	if err == nil || !strings.Contains(err.Error(), "fakehost") {
		t.Fatalf("Probe() = %v, want a scheme warning from the path-matched profile", err)
	}
}

// A host the Target repo never declares still gets the scheme rule, but its
// failed resolution must not surface as a probe error.
func TestRegistryRouteProfileCheck_UndeclaredHostStillChecksScheme(t *testing.T) {
	ch := profileRow(t, "reg = https://other.example.com/repo/index\n", `
[[routes]]
match-host = "acme.jfrog.io"
auth-scheme = "basic"
credential = { env = "SPINDRIFT_TEST_PROFILE_UNDECLARED" }
`)
	_, err := ch.Probe()
	if err == nil || !strings.Contains(err.Error(), "basic") {
		t.Fatalf("Probe() = %v, want a scheme warning", err)
	}
}

func TestRegistryRouteProfileCheck_DeriveFailureIsDegraded(t *testing.T) {
	routes := mustLoadRoutes(t, writeRoutesFile(t, `
[[routes]]
match-host = "profiled.example.com"
credential = { env = "SPINDRIFT_TEST_PROFILE_DEGRADED" }
`), driftRows)
	ch := registryRouteProfileRow(routes, driftRows, matchFakeProfile, func() ([]registrypathset.HostPathSet, error) {
		return nil, errors.New("boom")
	})
	_, err := ch.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Fatalf("Probe() = %v, want ErrDegraded", err)
	}
}

func TestRegistryRouteProfileCheckForRoutes_NoTargetRepoReturnsNil(t *testing.T) {
	withDriftRepoDir(t, "")
	c := minimalValidConfig()
	if got := registryRouteProfileCheckForRoutes(c, nil, driftRows); got != nil {
		t.Errorf("registryRouteProfileCheckForRoutes() = %#v, want nil without a Target repo", got)
	}
}

// The row is report-only: classify decides doctor's exit status, so an
// advisory finding can never change it.
func TestDoctorCheckSets_WiresRegistryRouteProfileRowIntoReportOnly(t *testing.T) {
	withDriftRepoDir(t, t.TempDir())
	withDriftMatchingRemote(t)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_PROFILE_WIRING" }
`)
	classify, report := doctorCheckSets(c)
	ch := checkByName(t, report, registryRouteProfileCheckName)
	if ch.Tier != doctor.Advisory {
		t.Errorf("Tier = %v, want Advisory", ch.Tier)
	}
	for _, cc := range classify {
		if cc.Name == registryRouteProfileCheckName {
			t.Errorf("classify contains %q, want it report-only", cc.Name)
		}
	}
}

func TestDoctorCheckSets_UnsetRoutesFileExcludesRegistryRouteProfileRow(t *testing.T) {
	withDriftRepoDir(t, t.TempDir())
	withDriftMatchingRemote(t)

	_, report := doctorCheckSets(minimalValidConfig())
	for _, ch := range report {
		if ch.Name == registryRouteProfileCheckName {
			t.Errorf("report contains %q with no routes file", ch.Name)
		}
	}
}
