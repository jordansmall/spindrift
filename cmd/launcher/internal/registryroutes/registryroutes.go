// Package registryroutes parses and validates a registry proxy routes file
// (ADR 0045): a TOML document declaring one or more Registry routes, each
// binding a match host, an upstream base URL, an auth scheme, and a
// credential reference in a single record -- the property ADR 0045 calls
// load-bearing, since it leaves no Box-reachable way to pair a credential
// meant for one host with a different one.
package registryroutes

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"spindrift.dev/launcher/internal/credresolver"
)

// credentialSourceKeys are the credential inline table keys that name a
// credential source (ADR 0045); exactly one must be present per route.
// "registry-name" is deliberately excluded -- it's a companion key for
// cargo-credentials, not a source of its own.
var credentialSourceKeys = []string{"env", "file", "netrc", "cargo-credentials"}

func isCredentialSourceKey(key string) bool {
	for _, k := range credentialSourceKeys {
		if k == key {
			return true
		}
	}
	return false
}

// Route is one entry of a routes file (ADR 0045), normalized and validated.
type Route struct {
	MatchHost        string
	UpstreamBaseURL  string
	AuthScheme       string
	EnforceAllowlist bool
	Credential       credresolver.Config
}

// rawFile is the strict TOML decode target for a routes file. Credential is
// decoded as a map, not a struct, so its exactly-one-source and unknown-key
// checks can be done by hand and reported against the offending route -- a
// fixed struct with DisallowUnknownFields would reject an unknown credential
// key too, but with a bare go-toml error that names neither the route nor
// the key the way the rest of this package's errors do.
type rawFile struct {
	Routes []rawRoute `toml:"routes"`
}

type rawRoute struct {
	MatchHost        string            `toml:"match-host"`
	UpstreamBaseURL  string            `toml:"upstream-base-url"`
	AuthScheme       string            `toml:"auth-scheme"`
	Credential       map[string]string `toml:"credential"`
	EnforceAllowlist bool              `toml:"enforce-allowlist"`
}

// Parse decodes, validates, and normalizes a routes file (ADR 0045) from
// data. Every returned error names the offending route (by its match-host,
// or "route N" when match-host itself is the problem) and field.
func Parse(data []byte) ([]Route, error) {
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var raw rawFile
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("registryroutes: parsing routes file: %w", err)
	}

	if len(raw.Routes) == 0 {
		return nil, fmt.Errorf("registryroutes: routes file declares no [[routes]] entries")
	}

	seenHosts := make(map[string]bool, len(raw.Routes))
	routes := make([]Route, 0, len(raw.Routes))
	for i, rr := range raw.Routes {
		label := routeLabel(rr.MatchHost, i)

		if rr.MatchHost == "" {
			return nil, fmt.Errorf("registryroutes: %s: match-host is empty", label)
		}
		if strings.TrimSpace(rr.MatchHost) != rr.MatchHost {
			return nil, fmt.Errorf("registryroutes: %s: match-host %q has leading or trailing whitespace, which can never match a real Host header", label, rr.MatchHost)
		}
		normalizedHost := hostOnly(rr.MatchHost)
		if seenHosts[normalizedHost] {
			return nil, fmt.Errorf("registryroutes: %s: match-host %q is declared by more than one route", label, rr.MatchHost)
		}
		seenHosts[normalizedHost] = true

		upstreamBaseURL, err := normalizeUpstreamBaseURL(label, rr.UpstreamBaseURL)
		if err != nil {
			return nil, err
		}

		authScheme := rr.AuthScheme
		if authScheme == "" {
			authScheme = "bearer"
		}
		if err := validateAuthScheme(label, authScheme); err != nil {
			return nil, err
		}

		cred, err := parseCredential(label, rr.Credential, upstreamBaseURL)
		if err != nil {
			return nil, err
		}
		routes = append(routes, Route{
			MatchHost:        rr.MatchHost,
			UpstreamBaseURL:  upstreamBaseURL,
			AuthScheme:       authScheme,
			EnforceAllowlist: rr.EnforceAllowlist,
			Credential:       cred,
		})
	}
	return routes, nil
}

// parseCredential validates a route's credential inline table and maps it
// onto credresolver.Config: exactly one of credentialSourceKeys must be
// present, "registry-name" is accepted only as cargo-credentials' companion,
// and any other key is an error. upstreamBaseURL is always carried through
// as Credential.UpstreamURL, since the netrc source keys its host match on
// it regardless of which source the route actually names.
func parseCredential(label string, m map[string]string, upstreamBaseURL string) (credresolver.Config, error) {
	for key := range m {
		if key == "registry-name" {
			continue
		}
		if !isCredentialSourceKey(key) {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential has unknown key %q", label, key)
		}
	}

	if _, ok := m["registry-name"]; ok {
		if _, ok := m["cargo-credentials"]; !ok {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q is only valid alongside %q", label, "registry-name", "cargo-credentials")
		}
	}

	var present []string
	for _, key := range credentialSourceKeys {
		if value, ok := m[key]; ok {
			if value == "" {
				return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q is empty", label, key)
			}
			present = append(present, key)
		}
	}
	switch len(present) {
	case 0:
		return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential names no source; exactly one of %s is required", label, strings.Join(credentialSourceKeys, ", "))
	case 1:
		// exactly one source: proceed below.
	default:
		return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential names more than one source: %s", label, strings.Join(present, ", "))
	}

	cfg := credresolver.Config{UpstreamURL: upstreamBaseURL}
	switch present[0] {
	case "env":
		cfg.FromEnv = m["env"]
	case "file":
		cfg.FromFile = m["file"]
		cfg.FileFormat = "raw"
	case "netrc":
		cfg.FromFile = m["netrc"]
		cfg.FileFormat = "netrc"
	case "cargo-credentials":
		if m["registry-name"] == "" {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q requires companion key %q", label, "cargo-credentials", "registry-name")
		}
		cfg.FromFile = m["cargo-credentials"]
		cfg.FileFormat = "cargo-credentials"
		cfg.RegistryName = m["registry-name"]
	}
	return cfg, nil
}

// normalizeUpstreamBaseURL validates that raw is an absolute http(s) URL
// with no userinfo, and strips a single trailing "/" so the trailing-slash
// and bare forms of the same upstream-base-url store identically -- a base
// path is otherwise permitted, unlike the scalar REGISTRY_PROXY_UPSTREAM_URL
// knob this route field replaces (ADR 0045: the route's own base path
// removes the path-doubling ambiguity that rule guarded against). Beyond
// that trailing-slash strip, raw is stored as written: scheme case and
// duplicate slashes are preserved.
func normalizeUpstreamBaseURL(label, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("registryroutes: %s: upstream-base-url %q is malformed: %w", label, raw, err)
	}
	if u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("registryroutes: %s: upstream-base-url %q must be an absolute http(s) URL", label, raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("registryroutes: %s: upstream-base-url must not contain userinfo", label)
	}
	return strings.TrimSuffix(raw, "/"), nil
}

// validateAuthScheme reports an error unless scheme is "bearer", "basic", or
// "header:<Name>" with Name a valid RFC 7230 header field name (ADR 0045).
func validateAuthScheme(label, scheme string) error {
	if scheme == "bearer" || scheme == "basic" {
		return nil
	}
	if name, ok := strings.CutPrefix(scheme, "header:"); ok && name != "" {
		if isValidHeaderFieldName(name) {
			return nil
		}
		return fmt.Errorf("registryroutes: %s: auth-scheme %q names an invalid header field name", label, scheme)
	}
	return fmt.Errorf("registryroutes: %s: auth-scheme %q is not one of \"bearer\", \"basic\", or \"header:<Name>\"", label, scheme)
}

// isValidHeaderFieldName reports whether name is a valid RFC 7230 "token":
// one or more of the allowed token characters, no separators, no CR/LF --
// hand-rolled so a crafted Name can't smuggle a header injection (CRLF) past
// validation and into a 502 at request time when Go's http layer rejects it.
func isValidHeaderFieldName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range []byte(name) {
		if !isTokenChar(c) {
			return false
		}
	}
	return true
}

func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		return true
	default:
		return false
	}
}

// FromScalars synthesizes the single bridge route equivalent to the
// pre-ADR-0045 REGISTRY_PROXY_* scalar knobs (see
// cmd/launcher/registrycredential.go's resolveRegistryProxyCredential),
// letting a Consumer still on the scalar knobs share the rest of the
// routes-based pipeline. It returns nil when upstreamURL is empty -- the
// documented opt-out that disables the registry proxy entirely, unchanged
// from the scalar knobs' own contract.
func FromScalars(upstreamURL, credFile, credEnv, fileFormat, registryName string) []Route {
	if upstreamURL == "" {
		return nil
	}
	u, err := url.Parse(upstreamURL)
	matchHost := ""
	if err == nil {
		matchHost = u.Host
	}
	return []Route{{
		MatchHost:       matchHost,
		UpstreamBaseURL: upstreamURL,
		AuthScheme:      "bearer",
		Credential: credresolver.Config{
			FromFile:     credFile,
			FromEnv:      credEnv,
			FileFormat:   fileFormat,
			UpstreamURL:  upstreamURL,
			RegistryName: registryName,
		},
	}}
}

// hostOnly lowercases hostport and strips any ":port" suffix -- mirrors
// registryproxy's hostOnly (registryproxy.go), which the routes this package
// validates are ultimately matched through, so two match-host strings that
// differ only in case or a trailing port collapse onto the same route at
// selection time and must be caught here as a duplicate rather than silently
// shadowing each other. A hostport with no port (net.SplitHostPort's
// "missing port" error) also has a single enclosing "[" "]" bracket pair
// stripped, if present, before lowercasing -- otherwise "[::1]" (no port)
// and "[::1]:443" would normalize to different strings even though an
// inbound "Host: [::1]:443" itself normalizes to the bracket-free "::1".
func hostOnly(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
	}
	return strings.ToLower(host)
}

// routeLabel names a route for an error message: by its match-host when it
// has one, or by its 1-based position in the file when match-host itself is
// what's missing or otherwise unusable as a label.
func routeLabel(matchHost string, index int) string {
	if matchHost != "" {
		return fmt.Sprintf("route %q", matchHost)
	}
	return fmt.Sprintf("route %d", index+1)
}
