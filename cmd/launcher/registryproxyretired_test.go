package main

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryroutes"
)

// TestValidateRetiredRegistryProxyKnobs_AllUnsetIsValid verifies that the
// overwhelmingly common case -- none of the five retired scalar
// REGISTRY_PROXY_* knobs set -- is accepted.
func TestValidateRetiredRegistryProxyKnobs_AllUnsetIsValid(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{})
	if err != nil {
		t.Errorf("expected nil error when nothing is set, got: %v", err)
	}
}

// TestValidateRetiredRegistryProxyKnobs_UpstreamURLAloneNamesKnobAndADR
// verifies that a single retired knob set produces an error naming that
// knob's env var, the replacement (REGISTRY_PROXY_ROUTES_FILE), and the
// governing ADR/issue -- so an operator hitting this for the first time
// knows both what broke and where the replacement is documented.
func TestValidateRetiredRegistryProxyKnobs_UpstreamURLAloneNamesKnobAndADR(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com"})
	if err == nil {
		t.Fatal("expected error when REGISTRY_PROXY_UPSTREAM_URL is set")
	}
	for _, want := range []string{
		"REGISTRY_PROXY_UPSTREAM_URL",
		"REGISTRY_PROXY_ROUTES_FILE",
		"ADR 0045",
		"#3145",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must mention %s", err.Error(), want)
		}
	}
}

// TestValidateRetiredRegistryProxyKnobs_SeveralKnobsNameEachInDeclarationOrder
// verifies that every set knob is named, in declaration order (not map
// order), so the error is deterministic across runs.
func TestValidateRetiredRegistryProxyKnobs_SeveralKnobsNameEachInDeclarationOrder(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{
		upstreamURL: "https://registry.example.com", credFile: "/cred-file", credEnv: "CRED_ENV",
		fileFormat: "netrc", cargoRegistryName: "my-registry",
	})
	if err == nil {
		t.Fatal("expected error when several retired knobs are set")
	}
	names := []string{
		"REGISTRY_PROXY_UPSTREAM_URL",
		"REGISTRY_PROXY_CREDENTIAL_FILE",
		"REGISTRY_PROXY_CREDENTIAL_ENV",
		"REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT",
		"REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME",
	}
	msg := err.Error()
	last := -1
	for _, name := range names {
		i := strings.Index(msg, name)
		if i < 0 {
			t.Fatalf("error %q must name %s", msg, name)
		}
		if i < last {
			t.Errorf("error %q names %s out of declaration order", msg, name)
		}
		last = i
	}
}

// TestValidateRetiredRegistryProxyKnobs_FileFormatRawCountsAsSet asserts the
// one place the old ambiguity check's "raw is inert" carve-out does not
// survive: the schema default disappears along with the retired knob, so a
// non-empty REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT -- even "raw" -- can only
// come from an operator who set it explicitly, and must be reported.
func TestValidateRetiredRegistryProxyKnobs_FileFormatRawCountsAsSet(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{fileFormat: "raw"})
	if err == nil {
		t.Fatal("expected error when REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=raw is explicitly set")
	}
	if !strings.Contains(err.Error(), "REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT") {
		t.Errorf("error %q must name REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT", err.Error())
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaDerivesMatchHost verifies the
// stanza derives match-host from the upstream URL
// (url.Parse(upstreamURL).Host) and carries the always-bearer auth-scheme --
// the only scheme the scalar knobs ever spoke. A plain https upstream on the
// default port names no upstream-origin: the host-rooted route match-host
// declares already says everything that URL did (ADR 0047, issue #3261).
func TestValidateRetiredRegistryProxyKnobs_StanzaDerivesMatchHost(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com"})
	msg := err.Error()
	for _, want := range []string{
		"[[routes]]",
		`match-host = "registry.example.com"`,
		`auth-scheme = "bearer"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("stanza %q must contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "upstream-origin") {
		t.Errorf("stanza %q must name no upstream-origin for a plain https upstream on the default port", msg)
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaNamesOriginForNonDefaultScheme
// verifies the stanza does name an upstream-origin when the retired upstream
// URL said something match-host alone cannot -- a non-default scheme or an
// explicit port -- and that the origin carries no path.
func TestValidateRetiredRegistryProxyKnobs_StanzaNamesOriginForNonDefaultScheme(t *testing.T) {
	for _, tc := range []struct {
		upstream string
		want     string
	}{
		{"http://registry.example.com/base", `upstream-origin = "http://registry.example.com"`},
		{"https://registry.example.com:8443/base", `upstream-origin = "https://registry.example.com:8443"`},
	} {
		t.Run(tc.upstream, func(t *testing.T) {
			err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: tc.upstream})
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("stanza %q must contain %q", msg, tc.want)
			}
			if strings.Contains(msg, "/base") {
				t.Errorf("stanza %q must not carry the retired URL's path", msg)
			}
		})
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaPlaceholdersWhenUpstreamUnset
// verifies that a credential-only knob set (no REGISTRY_PROXY_UPSTREAM_URL)
// still renders a usable skeleton stanza, with an obvious placeholder for
// the fields it can't derive rather than an empty or silently-wrong value.
func TestValidateRetiredRegistryProxyKnobs_StanzaPlaceholdersWhenUpstreamUnset(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{credFile: "/cred-file"})
	msg := err.Error()
	if !strings.Contains(msg, `match-host = "<derived from REGISTRY_PROXY_UPSTREAM_URL>"`) {
		t.Errorf("stanza %q must placeholder the underivable match-host", msg)
	}
	if !strings.Contains(msg, `credential = { file = "/cred-file" }`) {
		t.Errorf("stanza %q must derive the file credential source", msg)
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaCredentialEnvSource verifies
// the env credential source key.
func TestValidateRetiredRegistryProxyKnobs_StanzaCredentialEnvSource(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", credEnv: "MY_TOKEN"})
	if !strings.Contains(err.Error(), `credential = { env = "MY_TOKEN" }`) {
		t.Errorf("stanza %q must derive the env credential source", err.Error())
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaCredentialNetrcSource verifies
// the netrc credential source key, selected by
// REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=netrc.
func TestValidateRetiredRegistryProxyKnobs_StanzaCredentialNetrcSource(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", credFile: "/home/.netrc", fileFormat: "netrc"})
	if !strings.Contains(err.Error(), `credential = { netrc = "/home/.netrc" }`) {
		t.Errorf("stanza %q must derive the netrc credential source", err.Error())
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaCredentialNetrcSourceNoFile
// verifies that REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=netrc alone (no
// REGISTRY_PROXY_CREDENTIAL_FILE) still picks the netrc source key --
// derivable from the format alone -- rather than falling back to the
// generic "file"/"env" placeholder.
func TestValidateRetiredRegistryProxyKnobs_StanzaCredentialNetrcSourceNoFile(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", fileFormat: "netrc"})
	if !strings.Contains(err.Error(), `credential = { netrc = "<REGISTRY_PROXY_CREDENTIAL_FILE>" }`) {
		t.Errorf("stanza %q must derive the netrc credential source even without a file", err.Error())
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaCredentialCargoCredentialsSourceNoFile
// verifies the cargo-credentials analog: format alone still derives the
// cargo-credentials source key and its paired registry-name.
func TestValidateRetiredRegistryProxyKnobs_StanzaCredentialCargoCredentialsSourceNoFile(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", fileFormat: "cargo-credentials", cargoRegistryName: "my-registry"})
	if !strings.Contains(err.Error(), `credential = { cargo-credentials = "<REGISTRY_PROXY_CREDENTIAL_FILE>", registry-name = "my-registry" }`) {
		t.Errorf("stanza %q must derive the cargo-credentials source even without a file", err.Error())
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaCredentialCargoCredentialsSource
// verifies the cargo-credentials source key pairs with registry-name, the
// companion key registryroutes.Parse requires alongside it.
func TestValidateRetiredRegistryProxyKnobs_StanzaCredentialCargoCredentialsSource(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", credFile: "/home/.cargo/credentials.toml", fileFormat: "cargo-credentials", cargoRegistryName: "my-registry"})
	if !strings.Contains(err.Error(), `credential = { cargo-credentials = "/home/.cargo/credentials.toml", registry-name = "my-registry" }`) {
		t.Errorf("stanza %q must derive the cargo-credentials source with its registry-name", err.Error())
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaNoCredentialKeyWhenNoSource
// verifies that REGISTRY_PROXY_UPSTREAM_URL set alone, with neither
// credential knob set, renders no "credential" key at all -- that
// configuration was a documented, working unauthenticated pass-through
// proxy on origin/main, and the migration stanza must reproduce it exactly
// rather than inventing a bogus credential requirement.
func TestValidateRetiredRegistryProxyKnobs_StanzaNoCredentialKeyWhenNoSource(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com"})
	if strings.Contains(err.Error(), "credential") {
		t.Errorf("stanza %q must not contain a credential key for an unauthenticated pass-through", err.Error())
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaCargoRegistryNameAloneStillCargoCredentials
// verifies that REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME set alone (no
// file, env, or format) still renders the cargo-credentials source instead
// of falling through to the unauthenticated pass-through -- the operator
// did name a cargo registry, so treating it as unauthenticated would drop
// their intent silently.
func TestValidateRetiredRegistryProxyKnobs_StanzaCargoRegistryNameAloneStillCargoCredentials(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{
		upstreamURL: "https://registry.example.com", cargoRegistryName: "my-registry",
	})
	if !strings.Contains(err.Error(), `credential = { cargo-credentials = "<REGISTRY_PROXY_CREDENTIAL_FILE>", registry-name = "my-registry" }`) {
		t.Errorf("stanza %q must derive the cargo-credentials source from the registry name alone", err.Error())
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaUnrecognizedFileFormatIsCalledOut
// verifies that an unrecognized REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT (e.g.
// "npmrc", a real routes-file source key with different semantics -- it
// sends the whole file as a bearer token) still renders a parseable "file"
// key rather than silently going quiet, but calls out the mismatch so it
// isn't mistaken for a "raw" file credential.
func TestValidateRetiredRegistryProxyKnobs_StanzaUnrecognizedFileFormatIsCalledOut(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{
		upstreamURL: "https://registry.example.com", credFile: "/cred-file", fileFormat: "npmrc",
	})
	msg := err.Error()
	if !strings.Contains(msg, `credential = { file = "/cred-file" }`) {
		t.Errorf("stanza %q must still render a file key for an unrecognized format", msg)
	}
	for _, want := range []string{"npmrc", "raw", "netrc", "cargo-credentials"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stanza %q must call out the unrecognized format %q and the accepted formats", msg, want)
		}
	}
}

// TestValidateRetiredRegistryProxyKnobs_StanzaParsesThroughRegistryroutes feeds
// the rendered [[routes]] stanza -- for a representative table of knob
// combinations, including placeholders -- back through
// registryroutes.Parse, the real consumer of a routes file. A
// strings.Contains check against a hand-written literal (as every other
// stanza test in this file does) can't catch a stanza that reads right but
// fails to parse; this is the guard that would have caught the defective
// unauthenticated-pass-through stanza directly.
func TestValidateRetiredRegistryProxyKnobs_StanzaParsesThroughRegistryroutes(t *testing.T) {
	cases := []struct {
		name  string
		knobs retiredRegistryProxyKnobs
	}{
		{"unauthenticated pass-through", retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com"}},
		{"env", retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", credEnv: "MY_TOKEN"}},
		{"netrc", retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", credFile: "/home/.netrc", fileFormat: "netrc"}},
		{"netrc placeholder", retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", fileFormat: "netrc"}},
		{"cargo-credentials", retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", credFile: "/home/.cargo/credentials.toml", fileFormat: "cargo-credentials", cargoRegistryName: "my-registry"}},
		{"cargo-credentials placeholder", retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", fileFormat: "cargo-credentials", cargoRegistryName: "my-registry"}},
		{"cargo registry name alone", retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", cargoRegistryName: "my-registry"}},
		{"file, no format", retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", credFile: "/cred-file"}},
		{"unrecognized file format", retiredRegistryProxyKnobs{upstreamURL: "https://registry.example.com", credFile: "/cred-file", fileFormat: "npmrc"}},
		{"no upstream URL", retiredRegistryProxyKnobs{credFile: "/cred-file"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRetiredRegistryProxyKnobs(tc.knobs)
			if err == nil {
				t.Fatal("expected error")
			}
			stanza := extractStanza(t, err.Error())
			if _, err := registryroutes.Parse([]byte(stanza)); err != nil {
				t.Errorf("rendered stanza %q must parse via registryroutes.Parse, got: %v", stanza, err)
			}
		})
	}
}

// extractStanza pulls the [[routes]] stanza out of a
// validateRetiredRegistryProxyKnobs error, which prefixes it with
// human-facing prose (the knob names, ADR, issue) that isn't itself valid
// TOML.
func extractStanza(t *testing.T, msg string) string {
	t.Helper()
	const marker = "equivalent routes-file stanza:\n\n"
	i := strings.Index(msg, marker)
	if i < 0 {
		t.Fatalf("error %q must contain marker %q", msg, marker)
	}
	return msg[i+len(marker):]
}

// TestValidateRetiredRegistryProxyKnobs_StanzaStripsUserinfo verifies that
// an upstream URL carrying inline userinfo (e.g. "https://user:token@host/")
// never echoes the secret into the rendered stanza -- this error lands on
// stderr and in CI logs.
func TestValidateRetiredRegistryProxyKnobs_StanzaStripsUserinfo(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "https://user:s3cr3t@registry.example.com:8443/"})
	msg := err.Error()
	for _, want := range []string{
		`match-host = "registry.example.com:8443"`,
		`upstream-origin = "https://registry.example.com:8443"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("stanza %q must contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "s3cr3t") {
		t.Errorf("stanza %q must not leak the upstream URL's userinfo secret", msg)
	}
}

// A scheme-less upstream URL parses without error into an empty Host, so the
// userinfo strip above never runs on it -- the stanza must placeholder the
// value rather than echo the operator's secret into stderr and CI logs.
func TestValidateRetiredRegistryProxyKnobs_StanzaPlaceholdersHostlessUpstream(t *testing.T) {
	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobs{upstreamURL: "user:s3cr3t@registry.example.com"})
	msg := err.Error()
	if !strings.Contains(msg, `match-host = "<derived from REGISTRY_PROXY_UPSTREAM_URL>"`) {
		t.Errorf("stanza %q must placeholder match-host when the value has no host", msg)
	}
	if strings.Contains(msg, "s3cr3t") {
		t.Errorf("stanza %q must not leak the unparseable upstream URL's secret", msg)
	}
}

// TestRetiredRegistryProxyKnobsFromEnv_EachEnvVarFillsItsOwnField drives every
// one of the five (env var name, field) rows in fields() through the real
// read path -- t.Setenv one var at a time, then
// retiredRegistryProxyKnobsFromEnv + validateRetiredRegistryProxyKnobs --
// instead of constructing retiredRegistryProxyKnobs directly like every other
// test in this file. Every other stanza test builds the struct by hand, so a
// row in fields() swapped with its neighbor (e.g. REGISTRY_PROXY_CREDENTIAL_FILE
// wired to &k.fileFormat instead of &k.credFile) would still render a
// deceptively-plausible stanza and leave every one of those tests green; only
// reading through the env var name, as an operator's shell actually would,
// can catch that. Per-knob values are distinct enough (each embeds the
// knob's own name) that a transposed row lands the wrong value in the wrong
// stanza position rather than merely a wrong-looking but still-matching one.
func TestRetiredRegistryProxyKnobsFromEnv_EachEnvVarFillsItsOwnField(t *testing.T) {
	cases := []struct {
		envName      string
		value        string
		wantInStanza string
	}{
		{
			envName:      "REGISTRY_PROXY_UPSTREAM_URL",
			value:        "https://upstream-url-value.example.com",
			wantInStanza: `match-host = "upstream-url-value.example.com"`,
		},
		{
			envName:      "REGISTRY_PROXY_CREDENTIAL_FILE",
			value:        "/credential-file-value",
			wantInStanza: `credential = { file = "/credential-file-value" }`,
		},
		{
			envName:      "REGISTRY_PROXY_CREDENTIAL_ENV",
			value:        "credential-env-value",
			wantInStanza: `credential = { env = "credential-env-value" }`,
		},
		{
			// The value ("netrc") is a source-key selector, not itself echoed
			// into the stanza; a transposed row would instead pick the "file"
			// or fall through to no credential line at all, so asserting the
			// netrc key with the (file-less) placeholder still catches it.
			envName:      "REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT",
			value:        "netrc",
			wantInStanza: `credential = { netrc = "<REGISTRY_PROXY_CREDENTIAL_FILE>" }`,
		},
		{
			envName:      "REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME",
			value:        "cargo-registry-name-value",
			wantInStanza: `credential = { cargo-credentials = "<REGISTRY_PROXY_CREDENTIAL_FILE>", registry-name = "cargo-registry-name-value" }`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.envName, func(t *testing.T) {
			t.Setenv(tc.envName, tc.value)
			// REGISTRY_PROXY_UPSTREAM_URL is required for the other four
			// cases to render a parseable, easily-asserted stanza position
			// (an unset upstream renders its own <ALL_CAPS> placeholder
			// instead); it isn't itself under test outside its own case.
			if tc.envName != "REGISTRY_PROXY_UPSTREAM_URL" {
				t.Setenv("REGISTRY_PROXY_UPSTREAM_URL", "https://registry.example.com")
			}

			err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobsFromEnv())
			if err == nil {
				t.Fatal("expected error when a retired knob is set")
			}
			if !strings.Contains(err.Error(), tc.envName) {
				t.Errorf("error %q must name %s", err.Error(), tc.envName)
			}
			if !strings.Contains(err.Error(), tc.wantInStanza) {
				t.Errorf("stanza %q must contain %q", err.Error(), tc.wantInStanza)
			}
		})
	}
}

// withLoadedDoc points the package-level loadedDoc at doc for the duration of
// the calling test, restoring the prior value (nil in every other test in
// this package) via t.Cleanup so no test's document setting leaks into the
// next.
func withLoadedDoc(t *testing.T, doc *inputDocument) {
	t.Helper()
	prev := loadedDoc
	loadedDoc = doc
	t.Cleanup(func() { loadedDoc = prev })
}

// TestRetiredRegistryProxyKnobsFromEnv_ReadsInputDocumentSettings verifies
// that a retired knob supplied only through the ADR 0020 input document's
// settings section (e.g. a Consumer flake's `settings.REGISTRY_PROXY_CREDENTIAL_ENV`,
// never exported as ambient env by the wrapper) still trips the gate --
// mirroring the document-fallback precedence every other schema knob gets via
// getenvSchema, so the retired-knob gate isn't a silent exception to it.
func TestRetiredRegistryProxyKnobsFromEnv_ReadsInputDocumentSettings(t *testing.T) {
	withLoadedDoc(t, &inputDocument{Settings: map[string]string{
		"REGISTRY_PROXY_CREDENTIAL_ENV": "SOME_ENV_VAR",
	}})

	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobsFromEnv())
	if err == nil {
		t.Fatal("expected error when REGISTRY_PROXY_CREDENTIAL_ENV is set only via the input document's settings")
	}
	if !strings.Contains(err.Error(), "REGISTRY_PROXY_CREDENTIAL_ENV") {
		t.Errorf("error %q must name REGISTRY_PROXY_CREDENTIAL_ENV", err.Error())
	}
	if !strings.Contains(err.Error(), `credential = { env = "SOME_ENV_VAR" }`) {
		t.Errorf("stanza %q must derive the env credential source from the document-supplied value", err.Error())
	}
}

// TestRetiredRegistryProxyKnobsFromEnv_AmbientEnvWinsOverInputDocument pins
// the precedence order: an ambient env value for a retired knob overrides the
// input document's settings value for the same knob, the identical
// env-over-document precedence getenvSchema applies to every other knob.
func TestRetiredRegistryProxyKnobsFromEnv_AmbientEnvWinsOverInputDocument(t *testing.T) {
	withLoadedDoc(t, &inputDocument{Settings: map[string]string{
		"REGISTRY_PROXY_CREDENTIAL_ENV": "FROM_DOCUMENT",
	}})
	t.Setenv("REGISTRY_PROXY_CREDENTIAL_ENV", "FROM_AMBIENT_ENV")

	err := validateRetiredRegistryProxyKnobs(retiredRegistryProxyKnobsFromEnv())
	if err == nil {
		t.Fatal("expected error when REGISTRY_PROXY_CREDENTIAL_ENV is set")
	}
	if !strings.Contains(err.Error(), `credential = { env = "FROM_AMBIENT_ENV" }`) {
		t.Errorf("stanza %q must use the ambient env value, not the document's", err.Error())
	}
}
