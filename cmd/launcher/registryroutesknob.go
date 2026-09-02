package main

import (
	"fmt"
	"strings"
)

// registryProxyCredentialFileFormatDefault mirrors the baked default for
// REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT declared in lib/env-schema.nix.
const registryProxyCredentialFileFormatDefault = "raw"

// validateRegistryProxyRoutesAmbiguity reports a configuration error when
// routesFile is set alongside any of the five scalar REGISTRY_PROXY_* knobs
// (ADR 0044) -- a registry proxy is configured either via a routes file
// (ADR 0045) or via the scalar knobs, never both, since a routes file
// already carries its own upstream/auth-scheme/credential per route and
// mixing the two leaves no well-defined way to reconcile them. Pure: does no
// I/O and touches no process state.
//
// fileFormat's baked default is "raw" (lib/env-schema.nix), not empty, so an
// operator who never touched REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT must not
// be told it conflicts with the routes file merely because
// loadSchemaConfig always defaults it -- only a value other than "raw"
// counts as "set" here, mirroring how the registry-proxy-credential row
// already treats that knob as inert without REGISTRY_PROXY_CREDENTIAL_FILE.
func validateRegistryProxyRoutesAmbiguity(routesFile, upstreamURL, credFile, credEnv, fileFormat, cargoRegistryName string) error {
	if routesFile == "" {
		return nil
	}

	var set []string
	if upstreamURL != "" {
		set = append(set, "REGISTRY_PROXY_UPSTREAM_URL")
	}
	if credFile != "" {
		set = append(set, "REGISTRY_PROXY_CREDENTIAL_FILE")
	}
	if credEnv != "" {
		set = append(set, "REGISTRY_PROXY_CREDENTIAL_ENV")
	}
	if fileFormat != "" && fileFormat != registryProxyCredentialFileFormatDefault {
		set = append(set, "REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT")
	}
	if cargoRegistryName != "" {
		set = append(set, "REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME")
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("REGISTRY_PROXY_ROUTES_FILE is mutually exclusive with %s: a registry proxy is configured either via a routes file (ADR 0045) or via the scalar REGISTRY_PROXY_* knobs (ADR 0044), never both", strings.Join(set, ", "))
}
