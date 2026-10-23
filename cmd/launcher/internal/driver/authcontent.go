package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// oauthEntryType is the "type" tag an auth-store entry carries when it holds an
// OAuth login rather than an API key.
const oauthEntryType = "oauth"

// apiKeyEntryType is the "type" tag of an auth-store entry holding an API key.
const apiKeyEntryType = "api_key"

// AuthContent describes an env var holding a Provider-keyed auth store (a JSON
// object keyed by Provider id) whose OAuth entries are limited to
// OAuthProviders. It exists because the Driver refreshes an OAuth token inside
// the Box: that is safe only for a Provider whose refresh token never rotates.
type AuthContent struct {
	// Env is the env var holding the auth store.
	Env string
	// OAuthProviders are the Provider ids allowed an OAuth entry.
	OAuthProviders []string
	// APIKeyProviderFor maps an OAuth Provider id to the differently named
	// Provider its API key is under, so a rejection can name the variable.
	APIKeyProviderFor map[string]string
}

// AuthContentFor returns the named Driver's auth-store constraint from its
// generated Descriptor. An empty name defaults to "claude", as New does; ok is
// false for a Driver without one.
func AuthContentFor(name string) (a AuthContent, ok bool) {
	d, found := descriptor(orDefault(name))
	if !found || d.AuthContent == nil {
		return AuthContent{}, false
	}
	return *d.AuthContent, true
}

// Check validates content, the value of a.Env, and returns an error naming
// every Provider holding an OAuth entry outside OAuthProviders. rules supply
// the API-key env var each rejection points at. Errors never carry content.
func (a AuthContent) Check(content string, rules []CredentialRule) error {
	store, ok := parseStore(content)
	if !ok {
		return fmt.Errorf("%s must be a JSON object keyed by Provider id", a.Env)
	}
	providers := make([]string, 0, len(store))
	for p := range store {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	var errs []error
	for _, p := range providers {
		typ, ok := entryType(store[p])
		if !ok {
			errs = append(errs, fmt.Errorf("%s entry for the %s Provider must be an object with a \"type\"", a.Env, p))
			continue
		}
		if typ != oauthEntryType || slices.Contains(a.OAuthProviders, p) {
			continue
		}
		errs = append(errs, fmt.Errorf("%s holds an OAuth entry for the %s Provider, but only %s may use OAuth in a Box (a refresh there must not rotate the host's token); %s",
			a.Env, p, strings.Join(a.OAuthProviders, ", "), a.apiKeyPointer(p, rules)))
	}
	return errors.Join(errs...)
}

// parseStore decodes an auth store. A JSON null decodes without error to a nil
// map, so it is rejected here with every other non-object.
func parseStore(content string) (map[string]json.RawMessage, bool) {
	var store map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &store); err != nil || store == nil {
		return nil, false
	}
	return store, true
}

// entryType reads the exact "type" key. Decoding into a struct would match
// keys case-insensitively and let a later "Type" override the real one, hiding
// an OAuth entry from the guard.
func entryType(raw json.RawMessage) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", false
	}
	var typ string
	if err := json.Unmarshal(fields["type"], &typ); err != nil {
		return "", false
	}
	return typ, typ != ""
}

func (a AuthContent) apiKeyPointer(provider string, rules []CredentialRule) string {
	keyProvider := provider
	if mapped, ok := a.APIKeyProviderFor[provider]; ok {
		keyProvider = mapped
	}
	for _, r := range rules {
		if r.ModelPrefix == keyProvider+"/" && !slices.Contains(r.Env, a.Env) && len(r.Env) > 0 {
			return fmt.Sprintf("set %s for %s instead", r.Env[0], keyProvider)
		}
	}
	return fmt.Sprintf("spindrift carries no API key for the %s Provider, so remove the entry", keyProvider)
}
