package registrydiscover

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"path"
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

// ProbeResult is what the probe learned from one registry response.
type ProbeResult struct {
	// Scheme is the probe's "bearer" or "basic", or empty when the response
	// carried no usable challenge; a Host profile fill may instead supply any
	// routes-file scheme, including "header:<Name>".
	Scheme string
	// Header is the response's headers, for host-profile matching; nil when
	// unreachable.
	Header http.Header
	// Profile is the matched Host profile's advice; its zero Name means none
	// matched.
	Profile ProfileAdvice
	// SchemeFromProfile reports that Profile supplied Scheme because the probe
	// received none.
	SchemeFromProfile bool
}

// ProfileAdvice is a matched Host profile's advice, copied in by profileprobe
// so this package never imports hostprofile (ADR 0071).
type ProfileAdvice struct {
	Name, Note, BasicUsername, CredentialHelper string
	// Siblings maps an ecosystem name to path patterns relative to the registry
	// path a Target repo declares.
	Siblings map[string][]string
}

// Probe answers what an upstream base URL's registry reveals. The real probe
// reads the registry's WWW-Authenticate answer and response headers; it does
// not default to "bearer", so an empty Scheme means no usable challenge.
type Probe func(upstreamBaseURL string) ProbeResult

// Route is one proposed route, shaped to write directly into a registry routes
// file (registryroutes.Route) minus the optional keys this engine has no basis
// to guess (the per-ecosystem path declarations). Allow is set only from a Host
// profile's sibling paths.
type Route struct {
	MatchHost string
	// UpstreamBaseURL is not a routes file key: Render distills it down to an
	// upstream origin, and only when the scheme or port says something
	// match-host does not. It is kept here for the auth-scheme probe and the
	// credential-store match.
	UpstreamBaseURL string
	AuthScheme      string
	// Profile is ProbeResult.Profile.
	Profile ProfileAdvice
	// SchemeFromProfile is ProbeResult.SchemeFromProfile, kept only while
	// AuthScheme is the profile's own value.
	SchemeFromProfile bool
	// Allow is the Host profile's sibling paths for the ecosystems this host
	// serves, joined onto each declared registry path.
	Allow            []string
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

// Discover extracts registry declarations from repoDir over the given Strategies
// and proposes a route per unique host, searching stores in order for a
// matching credential. Each Strategy's CredentialStores bound which stores are
// queried for its declarations; a store outside a declaration's Strategy is named
// in StoresSearched but never queried.
func Discover(repoDir string, rows []ecosystem.Strategy, stores []Store, lookup Lookup, probe Probe) ([]Route, Report, error) {
	storesByEcosystem := ecosystem.CredentialStores(rows)
	declared, notes, err := Extract(repoDir, rows)
	if err != nil {
		return nil, Report{}, err
	}

	var routes []Route
	report := Report{NoRegistry: notes}
	seen := make(map[string]bool, len(declared))
	// The probe runs once per host, but its siblings apply to every ecosystem
	// declaring that host.
	byHost := make(map[string][]ecosystem.Declaration, len(declared))
	for _, d := range declared {
		host := registryvocab.HostKey(d.Host)
		byHost[host] = append(byHost[host], d)
	}
	for _, d := range declared {
		host := registryvocab.HostKey(d.Host)
		if seen[host] {
			continue
		}
		seen[host] = true

		matchedStore, searched, found := firstMatch(stores, storesByEcosystem, lookup, d)

		route := Route{
			MatchHost:       host,
			UpstreamBaseURL: d.UpstreamBaseURL,
		}
		res := probe(d.UpstreamBaseURL)
		route.AuthScheme = normalizeAuthScheme(res.Scheme)
		route.Profile = res.Profile
		// A profile scheme normalizeAuthScheme replaced is not the profile's any more.
		route.SchemeFromProfile = res.SchemeFromProfile && route.AuthScheme == res.Scheme
		route.Allow = siblingAllow(byHost[host], res.Profile)
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
		// The report stays valid through a collision; returning it lets the
		// caller show the operator which hosts were involved.
		return nil, report, err
	}

	return routes, report, nil
}

// siblingAllow joins each profile sibling for a declaration's ecosystem onto
// that declaration's registry path. A result of "/" is dropped: it would admit
// the whole host, which routes files refuse. A declaration contributes nothing
// when its URL does not parse, its subtree is "/" (path.Join clamps ".." at the
// root, so a sibling would land outside any declared subtree), or its decoded
// path is not canonical or carries "?" or "#" (the join would fail the routes
// file's allow validation or escape the declared subtree).
func siblingAllow(decls []ecosystem.Declaration, profile ProfileAdvice) []string {
	var allow []string
	for _, d := range decls {
		u, err := url.Parse(d.UpstreamBaseURL)
		if err != nil {
			continue
		}
		subtree := "/" + strings.Trim(u.Path, "/")
		if subtree == "/" || path.Clean(subtree) != subtree || strings.ContainsAny(subtree, "?#") {
			continue
		}
		for _, sibling := range profile.Siblings[d.Ecosystem] {
			if p := path.Join(subtree, sibling); p != "/" && !slices.Contains(allow, p) {
				allow = append(allow, p)
			}
		}
	}
	return allow
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
			slices.Sort(contested)
			parts := make([]string, len(contested))
			for i, name := range contested {
				hosts := make([]string, len(byName[name]))
				for j, idx := range byName[name] {
					hosts[j] = routes[idx].MatchHost
				}
				slices.Sort(hosts)
				parts[i] = fmt.Sprintf("%s (hosts %s)", name, strings.Join(hosts, ", "))
			}
			return fmt.Errorf("registrydiscover: could not disambiguate env placeholders after %d rounds: %s", round, strings.Join(parts, "; "))
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
// configured store name, so StoresSearched names even a store the declaration's
// Strategy does not read and never comes back empty. A lookup error counts as
// not-found and the search continues: one unreachable store must never abort
// discovery of the rest.
func firstMatch(stores []Store, storesByEcosystem map[string][]string, lookup Lookup, d ecosystem.Declaration) (store Store, searched []string, found bool) {
	rowStores := storesByEcosystem[d.Ecosystem]
	for _, s := range stores {
		searched = append(searched, s.Name)
		if !slices.Contains(rowStores, s.Name) {
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
