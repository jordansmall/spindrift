package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/registryroutes"
)

// retiredRegistryProxyKnobs bundles the five scalar REGISTRY_PROXY_* knobs
// (ADR 0044) retired by issue #3145.
type retiredRegistryProxyKnobs struct {
	upstreamURL, credFile, credEnv, fileFormat, cargoRegistryName string
}

type retiredRegistryProxyKnobField struct {
	envName string
	value   *string
}

// fields returns the five (env var name, field pointer) pairs, the one place
// each REGISTRY_PROXY_* env var name is paired with the field it fills, so the
// reader and the validation loop walk one table.
func (k *retiredRegistryProxyKnobs) fields() []retiredRegistryProxyKnobField {
	return []retiredRegistryProxyKnobField{
		{"REGISTRY_PROXY_UPSTREAM_URL", &k.upstreamURL},
		{"REGISTRY_PROXY_CREDENTIAL_FILE", &k.credFile},
		{"REGISTRY_PROXY_CREDENTIAL_ENV", &k.credEnv},
		{"REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT", &k.fileFormat},
		{"REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME", &k.cargoRegistryName},
	}
}

// retiredRegistryProxyKnobsFromEnv reads the five retired knobs from the
// environment, falling back to the loaded input document: lib/env-schema.nix
// no longer declares them, so a document written by an older spindrift would
// otherwise carry a setting that silently does nothing. Not getenvSchema,
// whose schemaDefault would read as a knob the operator set.
func retiredRegistryProxyKnobsFromEnv() retiredRegistryProxyKnobs {
	var k retiredRegistryProxyKnobs
	for _, f := range k.fields() {
		v := os.Getenv(f.envName)
		if v == "" && loadedDoc != nil {
			v = loadedDoc.Settings[f.envName]
		}
		*f.value = v
	}
	return k
}

// validateRetiredRegistryProxyKnobs reports a configuration error when any of
// the five scalar REGISTRY_PROXY_* knobs (ADR 0044) is set: issue #3145
// retires them in favor of the routes file (ADR 0045). It does no I/O; the
// caller reads the values in. A non-empty fileFormat counts as set even when
// it is "raw", because nothing upstream applies a schema default.
func validateRetiredRegistryProxyKnobs(knobs retiredRegistryProxyKnobs) error {
	var set []string
	for _, f := range knobs.fields() {
		if *f.value != "" {
			set = append(set, f.envName)
		}
	}
	if len(set) == 0 {
		return nil
	}

	verb := "is"
	if len(set) > 1 {
		verb = "are"
	}
	return fmt.Errorf(
		"%s %s retired in favor of REGISTRY_PROXY_ROUTES_FILE (ADR 0045, issue #3145); equivalent routes-file stanza:\n\n%s",
		strings.Join(set, ", "), verb,
		retiredRegistryProxyKnobsStanza(knobs),
	)
}

// retiredRegistryProxyKnobsStanza builds a copy-pasteable [[routes]] entry
// equivalent to the route the operator configured through the scalar knobs.
// auth-scheme is always "bearer", the only scheme those knobs supported. A
// field not derivable from what the operator set renders as an <ALL_CAPS>
// placeholder naming the knob to fill in, never an empty or wrong value.
func retiredRegistryProxyKnobsStanza(k retiredRegistryProxyKnobs) string {
	matchHost := "<derived from REGISTRY_PROXY_UPSTREAM_URL>"
	if k.upstreamURL != "" {
		// A scheme-less "user:s3cr3t@host" parses without error into an opaque
		// body with an empty Host, so demanding a Host keeps the operator's
		// userinfo out of this error, which goes to stderr and CI logs. u.Host
		// carries host[:port] only, never the inline userinfo a URL may embed.
		if u, err := url.Parse(k.upstreamURL); err == nil && u.Host != "" {
			matchHost = u.Host
		}
	}
	// registryroutes owns the rule for whether a stanza needs an
	// upstream-origin, so this remedy cannot disagree with what "spindrift
	// registry discover" writes.
	upstreamOrigin := registryroutes.UpstreamOriginFor(k.upstreamURL)

	var b strings.Builder
	b.WriteString("[[routes]]\n")
	fmt.Fprintf(&b, "match-host = %q\n", matchHost)
	if upstreamOrigin != "" {
		fmt.Fprintf(&b, "upstream-origin = %q\n", upstreamOrigin)
	}
	b.WriteString("auth-scheme = \"bearer\"\n")
	// ADR 0045's unauthenticated pass-through has no "credential" key at all,
	// so omit the line rather than print it empty.
	if cred := retiredRegistryProxyCredentialStanza(k); cred != "" {
		fmt.Fprintf(&b, "%s\n", cred)
	}
	return b.String()
}

// retiredRegistryProxyCredentialStanza builds the "credential = { ... }" line
// with exactly one source key, as registryroutes.Parse requires: fileFormat
// picks file, netrc, or cargo-credentials, and a registry name alone implies
// cargo-credentials. credEnv wins over credFile when both are set; either one
// alone already fails validation, so the pick only has to be deterministic.
func retiredRegistryProxyCredentialStanza(k retiredRegistryProxyKnobs) string {
	switch {
	case k.credEnv != "":
		return fmt.Sprintf("credential = { env = %q }", k.credEnv)
	case k.credFile != "" || k.fileFormat != "" || k.cargoRegistryName != "":
		file := k.credFile
		if file == "" {
			file = "<REGISTRY_PROXY_CREDENTIAL_FILE>"
		}
		format := k.fileFormat
		if format == "" && k.cargoRegistryName != "" {
			format = "cargo-credentials"
		}
		switch format {
		case "", "raw":
			return fmt.Sprintf("credential = { file = %q }", file)
		case "netrc":
			return fmt.Sprintf("credential = { netrc = %q }", file)
		case "cargo-credentials":
			name := k.cargoRegistryName
			if name == "" {
				name = "<REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME>"
			}
			return fmt.Sprintf("credential = { cargo-credentials = %q, registry-name = %q }", file, name)
		default:
			// The retired knob no longer validates this value, and an operator
			// value like "npmrc" is a real routes-file source key with
			// unrelated semantics (the whole file as a bearer token), so
			// rendering it as "file" would be wrong in a way that still
			// parses. Keep "file" so the stanza parses, and flag it.
			return fmt.Sprintf("credential = { file = %q }\n# unrecognized REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT %q; the retired knob only ever accepted \"raw\", \"netrc\", or \"cargo-credentials\"", file, format)
		}
	default:
		// REGISTRY_PROXY_UPSTREAM_URL alone was a documented unauthenticated
		// pass-through, and registryroutes.Parse treats an absent credential
		// key the same way, so emit no credential line.
		return ""
	}
}
