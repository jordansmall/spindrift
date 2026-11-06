package npmfamily

import (
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/ecosystemkit"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// nameYarn and yarnBindingEnvVar are consts because Yarn's EnvExports cannot
// read Yarn.Name or Yarn.BindingEnvVar without a package initialization
// cycle. Sharing the const keeps BindingEnvVar and the exported var one name.
const (
	nameYarn                   = "yarn"
	yarnBindingEnvVar          = "YARN_NPM_REGISTRY_SERVER"
	yarnUnsafeHTTPWhitelistVar = "YARN_UNSAFE_HTTP_WHITELIST"
	yarnLoopbackHost           = "127.0.0.1"
)

// Yarn is the yarn Strategy, which the Bundle registers.
var Yarn = ecosystem.Strategy{
	Name:             nameYarn,
	CredentialStores: []string{"netrc", "npmrc", "gradle-properties"},
	LockfileNames:    []string{"yarn.lock"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: ".yarnrc.yml",
	EnvExports:       yarnEnvExports,
	BindingEnvVar:    yarnBindingEnvVar,
	ConfigParser:     parseYarnRegistryConfig,
}

// yarnEnvExports adds the loopback whitelist to the shared binding. Yarn berry
// refuses every http: request whose hostname is not in unsafeHttpWhitelist
// (default empty, no loopback exemption), and the in-tree rewrite points even
// scoped-only registries at the Forwarder, so any yarn-tagged path needs it.
func yarnEnvExports(port int, _ string, getenv func(string) string, routes []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
	exports, warnings := npmFamilyBinding(port, routes, yarnBindingEnvVar, nameYarn)
	if len(boundTaggedPaths(routes, nameYarn)) == 0 {
		return exports, warnings
	}
	return append(exports, ecosystem.EnvExport{
		Name:  yarnUnsafeHTTPWhitelistVar,
		Value: withLoopbackHost(getenv(yarnUnsafeHTTPWhitelistVar)),
	}), warnings
}

// withLoopbackHost appends 127.0.0.1 to any existing comma-separated
// whitelist, returning it unchanged when 127.0.0.1 is already an exact entry.
func withLoopbackHost(existing string) string {
	if existing == "" {
		return yarnLoopbackHost
	}
	for _, entry := range strings.Split(existing, ",") {
		if strings.TrimSpace(entry) == yarnLoopbackHost {
			return existing
		}
	}
	return existing + "," + yarnLoopbackHost
}

// parseYarnRegistryConfig takes every "npmRegistryServer: <url>" line,
// top-level default or under npmScopes, marking the latter Scoped. The only
// indentation it tracks is whether a column-0 key opened the npmScopes block.
// That covers the shapes yarn berry emits without a YAML library for one key;
// adding one is an ADR 0048 promotion trigger, not a prohibition.
func parseYarnRegistryConfig(content string) ([]ecosystem.Declaration, bool, error) {
	indexByURL := make(map[string]int)
	var out []ecosystem.Declaration
	sawDeclaration := false
	inScopes := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := splitYAMLKeyValue(trimmed)
		if line[0] != ' ' && line[0] != '\t' {
			// A column-0 key opens or closes the npmScopes block; any other
			// column-0 line (e.g. a "---" document marker) closes it too.
			inScopes = ok && key == "npmScopes"
		}
		if !ok || key != "npmRegistryServer" {
			continue
		}
		sawDeclaration = true
		value = unquoteYAMLScalar(stripYAMLTrailingComment(value))
		host, upstreamBaseURL, ok := ecosystemkit.HTTPAbsoluteURL(value)
		if !ok {
			continue
		}
		out = addNpmFamilyDeclaration(out, indexByURL, ecosystem.Declaration{
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
			Scoped:          inScopes,
		})
	}

	// sawDeclaration means npmRegistryServer was set but every value was
	// unusable, distinct from a file that never sets the key at all.
	return out, sawDeclaration, nil
}
