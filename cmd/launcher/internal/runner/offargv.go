package runner

// OffArgvKeys names the box.Env keys whose values must never appear on a runner
// process's argv: ps and /proc/<pid>/cmdline expose argv to any local user for
// the Box's whole lifetime, while the process's own environment is readable only
// by its owner. The test is "must never appear on argv", not "is a credential":
// ISSUE_TEXT is a private issue body, sensitive without being a secret.
//
// The Driver credential half comes from credentialenv_gen.go (regenerated from
// the Driver credential rules); nonDriverOffArgvKeys holds only the rest. That
// half is the runner-local generated driverCredentialEnv slice because runner
// does not import internal/driver.
var OffArgvKeys = withDriverCredentialEnv(nonDriverOffArgvKeys)

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
