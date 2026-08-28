package main

import (
	"fmt"
	"os"
	"strings"
)

// validateRegistryProxyCredential reports a mutual-exclusion configuration
// error when both fromFile and fromEnv are set -- a Credential reference
// (ADR 0044) names exactly one source. Neither set is valid (the proxy then
// runs unauthenticated). Pure: does no I/O and touches no process state, so
// validate(c) can call it fail-fast without side effects.
func validateRegistryProxyCredential(fromFile, fromEnv string) error {
	if fromFile != "" && fromEnv != "" {
		return fmt.Errorf("REGISTRY_PROXY_CREDENTIAL_FILE and REGISTRY_PROXY_CREDENTIAL_ENV are mutually exclusive: a registry proxy credential names exactly one source")
	}
	return nil
}

// resolveRegistryProxyCredential resolves a Credential reference (ADR 0044)
// to its value exactly once. fromEnv, when set, is read via os.LookupEnv and
// the source variable is unset immediately via os.Unsetenv before this
// function returns -- the load-bearing step: it must happen before any Box
// is launched, since both runtimes build a Box's environment from process
// state captured after this point, and this credential is never added to
// that state to begin with. If fromEnv names a variable that turns out to be
// unset, or set to an empty string, that is a fail-closed configuration
// error: silently resolving to "" would start the proxy unauthenticated
// with no diagnostic. fromFile, when set, is read and trimmed of all
// leading/trailing whitespace; if that trim leaves nothing, or leaves an
// embedded newline or carriage return, it fails closed for the same
// reason -- an embedded newline would otherwise surface later as a cryptic
// HTTP header-validation error once the value is attached to an outbound
// request. Neither set resolves to "", nil (the proxy started later runs
// unauthenticated -- this is the only case where an empty credential is not
// an error, since no source was configured at all). Callers must call
// validateRegistryProxyCredential first to reject both being set; this
// function does not re-check that itself. If a caller skips validation and
// both are set anyway, it deterministically prefers fromEnv rather than
// erroring, since re-validating here would just duplicate that check.
func resolveRegistryProxyCredential(fromFile, fromEnv string) (string, error) {
	if fromEnv != "" {
		v, ok := os.LookupEnv(fromEnv)
		if err := os.Unsetenv(fromEnv); err != nil {
			return "", fmt.Errorf("unsetting registry proxy credential env var %s: %w", fromEnv, err)
		}
		if !ok || v == "" {
			return "", fmt.Errorf("registry proxy credential env var %s is unset or empty", fromEnv)
		}
		return v, nil
	}
	if fromFile != "" {
		b, err := os.ReadFile(fromFile)
		if err != nil {
			return "", fmt.Errorf("reading registry proxy credential file: %w", err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("registry proxy credential file %s is empty", fromFile)
		}
		if strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("registry proxy credential file %s contains an embedded newline", fromFile)
		}
		return v, nil
	}
	return "", nil
}
