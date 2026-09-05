// Package registryroutes parses and validates a registry proxy routes file
// (ADR 0045): a TOML document declaring one or more Registry routes, each
// binding a match host, an upstream base URL, an auth scheme, and a
// credential reference in a single record -- the property ADR 0045 calls
// load-bearing, since it leaves no Box-reachable way to pair a credential
// meant for one host with a different one.
package registryroutes

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"spindrift.dev/launcher/internal/credresolver"
)

// credentialSourceKeys are the credential inline table keys that name a
// credential source (ADR 0045); a route's credential table, when present,
// must name exactly one. Omitting the credential key altogether is also
// valid -- it opts the route out of authentication (a documented
// pass-through, not an oversight); see parseCredential.
// "registry-name" and "key" are deliberately excluded -- they're companion
// keys (for cargo-credentials and gradle-properties respectively), not
// sources of their own.
var credentialSourceKeys = []string{"env", "file", "netrc", "cargo-credentials", "exec", "npmrc", "gradle-properties"}

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
	// CargoRegistries names the Target repo's [registries.NAME] entries this
	// route serves (ADR 0045). Nil when the routes file omits the field
	// (back-compat) or when discovery (issue #3143) hasn't populated it yet;
	// an operator may also hand-write it.
	CargoRegistries []string
	// Allow names extra path patterns that extend a host-rooted route's
	// derived enforced path-set (ADR 0047, issue #3258) -- for a path shape
	// the Target repo's own manifests don't expose (e.g. an Artifactory
	// sibling download endpoint), rather than gating enforcement itself. Nil
	// or empty is valid and the common case. A legacy, non-host-rooted route
	// (one declaring upstream-base-url) is enforced by a different,
	// hardcoded mechanism (isAllowedPath's static ecosystem table) that
	// never reads this field, so declaring allow alongside
	// upstream-base-url is a parse-time error rather than a silent no-op.
	Allow []string
	// GradlePath is the operator-declared path gradle should bind to under a
	// host-rooted route (issue #3259). Gradle has no committed in-tree
	// config spindrift can scan (no InTreeConfigPath in ecosystem.Table),
	// so unlike every other entry in the enforced path-set, this one path
	// comes from operator declaration in the routes file rather than repo
	// scanning. "" when the field is omitted (the field is optional).
	GradlePath string
}

// rawFile is the strict TOML decode target for a routes file. Credential is
// decoded as a map, not a struct, so its exactly-one-source and unknown-key
// checks can be done by hand and reported against the offending route -- a
// fixed struct with DisallowUnknownFields would reject an unknown credential
// key too, but with a bare go-toml error that names neither the route nor
// the key the way the rest of this package's errors do. The map's value type
// is `any`, not `string`: the "exec" source's value is a TOML array (an
// argv), which go-toml decodes into a map[string]any as []interface{}; every
// other key's value-must-be-a-string check moves from decode-time to
// parseCredential accordingly.
type rawFile struct {
	Routes []rawRoute `toml:"routes"`
}

type rawRoute struct {
	MatchHost        string         `toml:"match-host"`
	UpstreamBaseURL  string         `toml:"upstream-base-url"`
	AuthScheme       string         `toml:"auth-scheme"`
	Credential       map[string]any `toml:"credential"`
	EnforceAllowlist bool           `toml:"enforce-allowlist"`
	CargoRegistries  []string       `toml:"cargo-registries"`
	Allow            []string       `toml:"allow"`
	GradlePath       string         `toml:"gradle-path"`
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
			return nil, fmt.Errorf("registryroutes: %s: match-host %q has leading or trailing whitespace, which can never be a real registry hostname and would corrupt the route's derived path prefix (see registryproxy.AssignPrefixes)", label, rr.MatchHost)
		}
		normalizedHost := hostOnly(rr.MatchHost)
		if seenHosts[normalizedHost] {
			return nil, fmt.Errorf("registryroutes: %s: match-host %q is declared by more than one route", label, rr.MatchHost)
		}
		seenHosts[normalizedHost] = true

		// upstream-base-url is optional: a route that omits it is host-rooted
		// (issue #3256 slice 1) rather than base-path-joined, and stores as
		// "" all the way through Route -- ValidateUpstreamBaseURL rejects an
		// empty string, so the normalize-and-validate call only runs when
		// the field is actually present.
		var upstreamBaseURL string
		if rr.UpstreamBaseURL != "" {
			var err error
			upstreamBaseURL, err = normalizeUpstreamBaseURL(label, rr.UpstreamBaseURL)
			if err != nil {
				return nil, err
			}
		}

		authScheme := rr.AuthScheme
		if authScheme == "" {
			authScheme = "bearer"
		}
		if err := validateAuthScheme(label, authScheme); err != nil {
			return nil, err
		}

		// credentialUpstreamURL stands in for upstreamBaseURL when a
		// host-rooted route leaves it empty: the netrc source
		// (credresolver's netrcFileResolver) parses Credential.UpstreamURL
		// only to pull out its bare host for the machine-name match, so
		// "https://" + match-host carries exactly the host a host-rooted
		// route already commits to, without inventing a path that doesn't
		// exist.
		credentialUpstreamURL := upstreamBaseURL
		if credentialUpstreamURL == "" {
			credentialUpstreamURL = "https://" + rr.MatchHost
		}

		cred, err := parseCredential(label, rr.MatchHost, rr.Credential, credentialUpstreamURL)
		if err != nil {
			return nil, err
		}

		if err := validateCargoRegistries(label, rr.CargoRegistries); err != nil {
			return nil, err
		}

		if upstreamBaseURL != "" && len(rr.Allow) > 0 {
			return nil, fmt.Errorf("registryroutes: %s: allow only applies to a host-rooted route (one that omits upstream-base-url); this route declares both upstream-base-url and allow", label)
		}
		if err := validateAllowPatterns(label, rr.Allow); err != nil {
			return nil, err
		}

		if upstreamBaseURL != "" && rr.GradlePath != "" {
			return nil, fmt.Errorf("registryroutes: %s: gradle-path only applies to a host-rooted route (one that omits upstream-base-url); this route declares both upstream-base-url and gradle-path", label)
		}

		// gradle-path is optional, the same gate upstream-base-url uses
		// above: a route that omits it stores "" all the way through
		// Route, and validateGradlePath is never called for the empty
		// case.
		var gradlePath string
		if rr.GradlePath != "" {
			var err error
			gradlePath, err = validateGradlePath(label, rr.GradlePath)
			if err != nil {
				return nil, err
			}
		}

		routes = append(routes, Route{
			MatchHost:        rr.MatchHost,
			UpstreamBaseURL:  upstreamBaseURL,
			AuthScheme:       authScheme,
			EnforceAllowlist: rr.EnforceAllowlist,
			Credential:       cred,
			CargoRegistries:  rr.CargoRegistries,
			Allow:            rr.Allow,
			GradlePath:       gradlePath,
		})
	}
	return routes, nil
}

// parseCredential validates a route's credential inline table and maps it
// onto credresolver.Config: exactly one of credentialSourceKeys must be
// present, "registry-name" is accepted only as cargo-credentials' companion,
// "key" only as gradle-properties' companion, and any other key is an
// error. upstreamBaseURL is always carried through as Credential.UpstreamURL,
// since the netrc source keys its host match on it regardless of which
// source the route actually names; matchHost is likewise always carried
// through as Credential.MatchHost, harmless for the sources that ignore it
// but load-bearing for exec (route-naming in a failure) and npmrc (the host
// its lookup keys on).
//
// m is nil, not merely empty, when the route omits the credential key
// altogether -- go-toml's decoder distinguishes the two -- and that nil case
// short-circuits to a zero credresolver.Config, credresolver.New's
// documented unauthenticated pass-through. A present-but-empty
// credential = {} falls through to the same "names no source" error as
// before: an operator who wrote the table meant to configure something.
//
// upstreamBaseURL here is really "whatever this route wants netrc's host
// match keyed on" -- Parse passes its "https://" + match-host stand-in, not
// the empty Route.UpstreamBaseURL itself, for a host-rooted route.
func parseCredential(label, matchHost string, m map[string]any, upstreamBaseURL string) (credresolver.Config, error) {
	if m == nil {
		return credresolver.Config{}, nil
	}

	for key := range m {
		if key == "registry-name" || key == "key" {
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
	if _, ok := m["key"]; ok {
		if _, ok := m["gradle-properties"]; !ok {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q is only valid alongside %q", label, "key", "gradle-properties")
		}
	}

	// Every key's value must be a string except "exec", whose value is a
	// TOML array (an argv) -- go-toml decodes that shape into []interface{},
	// so it can't share the generic string/empty check below and is pulled
	// out into execArgv instead. seenSource records which credentialSourceKeys
	// this route's table actually named, right here where each key is
	// classified, rather than recomputing that from strs/execArgv in a
	// second pass -- avoids duplicating the exec special case a second time.
	var execArgv []string
	strs := make(map[string]string, len(m))
	seenSource := make(map[string]bool, len(m))
	for key, v := range m {
		if key == "exec" {
			argv, err := parseExecArgv(label, v)
			if err != nil {
				return credresolver.Config{}, err
			}
			execArgv = argv
			seenSource[key] = true
			continue
		}
		s, ok := v.(string)
		if !ok {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q must be a string", label, key)
		}
		if s == "" {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q is empty", label, key)
		}
		strs[key] = s
		if isCredentialSourceKey(key) {
			seenSource[key] = true
		}
	}

	// Rebuilt in credentialSourceKeys' fixed order rather than m's -- go's
	// map iteration order is randomized, and this order feeds directly into
	// the "names more than one source" error text below, which must stay
	// deterministic across runs given the same input.
	var present []string
	for _, key := range credentialSourceKeys {
		if seenSource[key] {
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

	cfg := credresolver.Config{UpstreamURL: upstreamBaseURL, MatchHost: matchHost}
	switch present[0] {
	case "env":
		cfg.FromEnv = strs["env"]
	case "file":
		cfg.FromFile = strs["file"]
		cfg.FileFormat = "raw"
	case "netrc":
		cfg.FromFile = strs["netrc"]
		cfg.FileFormat = "netrc"
	case "cargo-credentials":
		// strs["registry-name"] reads "" both when the key is absent and
		// when go's map zero-value kicks in -- but present-but-empty was
		// already rejected above (the generic empty-value check on strs),
		// so this only ever fires on a missing companion key.
		if strs["registry-name"] == "" {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q requires companion key %q", label, "cargo-credentials", "registry-name")
		}
		cfg.FromFile = strs["cargo-credentials"]
		cfg.FileFormat = "cargo-credentials"
		cfg.RegistryName = strs["registry-name"]
	case "exec":
		cfg.ExecArgv = execArgv
	case "npmrc":
		cfg.FromFile = strs["npmrc"]
		cfg.FileFormat = "npmrc"
	case "gradle-properties":
		// Same reasoning as the cargo-credentials/registry-name guard above:
		// present-but-empty is already rejected, so strs["key"] == "" here
		// only means "key" is missing.
		if strs["key"] == "" {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q requires companion key %q", label, "gradle-properties", "key")
		}
		cfg.FromFile = strs["gradle-properties"]
		cfg.FileFormat = "gradle-properties"
		cfg.PropertyKey = strs["key"]
	}
	return cfg, nil
}

// parseExecArgv validates the "exec" credential value: a non-empty TOML
// array in which every element is a string and argv[0] is itself non-empty
// -- an empty argv[0] would reach exec.Command as an empty program name and
// fail with an OS error that never names the offending route the way this
// package's other errors do.
func parseExecArgv(label string, v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("registryroutes: %s: credential key %q must be an array of strings", label, "exec")
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("registryroutes: %s: credential key %q is empty", label, "exec")
	}
	argv := make([]string, len(arr))
	for i, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("registryroutes: %s: credential key %q must be an array of strings", label, "exec")
		}
		argv[i] = s
	}
	if argv[0] == "" {
		return nil, fmt.Errorf("registryroutes: %s: credential key %q has an empty argv[0]", label, "exec")
	}
	return argv, nil
}

// ValidateUpstreamBaseURL reports an error unless raw is an absolute
// http(s) URL with no userinfo. Exported so a per-route doctor row
// (cmd/launcher's registryRouteChecks) can reuse this package's own
// validation instead of reproducing it clause-for-clause -- a second,
// hand-copied version could silently drift out of sync with what Parse
// actually accepts.
func ValidateUpstreamBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse's *url.Error echoes the full raw URL, which may embed
		// userinfo; unwrap to the inner error so a malformed URL never
		// echoes a credential back (matching the userinfo branch below).
		if uerr, ok := err.(*url.Error); ok {
			err = uerr.Err
		}
		return fmt.Errorf("upstream-base-url is malformed: %w", err)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("upstream-base-url %q must be an absolute http(s) URL", raw)
	}
	if u.User != nil {
		// raw is omitted here, unlike the two errors above: it may embed a
		// credential (e.g. https://user:pass@host/), and this error must not
		// echo one back.
		return errors.New("upstream-base-url must not contain userinfo")
	}
	return nil
}

// normalizeUpstreamBaseURL validates raw via ValidateUpstreamBaseURL, wraps
// any failure with label the way every other error in this file is
// wrapped, and strips a single trailing "/" so the trailing-slash and bare
// forms of the same upstream-base-url store identically -- a base path is
// otherwise permitted (e.g. "https://artifactory.example.com/artifactory"),
// since each route names its own upstream-base-url explicitly rather than
// composing one from elsewhere (ADR 0045). Beyond that trailing-slash strip,
// raw is stored as written: scheme case and duplicate slashes are
// preserved.
func normalizeUpstreamBaseURL(label, raw string) (string, error) {
	if err := ValidateUpstreamBaseURL(raw); err != nil {
		return "", fmt.Errorf("registryroutes: %s: %w", label, err)
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

// cargoRegistryNamePattern matches cargo's own bare-key charset -- the same
// pattern bindregistry.cargoBareKeyPattern enforces on a [registries.NAME]
// section name, since a cargo-registries entry ultimately names a
// CARGO_REGISTRIES_<NAME>_TOKEN shell env var: anything outside
// [A-Za-z0-9_-] risks smuggling shell metadata into a sourced env file.
var cargoRegistryNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validateCargoRegistries rejects an empty name, a name outside
// cargoRegistryNamePattern, or a name repeated within names. A nil or empty
// names is valid (the field is optional, ADR 0045).
func validateCargoRegistries(label string, names []string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("registryroutes: %s: cargo-registries names an empty string", label)
		}
		if !cargoRegistryNamePattern.MatchString(name) {
			return fmt.Errorf("registryroutes: %s: cargo-registries name %q must match %s", label, name, cargoRegistryNamePattern.String())
		}
		if seen[name] {
			return fmt.Errorf("registryroutes: %s: cargo-registries names %q more than once", label, name)
		}
		seen[name] = true
	}
	return nil
}

// validateAllowPatterns rejects any pattern not already in the canonical
// subtree-root form registrypathset derives (leading "/", no trailing "/",
// no "." or ".." segment) -- checked via path.Clean rather than silently
// normalized, so a mistyped pattern fails loudly at parse time instead of
// matching (or failing to match) a request path for a reason that's purely
// a formatting mismatch once merged into an enforced path-set (ADR 0047,
// issue #3258). The literal pattern "/" is rejected too, even though it
// passes the canonical-form check (path.Clean("/") == "/"): pathSetAdmits
// treats a "/" entry in EnforcedPaths as "admit every path", which is a
// legitimate *derived* entry when a registry's whole host is one endpoint
// with no subpath, but as an operator-supplied allow override it is
// indistinguishable from disabling host-rooted enforcement outright -- the
// off switch ADR 0047 forbids. Nil or empty patterns are valid (the field
// is optional).
func validateAllowPatterns(label string, patterns []string) error {
	for _, p := range patterns {
		if p == "" || path.Clean(p) != p || !strings.HasPrefix(p, "/") {
			return fmt.Errorf("registryroutes: %s: allow pattern %q must be an absolute path already in canonical form (leading \"/\", no trailing \"/\", no \".\" or \"..\" segment)", label, p)
		}
		if p == "/" {
			return fmt.Errorf("registryroutes: %s: allow pattern %q would blanket-authorize the whole host, which is an off switch for host-rooted enforcement -- not permitted", label, p)
		}
	}
	return nil
}

// validateGradlePath validates a non-empty gradle-path (issue #3259): it
// must start with "/", contain no whitespace, contain no "$", "`", or "\",
// contain no "..", ".", or empty (doubled-slash) segment, and not be the
// bare root "/" -- a route-level gradle-path only ever ADDS a subtree on
// top of an already-resolved host-rooted route, so declaring "the whole
// host" needs no special field and is rejected outright. The "$"/"`"/"\"
// ban exists because this value is operator-declared (routes file) but
// ultimately flows into gradleRedirectScript's Groovy double-quoted string
// literal (ecosystem.GradleInitScript): an unescaped "$" there triggers
// Groovy's GString interpolation at init-script load time, and a
// backtick/backslash risks similar script-breaking, so a malformed or
// hostile routes-file entry is rejected here rather than reaching generated
// Groovy source. On success it returns path with all trailing "/" stripped
// (not just one -- otherwise "//" would normalize to "/" and slip past the
// bare-root check below as the very whole-host value it's meant to catch),
// the same normalization normalizeUpstreamBaseURL applies to
// upstream-base-url. Callers must gate on path != "" themselves -- ""
// (the field omitted) is valid and never reaches this function.
func validateGradlePath(label, gradlePath string) (string, error) {
	if strings.TrimSpace(gradlePath) != gradlePath || strings.ContainsAny(gradlePath, " \t\r\n") {
		return "", fmt.Errorf("registryroutes: %s: gradle-path %q must not contain whitespace", label, gradlePath)
	}
	if strings.ContainsAny(gradlePath, "$`\\") {
		return "", fmt.Errorf("registryroutes: %s: gradle-path %q must not contain %q, %q, or %q", label, gradlePath, "$", "`", "\\")
	}
	if !strings.HasPrefix(gradlePath, "/") {
		return "", fmt.Errorf("registryroutes: %s: gradle-path %q must start with \"/\"", label, gradlePath)
	}
	normalized := strings.TrimRight(gradlePath, "/")
	if normalized == "" {
		return "", fmt.Errorf("registryroutes: %s: gradle-path %q must name a specific path, not the whole host", label, gradlePath)
	}
	for i, seg := range strings.Split(normalized, "/") {
		if i == 0 {
			// normalized always starts with "/" (checked above), so
			// splitting on "/" always yields a leading "" for that prefix
			// -- not a real segment, and not the doubled-slash case the
			// empty-segment check below exists to catch.
			continue
		}
		switch seg {
		case "":
			return "", fmt.Errorf("registryroutes: %s: gradle-path %q must not contain an empty segment (doubled slash)", label, gradlePath)
		case ".":
			return "", fmt.Errorf("registryroutes: %s: gradle-path %q must not contain a %q segment", label, gradlePath, ".")
		case "..":
			return "", fmt.Errorf("registryroutes: %s: gradle-path %q must not contain a %q segment", label, gradlePath, "..")
		}
	}
	return normalized, nil
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
