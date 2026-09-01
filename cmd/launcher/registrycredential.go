package main

import (
	"fmt"
	"net/url"
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

// validateRegistryProxyUpstreamURL reports a configuration error when
// upstreamURL carries a non-empty path -- REGISTRY_PROXY_UPSTREAM_URL must
// name a bare origin with no path, since the proxy's rewrite logic joins the
// incoming request path onto whatever path the upstream URL already
// carries; a non-empty path here would double onto every proxied request
// path, guaranteeing 404s upstream. A query string is not a path and is
// left alone: registryproxy's rewrite hook deliberately merges an upstream
// RawQuery with the inbound one. Pure: does no I/O and touches no process
// state. An empty upstreamURL is not this function's problem to reject:
// unset is the documented opt-out that disables the registry proxy
// entirely, a policy owned elsewhere, not here.
func validateRegistryProxyUpstreamURL(upstreamURL string) error {
	if upstreamURL == "" {
		return nil
	}
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return fmt.Errorf("parsing REGISTRY_PROXY_UPSTREAM_URL %q: %w", upstreamURL, err)
	}
	// A scheme-less "host:port/path" upstream (missing "//") parses its
	// path into Opaque, not Path (net/url treats "host:port" as scheme
	// "host" with opaque "port/path") -- check there too, or this plausible
	// operator typo slips through here and fails downstream at
	// registryproxy.New with an unrelated "must be absolute" error instead
	// of naming the actual problem.
	path := u.Path
	if u.Opaque != "" {
		if i := strings.IndexByte(u.Opaque, '/'); i >= 0 {
			path = u.Opaque[i:]
		}
	}
	if path != "" && path != "/" {
		return fmt.Errorf("REGISTRY_PROXY_UPSTREAM_URL has path %q: it must name a bare origin with no path (e.g. https://registry.example.com)", path)
	}
	return nil
}

// resolveRegistryProxyCredential resolves a Credential reference (ADR 0044)
// to its value exactly once, via credentialFromSource (see its doc comment
// for the trim/newline/empty/fail-closed rules, and for what registryName is
// used for). Distinct from that shared logic: when fromEnv is set, the
// source variable is unset immediately via os.Unsetenv before this function
// returns -- the load-bearing step: it must happen before any Box is
// launched, since both runtimes build a Box's environment from process state
// captured after this point, and this credential is never added to that
// state to begin with. Callers must call validateRegistryProxyCredential
// first to reject both being set; this function does not re-check that
// itself. If a caller skips validation and both are set anyway, it
// deterministically prefers fromEnv rather than erroring, since
// re-validating here would just duplicate that check.
func resolveRegistryProxyCredential(fromFile, fromEnv, fileFormat, upstreamURL, registryName string) (string, error) {
	v, err := credentialFromSource(fromFile, fromEnv, fileFormat, upstreamURL, registryName)
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
func peekRegistryProxyCredential(fromFile, fromEnv, fileFormat, upstreamURL, registryName string) (string, error) {
	return credentialFromSource(fromFile, fromEnv, fileFormat, upstreamURL, registryName)
}

// credentialFromSource does the shared read+validate work for a Credential
// reference (ADR 0044): fromEnv, when set, is read via os.LookupEnv and
// fails closed if unset or empty. fromFile, when set, is read once; how its
// bytes turn into a credential depends on fileFormat. "raw" (also "", for
// zero-value safety) treats the whole file as the credential: trimmed of all
// leading/trailing whitespace, failing closed if that trim leaves nothing or
// leaves an embedded newline or carriage return. "netrc" instead parses the
// file as netrc-format text (netrcCredential, netrc.go) and extracts the
// password of the entry whose machine matches upstreamURL's bare host.
// "cargo-credentials" instead parses the file as a cargo credentials.toml
// (cargoCredentialsToken, cargocredentials.go) and extracts the token of the
// "[registries.NAME]" table named by registryName, failing closed with a
// config error if registryName is empty -- fileFormat is meaningless for the
// fromEnv branch, since an env var is always a single raw value, so
// upstreamURL and registryName are both unused there too. Neither fromFile
// nor fromEnv set resolves to "", nil. It does no os.Unsetenv or other side
// effect -- callers that need the unset-after-read safety property must do
// it themselves (see resolveRegistryProxyCredential).
func credentialFromSource(fromFile, fromEnv, fileFormat, upstreamURL, registryName string) (string, error) {
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
		switch fileFormat {
		case "", "raw":
			v := strings.TrimSpace(string(b))
			if v == "" {
				return "", fmt.Errorf("registry proxy credential file %s is empty", fromFile)
			}
			if strings.ContainsAny(v, "\r\n") {
				return "", fmt.Errorf("registry proxy credential file %s contains an embedded newline", fromFile)
			}
			return v, nil
		case "netrc":
			u, err := url.Parse(upstreamURL)
			if err != nil || u.Hostname() == "" {
				return "", fmt.Errorf("registry proxy credential file %s is in netrc format but REGISTRY_PROXY_UPSTREAM_URL %q has no parseable host", fromFile, upstreamURL)
			}
			// u.Hostname() strips any port, so a netrc entry keyed
			// "machine host:port" never matches -- the match is host-only,
			// same as REGISTRY_PROXY_UPSTREAM_URL's other consumers.
			return netrcCredential(b, fromFile, u.Hostname())
		case "cargo-credentials":
			if registryName == "" {
				return "", fmt.Errorf("registry proxy credential file %s is in cargo-credentials format but REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME is unset: it must be set when REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=cargo-credentials", fromFile)
			}
			return cargoCredentialsToken(b, fromFile, registryName)
		default:
			// Unreachable through configuration: choiceKnobRegistry rejects
			// any REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT value outside
			// raw/netrc/cargo-credentials before bootstrap ever reaches this
			// function. Kept as defense in depth for a caller that skips
			// validateChoice.
			return "", fmt.Errorf("registry proxy credential file %s has unrecognized format %q", fromFile, fileFormat)
		}
	}
	return "", nil
}
