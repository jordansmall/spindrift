package ecosystem

import (
	"strings"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// nameYarn and yarnBindingEnvVar are consts because yarnRow's EnvExports cannot
// read yarnRow.Name or yarnRow.BindingEnvVar without a package initialization
// cycle. Sharing the const keeps BindingEnvVar and the exported var one name.
const (
	nameYarn          = "yarn"
	yarnBindingEnvVar = "YARN_NPM_REGISTRY_SERVER"
)

// yarnRow is the yarn ecosystem's Table entry.
var yarnRow = Row{
	Name:             nameYarn,
	CredentialStores: []string{"netrc", "npmrc", "gradle-properties"},
	LockfileNames:    []string{"yarn.lock"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: ".yarnrc.yml",
	EnvExports: func(port int, prefix string, _ func(string) string, routes []registrymanifest.Route) ([]EnvExport, []string) {
		return npmFamilyBinding(port, prefix, routes, yarnBindingEnvVar, nameYarn)
	},
	BindingEnvVar: yarnBindingEnvVar,
	ConfigParser:  parseYarnRegistryConfig,
}

// parseYarnRegistryConfig ignores .yarnrc.yml indentation and nesting, taking
// every "npmRegistryServer: <url>" line, top-level default or under npmScopes.
// That covers the shapes yarn berry emits without a YAML library for one key;
// adding one is an ADR 0048 promotion trigger, not a prohibition.
func parseYarnRegistryConfig(content string) ([]Declaration, bool, error) {
	seenURL := make(map[string]bool)
	var out []Declaration
	sawDeclaration := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := splitYAMLKeyValue(trimmed)
		if !ok || key != "npmRegistryServer" {
			continue
		}
		sawDeclaration = true
		value = unquoteYAMLScalar(stripYAMLTrailingComment(value))
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

	// sawDeclaration means npmRegistryServer was set but every value was
	// unusable, distinct from a file that never sets the key at all.
	return out, sawDeclaration, nil
}
