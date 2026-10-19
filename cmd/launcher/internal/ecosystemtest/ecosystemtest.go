// Package ecosystemtest is the kit an ecosystem row's package calls from its
// own tests: Run replays a corpus of Target-repo trees through the registry
// pipeline's stages in order and checks each stage's output against the golden
// beside the case (issue #5654). It takes an ecosystem.Row and a directory,
// never ecosystem.Table, so a row proves itself over its own corpus alone.
//
// A corpus directory holds one subdirectory per case:
//
//	repo/                         the Target-repo tree, with its committed registry config
//	stores/<credresolver key>     optional operator credential store files (e.g. stores/netrc)
//	routes.toml                   optional operator routes file; route resolution reads it
//	                              instead of discovery's rendered output
//	upstream/<match-host>/<path>  optional upstream response bodies the proxy rewrites (stage 6)
//	classification.golden.txt     stage 1: the lockfile classification, empty when no lockfile matches
//	discovery.golden.toml         stage 2: the routes file discovery proposes
//	routes.golden.json            stage 3: the resolved routes
//	pathset.golden.json           stage 4: the derived enforced path-sets
//	binding.golden.json           stage 5: the env exports, home config and in-tree rewrite
//	rewrites.golden.json          stage 6: the proxy's responses for the upstream bodies
//
// A stage the row does not take part in has no golden and is skipped; a stale
// golden for one fails. Importing this package from non-test code pulls in
// testing; only _test.go files should do so.
package ecosystemtest

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/golden"
	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registrypathset"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryroutes"
	"spindrift.dev/launcher/internal/registryvocab"
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
		name:      "lockfile classification",
		golden:    "classification.golden.txt",
		takesPart: func(row ecosystem.Row) bool { return len(row.LockfileNames) > 0 },
		replay:    (*replay).classification,
	},
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
	{
		name:   "binding",
		golden: "binding.golden.json",
		asJSON: true,
		takesPart: func(row ecosystem.Row) bool {
			return row.EnvExports != nil || row.HomeConfig != nil || len(bindregistry.InTreeBindingsOf([]ecosystem.Row{row})) > 0
		},
		replay: (*replay).binding,
	},
	{
		name:      "proxy rewrites",
		golden:    "rewrites.golden.json",
		asJSON:    true,
		takesPart: func(row ecosystem.Row) bool { return len(row.RewriteRows) > 0 },
		replay:    (*replay).rewrites,
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

// memo caches one stage computation shared by later stages.
type memo[T any] struct {
	v    T
	err  error
	done bool
}

func (m *memo[T]) get(f func() (T, error)) (T, error) {
	if !m.done {
		m.v, m.err = f()
		m.done = true
	}
	return m.v, m.err
}

// replay is one case's state across stages: later stages consume earlier
// stages' output.
type replay struct {
	row ecosystem.Row
	dir string

	rendered memo[[]byte]
	parsed   memo[[]registryroutes.Route]
	sets     memo[[]registrypathset.HostPathSet]
	proxied  memo[[]registryproxy.Route]
}

func (c *replay) rows() []ecosystem.Row { return []ecosystem.Row{c.row} }

func (c *replay) repo() string { return filepath.Join(c.dir, "repo") }

func (c *replay) classification() ([]byte, error) {
	return []byte(bindregistry.ClassifyRows(c.repo(), c.rows()) + "\n"), nil
}

// discovery hands DiscoverRows a repo-relative display path for each store, so
// the golden carries no absolute temp path, and maps it back to the real file
// for the lookup.
func (c *replay) discovery() ([]byte, error) {
	return c.rendered.get(func() ([]byte, error) {
		var stores []registrydiscover.Store
		for _, k := range credresolver.StoreKinds() {
			display := filepath.ToSlash(filepath.Join("stores", k.SourceKey))
			if _, err := os.Stat(filepath.Join(c.dir, display)); errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, err
			}
			stores = append(stores, registrydiscover.Store{Name: k.SourceKey, Path: display})
		}
		lookup := func(s registrydiscover.Store, d ecosystem.Declaration) (bool, error) {
			return registrydiscover.StoreLookup(registrydiscover.Store{Name: s.Name, Path: filepath.Join(c.dir, s.Path)}, d)
		}
		routes, _, err := registrydiscover.DiscoverRows(
			c.repo(), c.rows(), stores,
			map[string][]string{c.row.Name: c.row.CredentialStores},
			lookup, func(string) string { return probedAuthScheme })
		if err != nil {
			return nil, err
		}
		return registrydiscover.Render(routes), nil
	})
}

func (c *replay) parsedRoutes() ([]registryroutes.Route, error) {
	return c.parsed.get(func() ([]registryroutes.Route, error) {
		input, err := os.ReadFile(filepath.Join(c.dir, "routes.toml"))
		if errors.Is(err, fs.ErrNotExist) {
			input, err = c.discovery()
		}
		if err != nil {
			return nil, err
		}
		return registryroutes.ParseRows(input, c.rows())
	})
}

func (c *replay) routes() ([]byte, error) {
	routes, err := c.parsedRoutes()
	if err != nil {
		return nil, err
	}
	return json.Marshal(routes)
}

func (c *replay) pathSets() ([]registrypathset.HostPathSet, error) {
	return c.sets.get(func() ([]registrypathset.HostPathSet, error) {
		return registrypathset.DeriveRows(c.repo(), c.rows())
	})
}

func (c *replay) pathSet() ([]byte, error) {
	sets, err := c.pathSets()
	if err != nil {
		return nil, err
	}
	return json.Marshal(sets)
}

// proxyRoutes is the proxy route table stages 5 and 6 run against. It mirrors
// package main's resolveRegistryRoutesFromFile, applyHostPathSet and
// registryproxy.AssignPrefixes, which the kit cannot import. Credentials stay
// empty: no secret is resolved.
func (c *replay) proxyRoutes() ([]registryproxy.Route, error) {
	return c.proxied.get(func() ([]registryproxy.Route, error) {
		parsed, err := c.parsedRoutes()
		if err != nil {
			return nil, err
		}
		sets, err := c.pathSets()
		if err != nil {
			return nil, err
		}
		byHost := make(map[string]registrypathset.HostPathSet, len(sets))
		for _, s := range sets {
			byHost[registryvocab.HostKey(s.Host)] = s
		}
		routes := make([]registryproxy.Route, len(parsed))
		for i, r := range parsed {
			hp, ok := byHost[registryvocab.HostKey(r.MatchHost)]
			if !ok && r.UpstreamOrigin == "" {
				return nil, fmt.Errorf("route %q is host-rooted but the Target repo declares no registry on that host; set the route's upstream-origin", r.MatchHost)
			}
			route := registryproxy.Route{
				MatchHost:      r.MatchHost,
				AuthScheme:     r.AuthScheme,
				Ecosystems:     r.Ecosystems,
				UpstreamOrigin: r.UpstreamOrigin,
				Allow:          r.Allow,
				Upstream:       cmp.Or(r.UpstreamOrigin, strings.TrimSuffix(hp.Origin, "/")),
			}
			seen := map[string]bool{}
			addPath := func(p string) {
				if !seen[p] {
					seen[p] = true
					route.EnforcedPaths = append(route.EnforcedPaths, p)
				}
			}
			for _, sub := range hp.Subtrees {
				addPath(sub.Path)
				route.EnforcedSubtrees = append(route.EnforcedSubtrees, registryvocab.Subtree{Ecosystem: sub.Ecosystem, Path: sub.Path})
			}
			for _, p := range r.Allow {
				addPath(p)
			}
			// The Forwarder keys a rewrite row's bases off the tag, so a declared
			// path keeps its own tagged subtree even when a derived path equals it.
			if p := r.Ecosystems.Path(c.row.Name); p != "" {
				addPath(p)
				route.EnforcedSubtrees = append(route.EnforcedSubtrees, registryvocab.Subtree{Ecosystem: c.row.Name, Path: p})
			}
			routes[i] = route
		}
		return registryproxy.AssignPrefixes(routes), nil
	})
}

// manifestRoutes projects proxyRoutes the way the dispatch package's
// registryManifestRoutes does.
func (c *replay) manifestRoutes() ([]registrymanifest.Route, error) {
	routes, err := c.proxyRoutes()
	if err != nil {
		return nil, err
	}
	out := make([]registrymanifest.Route, len(routes))
	for i, r := range routes {
		var host string
		if u, err := url.Parse(r.Upstream); err == nil {
			host = u.Host
		}
		out[i] = registrymanifest.Route{
			Prefix:        r.Prefix,
			UpstreamHost:  host,
			EnforcedPaths: slices.Clone(r.EnforcedSubtrees),
			Ecosystems:    r.Ecosystems,
		}
	}
	return out, nil
}
