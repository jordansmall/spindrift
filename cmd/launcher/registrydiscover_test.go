package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/registryroutes"
)

// writeCargoFixture writes a minimal .cargo/config.toml declaring one
// registry, the shape the cargo row's ConfigParser reads.
func writeCargoFixture(t *testing.T, dir, registryName, indexURL string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "[registries." + registryName + "]\nindex = \"" + indexURL + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// writeTwoRegistryCargoFixture writes a .cargo/config.toml declaring two
// [registries.NAME] entries on the same host with Artifactory-shaped index
// paths (ADR 0047 / spec #3253's field shape: one Artifactory host fronting
// both an internal repo and a proxied crates.io remote).
func writeTwoRegistryCargoFixture(t *testing.T, dir, host string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "[registries.internal]\n" +
		"index = \"https://" + host + "/artifactory/api/cargo/internal-crates/index\"\n" +
		"[registries.crates-io-remote]\n" +
		"index = \"https://" + host + "/artifactory/api/cargo/crates-io-remote/index\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestRunRegistryDiscover_ArtifactoryFieldShape_OneRouteNoPathKeys pins the
// 2026-09-04 field shape (ADR 0047 / spec #3253): one Artifactory host
// fronting two cargo registries collapses to a single route, and the
// written file carries only match-host/auth-scheme/credential -- no
// upstream-origin (Artifactory's plain-https default-port URL derives it)
// and none of the declared index paths. A second, unmatched host in the
// same run exercises the env-placeholder arm alongside it.
func TestRunRegistryDiscover_ArtifactoryFieldShape_OneRouteNoPathKeys(t *testing.T) {
	repoDir := t.TempDir()
	writeTwoRegistryCargoFixture(t, repoDir, "artifactory.example.com")
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte("registry=https://npm.other.example.com/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .npmrc: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "routes.toml")

	stores := []registrydiscover.Store{{Name: "netrc", Path: "/fake/.netrc"}}
	lookup := func(store registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
		return store.Name == "netrc" && d.Host == "artifactory.example.com", nil
	}
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, lookup, probe)
	if code != 0 {
		t.Fatalf("runRegistryDiscover code = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr.String())
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	for _, absent := range []string{"upstream-origin", "/artifactory/", "/index", "internal-crates", "crates-io-remote"} {
		if strings.Contains(string(data), absent) {
			t.Errorf("routes file = %s, must not contain %q", data, absent)
		}
	}

	routes, err := registryroutes.Parse(data)
	if err != nil {
		t.Fatalf("registryroutes.Parse: %v; data=%s", err, data)
	}
	if len(routes) != 2 {
		t.Fatalf("len(routes) = %d, want 2 (one per host, not one per registry)", len(routes))
	}

	byHost := make(map[string]registryroutes.Route, len(routes))
	for _, r := range routes {
		byHost[r.MatchHost] = r
	}

	art, ok := byHost["artifactory.example.com"]
	if !ok {
		t.Fatalf("routes = %+v, want a route for artifactory.example.com", routes)
	}
	if art.AuthScheme != "bearer" {
		t.Errorf("artifactory route AuthScheme = %q, want bearer", art.AuthScheme)
	}
	if art.Credential.FromFile != "/fake/.netrc" || art.Credential.FileFormat != "netrc" {
		t.Errorf("artifactory route Credential = %+v, want FromFile=/fake/.netrc FileFormat=netrc", art.Credential)
	}
	if art.UpstreamOrigin != "" {
		t.Errorf("artifactory route UpstreamOrigin = %q, want empty", art.UpstreamOrigin)
	}

	other, ok := byHost["npm.other.example.com"]
	if !ok {
		t.Fatalf("routes = %+v, want a route for npm.other.example.com", routes)
	}
	if other.Credential.FromEnv != "SPINDRIFT_REGISTRY_CREDENTIAL_NPM_OTHER_EXAMPLE_COM" {
		t.Errorf("unmatched route Credential = %+v, want the env placeholder", other.Credential)
	}
	if !strings.Contains(stdout.String(), "set these environment variables") {
		t.Errorf("stdout = %q, want the env-placeholder hint for the unmatched host", stdout.String())
	}
}

// TestRunRegistryDiscover_CargoFixtureEndToEnd_WritesParseableRoutesFile drives
// a cargo .cargo/config.toml fixture through the full runRegistryDiscover
// pipeline and checks the written file round-trips through
// registryroutes.Parse into the expected route and report/stdout lines.
func TestRunRegistryDiscover_CargoFixtureEndToEnd_WritesParseableRoutesFile(t *testing.T) {
	repoDir := t.TempDir()
	writeCargoFixture(t, repoDir, "foo", "https://cargo.example.com/index")

	outPath := filepath.Join(t.TempDir(), "routes.toml")

	stores := []registrydiscover.Store{{Name: "netrc", Path: "/fake/.netrc"}}
	lookup := func(store registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
		return store.Name == "netrc", nil
	}
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, lookup, probe)
	if code != 0 {
		t.Fatalf("runRegistryDiscover code = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	routes, err := registryroutes.Parse(data)
	if err != nil {
		t.Fatalf("registryroutes.Parse: %v; data=%s", err, data)
	}
	if len(routes) != 1 {
		t.Fatalf("len(routes) = %d, want 1", len(routes))
	}
	r := routes[0]
	if r.MatchHost != "cargo.example.com" {
		t.Errorf("MatchHost = %q, want cargo.example.com", r.MatchHost)
	}
	if r.AuthScheme != "bearer" {
		t.Errorf("AuthScheme = %q, want bearer", r.AuthScheme)
	}
	if r.Credential.FromFile != "/fake/.netrc" || r.Credential.FileFormat != "netrc" {
		t.Errorf("Credential = %+v, want FromFile=/fake/.netrc FileFormat=netrc", r.Credential)
	}

	if !strings.Contains(stdout.String(), "route cargo.example.com (auth bearer, credential netrc)") {
		t.Errorf("stdout = %q, want a route summary line", stdout.String())
	}
	if strings.Contains(stdout.String(), "https://cargo.example.com/index") {
		t.Errorf("stdout = %q, want no upstream URL in the route summary", stdout.String())
	}
	if !strings.Contains(stdout.String(), outPath) {
		t.Errorf("stdout = %q, want it to name the written file %q", stdout.String(), outPath)
	}
	if !strings.Contains(stdout.String(), "wrote routes file") {
		t.Errorf("stdout = %q, want the wrote-routes-file line", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr.String())
	}
}

// TestRunRegistryDiscover_CargoFixtureEndToEnd_RegistryNameCarriesThrough
// drives a cargo fixture whose host matches a cargo-credentials store through
// the full runRegistryDiscover pipeline, and confirms the cargo registry name
// (a cargo-credentials-only companion field, see discover.go's RegistryName
// handling) survives Render and comes back out of registryroutes.Parse on the
// parsed route's Credential.
func TestRunRegistryDiscover_CargoFixtureEndToEnd_RegistryNameCarriesThrough(t *testing.T) {
	repoDir := t.TempDir()
	writeCargoFixture(t, repoDir, "mycorp", "https://cargo.example.com/index")

	outPath := filepath.Join(t.TempDir(), "routes.toml")

	stores := []registrydiscover.Store{{Name: "cargo-credentials", Path: "/fake/.cargo/credentials.toml"}}
	lookup := func(store registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
		return store.Name == "cargo-credentials" && d.RegistryName == "mycorp", nil
	}
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, lookup, probe)
	if code != 0 {
		t.Fatalf("runRegistryDiscover code = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	routes, err := registryroutes.Parse(data)
	if err != nil {
		t.Fatalf("registryroutes.Parse: %v; data=%s", err, data)
	}
	if len(routes) != 1 {
		t.Fatalf("len(routes) = %d, want 1", len(routes))
	}
	r := routes[0]
	if r.Credential.FileFormat != "cargo-credentials" {
		t.Errorf("Credential.FileFormat = %q, want cargo-credentials", r.Credential.FileFormat)
	}
	if r.Credential.RegistryName != "mycorp" {
		t.Errorf("Credential.RegistryName = %q, want mycorp", r.Credential.RegistryName)
	}
}

// TestRunRegistryDiscover_UnmatchedHost_EnvPlaceholderAndHint verifies that a
// host no configured store matches gets an env-placeholder credential in the
// written file, and that stdout names the stores searched and hints at the
// placeholder env var to set.
func TestRunRegistryDiscover_UnmatchedHost_EnvPlaceholderAndHint(t *testing.T) {
	repoDir := t.TempDir()
	writeCargoFixture(t, repoDir, "foo", "https://cargo.example.com/index")

	outPath := filepath.Join(t.TempDir(), "routes.toml")

	stores := []registrydiscover.Store{
		{Name: "netrc", Path: "/fake/.netrc"},
		{Name: "cargo-credentials", Path: "/fake/credentials.toml"},
	}
	lookup := func(registrydiscover.Store, ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, lookup, probe)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "SPINDRIFT_REGISTRY_CREDENTIAL_CARGO_EXAMPLE_COM") {
		t.Errorf("routes file = %s, want the env placeholder", data)
	}

	if !strings.Contains(stdout.String(), "netrc") || !strings.Contains(stdout.String(), "cargo-credentials") {
		t.Errorf("stdout = %q, want the stores searched named", stdout.String())
	}
	if !strings.Contains(stdout.String(), "SPINDRIFT_REGISTRY_CREDENTIAL_CARGO_EXAMPLE_COM") {
		t.Errorf("stdout = %q, want the placeholder env var named in a hint", stdout.String())
	}
	if !strings.Contains(stdout.String(), "set these environment variables") {
		t.Errorf("stdout = %q, want a set-env-vars hint", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr.String())
	}
}

// TestRunRegistryDiscover_ExistingFile_RefusesWithoutForce_OverwritesWithForce
// verifies that a pre-existing routes file is left untouched and reported as
// an error on stderr without --force, and is overwritten when --force is
// passed.
func TestRunRegistryDiscover_ExistingFile_RefusesWithoutForce_OverwritesWithForce(t *testing.T) {
	repoDir := t.TempDir()
	writeCargoFixture(t, repoDir, "foo", "https://cargo.example.com/index")

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	sentinel := []byte("do-not-touch")
	if err := os.WriteFile(outPath, sentinel, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	stores := []registrydiscover.Store{{Name: "netrc", Path: "/fake/.netrc"}}
	lookup := func(registrydiscover.Store, ecosystem.Declaration) (bool, error) { return true, nil }
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, lookup, probe)
	if code == 0 {
		t.Fatalf("code = 0, want non-zero for an existing file without --force")
	}
	if !strings.Contains(stderr.String(), outPath) {
		t.Errorf("stderr = %q, want it to name the existing file", stderr.String())
	}
	if strings.Contains(stdout.String(), outPath) {
		t.Errorf("stdout = %q, want the refuse-to-overwrite error kept off stdout", stdout.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(sentinel) {
		t.Errorf("file content = %q, want unchanged %q", got, sentinel)
	}

	stdout.Reset()
	stderr.Reset()
	code = runRegistryDiscover(&stdout, &stderr, repoDir, outPath, true, stores, lookup, probe)
	if code != 0 {
		t.Fatalf("code = %d, want 0 with --force; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr.String())
	}
	got, err = os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(got), "do-not-touch") {
		t.Errorf("file content = %q, want overwritten", got)
	}
}

// TestRunRegistryDiscover_NeverWritesCredentialValue drives runRegistryDiscover
// with the real registrydiscover.StoreLookup against a netrc file whose
// password is the sentinel secret, so the sentinel has a genuine channel into
// the pipeline: StoreLookup reads it off disk, matches it to the fixture
// repo's cargo.example.com host, and the route it produces flows through
// Discover and WriteFile same as any real credential match would. If the
// writer ever started embedding a resolved value instead of a store
// reference, this sentinel is what would show up in the file.
func TestRunRegistryDiscover_NeverWritesCredentialValue(t *testing.T) {
	repoDir := t.TempDir()
	writeCargoFixture(t, repoDir, "foo", "https://cargo.example.com/index")

	const sentinelSecret = "s3kr1t-password-do-not-leak"
	netrcPath := filepath.Join(t.TempDir(), "netrc")
	netrcContent := "machine cargo.example.com\nlogin agent\npassword " + sentinelSecret + "\n"
	if err := os.WriteFile(netrcPath, []byte(netrcContent), 0o600); err != nil {
		t.Fatalf("WriteFile netrc: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	stores := []registrydiscover.Store{{Name: "netrc", Path: netrcPath}}
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, registrydiscover.StoreLookup, probe)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), netrcPath) {
		t.Errorf("routes file = %s, want it to reference the matched netrc store path %q", data, netrcPath)
	}
	if strings.Contains(string(data), sentinelSecret) {
		t.Errorf("routes file contains the sentinel credential value; must never")
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr.String())
	}
}

// TestRunRegistryDiscover_ReportDistinguishesSkippedFromNoRegistryDeclared
// verifies that a config file naming only a non-http/unusable registry URL
// (here, .npmrc's local-path value) is reported as skipped, never mislabeled
// as declaring no registry at all.
func TestRunRegistryDiscover_ReportDistinguishesSkippedFromNoRegistryDeclared(t *testing.T) {
	repoDir := t.TempDir()
	writeCargoFixture(t, repoDir, "foo", "https://cargo.example.com/index")
	// .npmrc names a registry, but its value is non-http -- this must not
	// print as "no registry declared".
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte("registry=/local/path\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .npmrc: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	stores := []registrydiscover.Store{{Name: "netrc", Path: "/fake/.netrc"}}
	lookup := func(store registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
		return store.Name == "netrc", nil
	}
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, lookup, probe)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	report := stdout.String()
	if !strings.Contains(report, "config declares only non-http/unusable registry URLs:") || !strings.Contains(report, ".npmrc") {
		t.Errorf("report = %q, want a skipped-URL note naming .npmrc", report)
	}
	if strings.Contains(report, "config present, no registry declared:") {
		t.Errorf("report = %q, must not claim no registry declared for a skipped-URL config", report)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr.String())
	}
}

// TestCmdRegistryDiscover_WrongArgCount_UsageError verifies that
// cmdRegistryDiscover rejects any argument count other than exactly two
// positionals with a usage message and exit code 1.
func TestCmdRegistryDiscover_WrongArgCount_UsageError(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"only-one"},
		{"a", "b", "c"},
	} {
		var out strings.Builder
		code := cmdRegistryDiscover(args, &out, &out)
		if code != 1 {
			t.Errorf("cmdRegistryDiscover(%v) code = %d, want 1", args, code)
		}
		if !strings.Contains(out.String(), "usage: spindrift registry discover <repo-dir> <routes-file> [--force]") {
			t.Errorf("cmdRegistryDiscover(%v) output = %q, want the usage message", args, out.String())
		}
	}
}

// TestCmdRegistryDiscover_ForceFlagAnyPosition_StillTwoPositionals verifies
// that --force is recognized wherever it appears in argv and doesn't count
// against the two required positionals.
func TestCmdRegistryDiscover_ForceFlagAnyPosition_StillTwoPositionals(t *testing.T) {
	// Hermetic: without this, cmdRegistryDiscover's default stores read the
	// real $HOME (only harmless here because repoDir's registry fixture
	// keeps the lookup from ever touching a real ~/.netrc).
	t.Setenv("HOME", t.TempDir())

	repoDir := t.TempDir()
	writeCargoFixture(t, repoDir, "foo", "https://cargo.example.com/index")
	outPath := filepath.Join(t.TempDir(), "routes.toml")

	var out strings.Builder
	code := cmdRegistryDiscover([]string{"--force", repoDir, outPath}, &out, &out)
	if code != 0 {
		t.Fatalf("code = %d, want 0; output=%s", code, out.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("Stat(%q): %v, want the file written", outPath, err)
	}
}

// TestCmdRegistryDiscover_HomeDirUnavailable_ErrorsInsteadOfProceeding
// covers os.UserHomeDir failing: it must surface as a command error, not
// silently fall back to zero configured stores (which used to make every
// unmatched-host report line name no stores at all).
func TestCmdRegistryDiscover_HomeDirUnavailable_ErrorsInsteadOfProceeding(t *testing.T) {
	t.Setenv("HOME", "")

	repoDir := t.TempDir()
	writeCargoFixture(t, repoDir, "foo", "https://cargo.example.com/index")
	outPath := filepath.Join(t.TempDir(), "routes.toml")

	var out strings.Builder
	code := cmdRegistryDiscover([]string{repoDir, outPath}, &out, &out)
	if code == 0 {
		t.Fatalf("code = 0, want non-zero when $HOME is unavailable; output=%s", out.String())
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Errorf("Stat(%q): file exists, want no file written", outPath)
	}
}

// TestRunRegistryDiscover_ZeroRoutes_RefusesToWriteAndNamesRepoDir covers a
// repoDir with no registry config at all (indistinguishable from a
// mistyped path): it must not produce a routes file that
// registryroutes.Parse then rejects for declaring zero [[routes]] entries.
func TestRunRegistryDiscover_ZeroRoutes_RefusesToWriteAndNamesRepoDir(t *testing.T) {
	repoDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "routes.toml")

	stores := []registrydiscover.Store{{Name: "netrc", Path: "/fake/.netrc"}}
	lookup := func(registrydiscover.Store, ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, lookup, probe)
	if code == 0 {
		t.Fatalf("code = 0, want non-zero for a repoDir with no registry declarations; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Errorf("Stat(%q): file exists, want no file written", outPath)
	}
	if !strings.Contains(stderr.String(), repoDir) {
		t.Errorf("stderr = %q, want it to name the repo dir %q", stderr.String(), repoDir)
	}
	if strings.Contains(stdout.String(), "nothing to write") {
		t.Errorf("stdout = %q, want the error message kept off stdout", stdout.String())
	}
}

// TestRunRegistryDiscover_PortOnlyHost_RefusesToWriteAndReportsSkipped
// covers the finding that a port-only registry URL (.npmrc's
// "registry=http://:8080/") must never write a routes file --
// registryroutes.Parse rejects an empty match-host, so this must land in
// the zero-routes path (same as TestRunRegistryDiscover_ZeroRoutes...)
// rather than the success path, and the report must name the file as
// declaring only an unusable URL, not as declaring nothing at all.
func TestRunRegistryDiscover_PortOnlyHost_RefusesToWriteAndReportsSkipped(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte("registry=http://:8080/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .npmrc: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	stores := []registrydiscover.Store{{Name: "netrc", Path: "/fake/.netrc"}}
	lookup := func(registrydiscover.Store, ecosystem.Declaration) (bool, error) { return false, nil }
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, lookup, probe)
	if code == 0 {
		t.Fatalf("code = 0, want non-zero for a repo declaring only a port-only URL; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Errorf("Stat(%q): file exists, want no file written (a match-host of \"\" is what registryroutes.Parse rejects)", outPath)
	}
	if !strings.Contains(stdout.String(), "config declares only non-http/unusable registry URLs") {
		t.Errorf("stdout = %q, want the skipped-config report section", stdout.String())
	}
}

// TestRunRegistryDiscover_ReportNamesMatchedUnmatchedAndEmptyConfigSections
// verifies that a single run's report correctly sorts a matched host, an
// unmatched host, and a config-present-but-empty file into their own
// sections, alongside the matched route's summary line and the unmatched
// host's env hint.
func TestRunRegistryDiscover_ReportNamesMatchedUnmatchedAndEmptyConfigSections(t *testing.T) {
	repoDir := t.TempDir()
	writeCargoFixture(t, repoDir, "foo", "https://cargo.example.com/index")
	if err := os.WriteFile(filepath.Join(repoDir, ".npmrc"), []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile .npmrc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".yarnrc.yml"), []byte("npmRegistryServer: https://yarn.example.com\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .yarnrc.yml: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	stores := []registrydiscover.Store{{Name: "netrc", Path: "/fake/.netrc"}}
	lookup := func(store registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
		return d.Host == "cargo.example.com", nil
	}
	probe := func(string) string { return "bearer" }

	var stdout, stderr strings.Builder
	code := runRegistryDiscover(&stdout, &stderr, repoDir, outPath, false, stores, lookup, probe)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	report := stdout.String()
	if !strings.Contains(report, "route cargo.example.com") {
		t.Errorf("report = %q, want the matched route summary line", report)
	}
	if !strings.Contains(report, "cargo.example.com") || !strings.Contains(report, "netrc") {
		t.Errorf("report = %q, want the matched cargo.example.com/netrc pairing", report)
	}
	if !strings.Contains(report, "yarn.example.com") {
		t.Errorf("report = %q, want the unmatched yarn.example.com host", report)
	}
	if !strings.Contains(report, ".npmrc") {
		t.Errorf("report = %q, want the config-present-no-registry .npmrc note", report)
	}
	if !strings.Contains(report, "set these environment variables") || !strings.Contains(report, "SPINDRIFT_REGISTRY_CREDENTIAL_YARN_EXAMPLE_COM") {
		t.Errorf("report = %q, want the env hint for the unmatched yarn.example.com host", report)
	}
	if !strings.Contains(report, "wrote routes file "+outPath) {
		t.Errorf("report = %q, want the wrote-routes-file line naming %q", report, outPath)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr.String())
	}
}
