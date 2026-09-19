package ecosystem

import (
	"fmt"
	"net/http"
	"strings"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// nameNpm exists so npmFamilyVars can compare against a const rather than
// npmRow.Name, which would create a package initialization cycle.
const nameNpm = "npm"

var npmRow = Row{
	Name:             nameNpm,
	LockfileNames:    []string{"package-lock.json"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: ".npmrc",
	EnvExports: func(port int, prefix string, _ func(string) string, routes []registrymanifest.Route) ([]EnvExport, []string) {
		return NpmFamilyBindings(port, prefix, routes)
	},
	EnvExportOrder: envExportOrderNpmFamily,
	BindingEnvVar:  "npm_config_registry",
	ConfigParser:   parseNpmRegistryConfig,
	RewriteRows: []registryvocab.RewriteRow{{
		Name:      "npm packument",
		Ecosystem: nameNpm,
		Method:    http.MethodGet,
		Matches:   npmPackumentMatches,
		Rewrite:   rewriteNpmPackument,
	}},
}

// parseNpmRegistryConfig scans a .npmrc for "registry=" and
// "@scope:registry=" lines. npm's format is ini-ish and accepts unquoted
// values, so a line-based scan matches npm's own parsing more closely than a
// general-purpose ini library would.
func parseNpmRegistryConfig(content string) ([]Declaration, bool, error) {
	seenURL := make(map[string]bool)
	var out []Declaration
	sawDeclaration := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "registry" && !strings.HasSuffix(key, ":registry") {
			continue
		}
		sawDeclaration = true
		host, upstreamBaseURL, ok := httpAbsoluteURL(value)
		if !ok || seenURL[upstreamBaseURL] {
			continue
		}
		seenURL[upstreamBaseURL] = true
		out = append(out, Declaration{
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
		})
	}

	// sawDeclaration tells the caller the file named a registry key but no
	// usable value, which differs from a file that names no registry key.
	return out, sawDeclaration, nil
}

type npmFamilyVar struct {
	name      string
	ecosystem string
}

// NpmFamilyBindings computes each of these entries independently (issue
// #3259): a route's tagged paths can differ across npm, pnpm, and yarn.
var npmFamilyVars = []npmFamilyVar{
	{name: "npm_config_registry", ecosystem: nameNpm},
	{name: "pnpm_config_registry", ecosystem: namePnpm},
	{name: "YARN_NPM_REGISTRY_SERVER", ecosystem: nameYarn},
}

// NpmFamilyBindings points npm, pnpm, and yarn berry at the Forwarder on
// routes[0], each through its own env var: pnpm no longer honors
// npm_config_*, and an env var is the only override that beats a Target
// repo's committed .npmrc. Unscoped registries only; these bind packument
// requests, and rewriteNpmPackument (#3401) re-points embedded tarball URLs.
func NpmFamilyBindings(port int, prefix string, routes []registrymanifest.Route) ([]EnvExport, []string) {
	route := firstRoute(routes)

	var exports []EnvExport
	var warnings []string
	for _, v := range npmFamilyVars {
		var matches []string
		for _, p := range route.EnforcedPaths {
			if p.Ecosystem == v.ecosystem {
				matches = append(matches, p.Path)
			}
		}

		switch len(matches) {
		case 0:
			// Nothing tagged for this ecosystem, so nothing to bind (AC3).
			// A pnpm registry declared only in .npmrc is tagged "npm", so
			// pnpm_config_registry lands here; the in-tree rewrite still
			// covers pnpm's traffic.
		case 1:
			// registrypathset renders a whole-host declaration as "/"
			// (pathset.go's normalizePath), which would concatenate into a
			// double slash below.
			matchPath := matches[0]
			if matchPath == "/" {
				matchPath = ""
			}
			exports = append(exports, EnvExport{
				Name:  v.name,
				Value: fmt.Sprintf("http://127.0.0.1:%d/%s%s/", port, prefix, matchPath),
			})
		default:
			// registrydiscover does not distinguish a scoped
			// "@scope:registry=" declaration from an unscoped "registry=",
			// so there is no way to tell which path is the default.
			warnings = append(warnings, fmt.Sprintf(
				"==> WARNING: route %q has %d %s-tagged paths (%s); %s is ambiguous and will not be bound",
				route.Prefix, len(matches), v.ecosystem, strings.Join(matches, ", "), v.name))
		}
	}
	return exports, warnings
}
