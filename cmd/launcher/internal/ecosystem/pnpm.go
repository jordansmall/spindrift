package ecosystem

import "strings"

// pnpmRow is the pnpm ecosystem's Table entry (see yarnRow's doc comment for
// why this row carries a BindingEnvVar despite a nil EnvExports).
var pnpmRow = Row{
	Name:             "pnpm",
	LockfileNames:    []string{"pnpm-lock.yaml"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: "pnpm-workspace.yaml",
	BindingEnvVar:    "pnpm_config_registry",
	ConfigParser:     parsePnpmRegistryConfig,
}

// isPnpmRegistryKey reports whether key is a real pnpm registry key: the
// bare top-level "registry", or a scoped catalog key "<scope>:registry"
// where scope starts with "@" (a quoted key like "\"@myorg:registry\""
// arrives here already unquoted by splitYAMLKeyValue). A suffix-only check
// would also match an unrelated key like "myregistry" or a YAML list item
// key like "- registry".
func isPnpmRegistryKey(key string) bool {
	if key == "registry" {
		return true
	}
	return strings.HasPrefix(key, "@") && strings.HasSuffix(key, ":registry")
}

// parsePnpmRegistryConfig is pnpmRow's ConfigParser: it scans content (a
// pnpm-workspace.yaml) line-by-line for the bare "registry:" key or a scoped
// catalog key like "\"@myorg:registry\":" with an http(s) value -- same
// line-based approach as parseYarnRegistryConfig, avoiding a YAML library
// for this single key (adding one is an ADR 0048 promotion trigger, see
// package doc, not a prohibition). isPnpmRegistryKey exact-matches the key
// so an unrelated key merely ending in "registry" or a YAML list item is
// never mistaken for a real pnpm registry declaration.
func parsePnpmRegistryConfig(content string) ([]Declaration, bool, error) {
	seenURL := make(map[string]bool)
	var out []Declaration
	sawDeclaration := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// A full-line comment -- skip it outright rather than feeding it
			// to splitYAMLKeyValue, which has no notion of "#" as special.
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

	// sawDeclaration means a real registry key (per isPnpmRegistryKey) was
	// set but every value was unusable -- distinct from a file with no such
	// key at all.
	return out, sawDeclaration, nil
}
