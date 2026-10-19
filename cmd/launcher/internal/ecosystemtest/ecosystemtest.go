// Package ecosystemtest is the kit an ecosystem row's package calls from its
// own tests: Run replays a corpus of Target-repo trees through the registry
// pipeline's stages in order and checks each stage's output against the golden
// beside the case (issue #5654). It takes an ecosystem.Row and a directory,
// never ecosystem.Table, so a row proves itself over its own corpus alone.
//
// A corpus directory holds one subdirectory per case:
//
//	repo/                      the Target-repo tree, with its committed registry config
//	stores/<credresolver key>  optional operator credential store files (e.g. stores/netrc)
//	routes.toml                optional operator routes file; route resolution reads it
//	                           instead of discovery's rendered output
//	discovery.golden.toml      stage 1: the routes file discovery proposes
//	routes.golden.json         stage 2: the resolved routes
//	pathset.golden.json        stage 3: the derived enforced path-sets
//
// A stage the row does not take part in has no golden and is skipped; a stale
// golden for one fails. Importing this package from non-test code pulls in
// testing; only _test.go files should do so.
package ecosystemtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/golden"
	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryroutes"
)

// probedAuthScheme stands in for discovery's network probe, which has no
// registry to ask in a corpus.
const probedAuthScheme = "bearer"

// stage is one pipeline stage. takesPart is its single participation
// predicate; replay produces the bytes its golden holds.
type stage struct {
	name      string
	golden    string
	asJSON    bool
	takesPart func(row ecosystem.Row) bool
	replay    func(c *replay) ([]byte, error)
}

// stages is the pipeline in order; each stage is one entry here and nowhere else.
var stages = []stage{
	{
		name:      "discovery",
		golden:    "discovery.golden.toml",
		takesPart: func(row ecosystem.Row) bool { return row.ConfigParser != nil },
		replay:    (*replay).discovery,
	},
	{
		name:      "route resolution",
		golden:    "routes.golden.json",
		asJSON:    true,
		takesPart: func(row ecosystem.Row) bool { return row.ConfigParser != nil || row.RouteDeclaration != nil },
		replay:    (*replay).routes,
	},
	{
		name:      "path-set derivation",
		golden:    "pathset.golden.json",
		asJSON:    true,
		takesPart: func(row ecosystem.Row) bool { return row.ConfigParser != nil },
		replay:    (*replay).pathSet,
	},
}

// Run replays every case under corpusDir against row: one subtest per case. A
// corpusDir with no case fails.
func Run(t *testing.T, row ecosystem.Row, corpusDir string) {
	t.Helper()
	update := golden.Update()
	for _, c := range caseDirs(t, corpusDir) {
		t.Run(c, func(t *testing.T) {
			for _, err := range replayCase(row, filepath.Join(corpusDir, c), update) {
				t.Error(err)
			}
		})
	}
}

func caseDirs(t testing.TB, corpusDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(corpusDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read %s: %v", corpusDir, err)
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Errorf("corpus %s has no case; add one at %s/<case>/repo", corpusDir, corpusDir)
	}
	return names
}

// replayCase runs every stage over the case in dir and returns one error per
// failing stage. A missing golden for a stage the row takes part in fails like
// any other mismatch; update mode writes it instead.
func replayCase(row ecosystem.Row, dir string, update bool) []error {
	c := &replay{row: row, dir: dir}
	var errs []error
	for _, s := range stages {
		path := filepath.Join(dir, s.golden)
		if !s.takesPart(row) {
			if err := golden.RemoveIfUpdate(path, update); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
			}
			continue
		}
		produced, err := s.replay(c)
		if err == nil {
			if s.asJSON {
				err = golden.CompareOrUpdateJSON(path, produced, update)
			} else {
				err = golden.CompareOrUpdateText(path, produced, update)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
		}
	}
	return errs
}

// replay is one case's state across stages: stage 2 consumes stage 1's output.
type replay struct {
	row ecosystem.Row
	dir string

	rendered []byte
	rendErr  error
	ran      bool
}

func (c *replay) rows() []ecosystem.Row { return []ecosystem.Row{c.row} }

func (c *replay) repo() string { return filepath.Join(c.dir, "repo") }

// discovery hands DiscoverRows a repo-relative display path for each store, so
// the golden carries no absolute temp path, and maps it back to the real file
// for the lookup.
func (c *replay) discovery() ([]byte, error) {
	if c.ran {
		return c.rendered, c.rendErr
	}
	c.ran = true
	var stores []registrydiscover.Store
	for _, k := range credresolver.StoreKinds() {
		display := filepath.ToSlash(filepath.Join("stores", k.SourceKey))
		if _, err := os.Stat(filepath.Join(c.dir, display)); err == nil {
			stores = append(stores, registrydiscover.Store{Name: k.SourceKey, Path: display})
		}
	}
	lookup := func(s registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
		return registrydiscover.StoreLookup(registrydiscover.Store{Name: s.Name, Path: filepath.Join(c.dir, s.Path)}, d)
	}
	routes, _, err := registrydiscover.DiscoverRows(
		c.repo(), c.rows(), stores,
		map[string][]string{c.row.Name: c.row.CredentialStores},
		lookup, func(string) string { return probedAuthScheme })
	if err != nil {
		c.rendErr = err
		return nil, err
	}
	c.rendered = registrydiscover.Render(routes)
	return c.rendered, nil
}

func (c *replay) routes() ([]byte, error) {
	input, err := os.ReadFile(filepath.Join(c.dir, "routes.toml"))
	if errors.Is(err, fs.ErrNotExist) {
		input, err = c.discovery()
	}
	if err != nil {
		return nil, err
	}
	routes, err := registryroutes.ParseRows(input, c.rows())
	if err != nil {
		return nil, err
	}
	return json.Marshal(routes)
}

func (c *replay) pathSet() ([]byte, error) {
	sets, err := registrypathset.DeriveRows(c.repo(), c.rows())
	if err != nil {
		return nil, err
	}
	return json.Marshal(sets)
}
