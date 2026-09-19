package registrydiscover

import (
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
)

// Store names one operator credential store discovery searches, in order.
type Store struct {
	Name string // "netrc" | "npmrc" | "cargo-credentials" | "gradle-properties"
	Path string // the store file's path, as written into credential references
}

// Lookup reports whether store holds a credential for the declaration. It is
// injected so engine tests run against no real store, and never returns the
// credential value.
type Lookup func(store Store, d ecosystem.Declaration) (found bool, err error)

// Probe answers the auth scheme for an upstream base URL. The real probe reads
// the registry's WWW-Authenticate answer and defaults to "bearer" when the
// registry is unreachable.
type Probe func(upstreamBaseURL string) string

// Route is one proposed route, shaped to write directly into a registry routes
// file (registryroutes.Route) minus the optional keys this engine has no basis
// to guess (allow, the per-ecosystem path declarations).
type Route struct {
	MatchHost string
	// UpstreamBaseURL is not a routes file key: Render distills it down to an
	// upstream origin, and only when the scheme or port says something
	// match-host does not. It is kept here for the auth-scheme probe and the
	// credential-store match.
	UpstreamBaseURL  string
	AuthScheme       string
	CredentialSource string // "netrc"|"npmrc"|"cargo-credentials"|"gradle-properties"|"env" (env = placeholder for unmatched)
	CredentialValue  string // store path, or placeholder env var name
	RegistryName     string // companion registry-name, cargo-credentials only
	PropertyKey      string // companion key, gradle-properties only
}

// MatchedHost is a report entry for a host discovery matched to a store.
type MatchedHost struct {
	Host      string
	StoreName string
	StorePath string
}

// UnmatchedHost is a report entry for a host discovery could not match to
// any store.
type UnmatchedHost struct {
	Host           string
	StoresSearched []string
}

// Report summarizes a Discover run: which declared hosts matched a store,
// which did not, and which config files declare no usable registry (carried
// through from Extract's Note, including its Skipped distinction).
type Report struct {
	Matched    []MatchedHost
	Unmatched  []UnmatchedHost
	NoRegistry []ecosystem.Note
}

// Discover extracts registry declarations from repoDir and proposes a route
// per unique host, searching stores in order for a matching credential.
func Discover(repoDir string, stores []Store, lookup Lookup, probe Probe) ([]Route, Report, error) {
	declared, notes, err := Extract(repoDir)
	if err != nil {
		return nil, Report{}, err
	}

	var routes []Route
	report := Report{NoRegistry: notes}
	seen := make(map[string]bool, len(declared))
	for _, d := range declared {
		host := registryvocab.HostKey(d.Host)
		if seen[host] {
			continue
		}
		seen[host] = true

		matchedStore, searched, found := firstMatch(stores, lookup, d)

		route := Route{
			MatchHost:       host,
			UpstreamBaseURL: d.UpstreamBaseURL,
			AuthScheme:      normalizeAuthScheme(probe(d.UpstreamBaseURL)),
		}
		if found {
			route.CredentialSource = matchedStore.Name
			route.CredentialValue = matchedStore.Path
			// Each kind's own StoreConfig already names every companion it
			// contributes, so Discover assigns both unconditionally rather
			// than branching per store name.
			if kind, ok := credresolver.KindBySourceKey(matchedStore.Name); ok && kind.StoreConfig != nil {
				cfg := kind.StoreConfig(matchedStore.Path, declarationFacts(d))
				route.RegistryName = cfg.RegistryName
				route.PropertyKey = cfg.PropertyKey
			}
			report.Matched = append(report.Matched, MatchedHost{Host: host, StoreName: matchedStore.Name, StorePath: matchedStore.Path})
		} else {
			route.CredentialSource = "env"
			route.CredentialValue = envPlaceholder(host)
			report.Unmatched = append(report.Unmatched, UnmatchedHost{Host: host, StoresSearched: searched})
		}
		routes = append(routes, route)
	}

	if err := disambiguateEnvPlaceholders(routes); err != nil {
		return nil, Report{}, err
	}

	return routes, report, nil
}

// disambiguateEnvPlaceholders resolves envPlaceholder collisions among this
// run's unmatched routes: envPlaceholder folds hyphens and dots alike to "_",
// so two hosts can share one name and a value an operator sets for one host
// silently reaches the other. It runs to a fixpoint against the whole table,
// not one pass, because suffixing a colliding name can itself collide with
// another route's untouched base name or with another round's suffix. Each
// round re-buckets by the current CredentialValue and suffixes every member
// of every 2+ bucket, never just the "losers" of some claim order, so the
// result depends only on the set of hosts, not on declaration order or Go's
// randomized map iteration order.
func disambiguateEnvPlaceholders(routes []Route) error {
	var envIdxs []int
	for i, r := range routes {
		if r.CredentialSource == "env" {
			envIdxs = append(envIdxs, i)
		}
	}

	// hostHash is fixed-width, so a route touched by a previous round
	// already ends in "_"+hostHash(its own host), and two touched routes
	// can collide again only if their hosts hash equal. Absent that, every
	// round with a remaining collision must suffix at least one route for
	// the first time, so len(envIdxs)+1 rounds is always enough; hitting
	// the bound means a real hostHash collision.
	for round := 0; ; round++ {
		byName := make(map[string][]int)
		for _, i := range envIdxs {
			byName[routes[i].CredentialValue] = append(byName[routes[i].CredentialValue], i)
		}

		var contested []string
		for name, idxs := range byName {
			if len(idxs) >= 2 {
				contested = append(contested, name)
			}
		}
		if len(contested) == 0 {
			return nil
		}
		if round == len(envIdxs) {
			return fmt.Errorf("registrydiscover: could not disambiguate env placeholder %q after %d rounds", slices.Min(contested), round)
		}
		for _, name := range contested {
			for _, i := range byName[name] {
				routes[i].CredentialValue = fmt.Sprintf("%s_%s", routes[i].CredentialValue, hostHash(routes[i].MatchHost))
			}
		}
	}
}

// hostHash renders host as 8 hex digits of fnv32a: short enough to keep the
// env var name readable, long enough that two hosts colliding on one base
// name essentially never also collide on the suffix.
func hostHash(host string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(host)) // fnv32a's Write never errors
	return fmt.Sprintf("%08X", h.Sum32())
}

// declarationFacts fills Host and HostKey separately because the stores key on
// different ones: npmrc on the raw host, gradle-properties on the HostKey.
func declarationFacts(d ecosystem.Declaration) credresolver.StoreFacts {
	return credresolver.StoreFacts{
		Host:            d.Host,
		HostKey:         registryvocab.HostKey(d.Host),
		UpstreamBaseURL: d.UpstreamBaseURL,
		RegistryName:    d.RegistryName,
	}
}

// firstMatch searches stores in order and returns the first hit plus every
// configured store name, so StoresSearched names even a store skipped as
// inapplicable and never comes back empty. A lookup error counts as not-found
// and the search continues: one unreachable store must never abort discovery
// of the rest.
func firstMatch(stores []Store, lookup Lookup, d ecosystem.Declaration) (store Store, searched []string, found bool) {
	facts := declarationFacts(d)
	for _, s := range stores {
		searched = append(searched, s.Name)
		// StoreApplicable (cargo-credentials only today: a declaration with
		// no RegistryName gives that lookup nothing to key on) marks a store
		// with nothing to search here, named above but never queried.
		if kind, ok := credresolver.KindBySourceKey(s.Name); ok && kind.StoreApplicable != nil && !kind.StoreApplicable(facts) {
			continue
		}
		ok, err := lookup(s, d)
		if err != nil || !ok {
			continue
		}
		return s, searched, true
	}
	return Store{}, searched, false
}

// normalizeAuthScheme applies registryroutes.validateAuthScheme's shape rule to
// probe's answer, but falls back to "bearer" instead of rejecting: a bad probe
// answer is a wrong guess for the operator to overwrite, not a routes-file error.
func normalizeAuthScheme(scheme string) string {
	if scheme == "bearer" || scheme == "basic" {
		return scheme
	}
	if name, ok := strings.CutPrefix(scheme, "header:"); ok && registryvocab.IsValidHeaderFieldName(name) {
		return scheme
	}
	return "bearer"
}

// envPlaceholder derives a stable, readable env var name from the host so an
// operator can grep the routes file for what still needs wiring. It returns a
// base name only; disambiguateEnvPlaceholders resolves collisions afterward.
func envPlaceholder(host string) string {
	var b strings.Builder
	b.WriteString("SPINDRIFT_REGISTRY_CREDENTIAL_")
	for _, c := range []byte(host) {
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
