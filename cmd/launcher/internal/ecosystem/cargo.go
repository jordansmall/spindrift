package ecosystem

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// nameCargo is cargoRow's Name. Cargo's rewrite rows tag themselves with it
// too: registryproxy matches a rewrite row only against subtrees tagged with
// the row's name, so the two spellings must never drift apart. Classification
// below spells "cargo" as well, but that is the nudge's family grouping, a
// separate concept that only coincides here.
const nameCargo = "cargo"

// CargoRetiredRouteKey is cargo's Row.RetiredRouteKey. Exported because
// registryroutes never spells the key itself.
const CargoRetiredRouteKey = "cargo-registries"

var cargoRow = Row{
	Name:             nameCargo,
	RetiredRouteKey:  CargoRetiredRouteKey,
	LockfileNames:    []string{"Cargo.lock"},
	Classification:   "cargo",
	InTreeConfigPath: ".cargo/config.toml",
	HomeConfig: &HomeConfig{
		HomeEnvVar:          "CARGO_HOME",
		HomeRelativeDefault: ".cargo",
		ConfigPath:          "config.toml",
		Render:              CargoConfigTOML,
	},
	RepoAwareHomeConfig: CargoRepoAwareConfig,
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

// CargoRouteRegistriesKey is the one key (besides "path") a
// [routes.ecosystems.cargo] block may carry. Exported so no consumer outside
// this package spells "cargo" or "registries" itself (ADR 0048).
const CargoRouteRegistriesKey = "registries"

// validateCargoRouteDeclaration is cargoRow's RouteDeclaration hook (issue
// #3403). Each name must match cargoBareKeyPattern and appear once: a name
// becomes a CARGO_REGISTRIES_<NAME>_TOKEN shell env var, so anything outside
// [A-Za-z0-9_-] risks smuggling shell metadata into a sourced env file.
// Errors are bare noun-phrases; the caller prefixes the key and the route.
func validateCargoRouteDeclaration(key string, value any) error {
	if key != CargoRouteRegistriesKey {
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

// CargoRouteRegistries reads a route's declared cargo registry names back out
// of blocks. It returns nil for a nil blocks, or one with no cargo entry.
func CargoRouteRegistries(blocks registryvocab.RouteEcosystems) []string {
	return blocks.Strings(nameCargo, CargoRouteRegistriesKey)
}

// CargoRouteBlock builds the ecosystems block a routes file's
// [routes.ecosystems.cargo] table projects into. Names go through
// registryvocab.StringsValue, so a hand-built block is identical to one a
// TOML decode or a manifest JSON round trip produces.
func CargoRouteBlock(names ...string) registryvocab.RouteEcosystems {
	return registryvocab.RouteEcosystems{
		nameCargo: registryvocab.RouteDeclaration{
			CargoRouteRegistriesKey: registryvocab.StringsValue(names),
		},
	}
}

// rawCargoConfig is the decode shape for the [registries.*] slice of
// .cargo/config.toml this package cares about. It deliberately does not
// DisallowUnknownFields: a real Cargo config carries many other tables
// ([source], [net], [build], ...) that are none of this package's business.
type rawCargoConfig struct {
	Registries map[string]struct {
		Index string `toml:"index"`
	} `toml:"registries"`
}

// parseCargoRegistryConfig is cargoRow's ConfigParser: it decodes a
// .cargo/config.toml for its [registries.<name>] entries. An index URL's
// leading "sparse+" is stripped because that prefix is cargo's own marker for
// the sparse protocol, not part of the URL the Forwarder or credential lookup
// ever sees. ParseCargoRegistryDecls answers a different question.
func parseCargoRegistryConfig(content string) ([]Declaration, bool, error) {
	var raw rawCargoConfig
	if err := toml.Unmarshal([]byte(content), &raw); err != nil {
		return nil, false, err
	}

	// Map iteration order is randomized; sort so output is deterministic.
	names := make([]string, 0, len(raw.Registries))
	for name := range raw.Registries {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []Declaration
	for _, name := range names {
		index := raw.Registries[name].Index
		stripped := strings.TrimPrefix(index, "sparse+")
		host, upstreamBaseURL, ok := httpAbsoluteURL(stripped)
		if !ok {
			// Skip the entry rather than erroring the whole file; only the
			// file's own TOML syntax is an error.
			continue
		}
		// raw.Registries' keys are already unique, so nothing needs deduping
		// here, unlike in the line-scanning parsers.
		out = append(out, Declaration{
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
			RegistryName:    name,
		})
	}

	// len(names) > 0 means the file named [registries.*] tables but every
	// index was unusable, distinct from a file naming no registry at all.
	return out, len(names) > 0, nil
}

// CargoRegistryDecl is one [registries.NAME] table found while scanning the
// Target repo's un-rewritten .cargo/config.toml (issue #3201). Index is
// verbatim (quotes stripped, "sparse+" kept): CargoSourceReplacements needs
// the real upstream URL byte-for-byte for the
// [source.spindrift-upstream-<name>] stanza.
type CargoRegistryDecl struct {
	Name  string
	Index string
}

// ParseCargoRegistryDecls scans the repo's own tracked .cargo/config.toml,
// before any rewrite, for every [registries.NAME] table carrying an `index`
// assignment, in header-appearance order. A name failing cargoBareKeyPattern
// is skipped: an untrusted name must never reach a caller that could turn it
// into a shell-sourced env var name. A repeated name keeps its first table.
func ParseCargoRegistryDecls(content string) []CargoRegistryDecl {
	raw := scanCargoNamedTable(content, "registries.", "index")
	decls := make([]CargoRegistryDecl, len(raw))
	for i, d := range raw {
		decls[i] = CargoRegistryDecl{Name: d.name, Index: d.value}
	}
	return decls
}

// CargoSourceDecl is one [source.NAME] table in the Target repo's un-rewritten
// .cargo/config.toml, keyed on `registry` rather than [registries.NAME]'s
// `index`. When the repo already claims a route's index URL (issue #3248),
// CargoSourceReplacements reuses that name rather than minting
// spindrift-upstream-<name>, as cargo's 1:1 URL to source-name rule demands.
type CargoSourceDecl struct {
	Name     string
	Registry string
}

// ParseCargoSourceDecls scans content for every [source.NAME] table carrying
// a `registry` assignment, in header-appearance order. It shares
// ParseCargoRegistryDecls' contract through scanCargoNamedTable; only the
// header prefix and key differ. A [source.NAME] carrying only `replace-with`
// claims no URL and yields no decl.
func ParseCargoSourceDecls(content string) []CargoSourceDecl {
	raw := scanCargoNamedTable(content, "source.", "registry")
	decls := make([]CargoSourceDecl, len(raw))
	for i, d := range raw {
		decls[i] = CargoSourceDecl{Name: d.name, Registry: d.value}
	}
	return decls
}

type namedCargoTableDecl struct {
	name  string
	value string
}

// scanCargoNamedTableOccurrences is scanCargoNamedTable's core line scan,
// split out for a caller that needs to see repeats: it applies neither the
// untrusted-name filter nor the dedupe, and reports a matching header with no
// matching key line as an empty-value decl. A section runs from its
// "[<headerPrefix>NAME]" header to the next "[...]" header or EOF.
func scanCargoNamedTableOccurrences(content, headerPrefix, key string) []namedCargoTableDecl {
	lines := strings.Split(content, "\n")

	var decls []namedCargoTableDecl

	inSection := false
	sectionName := ""
	sectionValue := ""
	haveValue := false

	flush := func() {
		if inSection {
			decls = append(decls, namedCargoTableDecl{name: sectionName, value: sectionValue})
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			// New header: close out whatever section (if any) precedes it.
			flush()

			header := strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			name, ok := strings.CutPrefix(header, headerPrefix)
			if ok {
				inSection = true
				sectionName = name
				sectionValue = ""
				haveValue = false
			} else {
				inSection = false
				sectionName = ""
				sectionValue = ""
				haveValue = false
			}
			continue
		}

		if inSection && !haveValue {
			k, value, ok := strings.Cut(trimmed, "=")
			if ok && strings.TrimSpace(k) == key {
				// First matching-key line wins even when malformed:
				// haveValue latches either way, so a rejected value leaves
				// the section value-less rather than falling through.
				sectionValue, _ = cargoTOMLStringValue(value)
				haveValue = true
			}
		}
	}
	flush()

	return decls
}

// scanCargoNamedTable is the line-based scan ParseCargoRegistryDecls and
// ParseCargoSourceDecls share, parameterized on header prefix and key. Over
// scanCargoNamedTableOccurrences it drops a name failing cargoBareKeyPattern,
// so an untrusted name never reaches a caller that could turn it into a shell
// variable name, drops a keyless table, and dedupes a repeated name.
func scanCargoNamedTable(content, headerPrefix, key string) []namedCargoTableDecl {
	raw := scanCargoNamedTableOccurrences(content, headerPrefix, key)

	var decls []namedCargoTableDecl
	seen := make(map[string]bool)
	for _, d := range raw {
		if d.value == "" || seen[d.name] || !cargoBareKeyPattern.MatchString(d.name) {
			continue
		}
		seen[d.name] = true
		decls = append(decls, d)
	}
	return decls
}

// cargoTOMLStringValue extracts the string a TOML key assigns, allowing
// nothing after the close quote but an optional "#" comment. The strictness is
// load-bearing: an index with trailing junk still parses as a URL matching its
// route and lands in a [source.NAME] stanza cargo never uses, so the replacement
// silently never binds. It is escape-unaware, so an escaped quote truncates.
func cargoTOMLStringValue(value string) (string, bool) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", false
	}

	quote := v[0]
	if quote != '"' && quote != '\'' {
		return "", false
	}

	rest := v[1:]
	end := strings.IndexByte(rest, quote)
	if end < 0 {
		return "", false
	}

	trailer := strings.TrimSpace(rest[end+1:])
	if trailer != "" && !strings.HasPrefix(trailer, "#") {
		return "", false
	}

	return rest[:end], true
}

// CargoUpstreamSource is one real-registry [source.NAME] stanza a
// CargoSourceReplacement replaces away from.
type CargoUpstreamSource struct {
	SourceName string
	IndexURL   string
}

// CargoSourceReplacement is the source-replacement plan for one manifest route
// with at least one matching declared cargo registry (issue #3201).
type CargoSourceReplacement struct {
	Prefix        string
	ProxySource   string
	LocalIndexURL string
	Upstreams     []CargoUpstreamSource
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

// cargoIndexPath returns index's path component, stripping "sparse+" first,
// for a host-rooted route's per-registry local URL (issue #3256): a registry
// served at its own real path must resolve through that path locally, or the
// Forwarder's per-registry subtree never admits the requests cargo sends. The
// result is always "" or a leading-"/", no-trailing-"/" path.
func cargoIndexPath(index string) string {
	raw := strings.TrimPrefix(index, "sparse+")
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Path, "/")
}

// cargoLocalIndexURL renders the Forwarder's sparse-protocol index URL for a
// route at prefix, the same shape CargoConfigTOML embeds, so string equality
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
// name, the one stanza CargoSourceReplacements must reuse rather than collide
// with under cargo's 1:1 URL to source-name rule.
const registryProxySourceName = "spindrift-registry-proxy"

// CargoSourceReplacements plans cargo source replacements (issue #3201, ADR
// 0044) from routes and the repo's un-rewritten .cargo/config.toml, and warns
// ("==> WARNING: "-prefixed) for each registry that bound to nothing. port and
// prefix must be CargoConfigTOML's own: cargo maps a URL to a source name 1:1,
// so a route landing on crates-io's local URL reuses spindrift-registry-proxy.
func CargoSourceReplacements(port int, prefix string, routes []registrymanifest.Route, repoConfig string) ([]CargoSourceReplacement, []string) {
	decls := ParseCargoRegistryDecls(repoConfig)

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

	// These are the table names the rendered home config already uses. A repo
	// source name equal to one of them can never be reused: cargo requires
	// [source.NAME] table names unique within one file, so reuse would be a
	// duplicate-table TOML error, not a mere URL collision.
	homeOwnedSourceNames := map[string]bool{
		"crates-io":             true,
		registryProxySourceName: true,
	}
	for _, route := range routes {
		if route.Prefix == "" {
			continue
		}
		// Over-reserve rather than under-reserve: the route's upstream
		// filtering happens later, so a decl that will end up unbound still
		// gets its name reserved here.
		for _, d := range hosted {
			if d.host == route.UpstreamHost {
				homeOwnedSourceNames[registryProxySourceName+"-"+route.Prefix+"-"+d.name] = true
			}
		}
	}
	for _, d := range decls {
		homeOwnedSourceNames["spindrift-upstream-"+d.Name] = true
	}

	// Maps a repo-declared [source.NAME]'s registry URL to NAME,
	// first-occurrence-wins. A guarded name is excluded up front so the
	// minting site below falls back to its minted name rather than reusing it.
	claimingSourceNameByURL := make(map[string]string)
	for _, sd := range ParseCargoSourceDecls(repoConfig) {
		if homeOwnedSourceNames[sd.Name] {
			continue
		}
		if _, claimed := claimingSourceNameByURL[sd.Registry]; claimed {
			continue
		}
		claimingSourceNameByURL[sd.Registry] = sd.Name
	}

	cratesIOLocalURL := cargoLocalIndexURL(port, prefix)
	sourceNameByLocalURL := map[string]string{cratesIOLocalURL: registryProxySourceName}

	var replacements []CargoSourceReplacement
	var warnings []string

	for _, route := range routes {
		if route.Prefix == "" || route.UpstreamHost == "" {
			continue
		}

		routeRegistries := CargoRouteRegistries(route.Ecosystems)

		var declared map[string]bool
		if len(routeRegistries) > 0 {
			declared = make(map[string]bool, len(routeRegistries))
			for _, name := range routeRegistries {
				declared[name] = true
			}
		}

		type matchedDecl struct {
			name       string
			index      string
			sourceName string
		}

		seenIndexURL := make(map[string]bool)
		bound := make(map[string]bool)
		var matched []matchedDecl
		for _, d := range hosted {
			if d.host != route.UpstreamHost {
				continue
			}
			if !cargoBareKeyPattern.MatchString(d.name) {
				continue
			}
			if declared != nil && !declared[d.name] {
				warnings = append(warnings, "==> WARNING: cargo registry "+strconv.Quote(d.name)+" matches route prefix "+strconv.Quote(route.Prefix)+"'s upstream host but is not declared in that route's cargo-registries")
				continue
			}
			// Marked before the dedupe check on purpose: a name collapsed
			// into an earlier name's stanza still binds through it.
			bound[d.name] = true
			if seenIndexURL[d.index] {
				continue
			}
			seenIndexURL[d.index] = true
			// Reuse the repo's own claiming source name (issue #3248) when one
			// exists for this exact index URL, byte-for-byte; cargo would
			// otherwise reject the merged config as a duplicate source.
			sourceName := "spindrift-upstream-" + d.name
			if claimed, ok := claimingSourceNameByURL[d.index]; ok {
				sourceName = claimed
			}
			matched = append(matched, matchedDecl{name: d.name, index: d.index, sourceName: sourceName})
		}

		for _, name := range routeRegistries {
			if bound[name] {
				continue
			}
			bound[name] = true // a name repeated in the declared list warns once
			// name is interpolated unquoted into "[registries.<name>]" here,
			// safe because validateCargoRouteDeclaration (issue #3403) pins
			// every cargo block entry to the bare-key pattern when the host
			// parses the routes file. The retired "cargo-registries" key
			// translates into that same block, so both spellings are pinned.
			warnings = append(warnings, "==> WARNING: cargo registry "+strconv.Quote(name)+" is declared on route prefix "+strconv.Quote(route.Prefix)+" (upstream host "+strconv.Quote(route.UpstreamHost)+") but the repo's .cargo/config.toml has no [registries."+name+"] with a well-formed index URL on that host, so it will not be bound to the Forwarder -- cargo will try to reach the real registry directly, which a network-less Box cannot do; verify the route's declared cargo registries against the repo's .cargo/config.toml")
		}

		if len(matched) == 0 {
			// This fabricates no placeholder export on purpose: with no
			// replacement there is no [registries.<proxy-source>] stanza for
			// cargo to look a token up against, so the export would be inert.
			// The declared-name warnings above are the coverage instead.
			continue
		}

		// One CargoSourceReplacement per distinct upstream index URL (issue
		// #3256), not one per route: a route serves its upstream host's real
		// path layout, so two registries sharing it need their own local URLs
		// and minted proxy sources, or the Forwarder's per-registry subtree
		// could never tell them apart.
		for _, m := range matched {
			localURL := cargoLocalIndexURLWithPath(port, route.Prefix, cargoIndexPath(m.index))
			proxySource, ok := sourceNameByLocalURL[localURL]
			if !ok {
				proxySource = registryProxySourceName + "-" + route.Prefix + "-" + m.name
				sourceNameByLocalURL[localURL] = proxySource
			}
			replacements = append(replacements, CargoSourceReplacement{
				Prefix:        route.Prefix,
				ProxySource:   proxySource,
				LocalIndexURL: localURL,
				Upstreams:     []CargoUpstreamSource{{SourceName: m.sourceName, IndexURL: m.index}},
			})
		}
	}

	return replacements, warnings
}

// CargoConfigTOMLWithReplacements renders the full $CARGO_HOME/config.toml
// once source-replacement stanzas are known (issue #3201): CargoConfigTOML's
// output, unchanged, followed by one stanza block per replacement, with the
// [registries.NAME] half emitted once per distinct ProxySource. An empty
// replacements slice returns CargoConfigTOML's output verbatim.
func CargoConfigTOMLWithReplacements(port int, prefix string, replacements []CargoSourceReplacement) string {
	base := CargoConfigTOML(port, prefix, nil)
	if len(replacements) == 0 {
		return base
	}

	var b strings.Builder
	b.WriteString(base)

	b.WriteString("\n[registry]\nglobal-credential-providers = [\"cargo:token\"]\n")

	// Two replacements can share one ProxySource, and repeating a table name
	// in one file is a duplicate-table TOML error, not a merge, so the pair is
	// emitted with the first replacement that claims the name.
	emittedProxySources := make(map[string]bool)

	for _, rep := range replacements {
		for _, up := range rep.Upstreams {
			fmt.Fprintf(&b, "\n[source.%s]\nregistry = %q\nreplace-with = %q\n", up.SourceName, up.IndexURL, rep.ProxySource)
		}

		if emittedProxySources[rep.ProxySource] {
			continue
		}
		emittedProxySources[rep.ProxySource] = true

		// The reused spindrift-registry-proxy source's stanza is already in
		// base, and emitting it again would collide on the same URL under
		// cargo's 1:1 rule, so only a freshly minted proxy source gets one.
		if rep.ProxySource != registryProxySourceName {
			fmt.Fprintf(&b, "\n[source.%s]\nregistry = %q\n", rep.ProxySource, rep.LocalIndexURL)
		}

		fmt.Fprintf(&b, "\n[registries.%s]\nindex = %q\n", rep.ProxySource, rep.LocalIndexURL)
	}

	return b.String()
}

// CargoRepoAwareConfig is the cargo row's RepoAwareHomeConfigRenderer (issue
// #3201): it plans source replacements from the cloned repo's own un-rewritten
// .cargo/config.toml, renders $CARGO_HOME/config.toml around that plan, and
// derives the placeholder exports cargo's credential lookup needs. A repo with
// no declared registries yields CargoConfigTOML's base render and no exports.
func CargoRepoAwareConfig(port int, prefix string, routes []registrymanifest.Route, repoConfig string) (content string, exports []EnvExport, warnings []string) {
	replacements, warnings := CargoSourceReplacements(port, prefix, routes, repoConfig)
	content = CargoConfigTOMLWithReplacements(port, prefix, replacements)
	exports = CargoReplacementPlaceholders(replacements)
	return content, exports, warnings
}

// CargoReplacementPlaceholders renders replacements into one
// CARGO_REGISTRIES_<PROXY-SOURCE>_TOKEN EnvExport each, deduped by var name:
// two routes sharing one ProxySource must not emit the same export twice.
func CargoReplacementPlaceholders(replacements []CargoSourceReplacement) []EnvExport {
	seen := make(map[string]bool)
	var exports []EnvExport
	for _, r := range replacements {
		name := CargoRegistryEnvVarName(r.ProxySource)
		if seen[name] {
			continue
		}
		seen[name] = true
		exports = append(exports, EnvExport{Name: name, Value: CargoPlaceholderToken})
	}
	return exports
}

// CargoConfigTOML renders the $CARGO_HOME/config.toml content. Cargo does not
// proxy table-valued config through CARGO_<SECTION>_<KEY> (cargo#5416, still
// open), so unlike Go or npm this binding can only be written as a file.
// "sparse+" is required: the Forwarder speaks plain HTTP and serves no
// git-clonable index. routes is ignored, satisfying the signature (#3259).
func CargoConfigTOML(port int, prefix string, routes []registrymanifest.Route) string {
	return fmt.Sprintf(`[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:%d/%s/"
`, port, prefix)
}

// cargoBareKeyPattern matches cargo/TOML's own bare-key charset. A quoted
// [registries."..."] table name can otherwise carry arbitrary text (spaces,
// ";", "$(...)"), and that text flows unquoted as a shell variable name into
// the env-export file entrypoint.sh sources, so any name failing this check
// must never reach a caller.
var cargoBareKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// CargoPlaceholderToken is the fixed, non-secret value emitted for every cargo
// source replacement (ADR 0044's issue #3053 amendment, re-keyed by #3201).
// cargo's client-side credential lookup aborts before the Forwarder is ever
// contacted unless something satisfies it locally; the Box to Forwarder hop
// stays unauthenticated and the Rewrite hook supplies the real credential.
const CargoPlaceholderToken = "spindrift-registry-proxy-placeholder-not-a-secret"

// CargoRegistryEnvVarName renders registryName into
// CARGO_REGISTRIES_<NAME>_TOKEN, uppercased with "-" mapped to "_", which is
// cargo's own convention for turning a [registries.NAME] name into an env var.
func CargoRegistryEnvVarName(registryName string) string {
	upper := strings.ToUpper(registryName)
	upper = strings.ReplaceAll(upper, "-", "_")
	return "CARGO_REGISTRIES_" + upper + "_TOKEN"
}

// RouteLocalURL renders route's own local Forwarder URL. The proxy listens on
// one port for every route, but each route answers only its own prefix-scoped
// path (issue #3142), so the rewrite target has to carry that prefix too.
func RouteLocalURL(route registrymanifest.Route, port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/" + route.Prefix
}

// rewriteCargoDL rewrites a cargo sparse-index config.json body's "dl" field to
// point at the Forwarder with the route's prefix re-inserted, so a later crate
// download (which cargo builds by joining dl with a crate path) round-trips
// through the same route instead of going straight upstream. It does no I/O
// and no logging; the caller logs from and to keyed off outcome.
func rewriteCargoDL(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	obj, ok := decodeOneJSONObject(body)
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

	edit, ok := repointRegistryURL(dlStr, rc)
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

	obj["dl"] = edit.To

	newBody, err := json.Marshal(obj)
	if err != nil {
		// Unreachable in practice: obj came from a successful decode above,
		// so every value in it is representable as JSON.
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	return registryvocab.RewriteResult{
		Body:    newBody,
		Edits:   []registryvocab.RewriteEdit{edit},
		Outcome: registryvocab.RewriteApplied,
	}
}
