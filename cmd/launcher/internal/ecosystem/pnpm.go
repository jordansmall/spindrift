package ecosystem

import "strings"

// namePnpm is pnpmRow's Name. npmFamilyVars (npm.go) compares against this
// const rather than pnpmRow.Name to avoid a package initialization cycle.
const namePnpm = "pnpm"

// pnpmRow is the pnpm ecosystem's Table entry. It carries a BindingEnvVar
// despite a nil EnvExports because npmRow's EnvExports (NpmFamilyBindings)
// renders all three npm-family vars, pnpm's included, in one call.
var pnpmRow = Row{
	Name:             namePnpm,
	LockfileNames:    []string{"pnpm-lock.yaml"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: "pnpm-workspace.yaml",
	BindingEnvVar:    "pnpm_config_registry",
	ConfigParser:     parsePnpmRegistryConfig,
}

// isPnpmRegistryKey reports whether key is the bare top-level "registry" or a
// scoped catalog key "<scope>:registry". splitYAMLKeyValue has already
// unquoted the key. A suffix-only check would also match an unrelated key
// like "myregistry" or a YAML list item key like "- registry".
func isPnpmRegistryKey(key string) bool {
	if key == "registry" {
		return true
	}
	return strings.HasPrefix(key, "@") && strings.HasSuffix(key, ":registry")
}

// parsePnpmRegistryConfig scans a pnpm-workspace.yaml line by line for the
// bare "registry" key or a scoped catalog key with an http(s) value. Scanning
// lines covers the shapes pnpm emits without a YAML library for one key;
// adding one is an ADR 0048 promotion trigger, not a prohibition.
func parsePnpmRegistryConfig(content string) ([]Declaration, bool, error) {
	seenURL := make(map[string]bool)
	var out []Declaration
	sawDeclaration := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// splitYAMLKeyValue has no notion of "#" as special, so
			// this loop drops full-line comments itself.
			continue
		}
		key, value, ok := splitYAMLKeyValue(trimmed)
		if !ok || !isPnpmRegistryKey(key) {
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

	// sawDeclaration means a real registry key was set but every value was
	// unusable, which is distinct from a file with no such key at all.
	return out, sawDeclaration, nil
}
