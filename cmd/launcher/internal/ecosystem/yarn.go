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
	EnvExports: func(port int, _ string, _ func(string) string, routes []registrymanifest.Route) ([]EnvExport, []string) {
		return npmFamilyBinding(port, routes, yarnBindingEnvVar, nameYarn)
	},
	BindingEnvVar: yarnBindingEnvVar,
	ConfigParser:  parseYarnRegistryConfig,
}

// parseYarnRegistryConfig takes every "npmRegistryServer: <url>" line,
// top-level default or under npmScopes, marking the latter Scoped. The only
// indentation it tracks is whether a column-0 key opened the npmScopes block.
// That covers the shapes yarn berry emits without a YAML library for one key;
// adding one is an ADR 0048 promotion trigger, not a prohibition.
func parseYarnRegistryConfig(content string) ([]Declaration, bool, error) {
	indexByURL := make(map[string]int)
	var out []Declaration
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
		host, upstreamBaseURL, ok := httpAbsoluteURL(value)
		if !ok {
			continue
		}
		out = addNpmFamilyDeclaration(out, indexByURL, Declaration{
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
			Scoped:          inScopes,
		})
	}

	// sawDeclaration means npmRegistryServer was set but every value was
	// unusable, distinct from a file that never sets the key at all.
	return out, sawDeclaration, nil
}
