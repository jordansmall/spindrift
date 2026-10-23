package ecosystem

import (
	"fmt"
	"net/http"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem/ecosystemkit"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// nameNpm and npmBindingEnvVar are consts because npmRow's EnvExports cannot
// read npmRow.Name or npmRow.BindingEnvVar without a package initialization
// cycle. Sharing the const keeps BindingEnvVar and the exported var one name.
const (
	nameNpm          = "npm"
	npmBindingEnvVar = "npm_config_registry"
)

var npmRow = Strategy{
	Name:             nameNpm,
	CredentialStores: []string{"netrc", "npmrc", "gradle-properties"},
	LockfileNames:    []string{"package-lock.json"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: ".npmrc",
	EnvExports: func(port int, _ string, _ func(string) string, routes []registrymanifest.Route) ([]EnvExport, []string) {
		return npmFamilyBinding(port, routes, npmBindingEnvVar, nameNpm)
	},
	BindingEnvVar: npmBindingEnvVar,
	ConfigParser:  parseNpmRegistryConfig,
	RewriteRows: []registryvocab.RewriteRow{{
		Name:          "npm packument",
		Ecosystem:     nameNpm,
		Method:        http.MethodGet,
		Matches:       npmPackumentMatches,
		RewriteStream: streamNpmPackument,
	}},
}

// parseNpmRegistryConfig scans a .npmrc for "registry=" and
// "@scope:registry=" lines. npm's format is ini-ish and accepts unquoted
// values, so a line-based scan matches npm's own parsing more closely than a
// general-purpose ini library would.
func parseNpmRegistryConfig(content string) ([]Declaration, bool, error) {
	indexByURL := make(map[string]int)
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
		host, upstreamBaseURL, ok := ecosystemkit.HTTPAbsoluteURL(value)
		if !ok {
			continue
		}
		out = addNpmFamilyDeclaration(out, indexByURL, Declaration{
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
			Scoped:          isScopedNpmFamilyKey(key),
		})
	}

	// sawDeclaration tells the caller the file named a registry key but no
	// usable value, which differs from a file that names no registry key.
	return out, sawDeclaration, nil
}

// isScopedNpmFamilyKey reports whether a key the Strategy's own predicate already
// matched is a scope's ("@acme:registry") rather than the default registry.
func isScopedNpmFamilyKey(key string) bool { return key != "registry" }

// addNpmFamilyDeclaration appends d unless its URL is already declared. A
// repeat declaration unscoped upgrades the earlier scoped one: the binding
// skips scoped declarations, so a URL that is also the default registry must
// not stay scoped just because a scoped line came first. registrypathset
// applies the same rule per host+path; change one, change both.
func addNpmFamilyDeclaration(out []Declaration, indexByURL map[string]int, d Declaration) []Declaration {
	if i, seen := indexByURL[d.UpstreamBaseURL]; seen {
		if !d.Scoped {
			out[i].Scoped = false
		}
		return out
	}
	indexByURL[d.UpstreamBaseURL] = len(out)
	return append(out, d)
}

// npmFamilyBinding points one npm-family tool at the Forwarder through envVar,
// searching every route for the one unscoped path tagged with ecosystem and
// binding that route's own prefix. Unlike BindHomes' routes[0] rule (#3142),
// npm need not follow route order: the unscoped registry is identifiable once
// scope is carried (#5720). Each Strategy computes its own var independently
// (issue #3259): a route's tagged paths can differ across npm, pnpm, and yarn.
// Unscoped registries only; these bind packument requests, and
// streamNpmPackument (#3401) re-points embedded tarball URLs.
func npmFamilyBinding(port int, routes []registrymanifest.Route, envVar, ecosystem string) ([]EnvExport, []string) {
	var matches []npmFamilyMatch
	for _, route := range routes {
		// An empty prefix has no Forwarder address to bind. BindHomes'
		// empty-prefix guard covers only routes[0], so later routes need their
		// own check.
		if route.Prefix == "" {
			continue
		}
		for _, p := range route.EnforcedPaths {
			if p.Ecosystem == ecosystem && !p.Scoped {
				matches = append(matches, npmFamilyMatch{prefix: route.Prefix, path: p.Path})
			}
		}
	}

	switch len(matches) {
	case 0:
		// Nothing unscoped and tagged for this ecosystem, so nothing to bind (AC3).
		// A registry declared only in another Strategy's config lands here
		// (a pnpm registry in .npmrc is tagged "npm"); the in-tree
		// rewrite still covers that traffic.
		return nil, nil
	case 1:
		// registrypathset renders a whole-host declaration as "/"
		// (pathset.go's normalizePath), which would concatenate into a
		// double slash below.
		matchPath := matches[0].path
		if matchPath == "/" {
			matchPath = ""
		}
		return []EnvExport{{
			Name:  envVar,
			Value: fmt.Sprintf("http://127.0.0.1:%d/%s%s/", port, matches[0].prefix, matchPath),
		}}, nil
	default:
		// Two or more unscoped paths, on one route or several: no way to tell
		// which is the default registry.
		entries := make([]string, len(matches))
		for i, m := range matches {
			entries[i] = fmt.Sprintf("route %q path %s", m.prefix, m.path)
		}
		return nil, []string{fmt.Sprintf(
			"==> WARNING: %d unscoped %s-tagged paths (%s); %s is ambiguous and will not be bound",
			len(matches), ecosystem, strings.Join(entries, ", "), envVar)}
	}
}

// npmFamilyMatch is one unscoped ecosystem-tagged path and the prefix of the
// route that carries it.
type npmFamilyMatch struct{ prefix, path string }
