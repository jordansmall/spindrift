package main

import (
	"strings"
	"testing"
)

// TestValidateRegistryProxyRoutesAmbiguity_RoutesFileAloneIsValid verifies
// that REGISTRY_PROXY_ROUTES_FILE alone, with every scalar REGISTRY_PROXY_*
// knob left unset (including REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT, whose
// baked default is "raw", not empty), is accepted.
func TestValidateRegistryProxyRoutesAmbiguity_RoutesFileAloneIsValid(t *testing.T) {
	err := validateRegistryProxyRoutesAmbiguity("/routes.toml", "", "", "", "raw", "")
	if err != nil {
		t.Errorf("expected nil error for routes file alone, got: %v", err)
	}
}

// TestValidateRegistryProxyRoutesAmbiguity_ScalarsAloneIsValid verifies that
// leaving REGISTRY_PROXY_ROUTES_FILE unset and using the scalar knobs is
// accepted -- this function guards only the file-plus-scalars combination.
func TestValidateRegistryProxyRoutesAmbiguity_ScalarsAloneIsValid(t *testing.T) {
	err := validateRegistryProxyRoutesAmbiguity("", "https://registry.example.com", "/cred", "", "raw", "")
	if err != nil {
		t.Errorf("expected nil error for scalar knobs alone, got: %v", err)
	}
}

// TestValidateRegistryProxyRoutesAmbiguity_NeitherSetIsValid verifies that
// leaving everything unset (the fully-off state) is accepted.
func TestValidateRegistryProxyRoutesAmbiguity_NeitherSetIsValid(t *testing.T) {
	if err := validateRegistryProxyRoutesAmbiguity("", "", "", "", "raw", ""); err != nil {
		t.Errorf("expected nil error when nothing is set, got: %v", err)
	}
}

// TestValidateRegistryProxyRoutesAmbiguity_OneScalarNamesBothKnobs verifies
// that a routes file plus exactly one scalar knob names both
// REGISTRY_PROXY_ROUTES_FILE and the offending scalar knob's env name.
func TestValidateRegistryProxyRoutesAmbiguity_OneScalarNamesBothKnobs(t *testing.T) {
	err := validateRegistryProxyRoutesAmbiguity("/routes.toml", "https://registry.example.com", "", "", "raw", "")
	if err == nil {
		t.Fatal("expected error when both REGISTRY_PROXY_ROUTES_FILE and REGISTRY_PROXY_UPSTREAM_URL are set")
	}
	for _, want := range []string{"REGISTRY_PROXY_ROUTES_FILE", "REGISTRY_PROXY_UPSTREAM_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %s", err.Error(), want)
		}
	}
}

// TestValidateRegistryProxyRoutesAmbiguity_SeveralScalarsNamesEachOne
// verifies that every set scalar knob is named, not just the first found.
func TestValidateRegistryProxyRoutesAmbiguity_SeveralScalarsNamesEachOne(t *testing.T) {
	err := validateRegistryProxyRoutesAmbiguity("/routes.toml", "https://registry.example.com", "/cred-file", "CRED_ENV", "netrc", "my-registry")
	if err == nil {
		t.Fatal("expected error when several scalar knobs are set alongside a routes file")
	}
	for _, want := range []string{
		"REGISTRY_PROXY_ROUTES_FILE",
		"REGISTRY_PROXY_UPSTREAM_URL",
		"REGISTRY_PROXY_CREDENTIAL_FILE",
		"REGISTRY_PROXY_CREDENTIAL_ENV",
		"REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT",
		"REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %s", err.Error(), want)
		}
	}
}

// TestValidateRegistryProxyRoutesAmbiguity_DefaultFileFormatNotConsideredSet
// verifies that REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT's baked default value
// "raw" does not itself count as "set" for ambiguity purposes -- an operator
// who never touched that knob shouldn't be told it conflicts with the
// routes file just because loadSchemaConfig always defaults it to "raw".
func TestValidateRegistryProxyRoutesAmbiguity_DefaultFileFormatNotConsideredSet(t *testing.T) {
	err := validateRegistryProxyRoutesAmbiguity("/routes.toml", "", "", "", "raw", "")
	if err != nil {
		t.Errorf("expected nil error when REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT is left at its default, got: %v", err)
	}
}
