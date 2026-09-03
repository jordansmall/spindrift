package ecosystem

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/registrymanifest"
)

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
	lines := strings.Split(content, "\n")

	var decls []CargoRegistryDecl
	seen := make(map[string]bool)

	inRegistriesSection := false
	sectionName := ""
	sectionIndex := ""
	haveIndex := false

	flush := func() {
		if inRegistriesSection && haveIndex && sectionIndex != "" && !seen[sectionName] && cargoBareKeyPattern.MatchString(sectionName) {
			seen[sectionName] = true
			decls = append(decls, CargoRegistryDecl{Name: sectionName, Index: sectionIndex})
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			// New header: close out whatever section (if any) precedes it.
			flush()

			header := strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			name, ok := strings.CutPrefix(header, "registries.")
			if ok {
				inRegistriesSection = true
				sectionName = name
				sectionIndex = ""
				haveIndex = false
			} else {
				inRegistriesSection = false
				sectionName = ""
				sectionIndex = ""
				haveIndex = false
			}
			continue
		}

		if inRegistriesSection && !haveIndex {
			key, value, ok := strings.Cut(trimmed, "=")
			if ok && strings.TrimSpace(key) == "index" {
				// "First index line wins" holds even when that line is
				// malformed: haveIndex latches either way, so a rejected
				// value leaves the section index-less rather than falling
				// through to a later index line.
				sectionIndex, _ = cargoTOMLStringValue(value)
				haveIndex = true
			}
		}
	}
	flush()

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

// cargoLocalIndexURL renders the Forwarder's own sparse-protocol index URL
// for a route at prefix -- the same shape CargoConfigTOML embeds in its
// [source.spindrift-registry-proxy] stanza, so string equality between two
// calls' results is exactly cargo's own "same URL" test.
func cargoLocalIndexURL(port int, prefix string) string {
	return "sparse+http://127.0.0.1:" + strconv.Itoa(port) + "/" + prefix + "/"
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

		seenIndexURL := make(map[string]bool)
		bound := make(map[string]bool)
		var upstreams []CargoUpstreamSource
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
			upstreams = append(upstreams, CargoUpstreamSource{
				SourceName: "spindrift-upstream-" + d.name,
				IndexURL:   d.index,
			})
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

		if len(upstreams) == 0 {
			// No placeholder export is fabricated here on purpose: under
			// source replacement a token binds to the replacement proxy
			// source, and with no replacement there is no
			// [registries.<proxy-source>] stanza for cargo to look one up
			// against, so an export would be inert. The declared-name
			// warnings above are the coverage instead.
			continue
		}

		localURL := cargoLocalIndexURL(port, route.Prefix)
		proxySource, ok := sourceNameByLocalURL[localURL]
		if !ok {
			proxySource = registryProxySourceName + "-" + route.Prefix
			sourceNameByLocalURL[localURL] = proxySource
		}

		replacements = append(replacements, CargoSourceReplacement{
			Prefix:        route.Prefix,
			ProxySource:   proxySource,
			LocalIndexURL: localURL,
			Upstreams:     upstreams,
		})
	}

	return replacements, warnings
}

// CargoConfigTOMLWithReplacements renders the full $CARGO_HOME/config.toml
// content once source-replacement stanzas are known (issue #3201):
// CargoConfigTOML(port, prefix)'s own output, unchanged, followed by one
// [source.spindrift-upstream-<name>]/[registries.<proxy source>] block per
// replacement. An empty replacements slice returns CargoConfigTOML's output
// verbatim -- the pre-#3201 render every existing caller still expects.
func CargoConfigTOMLWithReplacements(port int, prefix string, replacements []CargoSourceReplacement) string {
	base := CargoConfigTOML(port, prefix)
	if len(replacements) == 0 {
		return base
	}

	var b strings.Builder
	b.WriteString(base)

	b.WriteString("\n[registry]\nglobal-credential-providers = [\"cargo:token\"]\n")

	for _, rep := range replacements {
		for _, up := range rep.Upstreams {
			fmt.Fprintf(&b, "\n[source.%s]\nregistry = %q\nreplace-with = %q\n", up.SourceName, up.IndexURL, rep.ProxySource)
		}

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
