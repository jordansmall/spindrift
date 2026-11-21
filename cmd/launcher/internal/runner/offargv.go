package runner

import "sort"

// offArgvKeys names the box.Env keys whose values must never appear on a runner
// process's argv: ps and /proc/<pid>/cmdline expose argv to any local user for
// the Box's whole lifetime, while the process's own environment is readable only
// by its owner. The test is "must never appear on argv", not "is a credential":
// ISSUE_TEXT is a private issue body, sensitive without being a secret.
//
// The Driver credential half is the generated driverCredentialEnv
// (credentialenv_gen.go), since runner does not import internal/driver.
// It is unexported so no importer can widen or shrink it.
var offArgvKeys = withDriverCredentialEnv(nonDriverOffArgvKeys)

// IsOffArgvKey reports whether k's value must stay off a runner's argv.
func IsOffArgvKey(k string) bool { return offArgvKeys[k] }

// OffArgvKeys returns the off-argv key names, sorted, in a fresh slice the
// caller may modify.
func OffArgvKeys() []string {
	keys := make([]string, 0, len(offArgvKeys))
	for k := range offArgvKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var nonDriverOffArgvKeys = map[string]bool{
	"GH_TOKEN": true,
	// Registry-proxy TCP fallback secret, a bearer-token-shaped credential
	// (issue #3111).
	"REGISTRY_PROXY_TCP_SECRET": true,
	// Signal socket's TCP fallback secret, a bearer-token-shaped credential
	// (issue #3725).
	"SIGNAL_SOCKET_SECRET": true,
	"FORGEJO_TOKEN":        true,
	// The subject issue's injected body and comments (issue #3445). ADR 0032
	// keeps local issues private, and a private-repo GitHub body is sensitive.
	"ISSUE_TEXT": true,
}

func withDriverCredentialEnv(nonDriver map[string]bool) map[string]bool {
	keys := make(map[string]bool, len(nonDriver)+len(driverCredentialEnv))
	for k := range nonDriver {
		keys[k] = true
	}
	for _, k := range driverCredentialEnv {
		keys[k] = true
	}
	return keys
}
