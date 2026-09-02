package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// retiredRegistryProxyKnobs bundles the five scalar REGISTRY_PROXY_* knobs
// (ADR 0044) retired by issue #3145, threaded together through
// validateRetiredRegistryProxyKnobs and its stanza-rendering helpers as one
// value: named fields make which value is which explicit at every call site,
// unlike five positional string parameters.
type retiredRegistryProxyKnobs struct {
	upstreamURL, credFile, credEnv, fileFormat, cargoRegistryName string
}

// retiredRegistryProxyKnobField pairs one of the five knobs' env var name
// with a pointer to the struct field it fills, letting both
// retiredRegistryProxyKnobsFromEnv (which reads them) and the validation
// loop below (naming which knobs are set) walk the same table
// instead of each hand-enumerating the five env var names.
type retiredRegistryProxyKnobField struct {
	envName string
	value   *string
}

// fields returns the five (env var name, field pointer) pairs in
// declaration order -- the one place each REGISTRY_PROXY_* env var name is
// paired with the struct field it fills. (The names themselves still appear
// as prose elsewhere in this file, e.g. the stanza's <ALL_CAPS> placeholders
// and the unrecognized-format error text.)
func (k *retiredRegistryProxyKnobs) fields() []retiredRegistryProxyKnobField {
	return []retiredRegistryProxyKnobField{
		{"REGISTRY_PROXY_UPSTREAM_URL", &k.upstreamURL},
		{"REGISTRY_PROXY_CREDENTIAL_FILE", &k.credFile},
		{"REGISTRY_PROXY_CREDENTIAL_ENV", &k.credEnv},
		{"REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT", &k.fileFormat},
		{"REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME", &k.cargoRegistryName},
	}
}

// retiredRegistryProxyKnobsFromEnv reads the five retired scalar
// REGISTRY_PROXY_* knobs from the ambient environment, falling back to the
// loaded input document's settings the way every other knob resolves: a
// document written by an older spindrift (or by hand) still carries these
// keys, and lib/env-schema.nix no longer declares them, so without this
// fallback such a setting would silently do nothing instead of tripping the
// gate. Deliberately not getenvSchema: that layers schemaDefault on top, and
// a generated default -- REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT's "raw", say
// -- would read as a knob the operator set. This is the only I/O in this
// file -- validateRetiredRegistryProxyKnobs and the stanza builders below
// stay pure, taking the already-read values in.
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

// validateRetiredRegistryProxyKnobs reports a configuration error when any
// of the five scalar REGISTRY_PROXY_* knobs (ADR 0044) is set: issue #3145
// retires them in favor of the routes file (ADR 0045), the sole remaining
// way to configure a registry proxy. Pure: does no I/O and touches no
// process state -- the caller (registryProxyRoutesCheck, checks.go) calls
// retiredRegistryProxyKnobsFromEnv to read ambient env and the input
// document and hands the result in, since these knobs no longer exist on
// config now that the schema itself has dropped them.
//
// fileFormat has no "raw is inert" carve-out: the values arrive from
// retiredRegistryProxyKnobsFromEnv, which applies no schema default, so a
// non-empty value can only be an operator's own explicit setting -- "raw"
// included -- and always counts as "set".
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
// equivalent to the registry route the operator currently has configured via
// the scalar knobs -- match-host derived from url.Parse(upstreamURL).Host,
// auth-scheme always "bearer" (the only scheme the scalar knobs ever spoke)
// -- so migrating off them is "paste this stanza into
// REGISTRY_PROXY_ROUTES_FILE", not "read ADR 0045 from scratch". Any field
// not derivable from what the operator actually set (e.g. no
// REGISTRY_PROXY_UPSTREAM_URL, or one with no parseable Host, like a
// scheme-less "user:secret@host") renders as an obvious <ALL_CAPS>
// placeholder naming the knob to fill in, rather than an empty or
// silently-wrong value.
func retiredRegistryProxyKnobsStanza(k retiredRegistryProxyKnobs) string {
	matchHost := "<derived from REGISTRY_PROXY_UPSTREAM_URL>"
	upstreamBaseURL := "<REGISTRY_PROXY_UPSTREAM_URL>"
	if k.upstreamURL != "" {
		// Only a parsed, hosted URL is echoed back: a scheme-less
		// "user:s3cr3t@host" parses without error into an opaque body with an
		// empty Host, so anything short of a Host would carry the operator's
		// value -- userinfo and all -- past the strip below.
		if u, err := url.Parse(k.upstreamURL); err == nil && u.Host != "" {
			matchHost = u.Host
			// Strip any inline userinfo (https://user:token@host/) before
			// this lands in the stanza: this error is printed to stderr and
			// CI logs, and a credential belongs in the route's "credential"
			// key, never echoed back from the URL.
			u.User = nil
			upstreamBaseURL = u.String()
		}
	}

	var b strings.Builder
	b.WriteString("[[routes]]\n")
	fmt.Fprintf(&b, "match-host = %q\n", matchHost)
	fmt.Fprintf(&b, "upstream-base-url = %q\n", upstreamBaseURL)
	b.WriteString("auth-scheme = \"bearer\"\n")
	// A blank credential stanza (neither knob set, ADR 0045's documented
	// unauthenticated pass-through) means literally no "credential" key --
	// see retiredRegistryProxyCredentialStanza -- so it's omitted rather
	// than printed as an empty line.
	if cred := retiredRegistryProxyCredentialStanza(k); cred != "" {
		fmt.Fprintf(&b, "%s\n", cred)
	}
	return b.String()
}

// retiredRegistryProxyCredentialStanza builds the "credential = { ... }"
// line, choosing the inline-table key the way registryroutes.Parse
// (credential source parsing) would require: exactly one source key, "file"
// vs "netrc" vs "cargo-credentials" picked by fileFormat, "cargo-credentials"
// alone requiring a paired registry-name. fileFormat alone (no credFile)
// still picks netrc/cargo-credentials -- the format names the source key
// regardless of whether a path was given -- falling back to the
// <REGISTRY_PROXY_CREDENTIAL_FILE> placeholder for the path. cargoRegistryName
// alone (no fileFormat) is likewise treated as cargo-credentials -- naming a
// registry is itself a declaration of intent, unlike the "nothing at all"
// case below. credEnv wins over credFile/fileFormat when both are set:
// either one alone already trips validateRetiredRegistryProxyKnobs, so this
// function only renders the stanza for an already-refused config and just
// needs to pick something deterministic.
//
// Returns "" -- no credential line at all -- when none of credEnv, credFile,
// fileFormat, or cargoRegistryName is set: REGISTRY_PROXY_UPSTREAM_URL alone
// was a documented, working unauthenticated pass-through on origin/main
// (registryroutes.Parse treats an absent credential key the same way), and
// the migration stanza must reproduce that instead of inventing a bogus
// credential requirement.
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
			// REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT's schema choices were
			// exactly raw/netrc/cargo-credentials, but the retired knob no
			// longer validates the value at all -- an operator value like
			// "npmrc" is a real routes-file source key with unrelated
			// semantics (whole file as bearer token), so silently rendering
			// it as "file" would be wrong in a way that still parses. The
			// "file" key is kept (so the stanza stays parseable) but flagged
			// with a comment rather than guessed at.
			return fmt.Sprintf("credential = { file = %q }\n# unrecognized REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT %q; the retired knob only ever accepted \"raw\", \"netrc\", or \"cargo-credentials\"", file, format)
		}
	default:
		return ""
	}
}
