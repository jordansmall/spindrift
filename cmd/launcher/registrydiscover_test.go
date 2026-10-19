package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/registryroutes"
)

// These tests drive the discover command over the synthetic row alone; the
// real rows' declaration shapes, store pairings and route files are pinned by
// the ecosystem corpora under internal/ecosystem/testdata/corpus.
var discoverRows = []ecosystem.Row{synthetic.Row}

// writeDiscoverDeclarations writes lines ("name = url") to the row's in-tree config.
func writeDiscoverDeclarations(t *testing.T, dir string, row ecosystem.Row, lines ...string) {
	t.Helper()
	path := filepath.Join(dir, row.InTreeConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func discoverBearerProbe(string) string { return "bearer" }

// discoverOver calls runRegistryDiscover over rows with the fixed "bearer" probe.
func discoverOver(rows []ecosystem.Row, repoDir, outPath string, force bool, stores []registrydiscover.Store, lookup registrydiscover.Lookup) (code int, stdout, stderr string) {
	var out, errOut strings.Builder
	code = runRegistryDiscover(&out, &errOut, rows, repoDir, outPath, force, stores, lookup, discoverBearerProbe)
	return code, out.String(), errOut.String()
}

// discoverSynthetic is discoverOver over the synthetic row alone.
func discoverSynthetic(repoDir, outPath string, force bool, stores []registrydiscover.Store, lookup registrydiscover.Lookup) (code int, stdout, stderr string) {
	return discoverOver(discoverRows, repoDir, outPath, force, stores, lookup)
}

func discoverNetrcStore() []registrydiscover.Store {
	return []registrydiscover.Store{{Name: "netrc", Path: "/fake/.netrc"}}
}

func discoverNoMatch(registrydiscover.Store, ecosystem.Declaration) (bool, error) { return false, nil }
func discoverAlwaysMatch(registrydiscover.Store, ecosystem.Declaration) (bool, error) {
	return true, nil
}

// Pins the 2026-09-04 field shape (ADR 0047 / spec #3253): one Artifactory
// host fronting two registries collapses to a single route, and the written
// file carries only match-host, auth-scheme and credential. No upstream-origin
// (Artifactory's plain-https default-port URL derives it) and none of the
// declared index paths.
func TestRunRegistryDiscover_ArtifactoryFieldShape_OneRouteNoPathKeys(t *testing.T) {
	repoDir := t.TempDir()
	const host = "artifactory.example.com"
	twin := synthetic.Twin("twin")
	writeDiscoverDeclarations(t, repoDir, synthetic.Row,
		"internal = https://"+host+"/artifactory/api/internal-repo/index",
		"remote = https://"+host+"/artifactory/api/remote-repo/index")
	writeDiscoverDeclarations(t, repoDir, twin, "other = https://other.example.com/")

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	lookup := func(store registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
		return store.Name == "netrc" && d.Host == host, nil
	}

	code, stdout, stderr := discoverOver([]ecosystem.Row{synthetic.Row, twin}, repoDir, outPath, false, discoverNetrcStore(), lookup)
	if code != 0 {
		t.Fatalf("runRegistryDiscover code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, absent := range []string{"upstream-origin", "/artifactory/", "/index", "internal-repo", "remote-repo"} {
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

	matched, ok := byHost[host]
	if !ok {
		t.Fatalf("routes = %+v, want a route for %s", routes, host)
	}
	if matched.AuthScheme != "bearer" {
		t.Errorf("matched route AuthScheme = %q, want bearer", matched.AuthScheme)
	}
	if matched.Credential.FromFile != "/fake/.netrc" || matched.Credential.FileFormat != "netrc" {
		t.Errorf("matched route Credential = %+v, want FromFile=/fake/.netrc FileFormat=netrc", matched.Credential)
	}
	if matched.UpstreamOrigin != "" {
		t.Errorf("matched route UpstreamOrigin = %q, want empty", matched.UpstreamOrigin)
	}

	other, ok := byHost["other.example.com"]
	if !ok {
		t.Fatalf("routes = %+v, want a route for other.example.com", routes)
	}
	if other.Credential.FromEnv != "SPINDRIFT_REGISTRY_CREDENTIAL_OTHER_EXAMPLE_COM" {
		t.Errorf("unmatched route Credential = %+v, want the env placeholder", other.Credential)
	}
	if !strings.Contains(stdout, "set these environment variables") {
		t.Errorf("stdout = %q, want the env-placeholder hint for the unmatched host", stdout)
	}
}

func TestRunRegistryDiscover_EndToEnd_WritesParseableRoutesFile(t *testing.T) {
	repoDir := t.TempDir()
	writeDiscoverDeclarations(t, repoDir, synthetic.Row, "main = https://registry.example.com/index")
	outPath := filepath.Join(t.TempDir(), "routes.toml")

	lookup := func(store registrydiscover.Store, _ ecosystem.Declaration) (bool, error) {
		return store.Name == "netrc", nil
	}
	code, stdout, stderr := discoverSynthetic(repoDir, outPath, false, discoverNetrcStore(), lookup)
	if code != 0 {
		t.Fatalf("runRegistryDiscover code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
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
	if r.MatchHost != "registry.example.com" {
		t.Errorf("MatchHost = %q, want registry.example.com", r.MatchHost)
	}
	if r.AuthScheme != "bearer" {
		t.Errorf("AuthScheme = %q, want bearer", r.AuthScheme)
	}
	if r.Credential.FromFile != "/fake/.netrc" || r.Credential.FileFormat != "netrc" {
		t.Errorf("Credential = %+v, want FromFile=/fake/.netrc FileFormat=netrc", r.Credential)
	}

	if !strings.Contains(stdout, "route registry.example.com (auth bearer, credential netrc)") {
		t.Errorf("stdout = %q, want a route summary line", stdout)
	}
	if strings.Contains(stdout, "https://registry.example.com/index") {
		t.Errorf("stdout = %q, want no upstream URL in the route summary", stdout)
	}
	if !strings.Contains(stdout, outPath) {
		t.Errorf("stdout = %q, want it to name the written file %q", stdout, outPath)
	}
	if !strings.Contains(stdout, "wrote routes file") {
		t.Errorf("stdout = %q, want the wrote-routes-file line", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr)
	}
}

func TestRunRegistryDiscover_UnmatchedHost_EnvPlaceholderAndHint(t *testing.T) {
	repoDir := t.TempDir()
	writeDiscoverDeclarations(t, repoDir, synthetic.Row, "main = https://registry.example.com/index")
	outPath := filepath.Join(t.TempDir(), "routes.toml")

	stores := []registrydiscover.Store{
		{Name: "netrc", Path: "/fake/.netrc"},
		{Name: "other-store", Path: "/fake/other"},
	}
	code, stdout, stderr := discoverSynthetic(repoDir, outPath, false, stores, discoverNoMatch)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	const placeholder = "SPINDRIFT_REGISTRY_CREDENTIAL_REGISTRY_EXAMPLE_COM"
	if !strings.Contains(string(data), placeholder) {
		t.Errorf("routes file = %s, want the env placeholder", data)
	}

	if !strings.Contains(stdout, "netrc") || !strings.Contains(stdout, "other-store") {
		t.Errorf("stdout = %q, want the stores searched named", stdout)
	}
	if !strings.Contains(stdout, placeholder) {
		t.Errorf("stdout = %q, want the placeholder env var named in a hint", stdout)
	}
	if !strings.Contains(stdout, "set these environment variables") {
		t.Errorf("stdout = %q, want a set-env-vars hint", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr)
	}
}

func TestRunRegistryDiscover_ExistingFile_RefusesWithoutForce_OverwritesWithForce(t *testing.T) {
	repoDir := t.TempDir()
	writeDiscoverDeclarations(t, repoDir, synthetic.Row, "main = https://registry.example.com/index")

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	sentinel := []byte("do-not-touch")
	if err := os.WriteFile(outPath, sentinel, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code, stdout, stderr := discoverSynthetic(repoDir, outPath, false, discoverNetrcStore(), discoverAlwaysMatch)
	if code == 0 {
		t.Fatalf("code = 0, want non-zero for an existing file without --force")
	}
	if !strings.Contains(stderr, outPath) {
		t.Errorf("stderr = %q, want it to name the existing file", stderr)
	}
	if strings.Contains(stdout, outPath) {
		t.Errorf("stdout = %q, want the refuse-to-overwrite error kept off stdout", stdout)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(sentinel) {
		t.Errorf("file content = %q, want unchanged %q", got, sentinel)
	}

	code, stdout, stderr = discoverSynthetic(repoDir, outPath, true, discoverNetrcStore(), discoverAlwaysMatch)
	if code != 0 {
		t.Fatalf("code = %d, want 0 with --force; stdout=%s stderr=%s", code, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr)
	}
	got, err = os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(got), "do-not-touch") {
		t.Errorf("file content = %q, want overwritten", got)
	}
}

// Uses the real registrydiscover.StoreLookup, not a stub, so the sentinel
// password has a genuine channel into the pipeline: StoreLookup reads it off
// disk, matches the fixture repo's registry host, and the route it produces
// flows through Discover and WriteFile as any real match would. A writer
// embedding resolved values instead of store references would leak it.
func TestRunRegistryDiscover_NeverWritesCredentialValue(t *testing.T) {
	repoDir := t.TempDir()
	writeDiscoverDeclarations(t, repoDir, synthetic.Row, "main = https://registry.example.com/index")

	const sentinelSecret = "s3kr1t-password-do-not-leak"
	netrcPath := filepath.Join(t.TempDir(), "netrc")
	netrcContent := "machine registry.example.com\nlogin agent\npassword " + sentinelSecret + "\n"
	if err := os.WriteFile(netrcPath, []byte(netrcContent), 0o600); err != nil {
		t.Fatalf("WriteFile netrc: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	stores := []registrydiscover.Store{{Name: "netrc", Path: netrcPath}}
	code, stdout, stderr := discoverSynthetic(repoDir, outPath, false, stores, registrydiscover.StoreLookup)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
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
	if stderr != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr)
	}
}

func TestRunRegistryDiscover_ReportDistinguishesSkippedFromNoRegistryDeclared(t *testing.T) {
	repoDir := t.TempDir()
	skipped := synthetic.Twin("skipped")
	writeDiscoverDeclarations(t, repoDir, synthetic.Row, "main = https://registry.example.com/index")
	// A named but non-http value, so the report must call the config skipped
	// rather than "no registry declared".
	writeDiscoverDeclarations(t, repoDir, skipped, "main = /local/path")

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	code, report, stderr := discoverOver([]ecosystem.Row{synthetic.Row, skipped}, repoDir, outPath, false, discoverNetrcStore(), discoverAlwaysMatch)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stdout=%s stderr=%s", code, report, stderr)
	}

	if !strings.Contains(report, "config declares only non-http/unusable registry URLs:") || !strings.Contains(report, skipped.InTreeConfigPath) {
		t.Errorf("report = %q, want a skipped-URL note naming %s", report, skipped.InTreeConfigPath)
	}
	if strings.Contains(report, "config present, no registry declared:") {
		t.Errorf("report = %q, must not claim no registry declared for a skipped-URL config", report)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr)
	}
}

func TestCmdRegistryDiscover_WrongArgCount_UsageError(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"only-one"},
		{"a", "b", "c"},
	} {
		var out strings.Builder
		code := cmdRegistryDiscover(discoverRows, args, &out, &out)
		if code != 1 {
			t.Errorf("cmdRegistryDiscover(%v) code = %d, want 1", args, code)
		}
		if !strings.Contains(out.String(), "usage: spindrift registry discover <repo-dir> <routes-file> [--force]") {
			t.Errorf("cmdRegistryDiscover(%v) output = %q, want the usage message", args, out.String())
		}
	}
}

func TestCmdRegistryDiscover_ForceFlagAnyPosition_StillTwoPositionals(t *testing.T) {
	// Without this, cmdRegistryDiscover's default stores read the real $HOME,
	// harmless here only because repoDir's registry fixture keeps the lookup
	// from ever touching a real ~/.netrc.
	t.Setenv("HOME", t.TempDir())

	repoDir := t.TempDir()
	writeDiscoverDeclarations(t, repoDir, synthetic.Row, "main = https://registry.example.com/index")
	outPath := filepath.Join(t.TempDir(), "routes.toml")

	var out strings.Builder
	code := cmdRegistryDiscover(discoverRows, []string{"--force", repoDir, outPath}, &out, &out)
	if code != 0 {
		t.Fatalf("code = %d, want 0; output=%s", code, out.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("Stat(%q): %v, want the file written", outPath, err)
	}
}

// os.UserHomeDir failing must fail the command, not silently fall back to zero
// configured stores, which used to make every unmatched-host report line name
// no stores at all.
func TestCmdRegistryDiscover_HomeDirUnavailable_ErrorsInsteadOfProceeding(t *testing.T) {
	t.Setenv("HOME", "")

	repoDir := t.TempDir()
	writeDiscoverDeclarations(t, repoDir, synthetic.Row, "main = https://registry.example.com/index")
	outPath := filepath.Join(t.TempDir(), "routes.toml")

	var out strings.Builder
	code := cmdRegistryDiscover(discoverRows, []string{repoDir, outPath}, &out, &out)
	if code == 0 {
		t.Fatalf("code = 0, want non-zero when $HOME is unavailable; output=%s", out.String())
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Errorf("Stat(%q): file exists, want no file written", outPath)
	}
}

// A repoDir with no registry config is indistinguishable from a mistyped path,
// and must not produce a routes file that registryroutes.Parse then rejects for
// declaring zero [[routes]] entries.
func TestRunRegistryDiscover_ZeroRoutes_RefusesToWriteAndNamesRepoDir(t *testing.T) {
	repoDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "routes.toml")

	code, stdout, stderr := discoverSynthetic(repoDir, outPath, false, discoverNetrcStore(), discoverNoMatch)
	if code == 0 {
		t.Fatalf("code = 0, want non-zero for a repoDir with no registry declarations; stdout=%s stderr=%s", stdout, stderr)
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Errorf("Stat(%q): file exists, want no file written", outPath)
	}
	if !strings.Contains(stderr, repoDir) {
		t.Errorf("stderr = %q, want it to name the repo dir %q", stderr, repoDir)
	}
	if strings.Contains(stdout, "nothing to write") {
		t.Errorf("stdout = %q, want the error message kept off stdout", stdout)
	}
}

// A config declaring only unusable URLs must land in the zero-routes path
// rather than the success path. The report must name the file as declaring
// only an unusable URL, not as declaring nothing at all.
func TestRunRegistryDiscover_OnlyUnusableURLs_RefusesToWriteAndReportsSkipped(t *testing.T) {
	repoDir := t.TempDir()
	writeDiscoverDeclarations(t, repoDir, synthetic.Row, "main = /local/path")
	outPath := filepath.Join(t.TempDir(), "routes.toml")

	code, stdout, stderr := discoverSynthetic(repoDir, outPath, false, discoverNetrcStore(), discoverNoMatch)
	if code == 0 {
		t.Fatalf("code = 0, want non-zero for a repo declaring only an unusable URL; stdout=%s stderr=%s", stdout, stderr)
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Errorf("Stat(%q): file exists, want no file written", outPath)
	}
	if !strings.Contains(stdout, "config declares only non-http/unusable registry URLs") {
		t.Errorf("stdout = %q, want the skipped-config report section", stdout)
	}
	if !strings.Contains(stderr, "nothing to write") {
		t.Errorf("stderr = %q, want the nothing-to-write error", stderr)
	}
}

func TestRunRegistryDiscover_ReportNamesMatchedUnmatchedAndEmptyConfigSections(t *testing.T) {
	repoDir := t.TempDir()
	empty := synthetic.Twin("empty")
	unmatched := synthetic.Twin("unmatched")
	writeDiscoverDeclarations(t, repoDir, synthetic.Row, "main = https://matched.example.com/index")
	writeDiscoverDeclarations(t, repoDir, empty, "")
	writeDiscoverDeclarations(t, repoDir, unmatched, "main = https://unmatched.example.com")

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	lookup := func(_ registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
		return d.Host == "matched.example.com", nil
	}
	code, report, stderr := discoverOver([]ecosystem.Row{synthetic.Row, empty, unmatched}, repoDir, outPath, false, discoverNetrcStore(), lookup)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stdout=%s stderr=%s", code, report, stderr)
	}

	if !strings.Contains(report, "route matched.example.com") {
		t.Errorf("report = %q, want the matched route summary line", report)
	}
	if !strings.Contains(report, "matched.example.com found in netrc (/fake/.netrc)") {
		t.Errorf("report = %q, want the matched.example.com/netrc pairing", report)
	}
	if !strings.Contains(report, "unmatched.example.com") {
		t.Errorf("report = %q, want the unmatched.example.com host", report)
	}
	if !strings.Contains(report, "config present, no registry declared:") || !strings.Contains(report, empty.InTreeConfigPath) {
		t.Errorf("report = %q, want the config-present-no-registry note naming %s", report, empty.InTreeConfigPath)
	}
	if !strings.Contains(report, "set these environment variables") || !strings.Contains(report, "SPINDRIFT_REGISTRY_CREDENTIAL_UNMATCHED_EXAMPLE_COM") {
		t.Errorf("report = %q, want the env hint for the unmatched.example.com host", report)
	}
	if !strings.Contains(report, "wrote routes file "+outPath) {
		t.Errorf("report = %q, want the wrote-routes-file line naming %q", report, outPath)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty on a success path", stderr)
	}
}

// On a disambiguation-bound error, runRegistryDiscover must still print the
// report Discover had already built, not just the bare error: an operator
// needs to see which hosts went unmatched even though the collision means no
// routes file gets written. Issue #3508.
func TestRunRegistryDiscover_DisambiguationBoundExhausted_StillPrintsReport(t *testing.T) {
	// Same brute-forced colliding-host fixture as
	// TestDiscover_BoundExhaustedPropagatesErrorAndReturnsNilRoutes: both
	// hosts fold to one envPlaceholder and collide under hostHash too, so no
	// round of suffixing can separate them and the bound trips for real.
	// The premise guards need unexported helpers, so only that test has
	// them: a code == 0 failure here means the fixture went stale.
	const host1 = "a.a-a.a.a-a.a-a.a-a-a.a-a.a-a-a-a.a.a.example.com"
	const host2 = "a.a.a.a.a-a.a.a.a.a.a.a-a-a.a.a-a-a.a.example.com"

	// A non-colliding matched host, so the matched section is covered too.
	const matchedHost = "matched.example.com"

	repoDir := t.TempDir()
	writeDiscoverDeclarations(t, repoDir, synthetic.Row,
		"one = https://"+host1+"/",
		"two = https://"+host2+"/",
		"three = https://"+matchedHost+"/")

	outPath := filepath.Join(t.TempDir(), "routes.toml")
	lookup := func(_ registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
		return d.Host == matchedHost, nil
	}

	code, stdout, stderr := discoverSynthetic(repoDir, outPath, false, discoverNetrcStore(), lookup)
	if code == 0 {
		t.Fatalf("code = 0, want non-zero on an unresolvable collision")
	}
	if !strings.Contains(stdout, "unmatched:") || !strings.Contains(stdout, host1) || !strings.Contains(stdout, host2) {
		t.Errorf("stdout = %q, want the unmatched report naming both colliding hosts", stdout)
	}
	if want := matchedHost + " found in netrc (/fake/.netrc)"; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want the matched line %q", stdout, want)
	}
	if !strings.Contains(stderr, "registry discover:") {
		t.Errorf("stderr = %q, want the propagated disambiguation error", stderr)
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Errorf("routes file %q was written, want none on this error path", outPath)
	}
}

// The store list comes from credresolver's kind table, in its search order
// with paths under $HOME, not from a hand-maintained parallel copy (acceptance
// criterion 3 of issue #3407). The table's own order and paths are pinned in
// credresolver.
func TestDefaultRegistryDiscoverStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stores, err := defaultRegistryDiscoverStores()
	if err != nil {
		t.Fatalf("defaultRegistryDiscoverStores: unexpected error: %v", err)
	}

	kinds := credresolver.StoreKinds()
	if len(stores) != len(kinds) {
		t.Fatalf("stores = %+v, want one per store kind %+v", stores, kinds)
	}
	for i, kind := range kinds {
		want := registrydiscover.Store{Name: kind.SourceKey, Path: filepath.Join(append([]string{home}, kind.StorePath...)...)}
		if stores[i] != want {
			t.Errorf("stores[%d] = %+v, want %+v", i, stores[i], want)
		}
	}
}
