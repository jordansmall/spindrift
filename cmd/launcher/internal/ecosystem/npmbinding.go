package ecosystem

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// npmFamilyVar is one of the three env vars NpmFamilyBindings binds, paired
// with the registrypathset/registrymanifest ecosystem tag its host-rooted
// path lookup keys on.
type npmFamilyVar struct {
	name      string
	ecosystem string
}

// npmFamilyVars is the fixed npm/pnpm/yarn var-to-ecosystem-tag mapping
// NpmFamilyBindings walks, one independent computation per entry (issue
// #3259) -- unlike a shared bare-root URL, a host-rooted route's per-
// ecosystem tagged paths can differ across the three, so each var is decided
// on its own.
var npmFamilyVars = []npmFamilyVar{
	{name: "npm_config_registry", ecosystem: "npm"},
	{name: "pnpm_config_registry", ecosystem: "pnpm"},
	{name: "YARN_NPM_REGISTRY_SERVER", ecosystem: "yarn"},
}

// NpmFamilyBindings mirrors the npm/pnpm/yarn berry portion of the deleted
// entrypoint.sh phase_registry_proxy_forwarder (see git history): all three
// package managers get pointed at the same Forwarder URL, each via the one
// env-var-override mechanism that beats a Target repo's own committed
// project-level config (npm's env > project .npmrc > user .npmrc > global
// .npmrc; pnpm's own pnpm_config_* prefix, since it no longer honors
// npm_config_*; yarn berry's YARN_<KEY> single-key override convention).
// Unlike cargo, npm has no per-registry table -- the env var overrides its
// one default registry outright, and it wins even over a Target repo's own
// committed project-level .npmrc. These bindings cover packument/metadata
// requests only, not the tarball fetch that follows: npm's packument JSON
// embeds an absolute tarball URL that pacote fetches verbatim rather than
// deriving it from this registry setting, so that request leaves the proxy
// and reaches upstream directly, unauthenticated -- the same accepted gap
// ADR 0044 documents for cargo's own download endpoint (see ADR 0044's
// Update, issue #2854); a documented, accepted gap, not an oversight.
// Unscoped only -- per-scope registry entries stay entrypoint-side, applied
// by the *_intree_binding_apply phases.
//
// routes[0] is the manifest route these bindings point at -- see
// runBindRegistryBindings in cmd/launcher/driver-exec/bindregistry_cmd.go
// for why it's always the first manifest route's prefix. An empty routes
// (no route info at all, the pre-#3259 legacy contract) renders the bare
// route root for all three vars, unchanged. Otherwise each var is decided
// independently against routes[0]: a non-host-rooted route keeps the same
// bare-root URL (the substring-replacing in-tree rewrite already preserves
// whatever real path a scoped/unscoped declaration names, so the env var
// needs no path of its own there); a host-rooted route instead looks up
// routes[0].EnforcedPaths tagged for that var's own ecosystem -- zero
// matches exports nothing for that var (the route declares no such registry
// at all, so there's nothing to bind, matching AC3's fallback); exactly one
// match exports the full-path URL onto that tagged subtree; more than one
// match is ambiguous (registrydiscover doesn't distinguish a scoped
// "@scope:registry=" declaration from an unscoped "registry=" one, so there
// is no way to tell which tagged path, if any, is the default registry the
// env var should point at) and exports nothing for that var, with a warning
// instead.
//
// One tagging wrinkle worth knowing: a pnpm registry declared only in
// .npmrc (pnpm honors .npmrc, not just its own pnpm-workspace.yaml) is
// extracted and tagged "npm" by registrydiscover.extractNpm, since .npmrc
// is scanned as the npm row's own InTreeConfigPath -- extractPnpm only
// scans pnpm-workspace.yaml (see ecosystem.Table's pnpm row). So under a
// host-rooted route, pnpm_config_registry's lookup here finds no
// "pnpm"-tagged path and falls back to unset (no export), even though the
// in-tree rewrite still correctly covers pnpm's actual traffic by rewriting
// that same .npmrc's host in place.
func NpmFamilyBindings(port int, prefix string, routes []registrymanifest.Route) ([]EnvExport, []string) {
	route := firstRoute(routes)

	bareURL := fmt.Sprintf("http://127.0.0.1:%d/%s/", port, prefix)

	var exports []EnvExport
	var warnings []string
	for _, v := range npmFamilyVars {
		if !route.HostRooted {
			exports = append(exports, EnvExport{Name: v.name, Value: bareURL})
			continue
		}

		var matches []string
		for _, p := range route.EnforcedPaths {
			if p.Ecosystem == v.ecosystem {
				matches = append(matches, p.Path)
			}
		}

		switch len(matches) {
		case 0:
			// No tagged path for this ecosystem on this route -- no
			// declaration to bind, fall back to no export (AC3).
		case 1:
			// registrypathset's Path convention renders a whole-host
			// declaration as the literal "/" (pathset.go's normalizePath),
			// not "". Left as-is, that "/" concatenates into a double
			// slash below (".../prefix//"); normalize it to "" first --
			// "no extra path segment to embed" -- mirroring cargo's own
			// ""-means-"no path" convention (see cargoIndexPath).
			matchPath := matches[0]
			if matchPath == "/" {
				matchPath = ""
			}
			exports = append(exports, EnvExport{
				Name:  v.name,
				Value: fmt.Sprintf("http://127.0.0.1:%d/%s%s/", port, prefix, matchPath),
			})
		default:
			warnings = append(warnings, fmt.Sprintf(
				"==> WARNING: route %q has %d %s-tagged paths (%s); %s is ambiguous and will not be bound",
				route.Prefix, len(matches), v.ecosystem, strings.Join(matches, ", "), v.name))
		}
	}
	return exports, warnings
}
