package ecosystem

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// cargoRow is the cargo ecosystem's Table entry (see ecosystem.go's Table
// doc for why order matters and why RepoAwareHomeConfig is non-nil only
// here).
var cargoRow = Row{
	Name:             "cargo",
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
}

// CargoRegistryDecl is one [registries.NAME] table found while scanning the
// Target repo's *un-rewritten* .cargo/config.toml (issue #3201) -- Index is
// captured verbatim (quotes stripped, "sparse+" scheme kept if present),
// since CargoSourceReplacements needs the real upstream URL byte-for-byte to
// emit it into the [source.spindrift-upstream-<name>] stanza cargo replaces
// away from.
type CargoRegistryDecl struct {
	Name  string
	Index string
}

// ParseCargoRegistryDecls scans content -- the repo's own tracked
// .cargo/config.toml, before any rewrite -- for every [registries.NAME]
// table carrying an `index` assignment, returning one decl per table in
// header-appearance order.
//
// The scan is line-based rather than a TOML parse: a section runs from its
// "[registries.NAME]" header to the next "[...]" header (of any table) or
// EOF. It deliberately never asks whether the index points at a Forwarder
// URL -- the repo config it reads has no Forwarder URL in it yet, that's
// the whole point of source replacement -- so every table with an `index`
// line qualifies.
//
// A section whose name fails cargoBareKeyPattern (e.g. a quoted TOML key
// like [registries."evil; rm -rf /"]) is skipped entirely: an untrusted
// name must never reach a caller that could turn it into a shell-sourced
// env var name or a TOML table name. A table with no `index` line yields no
// decl. A name repeated across two headers is deduped, keeping its first
// occurrence.
func ParseCargoRegistryDecls(content string) []CargoRegistryDecl {
	raw := scanCargoNamedTable(content, "registries.", "index")
	decls := make([]CargoRegistryDecl, len(raw))
	for i, d := range raw {
		decls[i] = CargoRegistryDecl{Name: d.name, Index: d.value}
	}
	return decls
}

// CargoSourceDecl is one [source.NAME] table found while scanning the
// Target repo's own un-rewritten .cargo/config.toml -- the sibling of
// CargoRegistryDecl, keyed on the `registry` assignment a [source.NAME]
// table uses to name a real registry URL, rather than the `index` key a
// [registries.NAME] table uses. CargoSourceReplacements consults these to
// tell when the repo config already claims a route's index URL under its
// own source name (issue #3248): reusing that name instead of minting
// spindrift-upstream-<name> is what keeps cargo's URL->source-name 1:1 rule
// from rejecting the merged config.
type CargoSourceDecl struct {
	Name     string
	Registry string
}

// ParseCargoSourceDecls scans content for every [source.NAME] table
// carrying a `registry` assignment, returning one decl per table in
// header-appearance order. It shares ParseCargoRegistryDecls' line-based
// scan and its untrusted-name/no-key/dedup contract verbatim (see that
// function's doc comment) via scanCargoNamedTable -- only the header prefix
// ("source." not "registries.") and key ("registry" not "index") differ. A
// [source.NAME] table with no `registry` line -- e.g. one carrying only
// `replace-with`, cargo's other use of a [source.…] table -- claims no URL
// and yields no decl.
func ParseCargoSourceDecls(content string) []CargoSourceDecl {
	raw := scanCargoNamedTable(content, "source.", "registry")
	decls := make([]CargoSourceDecl, len(raw))
	for i, d := range raw {
		decls[i] = CargoSourceDecl{Name: d.name, Registry: d.value}
	}
	return decls
}

// namedCargoTableDecl is scanCargoNamedTable's table-agnostic result: a
// table name paired with the one string value its scan key assigned.
type namedCargoTableDecl struct {
	name  string
	value string
}

// scanCargoNamedTableOccurrences is scanCargoNamedTable's core line scan,
// split out so a caller that needs to see repeats -- e.g. a test asserting
// a rendered file never declares the same table twice -- doesn't have to
// reimplement the scan to get them: it applies neither
// scanCargoNamedTable's untrusted-name filter nor its dedupe, and reports a
// matching header with no matching key line as an empty-value decl rather
// than omitting it. A section runs from its "[<headerPrefix>NAME]" header to
// the next "[...]" header (of any table) or EOF.
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
				// "First matching-key line wins" holds even when that line
				// is malformed: haveValue latches either way, so a rejected
				// value leaves the section value-less rather than falling
				// through to a later line.
				sectionValue, _ = cargoTOMLStringValue(value)
				haveValue = true
			}
		}
	}
	flush()

	return decls
}

// scanCargoNamedTable is the line-based table scan ParseCargoRegistryDecls
// and ParseCargoSourceDecls share, parameterized on the header prefix that
// opens a matching table ("registries." or "source.") and the key whose
// string value the table must carry to yield a decl ("index" or
// "registry"). It wraps scanCargoNamedTableOccurrences' raw per-header scan
// with the filters callers need: a section whose name fails
// cargoBareKeyPattern (e.g. a quoted TOML key like [registries."evil; rm -rf
// /"]) is skipped entirely -- an untrusted name must never reach a caller
// that could turn it into a shell-sourced env var name or a TOML table name.
// A table with no matching key line yields no decl. A name repeated across
// two headers is deduped, keeping its first occurrence.
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

// cargoTOMLStringValue extracts the string a TOML key's right-hand side
// assigns, taking the text up to the matching close of the opening quote
// (basic `"…"` or literal `'…'`) and requiring what follows to be nothing
// but an optional `#` comment.
//
// The strictness is load-bearing, not pedantry: an index value carrying
// trailing junk (a legal-TOML comment, say) still parses as a URL whose
// host matches its route, so it survives CargoSourceReplacements' matching
// and lands verbatim in a `[source.…] registry = %q` stanza. cargo then
// looks for a source URL the repo never uses and the replacement silently
// never binds. Rejecting the value instead leaves the registry unbound,
// which fails loudly at fetch time.
//
// It is escape-unaware: a basic string containing an escaped `\"` ends at
// that byte, not the real closing quote, so a legal TOML index using one is
// truncated and (via the trailer check) rejected -- the same loud-failure
// consequence as above, not a silent misbind.
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

// CargoUpstreamSource is one real-registry [source.…] stanza a
// CargoSourceReplacement replaces away from -- SourceName is the
// "spindrift-upstream-<name>" table name, IndexURL the decl's verbatim
// Index value.
type CargoUpstreamSource struct {
	SourceName string
	IndexURL   string
}

// CargoSourceReplacement is the source-replacement plan for one manifest
// route with at least one matching declared cargo registry (issue #3201):
// the proxy source cargo's credential lookup binds to, the local Forwarder
// URL it replaces every Upstream with, and the Upstreams themselves.
type CargoSourceReplacement struct {
	Prefix        string
	ProxySource   string
	LocalIndexURL string
	Upstreams     []CargoUpstreamSource
}

// cargoIndexHost extracts the host cargo would actually connect to for
// index -- stripping a leading "sparse+" (cargo's own scheme prefix, not
// part of the URL proper) before parsing -- and reports whether index names
// a well-formed http(s) URL with a host at all. It uses u.Host, not
// u.Hostname(): registrymanifest.Route.UpstreamHost is minted as u.Host
// (box.go), which carries "host:port" whenever the upstream URL has an
// explicit port, and u.Hostname() strips that port -- comparing a
// port-stripped host against a ported UpstreamHost would silently drop
// every ported upstream. A decl failing this check can never match any
// route's UpstreamHost, so CargoSourceReplacements drops it up front rather
// than carrying an unmatchable candidate forward.
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

// cargoIndexPath extracts the path component of index -- stripping the same
// leading "sparse+" cargoIndexHost strips before parsing -- for a
// host-rooted route's per-registry local URL (issue #3256): a registry
// served at its own real path (e.g. an Artifactory
// "/artifactory/api/cargo/internal") must resolve through that same path
// locally, or the Forwarder's per-registry enforced subtree never admits the
// requests cargo actually sends. index has already passed cargoIndexHost's
// well-formed-URL check by the time any caller here reaches it, so a parse
// failure is unreachable in practice; it degrades to "" (no path to embed)
// rather than panicking, matching a rootless index's own legitimate shape.
// The result is always either "" or a leading-"/", no-trailing-"/" path.
func cargoIndexPath(index string) string {
	raw := strings.TrimPrefix(index, "sparse+")
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Path, "/")
}

// cargoLocalIndexURL renders the Forwarder's own sparse-protocol index URL
// for a route at prefix -- the same shape CargoConfigTOML embeds in its
// [source.spindrift-registry-proxy] stanza, so string equality between two
// calls' results is exactly cargo's own "same URL" test.
func cargoLocalIndexURL(port int, prefix string) string {
	return cargoLocalIndexURLWithPath(port, prefix, "")
}

// cargoLocalIndexURLWithPath renders the Forwarder's sparse-protocol index
// URL for a registry at prefix whose real upstream index lives at indexPath
// (see cargoIndexPath) -- issue #3256's per-registry local URL, needed so
// two registries sharing one host-rooted route's prefix resolve through two
// distinct URLs instead of folding onto one. indexPath is either "" (no path
// to embed -- cargoLocalIndexURL's own shape) or a leading-"/",
// no-trailing-"/" path (cargoIndexPath's own contract), so prefix and
// indexPath always join with exactly one "/" between them and the result
// always carries exactly one trailing "/".
func cargoLocalIndexURLWithPath(port int, prefix, indexPath string) string {
	return "sparse+http://127.0.0.1:" + strconv.Itoa(port) + "/" + prefix + indexPath + "/"
}

// registryProxySourceName is the crates-io replacement's own proxy source
// name (CargoConfigTOML's [source.spindrift-registry-proxy]) -- the one
// stanza CargoSourceReplacements must reuse rather than collide with, per
// cargo's URL->source-name 1:1 constraint.
const registryProxySourceName = "spindrift-registry-proxy"

// CargoSourceReplacements builds the cargo source-replacement plan (issue
// #3201, ADR 0044 amendment) from routes (manifest order) and repoConfig --
// the Target repo's own un-rewritten .cargo/config.toml, read post-clone.
// port and prefix are the same values CargoConfigTOML(port, prefix) was
// rendered with, so this function can tell when a named-registry route's
// own local Forwarder URL coincides with the crates-io replacement's
// (route.Prefix == prefix): cargo maps URL->source name 1:1, so that case
// must reuse spindrift-registry-proxy rather than mint a second [source.…]
// stanza carrying the same URL.
//
// For each route (skipping one with an empty Prefix or UpstreamHost --
// neither can be rendered into a stanza), every parsed decl whose Index
// host matches route.UpstreamHost is a candidate. If the route declares a
// non-empty CargoRegistries, only decls named in that list become
// Upstreams; every other host-matching decl instead produces one returned
// warning (already "==> WARNING: "-prefixed, per this repo's convention
// that the producer prefixes and the caller prints the line bare) naming
// the registry and the route's prefix. Candidates are deduped by
// their real Index URL -- two registry names pointing at the same upstream
// index collapse to one [source.spindrift-upstream-…] stanza -- and any
// name failing cargoBareKeyPattern is dropped rather than ever reaching a
// caller. A route that ends up with no Upstreams is omitted from the
// result entirely.
//
// A route (issue #3256) never folds its candidates onto one route-wide
// local URL: it emits one CargoSourceReplacement per distinct Index URL,
// each carrying that registry's own index path (cargoIndexPath) and its own
// minted proxy source, since the route serves its upstream host's real path
// layout and two registries there occupy two different paths.
//
// The mirror-image warning covers the drift the other way: every name in a
// route's CargoRegistries that produced no Upstream -- undeclared in the
// repo config, index unparseable, index on another host, or name rejected
// by cargoBareKeyPattern -- yields one warning, in declared order. A name
// deduped away by an earlier name's identical index URL still binds through
// that shared stanza and so does not warn. This is the only signal such a
// registry gets: nothing binds it, so a network-less cargo build would
// otherwise fail with no diagnostic at all.
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

	// homeOwnedSourceNames are table names the rendered home config already
	// uses for something else (CargoConfigTOML's own crates-io/proxy pair,
	// every route's own per-route proxy name it might yet mint below, and
	// every decl's own "spindrift-upstream-<name>" mint further down). A
	// repo source name equal to one of these can never be reused: cargo's
	// [source.…] table names must be unique within one file, so reuse here
	// would be a duplicate-table TOML error, not a mere URL collision.
	homeOwnedSourceNames := map[string]bool{
		"crates-io":             true,
		registryProxySourceName: true,
	}
	for _, route := range routes {
		if route.Prefix == "" {
			continue
		}
		// Every name the route could mint needs reserving here, on an
		// over-reserve-rather-than-under-reserve footing: the route's own
		// upstream filtering (declared-list, host match) happens later, so
		// a decl this loop can't yet tell will end up unbound still gets
		// its name reserved.
		for _, d := range hosted {
			if d.host == route.UpstreamHost {
				homeOwnedSourceNames[registryProxySourceName+"-"+route.Prefix+"-"+d.name] = true
			}
		}
	}
	for _, d := range decls {
		homeOwnedSourceNames["spindrift-upstream-"+d.Name] = true
	}

	// claimingSourceNameByURL maps a repo-declared [source.NAME]'s registry
	// URL to NAME, first-occurrence-wins (ParseCargoSourceDecls already
	// dedupes by name; a second name claiming the same URL as an earlier one
	// keeps the earlier name here). A guarded name is excluded up front so
	// the minting site below falls back to its pre-existing minted name
	// rather than ever considering the reuse.
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

		var declared map[string]bool
		if len(route.CargoRegistries) > 0 {
			declared = make(map[string]bool, len(route.CargoRegistries))
			for _, name := range route.CargoRegistries {
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
			// into an earlier name's stanza still binds through it, so it is
			// bound, not missing.
			bound[d.name] = true
			if seenIndexURL[d.index] {
				continue
			}
			seenIndexURL[d.index] = true
			// Reuse the repo's own claiming source name (issue #3248) when
			// one exists for this exact index URL, byte-for-byte -- cargo
			// would otherwise reject the merged config as a duplicate
			// source. Falling back to the minted name (including its
			// pre-existing collision, if the URL is unclaimed) is the
			// pre-#3248 default.
			sourceName := "spindrift-upstream-" + d.name
			if claimed, ok := claimingSourceNameByURL[d.index]; ok {
				sourceName = claimed
			}
			matched = append(matched, matchedDecl{name: d.name, index: d.index, sourceName: sourceName})
		}

		for _, name := range route.CargoRegistries {
			if bound[name] {
				continue
			}
			bound[name] = true // a name repeated in the declared list warns once
			// name is interpolated unquoted into "[registries.<name>]" here,
			// two tokens after the quoted use above -- safe because
			// registryroutes.validateCargoRegistries already pinned every
			// route.CargoRegistries entry to a bare-key pattern
			// ([A-Za-z0-9_-]+) at manifest parse time, before it ever
			// reaches this loop.
			warnings = append(warnings, "==> WARNING: cargo registry "+strconv.Quote(name)+" is declared on route prefix "+strconv.Quote(route.Prefix)+" (upstream host "+strconv.Quote(route.UpstreamHost)+") but the repo's .cargo/config.toml has no [registries."+name+"] with a well-formed index URL on that host, so it will not be bound to the Forwarder -- cargo will try to reach the real registry directly, which a network-less Box cannot do; verify the manifest's cargo-registries against the repo's .cargo/config.toml")
		}

		if len(matched) == 0 {
			// No placeholder export is fabricated here on purpose: under
			// source replacement a token binds to the replacement proxy
			// source, and with no replacement there is no
			// [registries.<proxy-source>] stanza for cargo to look one up
			// against, so an export would be inert. The declared-name
			// warnings above are the coverage instead.
			continue
		}

		// One CargoSourceReplacement per distinct upstream index URL (issue
		// #3256), not one per route: a route serves its upstream host's own
		// real path layout, so two registries sharing the route must resolve
		// through their own two local URLs (each carrying its own index path
		// -- cargoIndexPath) and their own two minted proxy sources, or the
		// Forwarder's per-registry enforced subtree could never tell them
		// apart.
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
// content once source-replacement stanzas are known (issue #3201):
// CargoConfigTOML(port, prefix)'s own output, unchanged, followed by one
// [source.spindrift-upstream-<name>]/[registries.<proxy source>] block per
// replacement, with the [registries....] half emitted once per distinct
// ProxySource rather than once per replacement. An empty replacements slice
// returns CargoConfigTOML's output verbatim -- the pre-#3201 render every
// existing caller still expects.
func CargoConfigTOMLWithReplacements(port int, prefix string, replacements []CargoSourceReplacement) string {
	base := CargoConfigTOML(port, prefix, nil)
	if len(replacements) == 0 {
		return base
	}

	var b strings.Builder
	b.WriteString(base)

	b.WriteString("\n[registry]\nglobal-credential-providers = [\"cargo:token\"]\n")

	// Two replacements can share one ProxySource -- two upstream index URLs
	// differing only in scheme resolve to the same local URL, so the second
	// reuses the first's minted name -- and repeating either table name in
	// one file is a duplicate-table TOML error, not a merge. The pair is
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

		// The reused spindrift-registry-proxy source's [source....] stanza is
		// already in base (CargoConfigTOML's crates-io replacement) -- emitting
		// it again would collide on the same URL (cargo's 1:1 URL->source-name
		// rule), so only a freshly minted per-route proxy source gets one here.
		if rep.ProxySource != registryProxySourceName {
			fmt.Fprintf(&b, "\n[source.%s]\nregistry = %q\n", rep.ProxySource, rep.LocalIndexURL)
		}

		fmt.Fprintf(&b, "\n[registries.%s]\nindex = %q\n", rep.ProxySource, rep.LocalIndexURL)
	}

	return b.String()
}

// CargoRepoAwareConfig is the cargo row's RepoAwareHomeConfigRenderer (issue
// #3201): it plans source replacements from repoConfig -- the cloned repo's
// own un-rewritten .cargo/config.toml, per route -- renders the full
// $CARGO_HOME/config.toml content around that plan, and derives the
// placeholder exports cargo's client-side credential lookup needs. A repo
// with no declared registries (repoConfig == "" or no matching
// [registries.*] table) yields CargoConfigTOML's own base render, no
// exports, matching the pre-#3201 output for a repo that never used named
// registries.
func CargoRepoAwareConfig(port int, prefix string, routes []registrymanifest.Route, repoConfig string) (content string, exports []EnvExport, warnings []string) {
	replacements, warnings := CargoSourceReplacements(port, prefix, routes, repoConfig)
	content = CargoConfigTOMLWithReplacements(port, prefix, replacements)
	exports = CargoReplacementPlaceholders(replacements)
	return content, exports, warnings
}

// CargoReplacementPlaceholders renders replacements into one
// CARGO_REGISTRIES_<PROXY-SOURCE>_TOKEN EnvExport per replacement, deduped
// by var name -- the reuse case (two routes' Upstreams sharing one
// ProxySource, e.g. both bound to spindrift-registry-proxy) must not emit
// the same export twice.
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

// CargoConfigTOML renders the $CARGO_HOME/config.toml content, mirroring the
// heredoc from the deleted entrypoint.sh phase_registry_proxy_forwarder (see
// git history) verbatim. Cargo's crates-io source-replacement config is
// table-valued, and Cargo does not proxy table-valued config through its
// CARGO_<SECTION>_<KEY> env-var mechanism (cargo#5416, still open) -- so
// unlike Go or npm this binding can only be applied by writing a file, not
// by exporting an env var. driver-exec bind-registry's bindings mode
// (runBindRegistryBindings in cmd/launcher/driver-exec/bindregistry_cmd.go)
// resolves $CARGO_HOME and writes this content to disk; this function stays
// a pure string-builder so it's unit-testable without touching a
// filesystem. Cargo's sparse protocol (the "sparse+" scheme prefix) is
// required here, not optional -- the Forwarder speaks plain HTTP, and
// Cargo's legacy git-based index protocol assumes a git-clonable index
// repo, which the Forwarder doesn't serve. prefix is the manifest route this
// config binds to -- see runBindRegistryBindings in
// cmd/launcher/driver-exec/bindregistry_cmd.go for why it's always the
// first manifest route's prefix. routes is accepted (and ignored) only to
// satisfy HomeConfigRenderer's signature (issue #3259): this is cargo's
// pre-clone *base* template, unrelated to cargo's real host-rooted logic,
// which lives entirely in the post-clone CargoRepoAwareConfig/
// CargoConfigTOMLWithReplacements path.
func CargoConfigTOML(port int, prefix string, routes []registrymanifest.Route) string {
	return fmt.Sprintf(`[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:%d/%s/"
`, port, prefix)
}

// cargoBareKeyPattern matches cargo/TOML's own bare-key charset -- letters,
// digits, "-", and "_". A quoted [registries."..."] table name can otherwise
// carry arbitrary single-line text (spaces, ";", backticks, "$(...)", ...);
// since that text flows unquoted (as a shell variable name, not just a
// value) into driver-exec's rendered env-export file that entrypoint.sh
// sources, any name failing this check must never reach a caller.
var cargoBareKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// CargoPlaceholderToken is the fixed, non-secret value emitted for every
// cargo source replacement bound to the Forwarder, keyed to the replacement
// proxy source cargo looks credentials up against (ADR 0044's issue #3053
// amendment, re-keyed by issue #3201's source replacement). cargo's
// client-side credential lookup (cargo:token) aborts before the Forwarder is
// ever contacted unless something satisfies it locally; this placeholder
// exists only to satisfy that local check; the Box->Forwarder hop stays
// unauthenticated, and the Forwarder's Rewrite hook replaces the
// Authorization header on the Forwarder->upstream hop with the real
// credential regardless of what arrives here. The value is fixed and
// self-documenting so that leaking it in a log is visibly harmless.
const CargoPlaceholderToken = "spindrift-registry-proxy-placeholder-not-a-secret"

// CargoRegistryEnvVarName renders registryName into the env var name cargo's
// own credential-provider machinery reads for it: CARGO_REGISTRIES_<NAME>_TOKEN,
// with NAME uppercased and "-" mapped to "_" -- cargo's own convention for
// turning a [registries.NAME] table name into an env var.
func CargoRegistryEnvVarName(registryName string) string {
	upper := strings.ToUpper(registryName)
	upper = strings.ReplaceAll(upper, "-", "_")
	return "CARGO_REGISTRIES_" + upper + "_TOKEN"
}

// RouteLocalURL renders route's own local Forwarder URL: the proxy listens
// on one port for every route, but each route answers only its own
// prefix-scoped path (issue #3142), so the in-tree rewrite target has to
// carry that prefix too, not just the bare "http://127.0.0.1:<port>".
//
// Exported because a caller outside this package needs the same value to
// build its own host-rewrite records for a route that survived upstream-host
// collision filtering, a step this package has no reason to know about.
func RouteLocalURL(route registrymanifest.Route, port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/" + route.Prefix
}
