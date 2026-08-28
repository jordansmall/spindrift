package main

import (
	"fmt"
	"os"
	"strings"
)

// validateRegistryProxyCredential reports a mutual-exclusion configuration
// error when both fromFile and fromEnv are set -- a Credential reference
// (ADR 0044) names exactly one source. Neither set is valid (the proxy then
// runs unauthenticated). Pure: does no I/O and touches no process state;
// this function alone can be called fail-fast without side effects -- the
// row's own Probe (checks.go) separately peeks the credential's actual
// resolvability, which does I/O.
func validateRegistryProxyCredential(fromFile, fromEnv string) error {
	if fromFile != "" && fromEnv != "" {
		return fmt.Errorf("REGISTRY_PROXY_CREDENTIAL_FILE and REGISTRY_PROXY_CREDENTIAL_ENV are mutually exclusive: a registry proxy credential names exactly one source")
	}
	return nil
}

// resolveRegistryProxyCredential resolves a Credential reference (ADR 0044)
// to its value exactly once, via credentialFromSource (see its doc comment
// for the trim/newline/empty/fail-closed rules). Distinct from that shared
// logic: when fromEnv is set, the source variable is unset immediately via
// os.Unsetenv before this function returns -- the load-bearing step: it must
// happen before any Box is launched, since both runtimes build a Box's
// environment from process state captured after this point, and this
// credential is never added to that state to begin with. Callers must call
// validateRegistryProxyCredential first to reject both being set; this
// function does not re-check that itself. If a caller skips validation and
// both are set anyway, it deterministically prefers fromEnv rather than
// erroring, since re-validating here would just duplicate that check.
func resolveRegistryProxyCredential(fromFile, fromEnv string) (string, error) {
	v, err := credentialFromSource(fromFile, fromEnv)
	if fromEnv != "" {
		if uerr := os.Unsetenv(fromEnv); uerr != nil {
			return "", fmt.Errorf("unsetting registry proxy credential env var %s: %w", fromEnv, uerr)
		}
	}
	return v, err
}

// peekRegistryProxyCredential resolves a Credential reference (ADR 0044)
// via the same read/validate logic as resolveRegistryProxyCredential (see
// credentialFromSource for those rules), but never calls os.Unsetenv -- a
// non-destructive read for callers, such as doctor's registry-proxy-credential
// check, that need to report on resolvability without consuming the
// credential ahead of the real resolution that must still happen later (see
// resolveRegistryProxyCredential's doc comment for why that later unset is
// load-bearing).
func peekRegistryProxyCredential(fromFile, fromEnv string) (string, error) {
	return credentialFromSource(fromFile, fromEnv)
}

// credentialFromSource does the shared read+validate work for a Credential
// reference (ADR 0044): fromEnv, when set, is read via os.LookupEnv and
// fails closed if unset or empty; fromFile, when set, is read and trimmed of
// all leading/trailing whitespace, failing closed if that trim leaves
// nothing or leaves an embedded newline or carriage return. Neither set
// resolves to "", nil. It does no os.Unsetenv or other side effect --
// callers that need the unset-after-read safety property must do it
// themselves (see resolveRegistryProxyCredential).
func credentialFromSource(fromFile, fromEnv string) (string, error) {
	if fromEnv != "" {
		v, ok := os.LookupEnv(fromEnv)
		if !ok || v == "" {
			return "", fmt.Errorf("registry proxy credential env var %s is unset or empty", fromEnv)
		}
		return v, nil
	}
	if fromFile != "" {
		b, err := os.ReadFile(fromFile)
		if err != nil {
			return "", fmt.Errorf("reading registry proxy credential file %s: %w", fromFile, err)
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
