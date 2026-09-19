package ecosystem

import "strings"

// nameYarn is yarnRow's Name. npmFamilyVars (npm.go) compares against this
// const rather than yarnRow.Name to avoid a package initialization cycle.
const nameYarn = "yarn"

// yarnRow is the yarn ecosystem's Table entry. It carries a BindingEnvVar
// despite a nil EnvExports because npmRow's EnvExports (NpmFamilyBindings)
// renders all three npm-family vars, yarn's included, in one call.
var yarnRow = Row{
	Name:             nameYarn,
	LockfileNames:    []string{"yarn.lock"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: ".yarnrc.yml",
	BindingEnvVar:    "YARN_NPM_REGISTRY_SERVER",
	ConfigParser:     parseYarnRegistryConfig,
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
