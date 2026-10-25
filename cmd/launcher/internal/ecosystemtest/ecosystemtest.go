// Package ecosystemtest is the kit an ecosystem Strategy's package calls from its
// own tests: Run replays a corpus of Target-repo trees through the registry
// pipeline's stages in order and checks each stage's output against the golden
// beside the case (issue #5654). It takes an ecosystem.Strategy and a directory,
// never ecosystem.Table, so a Strategy proves itself over its own corpus, plus the
// neighbour Strategies a case names, which the caller resolves.
//
// A corpus directory holds one subdirectory per case:
//
//	repo/                         the Target-repo tree, with its committed registry config
//	stores/<credresolver key>     optional operator credential store files (e.g. stores/netrc)
//	routes.toml                   optional operator routes file; route resolution reads it
//	                              instead of discovery's rendered output; one the parser
//	                              refuses is pinned in routes.golden.json, and the stages
//	                              that consume the resolved routes (path-set, binding,
//	                              bindings summary, rewrites) have no golden;
//	                              classification and the lockfile scan still run
//	env.json                      optional JSON object of string to string: the prior
//	                              environment the binding and bindings summary stages
//	                              read (default empty)
//	case.json                     optional JSON object, unknown keys refused: "rows", the
//	                              names of neighbour Strategies replayed beside the primary Strategy
//	                              (naming the primary Strategy too fixes its place in the order,
//	                              as ecosystem.Table does in production; else it comes first),
//	                              and "port", the Forwarder port the binding and rewrites
//	                              stages render against (default bindregistry.ForwarderPort);
//	                              the lockfile scan and bindings summary run production code
//	                              on the fixed ForwarderPort, so "port" does not reach them
//	upstream/<match-host>/<path>  optional upstream response bodies the proxy rewrites (stage 8)
//	classification.golden.txt     stage 1: the lockfile classification, empty when no lockfile matches
//	lockfile-scan.golden.txt      stage 2: the warnings for a tracked lockfile still naming the
//	                              Forwarder URL, empty when none does
//	discovery.golden.toml         stage 3: the routes file discovery proposes
//	routes.golden.json            stage 4: the resolved routes, or {"refused": <error>}
//	pathset.golden.json           stage 5: {"derived": the in-tree path-sets, "enforced":
//	                              each route's enforced paths and tagged subtrees}
//	binding.golden.json           stage 6: the env exports, home config and in-tree rewrite
//	summary.golden.txt            stage 7: what bindings mode prints for those routes
//	rewrites.golden.json          stage 8: the proxy's responses for the upstream bodies
//
// A stage the Strategy does not take part in has no golden and is skipped; a stale
// golden for one fails. Importing this package from non-test code pulls in
// testing; only _test.go files should do so.
package ecosystemtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// stage is one pipeline stage. It runs when takesPart holds and, for an
// afterRoutes stage, the case's routes file was not refused; replay produces
// the bytes its golden holds. afterRoutes marks a stage that consumes the
// resolved routes.
type stage struct {
	name        string
	golden      string
	asJSON      bool
	afterRoutes bool
	takesPart   func(row ecosystem.Strategy) bool
	replay      func(c *replay) ([]byte, error)
}

// everyRow is the predicate of a stage no Strategy can opt out of.
func everyRow(ecosystem.Strategy) bool { return true }

// stages is the pipeline in order; each stage is one entry here and nowhere else.
var stages = []stage{
	{
		name:      "lockfile classification",
		golden:    "classification.golden.txt",
		takesPart: func(row ecosystem.Strategy) bool { return len(row.LockfileNames) > 0 },
		replay:    (*replay).classification,
	},
	{
		name:      "lockfile scan",
		golden:    "lockfile-scan.golden.txt",
		takesPart: func(row ecosystem.Strategy) bool { return len(row.LockfileNames) > 0 },
		replay:    (*replay).lockfileScan,
	},
	{
		name:      "discovery",
		golden:    "discovery.golden.toml",
		takesPart: func(row ecosystem.Strategy) bool { return row.ConfigParser != nil },
		replay:    (*replay).discovery,
	},
	{
		// Every Strategy: the generic `path` key needs no hook, and a route's declared
		// path reaches the enforced path-set whatever the Strategy.
		name:      "route resolution",
		golden:    "routes.golden.json",
		asJSON:    true,
		takesPart: everyRow,
		replay:    (*replay).routes,
	},
	{
		name:        "path-set derivation",
		golden:      "pathset.golden.json",
		asJSON:      true,
		afterRoutes: true,
		takesPart:   everyRow,
		replay:      (*replay).pathSet,
	},
	{
		name:        "binding",
		golden:      "binding.golden.json",
		asJSON:      true,
		afterRoutes: true,
		takesPart: func(row ecosystem.Strategy) bool {
			return row.EnvExports != nil || row.HomeConfig != nil || len(bindregistry.InTreeBindings([]ecosystem.Strategy{row})) > 0
		},
		replay: (*replay).binding,
	},
	{
		name:        "bindings summary",
		golden:      "summary.golden.txt",
		afterRoutes: true,
		takesPart:   func(row ecosystem.Strategy) bool { return row.EnvExports != nil || row.HomeConfig != nil },
		replay:      (*replay).bindingsSummary,
	},
	{
		name:        "proxy rewrites",
		golden:      "rewrites.golden.json",
		asJSON:      true,
		afterRoutes: true,
		takesPart:   func(row ecosystem.Strategy) bool { return len(row.RewriteRows) > 0 },
		replay:      (*replay).rewrites,
	},
}

// StrategyLookup resolves a neighbour Strategy name from a case's case.json. A nil
// StrategyLookup is allowed for a corpus whose cases name no neighbour.
type StrategyLookup func(name string) (ecosystem.Strategy, bool)

// Run replays every case under corpusDir against row: one subtest per case. A
// corpusDir with no case fails. lookup resolves the neighbour Strategy names a
// case's case.json lists; it may be nil when no case names a neighbour.
func Run(t *testing.T, row ecosystem.Strategy, corpusDir string, lookup StrategyLookup) {
	t.Helper()
	update := golden.Update()
	for _, c := range caseDirs(t, corpusDir) {
		t.Run(c, func(t *testing.T) {
			for _, err := range replayCase(row, filepath.Join(corpusDir, c), update, lookup) {
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
// failing stage. A missing golden for a stage the Strategy takes part in fails like
// any other mismatch; update mode writes it instead. A case whose operator
// routes file is refused skips the stages that consume the resolved routes;
// classification, which reads the repo alone, still runs.
func replayCase(row ecosystem.Strategy, dir string, update bool, lookup StrategyLookup) []error {
	cfg, err := readCaseConfig(dir)
	if err != nil {
		return []error{err}
	}
	rows, err := cfg.resolveRows(row, lookup)
	if err != nil {
		return []error{err}
	}
	c := &replay{row: row, allRows: rows, port: bindregistry.ForwarderPort, dir: dir}
	if cfg.Port != nil {
		c.port = *cfg.Port
	}
	var errs []error
	for _, s := range stages {
		path := filepath.Join(dir, s.golden)
		if !s.takesPart(row) || (s.afterRoutes && c.refused()) {
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

// caseConfig is a case's case.json.
type caseConfig struct {
	Rows []string `json:"rows"`
	// Port is a pointer so an explicit "port": 0 is refused, not defaulted.
	Port *int `json:"port"`
}

func readCaseConfig(dir string) (caseConfig, error) {
	var cfg caseConfig
	raw, err := os.ReadFile(filepath.Join(dir, "case.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("case.json: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("case.json: %w", err)
	}
	if cfg.Port != nil && (*cfg.Port < 1 || *cfg.Port > 65535) {
		return cfg, fmt.Errorf("case.json: port %d is outside 1-65535", *cfg.Port)
	}
	return cfg, nil
}

// resolveRows is the case's Strategies in the order the pipeline reads them. Naming
// the primary Strategy fixes its position among the neighbours, which matters where
// production's ecosystem.Table order shows in a golden; left unnamed it comes
// first. A Strategy named twice would declare twice.
func (cfg caseConfig) resolveRows(primary ecosystem.Strategy, lookup StrategyLookup) ([]ecosystem.Strategy, error) {
	seen := map[string]bool{}
	var rows []ecosystem.Strategy
	for _, name := range cfg.Rows {
		if seen[name] {
			return nil, fmt.Errorf("case.json: row %q is named twice", name)
		}
		seen[name] = true
		if name == primary.Name {
			rows = append(rows, primary)
			continue
		}
		var row ecosystem.Strategy
		ok := false
		if lookup != nil {
			row, ok = lookup(name)
		}
		if !ok {
			return nil, fmt.Errorf("case.json: row %q is not known to the lookup", name)
		}
		rows = append(rows, row)
	}
	if !seen[primary.Name] {
		rows = append([]ecosystem.Strategy{primary}, rows...)
	}
	return rows, nil
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
	// row is the primary Strategy: the binding stage's renderers and in-tree gate,
	// the bindings summary (BindHomes) and the rewrites stage's RewriteRows use
	// it alone. allRows, which holds row, widens what the pipeline reads across
	// Strategies, including the rewrites stage's proxy routes.
	row     ecosystem.Strategy
	allRows []ecosystem.Strategy
	port    int
	dir     string

	rendered memo[[]byte]
	parsed   memo[[]registryroutes.Route]
	sets     memo[[]registrypathset.HostPathSet]
	proxied  memo[[]registryproxy.Route]
}

// primaryRows is what the stages that render only the case's own Strategy see.
func (c *replay) primaryRows() []ecosystem.Strategy { return []ecosystem.Strategy{c.row} }

func (c *replay) repo() string { return filepath.Join(c.dir, "repo") }

func (c *replay) classification() ([]byte, error) {
	return []byte(bindregistry.Classify(c.repo(), c.allRows) + "\n"), nil
}

// discovery hands Discover a repo-relative display path for each store, so
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
		routes, _, err := registrydiscover.Discover(
			c.repo(), c.allRows, stores,
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
		operator := err == nil
		if errors.Is(err, fs.ErrNotExist) {
			input, err = c.discovery()
		}
		if err != nil {
			return nil, err
		}
		routes, err := registryroutes.Parse(input, c.allRows)
		if err != nil && operator {
			return nil, &refusedError{err}
		}
		return routes, err
	})
}

// refusedError is Parse rejecting an operator-written routes file, which a
// case may pin as its outcome. Discovery's own output failing to parse is a
// bug in the Strategy, never a refusal, so it stays a plain error.
type refusedError struct{ error }

func (e *refusedError) Unwrap() error { return e.error }

func (c *replay) refused() bool {
	_, err := c.parsedRoutes()
	var r *refusedError
	return errors.As(err, &r)
}

func (c *replay) routes() ([]byte, error) {
	routes, err := c.parsedRoutes()
	var r *refusedError
	if errors.As(err, &r) {
		return json.Marshal(map[string]string{"refused": err.Error()})
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(routes)
}

func (c *replay) pathSets() ([]registrypathset.HostPathSet, error) {
	return c.sets.get(func() ([]registrypathset.HostPathSet, error) {
		return registrypathset.Derive(c.repo(), c.allRows)
	})
}

// pathSetOutput is the path-set stage's golden: the in-tree derivation, and the
// enforced view the Forwarder checks requests against, which also carries each
// route's declared path and allow entries.
type pathSetOutput struct {
	Derived  []registrypathset.HostPathSet `json:"derived"`
	Enforced []enforcedOutput              `json:"enforced"`
}

type enforcedOutput struct {
	MatchHost        string                  `json:"matchHost"`
	Upstream         string                  `json:"upstream"`
	EnforcedPaths    []string                `json:"enforcedPaths"`
	EnforcedSubtrees []registryvocab.Subtree `json:"enforcedSubtrees"`
}

func (c *replay) pathSet() ([]byte, error) {
	sets, err := c.pathSets()
	if err != nil {
		return nil, err
	}
	routes, err := c.proxyRoutes()
	if err != nil {
		return nil, err
	}
	out := pathSetOutput{Derived: sets, Enforced: make([]enforcedOutput, len(routes))}
	for i, r := range routes {
		out.Enforced[i] = enforcedOutput{
			MatchHost:        r.MatchHost,
			Upstream:         r.Upstream,
			EnforcedPaths:    r.EnforcedPaths,
			EnforcedSubtrees: r.EnforcedSubtrees,
		}
	}
	return json.Marshal(out)
}

// proxyRoutes is the proxy route table stages 4 to 6 run against: the
// production projection (registrypathset.ApplyToRoute, then
// registryproxy.AssignPrefixes) over the path sets derived from every Strategy the
// case names.
// Credentials stay empty: no secret is resolved.
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
			byHost[s.Host] = s
		}
		routes := make([]registryproxy.Route, len(parsed))
		for i, r := range parsed {
			route, err := registrypathset.ApplyToRoute(r.ProxyRoute(), byHost, c.allRows)
			if err != nil {
				return nil, err
			}
			routes[i] = route
		}
		return registryproxy.AssignPrefixes(routes), nil
	})
}

// manifestRoutes projects proxyRoutes through registryproxy.ManifestRoutes.
func (c *replay) manifestRoutes() ([]registrymanifest.Route, error) {
	routes, err := c.proxyRoutes()
	if err != nil {
		return nil, err
	}
	return registryproxy.ManifestRoutes(routes), nil
}
