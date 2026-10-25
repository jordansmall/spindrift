package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/hostprofile"
	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/registryroutes"
	"spindrift.dev/launcher/internal/registryvocab"
)

// profileProbe is one declared registry URL and discovery-probe response a
// corpus case would put in front of the Host profile table.
type profileProbe struct {
	url    string
	header http.Header
}

// uncoveredProfileRows names every row no probe matches. It takes the matcher
// so a test can supply a row the shipped table does not carry.
func uncoveredProfileRows(rows []hostprofile.Row, match profileMatcher, probes []profileProbe) []string {
	covered := map[string]bool{}
	for _, p := range probes {
		if row, ok := match(p.url, p.header); ok {
			covered[row.Name] = true
		}
	}
	var missing []string
	for _, row := range rows {
		if !covered[row.Name] {
			missing = append(missing, row.Name)
		}
	}
	return missing
}

func TestUncoveredProfileRowsNamesAnUnmatchedRow(t *testing.T) {
	fake := hostprofile.Row{Name: "fake-host"}
	known := hostprofile.Row{Name: "real-host"}
	match := func(declaredURL string, _ http.Header) (hostprofile.Row, bool) {
		if declaredURL == "https://real.example" {
			return known, true
		}
		return hostprofile.Row{}, false
	}
	probes := []profileProbe{{url: "https://real.example"}, {url: "https://other.example"}}

	got := uncoveredProfileRows([]hostprofile.Row{known, fake}, match, probes)
	if !slices.Equal(got, []string{"fake-host"}) {
		t.Errorf("uncoveredProfileRows = %v, want [fake-host]", got)
	}
	if got := uncoveredProfileRows([]hostprofile.Row{known}, match, probes); len(got) != 0 {
		t.Errorf("uncoveredProfileRows with every row matched = %v, want none", got)
	}
}

// corpusProfileProbes gathers, from one corpus case, every declared registry
// URL a Host profile could match: the committed config's declarations and the
// operator routes file's origins and per-ecosystem paths, each paired with the
// case's stubbed probe headers. A routes file the parser refuses contributes
// nothing, but only when the case pins that refusal in routes.golden.json, as
// ecosystemtest does; any other parse or read failure fails the test.
func corpusProfileProbes(t *testing.T, caseDir string) []profileProbe {
	t.Helper()
	rows := ecosystem.Registered()
	var header http.Header
	// A case naming a test-only neighbour row cannot be parsed against the
	// registered rows, so a parse failure on its routes file is tolerated.
	syntheticRows := false
	raw, err := os.ReadFile(filepath.Join(caseDir, "case.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s/case.json: %v", caseDir, err)
	}
	if err == nil {
		var cfg struct {
			Rows  []string `json:"rows"`
			Probe *struct {
				Headers map[string]string `json:"headers"`
			} `json:"probe"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("%s/case.json: %v", caseDir, err)
		}
		for _, name := range cfg.Rows {
			if !slices.ContainsFunc(rows, func(s ecosystem.Strategy) bool { return s.Name == name }) {
				syntheticRows = true
			}
		}
		if cfg.Probe != nil {
			header = http.Header{}
			for k, v := range cfg.Probe.Headers {
				header.Set(k, v)
			}
		}
	}

	var urls []string
	decls, _, err := registrydiscover.Extract(filepath.Join(caseDir, "repo"), rows)
	if err != nil {
		t.Fatalf("%s: extract: %v", caseDir, err)
	}
	for _, d := range decls {
		urls = append(urls, d.UpstreamBaseURL)
	}
	raw, err = os.ReadFile(filepath.Join(caseDir, "routes.toml"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s/routes.toml: %v", caseDir, err)
	}
	if err == nil {
		routes, err := registryroutes.Parse(raw, rows)
		if err != nil && !syntheticRows && !routesRefusalPinned(t, caseDir) {
			t.Fatalf("%s/routes.toml: %v (no refusal pinned in routes.golden.json)", caseDir, err)
		}
		for _, r := range routes {
			origin := r.UpstreamOrigin
			if origin == "" {
				origin = "https://" + r.MatchHost
			}
			urls = append(urls, origin)
			for _, block := range r.Ecosystems {
				if p, ok := block[registryvocab.RouteDeclarationPathKey].(string); ok {
					urls = append(urls, origin+p)
				}
			}
		}
	}

	probes := make([]profileProbe, len(urls))
	for i, u := range urls {
		probes[i] = profileProbe{url: u, header: header}
	}
	if len(urls) == 0 && header != nil {
		probes = append(probes, profileProbe{header: header})
	}
	return probes
}

// routesRefusalPinned reports whether the case records its routes file's
// refusal as the expected outcome, the signal ecosystemtest writes.
func routesRefusalPinned(t *testing.T, caseDir string) bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(caseDir, "routes.golden.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("%s/routes.golden.json: %v", caseDir, err)
	}
	var golden struct {
		Refused *string `json:"refused"`
	}
	return json.Unmarshal(raw, &golden) == nil && golden.Refused != nil
}

// TestEveryHostProfileRowHasACorpusCase keeps each Host profile row proven by a
// golden: a row nothing in the ecosystem corpora matches is untested advice.
func TestEveryHostProfileRowHasACorpusCase(t *testing.T) {
	var probes []profileProbe
	cases := 0
	corpora, err := filepath.Glob(filepath.Join("internal", "ecosystem", "*", "testdata", "corpus"))
	if err != nil || len(corpora) == 0 {
		t.Fatalf("no ecosystem corpus found (glob err %v); the walk root is wrong", err)
	}
	for _, root := range corpora {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			if fi, statErr := os.Stat(filepath.Join(path, "repo")); statErr != nil || !fi.IsDir() {
				return nil
			}
			cases++
			probes = append(probes, corpusProfileProbes(t, path)...)
			return fs.SkipDir
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if cases == 0 {
		t.Fatal("no corpus case found under internal/ecosystem; the walk is broken")
	}

	for _, name := range uncoveredProfileRows(hostprofile.Rows(), hostprofile.Match, probes) {
		t.Errorf("Host profile row %q has no corpus case: add an ecosystemtest corpus case under internal/ecosystem/<name>/testdata/corpus whose declared registry URL or case.json probe headers match it", name)
	}
}
