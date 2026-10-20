package ecosystem

import (
	"strings"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// namePnpm and pnpmBindingEnvVar are consts because pnpmRow's EnvExports cannot
// read pnpmRow.Name or pnpmRow.BindingEnvVar without a package initialization
// cycle. Sharing the const keeps BindingEnvVar and the exported var one name.
const (
	namePnpm          = "pnpm"
	pnpmBindingEnvVar = "pnpm_config_registry"
)

// pnpmRow is the pnpm ecosystem's Table entry.
var pnpmRow = Row{
	Name:             namePnpm,
	CredentialStores: []string{"netrc", "npmrc", "gradle-properties"},
	LockfileNames:    []string{"pnpm-lock.yaml"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: "pnpm-workspace.yaml",
	EnvExports: func(port int, _ string, _ func(string) string, routes []registrymanifest.Route) ([]EnvExport, []string) {
		return npmFamilyBinding(port, routes, pnpmBindingEnvVar, namePnpm)
	},
	BindingEnvVar: pnpmBindingEnvVar,
	ConfigParser:  parsePnpmRegistryConfig,
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
	indexByURL := make(map[string]int)
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
		if !ok {
			continue
		}
		out = addNpmFamilyDeclaration(out, indexByURL, Declaration{
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
			Scoped:          isScopedNpmFamilyKey(key),
		})
	}

	// sawDeclaration means a real registry key was set but every value was
	// unusable, which is distinct from a file with no such key at all.
	return out, sawDeclaration, nil
}
