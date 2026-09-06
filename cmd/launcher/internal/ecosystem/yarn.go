package ecosystem

import "strings"

// nameYarn is yarnRow's Name -- npmFamilyVars (npm.go) compares against
// this const instead of yarnRow.Name directly, to avoid a package
// initialization cycle (see nameGo's doc comment in go.go for the general
// shape of the cycle this sidesteps).
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

// parseYarnRegistryConfig is yarnRow's ConfigParser: it scans content (a
// .yarnrc.yml) line-by-line for "npmRegistryServer: <url>" keys -- both the
// top-level default and any nested under npmScopes. A line-based scan of
// this one known key, ignoring indentation/nesting structure entirely,
// covers the shapes yarn berry actually emits without pulling in a YAML
// library for a single key -- adding one would be an ADR 0048 promotion
// trigger (see package doc), not a prohibition.
func parseYarnRegistryConfig(content string) ([]Declaration, bool, error) {
	seenURL := make(map[string]bool)
	var out []Declaration
	sawDeclaration := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// A full-line comment (yarn berry's .yarnrc.yml has no other
			// comment syntax) -- must not be mistaken for a declaration,
			// same as parseNpmRegistryConfig's "#"/";" line filter.
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
	// unusable -- distinct from a file that never sets the key at all.
	return out, sawDeclaration, nil
}
