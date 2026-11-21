package cargo

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/ecosystemkit"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// nameCargo is Cargo's Name and its rewrite rows' tag: registryproxy matches a
// rewrite row only against subtrees tagged with the Strategy's name. The
// Classification "cargo" is the nudge's family grouping and only coincides.
const nameCargo = "cargo"

// Strategy binds through source replacement in a repo-aware home config, not
// an in-tree rewrite (issue #3201).
var Strategy = ecosystem.Strategy{
	Name:             nameCargo,
	CredentialStores: []string{"netrc", "npmrc", "gradle-properties", "cargo-credentials"},
	RetiredRouteKey:  ecosystem.CargoRetiredRouteKey,
	LockfileNames:    []string{"Cargo.lock"},
	Classification:   "cargo",
	InTreeConfigPath: ".cargo/config.toml",
	HomeConfig: &ecosystem.HomeConfig{
		HomeEnvVar:          "CARGO_HOME",
		HomeRelativeDefault: ".cargo",
		ConfigPath:          "config.toml",
		Render: func(port int, prefix string, routes []registrymanifest.Route) (string, bool) {
			return cargoConfigTOML(port, prefix, routes), true
		},
	},
	RepoAwareHomeConfig: cargoRepoAwareConfig,
	RewriteRows: []registryvocab.RewriteRow{{
		Name:      "cargo config.json",
		Ecosystem: nameCargo,
		Method:    http.MethodGet,
		Matches: func(routeRelativePath, base string) bool {
			return routeRelativePath == registryvocab.JoinBase(base, "/config.json")
		},
		Rewrite: rewriteCargoDL,
	}},
	ConfigParser:     parseCargoRegistryConfig,
	RouteDeclaration: validateCargoRouteDeclaration,
}

// validateCargoRouteDeclaration is Cargo's RouteDeclaration hook (issue
// #3403). Each name must match cargoBareKeyPattern: it only filters the repo's
// bare-key-filtered [registries.NAME] entries, so a non-bare name could never
// match, and the undeclared-registry warning interpolates it unquoted.
// Rejecting a repeated name is input hygiene only. Errors are predicates; the
// caller prefixes "<route>: <key> ".
func validateCargoRouteDeclaration(key string, value any) error {
	if key != ecosystem.CargoRouteRegistriesKey {
		return errors.New("is not a key cargo's route declaration accepts")
	}

	raw, ok := value.([]any)
	if !ok {
		return errors.New("must be an array of strings")
	}
	names := make([]string, len(raw))
	for i, elem := range raw {
		s, ok := elem.(string)
		if !ok {
			return errors.New("must be an array of strings")
		}
		names[i] = s
	}

	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" {
			return errors.New("names an empty string")
		}
		if !cargoBareKeyPattern.MatchString(name) {
			return fmt.Errorf("name %q must match %s", name, cargoBareKeyPattern.String())
		}
		if seen[name] {
			return fmt.Errorf("names %q more than once", name)
		}
		seen[name] = true
	}
	return nil
}

// cargoRouteRegistries reads a route's declared cargo registry names back out
// of blocks. It returns nil for a nil blocks, or one with no cargo entry.
func cargoRouteRegistries(blocks registryvocab.RouteEcosystems) []string {
	return blocks.Strings(nameCargo, ecosystem.CargoRouteRegistriesKey)
}

// rawCargoConfig is the [registries.*] slice of .cargo/config.toml. It does
// not DisallowUnknownFields: a real config carries many other tables.
type rawCargoConfig struct {
	Registries map[string]struct {
		Index string `toml:"index"`
	} `toml:"registries"`
}

// parseCargoRegistryConfig is Cargo's ConfigParser. It strips an index URL's
// "sparse+", cargo's protocol marker that the Forwarder and credential lookup
// never see; parseCargoRegistryDecls keeps the index verbatim instead.
func parseCargoRegistryConfig(content string) ([]ecosystem.Declaration, bool, error) {
	var raw rawCargoConfig
	if err := toml.Unmarshal([]byte(content), &raw); err != nil {
		return nil, false, err
	}

	names := slices.Sorted(maps.Keys(raw.Registries))

	var out []ecosystem.Declaration
	for _, name := range names {
		index := raw.Registries[name].Index
		stripped := strings.TrimPrefix(index, "sparse+")
		host, upstreamBaseURL, ok := ecosystemkit.HTTPAbsoluteURL(stripped)
		if !ok {
			// Skip a malformed index: only the file's own TOML syntax errors
			// the whole file.
			continue
		}
		out = append(out, ecosystem.Declaration{
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
			RegistryName:    name,
		})
	}

	// len(names) > 0 means the file named [registries.*] tables but every
	// index was unusable, distinct from a file naming no registry at all.
	return out, len(names) > 0, nil
}

// cargoRegistryDecl is one [registries.NAME] table found while scanning the
// Target repo's un-rewritten .cargo/config.toml (issue #3201). Index is
// verbatim (quotes stripped, "sparse+" kept): cargoSourceReplacements needs
// the real upstream URL byte-for-byte for the
// [source.spindrift-upstream-<name>] stanza.
type cargoRegistryDecl struct {
	Name  string
	Index string
}

// parseCargoRegistryDecls decodes the repo's own un-rewritten
// .cargo/config.toml for every [registries.NAME] table carrying an `index`,
// sorted by name. It decodes with go-toml like parseCargoRegistryConfig, so it
// reads every spelling discovery accepts and yields nil where discovery
// errors. Name order means two registries sharing one index path take their
// shared proxy name (and CARGO_REGISTRIES_*_TOKEN var) from the alphabetically
// first, not the first written.
func parseCargoRegistryDecls(content string) []cargoRegistryDecl {
	var raw rawCargoConfig
	if err := toml.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}

	var decls []cargoRegistryDecl
	for _, name := range sortedBareNames(raw.Registries) {
		if index := raw.Registries[name].Index; index != "" {
			decls = append(decls, cargoRegistryDecl{Name: name, Index: index})
		}
	}
	return decls
}

// sortedBareNames is the one place the cargoBareKeyPattern name filter lives:
// m's keys in name order, minus any failing the pattern.
func sortedBareNames[V any](m map[string]V) []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if cargoBareKeyPattern.MatchString(name) {
			names = append(names, name)
		}
	}
	return names
}

// cargoSourceDecl is one [source.NAME] table in the Target repo's un-rewritten
// .cargo/config.toml, keyed on `registry` rather than [registries.NAME]'s
// `index`. When the repo already claims a route's index URL (issue #3248),
// cargoSourceReplacements reuses that name rather than minting
// spindrift-upstream-<name>, as cargo's 1:1 URL to source-name rule demands.
type cargoSourceDecl struct {
	Name     string
	Registry string
}

// cargoSourceConfig is the [source.*] slice of .cargo/config.toml, decoded
// apart from rawCargoConfig so a mistyped source key cannot fail discovery.
type cargoSourceConfig struct {
	Source map[string]struct {
		Registry string `toml:"registry"`
	} `toml:"source"`
}

// parseCargoSourceDecls is parseCargoRegistryDecls' counterpart for every
// [source.NAME] table carrying a `registry`, with the same decode, ordering
// and name filter. A [source.NAME] carrying only `replace-with` claims no URL
// and yields no decl.
func parseCargoSourceDecls(content string) []cargoSourceDecl {
	var raw cargoSourceConfig
	if err := toml.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}

	var decls []cargoSourceDecl
	for _, name := range sortedBareNames(raw.Source) {
		if registry := raw.Source[name].Registry; registry != "" {
			decls = append(decls, cargoSourceDecl{Name: name, Registry: registry})
		}
	}
	return decls
}

// cargoUpstreamSource is one real-registry [source.NAME] stanza a
// cargoSourceReplacement replaces away from.
type cargoUpstreamSource struct {
	SourceName string
	IndexURL   string
}

// cargoSourceReplacement is the source-replacement plan for one distinct
// upstream index URL served on one manifest route (issues #3201, #3256). A
// route matching several registries yields one replacement per distinct index
// URL, all sharing its Prefix. Upstream is the registry it redirects away from.
type cargoSourceReplacement struct {
	Prefix        string
	ProxySource   string
	LocalIndexURL string
	Upstream      cargoUpstreamSource
}

// cargoIndexHost returns the host cargo would connect to for index, stripping
// a leading "sparse+" first, and reports whether index is a well-formed
// http(s) URL with a host. It uses u.Host, not u.Hostname():
// registrymanifest.Route.UpstreamHost is minted as u.Host and carries
// "host:port", so a port-stripped compare would drop every ported upstream.
func cargoIndexHost(index string) (string, bool) {
	raw := strings.TrimPrefix(index, "sparse+")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	return u.Host, true
}

// cargoRouteHostMatches reports whether indexHost, a cargoIndexHost result,
// names route's upstream. Hosts are case-insensitive and neither side is
// lowercased at its source, so case folds; the port still compares verbatim.
func cargoRouteHostMatches(indexHost string, route registrymanifest.Route) bool {
	return strings.EqualFold(indexHost, route.UpstreamHost)
}

// cargoIndexPath returns index's path component, stripping "sparse+" first,
// for a host-rooted route's per-registry local URL (issue #3256): a registry
// served at its own real path must resolve through that path locally, or the
// Forwarder's per-registry subtree never admits cargo's requests. The result
// is "" or a leading-"/", no-trailing-"/" path. It trims the decoded u.Path,
// so "%2F" and "/" collapse onto one local URL here while
// cargoCanonicalSourceURLKey keys them apart (#3256 behaviour, kept by #3251).
func cargoIndexPath(index string) string {
	raw := strings.TrimPrefix(index, "sparse+")
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Path, "/")
}

// cargoLocalIndexURL renders the Forwarder's sparse-protocol index URL for a
// route at prefix, the same shape cargoConfigTOML embeds, so string equality
// between two calls' results is cargo's own "same URL" test.
func cargoLocalIndexURL(port int, prefix string) string {
	return cargoLocalIndexURLWithPath(port, prefix, "")
}

// cargoLocalIndexURLWithPath renders the Forwarder's index URL for a registry
// at prefix whose upstream index lives at indexPath (issue #3256), so two
// registries sharing a host-rooted route's prefix resolve through two distinct
// URLs instead of folding onto one. indexPath is "" or a leading-"/",
// no-trailing-"/" path, so the result carries exactly one trailing "/".
func cargoLocalIndexURLWithPath(port int, prefix, indexPath string) string {
	return "sparse+http://127.0.0.1:" + strconv.Itoa(port) + "/" + prefix + indexPath + "/"
}

// registryProxySourceName is the crates-io replacement's own proxy source
// name, the one stanza cargoSourceReplacements must reuse rather than collide
// with under cargo's 1:1 URL to source-name rule.
const registryProxySourceName = "spindrift-registry-proxy"

// cargoCanonicalSourceURLKey keys value (a [source.NAME].registry or
// [registries.NAME].index) on cargo's own CanonicalUrl/SourceId axes: one
// trailing path slash trimmed and the host lowercased, plus url.Parse's scheme
// folding; the default port cargo also folds is left unfolded. "sparse+" is
// kept: cargo treats sparse and git indexes as different SourceIds (issue
// #3251). A value that is not an http(s) URL with a host keys as itself.
func cargoCanonicalSourceURLKey(value string) string {
	sparse := strings.HasPrefix(value, "sparse+")
	raw := strings.TrimPrefix(value, "sparse+")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return value
	}
	u.Host = strings.ToLower(u.Host)
	// Trim the escaped path's trailing slash and re-derive Path from it:
	// trimming only u.Path leaves RawPath stale, and u.String() then decodes
	// %2F into a structural "/" and drops the trim whenever the path holds
	// percent-encoding.
	escaped := strings.TrimSuffix(u.EscapedPath(), "/")
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		// Unreachable: EscapedPath() is valid by construction.
		return value
	}
	u.Path = decoded
	u.RawPath = escaped
	key := u.String()
	if sparse {
		key = "sparse+" + key
	}
	return key
}

// cargoSourceReplacements plans cargo source replacements (issue #3201, ADR
// 0044) from routes and the repo's un-rewritten .cargo/config.toml, and warns
// ("==> WARNING: "-prefixed) for each registry that bound to nothing. port and
// prefix must be cargoConfigTOML's own: cargo maps a URL to a source name 1:1,
// so a route landing on crates-io's local URL reuses spindrift-registry-proxy.
func cargoSourceReplacements(port int, prefix string, routes []registrymanifest.Route, repoConfig string) ([]cargoSourceReplacement, []string) {
	decls := parseCargoRegistryDecls(repoConfig)

	type hostedDecl struct {
		name  string
		host  string
		index string
	}
	hosted := make([]hostedDecl, 0, len(decls))
	for _, d := range decls {
		host, ok := cargoIndexHost(d.Index)
		if !ok {
			continue
		}
		hosted = append(hosted, hostedDecl{name: d.Name, host: host, index: d.Index})
	}

	// Table names the rendered home config already uses: reusing one as a repo
	// source name would be a duplicate-table TOML error.
	homeOwnedSourceNames := map[string]bool{
		"crates-io":             true,
		registryProxySourceName: true,
	}
	for _, route := range routes {
		if route.Prefix == "" {
			continue
		}
		// Over-reserves: a decl the later route filtering leaves unbound
		// still gets its name reserved.
		for _, d := range hosted {
			if cargoRouteHostMatches(d.host, route) {
				homeOwnedSourceNames[registryProxySourceName+"-"+route.Prefix+"-"+d.name] = true
			}
		}
	}
	for _, d := range decls {
		homeOwnedSourceNames["spindrift-upstream-"+d.Name] = true
	}

	// Each repo [source.NAME] claims its registry URL, first by name winning.
	// A home-owned name is excluded so the minting site below falls back to
	// its minted name.
	claimingSourceNameByURL := make(map[string]string)
	for _, sd := range parseCargoSourceDecls(repoConfig) {
		if homeOwnedSourceNames[sd.Name] {
			continue
		}
		key := cargoCanonicalSourceURLKey(sd.Registry)
		if _, claimed := claimingSourceNameByURL[key]; claimed {
			continue
		}
		claimingSourceNameByURL[key] = sd.Name
	}

	cratesIOLocalURL := cargoLocalIndexURL(port, prefix)
	sourceNameByLocalURL := map[string]string{cratesIOLocalURL: registryProxySourceName}

	var replacements []cargoSourceReplacement
	var warnings []string

	// The ownership pre-pass and the binding loop share this filter, so an
	// owner is always a route that goes on to bind the registry. undeclared is
	// a host match the route's explicit registries list excludes.
	routeCandidate := func(d hostedDecl, route registrymanifest.Route, routeRegistries []string) (binds, undeclared bool) {
		if !cargoRouteHostMatches(d.host, route) || !cargoBareKeyPattern.MatchString(d.name) {
			return false, false
		}
		if len(routeRegistries) > 0 && !slices.Contains(routeRegistries, d.name) {
			return false, true
		}
		return true, false
	}

	// A [source.X] carries one replace-with, so one route must own each
	// upstream source (issue #6272): the first route that explicitly declares
	// a matching registry, else the first unrestricted route on its host.
	ownerPrefix := make(map[string]string)
	ownerRegistry := make(map[string]string)
	ownerIsExplicit := make(map[string]bool)
	for _, route := range routes {
		if route.Prefix == "" || route.UpstreamHost == "" {
			continue
		}
		routeRegistries := cargoRouteRegistries(route.Ecosystems)
		explicit := len(routeRegistries) > 0
		for _, d := range hosted {
			if binds, _ := routeCandidate(d, route, routeRegistries); !binds {
				continue
			}
			key := cargoCanonicalSourceURLKey(d.index)
			if _, owned := ownerPrefix[key]; owned && (ownerIsExplicit[key] || !explicit) {
				continue
			}
			ownerPrefix[key] = route.Prefix
			ownerRegistry[key] = d.name
			ownerIsExplicit[key] = explicit
		}
	}

	for _, route := range routes {
		if route.Prefix == "" || route.UpstreamHost == "" {
			continue
		}

		routeRegistries := cargoRouteRegistries(route.Ecosystems)

		type matchedDecl struct {
			name       string
			index      string
			sourceName string
		}

		seenIndexURL := make(map[string]bool)
		bound := make(map[string]bool)
		var matched []matchedDecl
		for _, d := range hosted {
			binds, undeclared := routeCandidate(d, route, routeRegistries)
			if undeclared {
				warnings = append(warnings, "==> WARNING: cargo registry "+strconv.Quote(d.name)+" matches route prefix "+strconv.Quote(route.Prefix)+"'s upstream host but is not declared in that route's [routes.ecosystems."+nameCargo+"] "+ecosystem.CargoRouteRegistriesKey)
			}
			if !binds {
				continue
			}
			// Marked before the dedupe: a name collapsed into an earlier
			// name's stanza still binds through it.
			bound[d.name] = true
			// Two decls cargo treats as one SourceId dedupe to one stanza
			// (issue #3251).
			canonicalIndexURL := cargoCanonicalSourceURLKey(d.index)
			if seenIndexURL[canonicalIndexURL] {
				continue
			}
			seenIndexURL[canonicalIndexURL] = true
			if owner := ownerPrefix[canonicalIndexURL]; owner != route.Prefix {
				warnings = append(warnings, "==> WARNING: cargo registry "+strconv.Quote(d.name)+" matches route prefix "+strconv.Quote(route.Prefix)+"'s upstream host but is already bound to route prefix "+strconv.Quote(owner)+" as registry "+strconv.Quote(ownerRegistry[canonicalIndexURL])+", which shares that upstream host; one cargo source can be replaced through only one route, so route prefix "+strconv.Quote(route.Prefix)+" will not serve it")
				continue
			}
			// Reuse the repo's own claiming source name (issues #3248,
			// #3251) rather than minting a second stanza for the same URL.
			sourceName := "spindrift-upstream-" + d.name
			if claimed, ok := claimingSourceNameByURL[canonicalIndexURL]; ok {
				sourceName = claimed
			}
			matched = append(matched, matchedDecl{name: d.name, index: d.index, sourceName: sourceName})
		}

		for _, name := range routeRegistries {
			if bound[name] {
				continue
			}
			bound[name] = true // a name repeated in the declared list warns once
			// Unquoted interpolation is safe: validateCargoRouteDeclaration
			// (issue #3403) pinned every name to the bare-key pattern, and
			// registryroutes refuses the retired "cargo-registries" key.
			warnings = append(warnings, "==> WARNING: cargo registry "+strconv.Quote(name)+" is declared on route prefix "+strconv.Quote(route.Prefix)+" (upstream host "+strconv.Quote(route.UpstreamHost)+") but the repo's .cargo/config.toml has no [registries."+name+"] with a well-formed index URL on that host, so it will not be bound to the Forwarder -- cargo will try to reach the real registry directly, which a network-less Box cannot do; verify the route's declared cargo registries against the repo's .cargo/config.toml")
		}

		if len(matched) == 0 {
			// No placeholder export: with no [registries.<proxy-source>]
			// stanza it would be inert.
			continue
		}

		// One replacement per distinct upstream index URL, not per route
		// (issue #3256): registries sharing a route need their own local URLs
		// and proxy sources or the Forwarder's per-registry subtree could not
		// tell them apart.
		for _, m := range matched {
			localURL := cargoLocalIndexURLWithPath(port, route.Prefix, cargoIndexPath(m.index))
			proxySource, ok := sourceNameByLocalURL[localURL]
			if !ok {
				proxySource = registryProxySourceName + "-" + route.Prefix + "-" + m.name
				sourceNameByLocalURL[localURL] = proxySource
			}
			replacements = append(replacements, cargoSourceReplacement{
				Prefix:        route.Prefix,
				ProxySource:   proxySource,
				LocalIndexURL: localURL,
				Upstream:      cargoUpstreamSource{SourceName: m.sourceName, IndexURL: m.index},
			})
		}
	}

	return replacements, warnings
}

// cargoConfigTOMLWithReplacements renders the full $CARGO_HOME/config.toml
// (issue #3201): cargoConfigTOML's output followed by one stanza block per
// replacement, its [registries.NAME] half once per distinct ProxySource.
func cargoConfigTOMLWithReplacements(port int, prefix string, replacements []cargoSourceReplacement) string {
	base := cargoConfigTOML(port, prefix, nil)
	if len(replacements) == 0 {
		return base
	}

	var b strings.Builder
	b.WriteString(base)

	b.WriteString("\n[registry]\nglobal-credential-providers = [\"cargo:token\"]\n")

	// A repeated table name is a duplicate-table TOML error, not a merge, so
	// a shared ProxySource is emitted with its first replacement. The planner
	// already gives each upstream one owner (issue #6272); the upstream guard
	// only keeps a regression there from emitting invalid TOML.
	emittedProxySources := make(map[string]bool)
	emittedUpstreamSources := make(map[string]bool)

	for _, rep := range replacements {
		if !emittedUpstreamSources[rep.Upstream.SourceName] {
			emittedUpstreamSources[rep.Upstream.SourceName] = true
			fmt.Fprintf(&b, "\n[source.%s]\nregistry = %q\nreplace-with = %q\n", rep.Upstream.SourceName, rep.Upstream.IndexURL, rep.ProxySource)
		}

		if emittedProxySources[rep.ProxySource] {
			continue
		}
		emittedProxySources[rep.ProxySource] = true

		// The reused spindrift-registry-proxy stanza is already in base.
		if rep.ProxySource != registryProxySourceName {
			fmt.Fprintf(&b, "\n[source.%s]\nregistry = %q\n", rep.ProxySource, rep.LocalIndexURL)
		}

		fmt.Fprintf(&b, "\n[registries.%s]\nindex = %q\n", rep.ProxySource, rep.LocalIndexURL)
	}

	return b.String()
}

// cargoRepoAwareConfig is the cargo Strategy's RepoAwareHomeConfigRenderer
// (issue #3201), adding the placeholder exports cargo's credential lookup
// needs.
func cargoRepoAwareConfig(port int, prefix string, routes []registrymanifest.Route, repoConfig string) (content string, exports []ecosystem.EnvExport, warnings []string) {
	replacements, warnings := cargoSourceReplacements(port, prefix, routes, repoConfig)
	content = cargoConfigTOMLWithReplacements(port, prefix, replacements)
	exports = cargoReplacementPlaceholders(replacements)
	return content, exports, warnings
}

// cargoReplacementPlaceholders renders one CARGO_REGISTRIES_<PROXY-SOURCE>_TOKEN
// EnvExport per distinct ProxySource.
func cargoReplacementPlaceholders(replacements []cargoSourceReplacement) []ecosystem.EnvExport {
	seen := make(map[string]bool)
	var exports []ecosystem.EnvExport
	for _, r := range replacements {
		name := cargoRegistryEnvVarName(r.ProxySource)
		if seen[name] {
			continue
		}
		seen[name] = true
		exports = append(exports, ecosystem.EnvExport{Name: name, Value: cargoPlaceholderToken})
	}
	return exports
}

// cargoConfigTOML renders the $CARGO_HOME/config.toml content. Cargo does not
// proxy table-valued config through CARGO_<SECTION>_<KEY> (cargo#5416, still
// open), so unlike Go or npm this binding can only be written as a file.
// "sparse+" is required: the Forwarder speaks plain HTTP and serves no
// git-clonable index. routes is ignored, satisfying the signature (#3259).
func cargoConfigTOML(port int, prefix string, routes []registrymanifest.Route) string {
	return fmt.Sprintf(`[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:%d/%s/"
`, port, prefix)
}

// cargoBareKeyPattern matches cargo/TOML's bare-key charset. A quoted
// [registries."..."] name can carry arbitrary text ("$(...)") that would
// become an env var name in box exports and driver-exec bind-registry's
// sourceable env file, so a failing name must never reach a caller.
var cargoBareKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// cargoPlaceholderToken is the fixed, non-secret value emitted for every cargo
// source replacement (ADR 0044's issue #3053 amendment, re-keyed by #3201).
// cargo's client-side credential lookup aborts before the Forwarder is ever
// contacted unless something satisfies it locally; the Box to Forwarder hop
// stays unauthenticated and the Rewrite hook supplies the real credential.
const cargoPlaceholderToken = "spindrift-registry-proxy-placeholder-not-a-secret"

// cargoRegistryEnvVarName renders registryName into
// CARGO_REGISTRIES_<NAME>_TOKEN, uppercased with "-" mapped to "_", which is
// cargo's own convention for turning a [registries.NAME] name into an env var.
func cargoRegistryEnvVarName(registryName string) string {
	upper := strings.ToUpper(registryName)
	upper = strings.ReplaceAll(upper, "-", "_")
	return "CARGO_REGISTRIES_" + upper + "_TOKEN"
}

// cargoDLMarkers are the placeholders cargo substitutes in a registry's dl
// before fetching a crate.
var cargoDLMarkers = []string{"{crate}", "{version}", "{prefix}", "{lowerprefix}", "{sha256-checksum}"}

// rewriteCargoDL repoints a cargo sparse-index config.json's "dl" at the
// Forwarder with the route's prefix re-inserted, so crate downloads (built
// from dl) round-trip through the same route instead of going upstream. The
// caller does the logging.
func rewriteCargoDL(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	obj, ok := ecosystemkit.DecodeOneJSONObject(body)
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	dlRaw, ok := obj["dl"]
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	dlStr, ok := dlRaw.(string)
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	edit, ok := ecosystemkit.RepointRegistryURL(dlStr, rc)
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	if edit.To == "" {
		return registryvocab.RewriteResult{
			Body:    body,
			Edits:   []registryvocab.RewriteEdit{edit},
			Outcome: registryvocab.RewriteSkippedForeignHost,
		}
	}

	edit = ecosystemkit.SpliceTemplateRawPath(dlStr, edit, rc, cargoDLMarkers)
	obj["dl"] = edit.To

	newBody, err := json.Marshal(obj)
	if err != nil {
		// Unreachable: obj came from a successful decode.
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	return registryvocab.RewriteResult{
		Body:    newBody,
		Edits:   []registryvocab.RewriteEdit{edit},
		Outcome: registryvocab.RewriteApplied,
	}
}
