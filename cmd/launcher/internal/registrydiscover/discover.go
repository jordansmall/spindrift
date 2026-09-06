package registrydiscover

import (
	"fmt"
	"hash/fnv"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
)

// Store names one operator credential store discovery searches, in order.
type Store struct {
	Name string // "netrc" | "npmrc" | "cargo-credentials" | "gradle-properties"
	Path string // the store file's path, as written into credential references
}

// Lookup reports whether store holds a credential for the declaration --
// injected so engine tests run against no real store. It never returns the
// credential value; found is all the engine needs.
type Lookup func(store Store, d ecosystem.Declaration) (found bool, err error)

// Probe answers the auth scheme for an upstream base URL. Injected; the
// real probe reads the registry's WWW-Authenticate answer and defaults to
// "bearer" when unreachable.
type Probe func(upstreamBaseURL string) string

// Route is one proposed route, engine output -- shaped to write directly
// into a registry routes file (registryroutes.Route), minus the optional
// keys this engine has no basis to guess (allow, the per-ecosystem path
// declarations).
type Route struct {
	MatchHost string
	// UpstreamBaseURL is the full URL the config declared, kept for the
	// auth-scheme probe and the credential-store match. It is not a routes
	// file key: Render distills it down to an upstream-origin, and only when
	// the scheme or port says something match-host does not (see
	// upstreamOrigin).
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

// Report summarizes a Discover run for the operator: which declared hosts
// matched a store, which didn't, and which config files are present but
// declare no registry -- either naming no registry at all, or naming only
// unusable ones (carried through from Extract's Note, including its
// Skipped distinction).
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
			if matchedStore.Name == "cargo-credentials" {
				route.RegistryName = d.RegistryName
			}
			if matchedStore.Name == "gradle-properties" {
				route.PropertyKey = host
			}
			report.Matched = append(report.Matched, MatchedHost{Host: host, StoreName: matchedStore.Name, StorePath: matchedStore.Path})
		} else {
			route.CredentialSource = "env"
			route.CredentialValue = envPlaceholder(host)
			report.Unmatched = append(report.Unmatched, UnmatchedHost{Host: host, StoresSearched: searched})
		}
		routes = append(routes, route)
	}

	disambiguateEnvPlaceholders(routes)

	return routes, report, nil
}

// disambiguateEnvPlaceholders resolves envPlaceholder collisions among this
// run's unmatched routes: two hosts differing only in which byte a hyphen
// vs. a dot occupies (both fold to "_") would otherwise share one env var
// name, so a value an operator sets for one host would silently also reach
// the other. Every route sharing a base name gets a short host-keyed hash
// suffix -- deterministic per host and independent of declaration order, so
// a route's name never depends on what else happened to be discovered
// alongside it in a way an operator could not reproduce by hand. Hosts with
// a unique base name are untouched.
func disambiguateEnvPlaceholders(routes []Route) {
	byName := make(map[string][]int)
	for i, r := range routes {
		if r.CredentialSource != "env" {
			continue
		}
		byName[r.CredentialValue] = append(byName[r.CredentialValue], i)
	}
	for _, idxs := range byName {
		if len(idxs) < 2 {
			continue
		}
		for _, i := range idxs {
			routes[i].CredentialValue = fmt.Sprintf("%s_%s", routes[i].CredentialValue, hostHash(routes[i].MatchHost))
		}
	}
}

// hostHash renders an 8-hex-digit fnv32a hash of host -- short enough to
// keep the env var name readable, long enough that two distinct hosts
// colliding on the same base name essentially never also collide on the
// suffix.
func hostHash(host string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(host)) // fnv32a's Write never errors
	return fmt.Sprintf("%08X", h.Sum32())
}

// firstMatch searches stores in order for a credential matching d, returning
// the first hit plus every configured store name, in order (so the report's
// StoresSearched always names what was considered, even a store skipped as
// inapplicable, e.g. cargo-credentials for a non-cargo declaration -- a
// store list that skips every entry must never leave StoresSearched empty
// and the report naming nothing). A lookup error is treated as not-found and
// the search continues -- one unreachable store must never abort discovery
// of the rest.
func firstMatch(stores []Store, lookup Lookup, d ecosystem.Declaration) (store Store, searched []string, found bool) {
	for _, s := range stores {
		searched = append(searched, s.Name)
		// cargo-credentials keys its lookup on RegistryName, not the host --
		// a declaration with none (today, every non-cargo ecosystem, since
		// only cargo's ConfigParser populates the field) has nothing for
		// that lookup to key on, so it's still named above but never
		// actually queried.
		if s.Name == "cargo-credentials" && d.RegistryName == "" {
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

// normalizeAuthScheme accepts probe's answer verbatim when it is "bearer",
// "basic", or "header:<Name>" with Name a valid RFC 7230 token, and falls
// back to "bearer" for anything else -- mirrors
// registryroutes.validateAuthScheme's shape rule, but a bad probe answer
// here is a wrong guess to overwrite, not a routes-file error to reject.
func normalizeAuthScheme(scheme string) string {
	if scheme == "bearer" || scheme == "basic" {
		return scheme
	}
	if name, ok := strings.CutPrefix(scheme, "header:"); ok && registryvocab.IsValidHeaderFieldName(name) {
		return scheme
	}
	return "bearer"
}

// envPlaceholder names the environment variable an unmatched route's
// credential value points at -- a stable, readable name derived from the
// host so an operator can grep the routes file for what still needs wiring.
// This is a base name only: two hosts that fold to the same base name are
// disambiguated afterward by disambiguateEnvPlaceholders.
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
