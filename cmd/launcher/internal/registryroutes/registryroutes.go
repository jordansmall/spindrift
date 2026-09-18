// Package registryroutes parses and validates a registry proxy routes file
// (ADR 0045): a TOML document declaring one or more Registry routes, each
// binding a match host, an auth scheme, and a credential reference in one
// record, so nothing the Box can reach pairs a credential meant for one host
// with a different one.
package registryroutes

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
)

// credentialSourceKeys are the credential inline table keys that name a
// credential source (ADR 0045); a route's credential table, when present,
// must name exactly one. Omitting the credential key is valid and opts the
// route out of authentication. "registry-name" and "key" are excluded: they
// are companion keys, not sources of their own (issue #3407).
var credentialSourceKeys = credresolver.SourceKeys()

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
	MatchHost  string
	AuthScheme string
	// UpstreamOrigin is the operator-declared scheme://host[:port] this route
	// forwards to, overriding the origin the Target repo's committed config
	// implies (ADR 0047, issue #3261). It covers what that config cannot
	// supply: a non-default scheme or port, and a host serving only ecosystems
	// with nothing committed to scan. "" when omitted, and never has a path.
	UpstreamOrigin string
	Credential     credresolver.Config
	// Ecosystems is the route's per-ecosystem [routes.ecosystems.<name>]
	// declaration block (issue #3403), keyed by ecosystem.Table row name. It
	// holds every per-ecosystem declaration a route can make; downstream hops
	// read them back out of this block, not through dedicated fields. Nil when
	// the route declares nothing per-ecosystem, never an empty map.
	Ecosystems registryvocab.RouteEcosystems
	// Allow names extra path patterns that extend a host-rooted route's derived
	// enforced path-set (ADR 0047, issue #3258), for a path shape the Target
	// repo's own manifests don't expose, such as an Artifactory sibling
	// download endpoint. Every route is host-rooted (issue #3261), so this is
	// the only recourse; it never gates enforcement itself. Nil or empty is valid.
	Allow []string
}

// rawFile is the strict TOML decode target for a routes file. Credential
// decodes as a map, not a struct, so the exactly-one-source and unknown-key
// checks can name the offending route and key, which DisallowUnknownFields
// alone cannot. Its value type is `any` because the "exec" source's value is
// a TOML array, so the string checks move to parseCredential.
type rawFile struct {
	Routes []rawRoute `toml:"routes"`
}

// All five retired keys stay decodable fields, so the strict decoder reports
// retiredRouteKeysError's migration remedy rather than a bare go-toml
// unknown-key error. Each is a pointer to distinguish "declared" from
// "declared with the zero value": enforce-allowlist = false (ADR 0047, issue
// #3261) and gradle-path = "" (ADR 0048, issue #3405) are retired too.
type rawRoute struct {
	MatchHost        string         `toml:"match-host"`
	UpstreamBaseURL  *string        `toml:"upstream-base-url"`
	UpstreamOrigin   string         `toml:"upstream-origin"`
	AuthScheme       string         `toml:"auth-scheme"`
	Credential       map[string]any `toml:"credential"`
	EnforceAllowlist *bool          `toml:"enforce-allowlist"`
	CargoRegistries  *[]string      `toml:"cargo-registries"`
	Allow            []string       `toml:"allow"`
	GradlePath       *string        `toml:"gradle-path"`
	GoPath           *string        `toml:"go-path"`
	// Ecosystems decodes [routes.ecosystems.<name>] (issue #3403), keyed by the
	// ecosystem name the operator wrote. Each block stays a bare map so the
	// per-ecosystem key checks can run by hand against ecosystem.Table and name
	// the offending route, name, and key; a fixed struct would only ever know
	// about "path", never a row-specific key like cargo's "registries".
	Ecosystems map[string]map[string]any `toml:"ecosystems"`
}

// Parse decodes, validates, and normalizes a routes file (ADR 0045) from data.
// Every returned error names the offending route (by its match-host, or "route
// N" when match-host itself is the problem) and field. It wraps parseRoutes,
// fixing rows to ecosystem.Table, so a test wanting a fake row can drive
// parseRoutes directly with one.
func Parse(data []byte) ([]Route, error) {
	return parseRoutes(data, ecosystem.Table)
}

func parseRoutes(data []byte, rows []ecosystem.Row) ([]Route, error) {
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
		normalizedHost := registryvocab.HostKey(rr.MatchHost)
		if seenHosts[normalizedHost] {
			return nil, fmt.Errorf("registryroutes: %s: match-host %q is declared by more than one route", label, rr.MatchHost)
		}
		seenHosts[normalizedHost] = true

		if err := retiredRouteKeysError(label, rr); err != nil {
			return nil, err
		}

		// ValidateUpstreamOrigin rejects "", so only a route that declared the
		// optional field is validated; one that omits it stores "" and derives
		// its origin from the Target repo's committed config.
		var upstreamOrigin string
		if rr.UpstreamOrigin != "" {
			if err := ValidateUpstreamOrigin(rr.UpstreamOrigin); err != nil {
				return nil, fmt.Errorf("registryroutes: %s: %w", label, err)
			}
			upstreamOrigin = strings.TrimSuffix(rr.UpstreamOrigin, "/")
		}

		authScheme := rr.AuthScheme
		if authScheme == "" {
			authScheme = "bearer"
		}
		if err := validateAuthScheme(label, authScheme); err != nil {
			return nil, err
		}

		// The netrc source parses Credential.UpstreamURL only for its bare host,
		// so "https://" + match-host supplies that host without inventing a
		// path when the route declares no origin.
		credentialUpstreamURL := upstreamOrigin
		if credentialUpstreamURL == "" {
			credentialUpstreamURL = "https://" + rr.MatchHost
		}

		cred, err := parseCredential(label, rr.MatchHost, rr.Credential, credentialUpstreamURL)
		if err != nil {
			return nil, err
		}

		if err := validateAllowPatterns(label, rr.Allow); err != nil {
			return nil, err
		}

		ecosystems, err := buildRouteEcosystems(label, rr, rows)
		if err != nil {
			return nil, err
		}

		routes = append(routes, Route{
			MatchHost:      rr.MatchHost,
			UpstreamOrigin: upstreamOrigin,
			AuthScheme:     authScheme,
			Credential:     cred,
			Ecosystems:     ecosystems,
			Allow:          rr.Allow,
		})
	}
	return routes, nil
}

const (
	pathKey            = registryvocab.RouteDeclarationPathKey
	cargoRegistriesKey = ecosystem.CargoRouteRegistriesKey
)

// retiredRouteKeys pairs each retired top-level key (ADR 0048, issue #3405)
// with two readers: declared reports whether the route spells the key at all,
// and block renders it as the [routes.ecosystems.<name>] block it stands for.
// block returns nil for a declared-but-empty value, since a stanza printing
// path = "" would not re-parse. The fixed order keeps reporting deterministic.
var retiredRouteKeys = []struct {
	key      string
	declared func(rawRoute) bool
	block    func(rawRoute) map[string]any
}{
	{
		key:      ecosystem.GradleRetiredRouteKey,
		declared: func(rr rawRoute) bool { return rr.GradlePath != nil },
		block: func(rr rawRoute) map[string]any {
			if rr.GradlePath == nil || *rr.GradlePath == "" {
				return nil
			}
			return map[string]any{pathKey: *rr.GradlePath}
		},
	},
	{
		key:      ecosystem.GoRetiredRouteKey,
		declared: func(rr rawRoute) bool { return rr.GoPath != nil },
		block: func(rr rawRoute) map[string]any {
			if rr.GoPath == nil || *rr.GoPath == "" {
				return nil
			}
			return map[string]any{pathKey: *rr.GoPath}
		},
	},
	{
		key:      ecosystem.CargoRetiredRouteKey,
		declared: func(rr rawRoute) bool { return rr.CargoRegistries != nil },
		block: func(rr rawRoute) map[string]any {
			if rr.CargoRegistries == nil || len(*rr.CargoRegistries) == 0 {
				return nil
			}
			return map[string]any{cargoRegistriesKey: registryvocab.StringsValue(*rr.CargoRegistries)}
		},
	},
}

// buildRouteEcosystems builds rr's Ecosystems block, validating each declared
// block against the matching row. It walks the rows in order, then any name
// the rows don't know, sorted, so an unknown name is reported
// deterministically. The result is nil, not empty, when rr declares nothing
// per-ecosystem (registryvocab.RouteEcosystems' "absent is nil" convention).
func buildRouteEcosystems(label string, rr rawRoute, rows []ecosystem.Row) (registryvocab.RouteEcosystems, error) {
	blocks := make(registryvocab.RouteEcosystems)

	handled := make(map[string]bool, len(rr.Ecosystems))
	for _, row := range rows {
		raw, ok := rr.Ecosystems[row.Name]
		if !ok {
			continue
		}
		handled[row.Name] = true
		block, err := buildRouteDeclarationBlock(label, row, raw)
		if err != nil {
			return nil, err
		}
		blocks[row.Name] = block
	}

	var unknown []string
	for name := range rr.Ecosystems {
		if !handled[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("registryroutes: %s: [routes.ecosystems.%q] names an ecosystem spindrift doesn't know", label, unknown[0])
	}

	if len(blocks) == 0 {
		return nil, nil
	}
	return blocks, nil
}

// buildRouteDeclarationBlock validates one ecosystem's declaration block
// (issue #3403) against row: "path" via the shared canonical-path rules
// (validateDeclaredPath), every other key via row's own RouteDeclaration hook,
// a nil hook rejecting every such key. Keys are walked in sorted order so a
// block declaring two problems always reports the same one first.
func buildRouteDeclarationBlock(label string, row ecosystem.Row, raw map[string]any) (registryvocab.RouteDeclaration, error) {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	block := make(registryvocab.RouteDeclaration, len(raw))
	for _, key := range keys {
		value := raw[key]
		keyLabel := registryvocab.RouteDeclarationKeyLabel(row.Name, key)
		if key == pathKey {
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("registryroutes: %s: %s must be a string", label, keyLabel)
			}
			normalized, err := validateDeclaredPath(label, keyLabel, s)
			if err != nil {
				return nil, err
			}
			block[key] = normalized
			continue
		}
		if row.RouteDeclaration == nil {
			return nil, fmt.Errorf("registryroutes: %s: %s is not a key %s's route declaration accepts", label, keyLabel, row.Name)
		}
		if err := row.RouteDeclaration(key, value); err != nil {
			return nil, fmt.Errorf("registryroutes: %s: %s %w", label, keyLabel, err)
		}
		block[key] = value
	}
	return block, nil
}

// retiredRouteKeysError reports a configuration error when rr declares the
// ADR 0047 pair (upstream-base-url, enforce-allowlist; issue #3261) or the
// ADR 0048 trio (gradle-path, go-path, cargo-registries; issue #3405), naming
// every offending key in one error with a copy-pasteable replacement stanza.
// Detection is by presence, so enforce-allowlist = false is as retired as true.
func retiredRouteKeysError(label string, rr rawRoute) error {
	var pathGroup []string
	if rr.UpstreamBaseURL != nil {
		pathGroup = append(pathGroup, "upstream-base-url")
	}
	if rr.EnforceAllowlist != nil {
		pathGroup = append(pathGroup, "enforce-allowlist")
	}

	var ecosystemGroup []string
	for _, entry := range retiredRouteKeys {
		if entry.declared(rr) {
			ecosystemGroup = append(ecosystemGroup, entry.key)
		}
	}

	if len(pathGroup) == 0 && len(ecosystemGroup) == 0 {
		return nil
	}

	var clauses []string
	if len(pathGroup) > 0 {
		clauses = append(clauses, fmt.Sprintf(
			"%s %s retired (ADR 0047, issue #3261): every route is now host-rooted, and enforcement against the derived path-set is unconditional -- there is no off switch, and allow is the only recourse for a path that set misses",
			strings.Join(pathGroup, ", "), retiredKeysVerb(pathGroup),
		))
	}
	if len(ecosystemGroup) > 0 {
		clauses = append(clauses, fmt.Sprintf(
			"%s %s retired (ADR 0048, issue #3405): the routes file's per-ecosystem keys become one [routes.ecosystems.<name>] block with one typed key, path -- the row validates any further keys",
			strings.Join(ecosystemGroup, ", "), retiredKeysVerb(ecosystemGroup),
		))
	}

	return fmt.Errorf(
		"registryroutes: %s: %s; equivalent routes-file stanza:\n\n%s",
		label, strings.Join(clauses, "; "),
		retiredRouteStanza(rr),
	)
}

func retiredKeysVerb(keys []string) string {
	if len(keys) > 1 {
		return "are"
	}
	return "is"
}

// retiredRouteStanza builds the replacement [[routes]] entry for a route that
// still declares a retired key, so migrating is paste-this-back rather than
// re-deriving the route from ADR 0047 or ADR 0048. A retired upstream-base-url
// supplies upstream-origin only when it says something match-host cannot, and
// its path is dropped: a host-rooted route derives the paths it serves.
func retiredRouteStanza(rr rawRoute) string {
	var b strings.Builder
	b.WriteString("[[routes]]\n")
	fmt.Fprintf(&b, "match-host = %q\n", rr.MatchHost)
	if rr.UpstreamOrigin != "" {
		fmt.Fprintf(&b, "upstream-origin = %q\n", rr.UpstreamOrigin)
	} else if rr.UpstreamBaseURL != nil {
		if origin := UpstreamOriginFor(*rr.UpstreamBaseURL); origin != "" {
			fmt.Fprintf(&b, "upstream-origin = %q\n", origin)
		}
	}
	if rr.AuthScheme != "" {
		fmt.Fprintf(&b, "auth-scheme = %q\n", rr.AuthScheme)
	}
	if cred := retiredRouteCredentialInline(rr.Credential); cred != "" {
		fmt.Fprintf(&b, "credential = %s\n", cred)
	}
	if len(rr.Allow) > 0 {
		fmt.Fprintf(&b, "allow = %s\n", tomlStringArray(rr.Allow))
	}
	b.WriteString(retiredRouteEcosystemBlocks(mergeRetiredRouteEcosystems(rr)))
	return b.String()
}

// mergeRetiredRouteEcosystems folds each retired top-level key's equivalent
// block into rr's own [routes.ecosystems.<name>] blocks, so retiredRouteStanza
// renders one block per ecosystem whichever spelling the route used, and never
// echoes a retired key that would fail to parse. A name declared both ways
// keeps the explicit block's keys and gains only what that block omits.
func mergeRetiredRouteEcosystems(rr rawRoute) map[string]map[string]any {
	merged := make(map[string]map[string]any, len(rr.Ecosystems))
	for name, block := range rr.Ecosystems {
		copied := make(map[string]any, len(block))
		for key, value := range block {
			copied[key] = value
		}
		merged[name] = copied
	}

	for _, entry := range retiredRouteKeys {
		raw := entry.block(rr)
		if raw == nil {
			continue
		}
		row, ok := ecosystem.RowByRetiredRouteKey(entry.key)
		if !ok {
			// Broken invariant (a row dropping its RetiredRouteKey), not
			// operator input: rr declared this key, so skipping it would
			// silently drop it from the printed stanza.
			panic(fmt.Sprintf("registryroutes: retired route key %q resolves to no ecosystem.Table row", entry.key))
		}
		block, ok := merged[row.Name]
		if !ok {
			block = make(map[string]any, len(raw))
			merged[row.Name] = block
		}
		for key, value := range raw {
			if _, declared := block[key]; !declared {
				block[key] = value
			}
		}
	}

	return merged
}

// retiredRouteEcosystemBlocks renders a route's [routes.ecosystems.<name>]
// blocks (issue #3403) back as TOML sub-tables. A sub-table inside a
// [[routes]] entry ends that entry's top-level keys, which is why
// retiredRouteStanza appends these last. Names and keys are sorted, since go
// randomizes map iteration and an operator is told to copy-paste this text.
func retiredRouteEcosystemBlocks(ecosystems map[string]map[string]any) string {
	names := make([]string, 0, len(ecosystems))
	for name := range ecosystems {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		block := ecosystems[name]
		keys := make([]string, 0, len(block))
		for key := range block {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		fmt.Fprintf(&b, "\n[routes.ecosystems.%s]\n", tomlKey(name))
		for _, key := range keys {
			fmt.Fprintf(&b, "%s = %s\n", tomlKey(key), tomlValue(block[key]))
		}
	}
	return b.String()
}

// retiredRouteCredentialInline renders a route's credential map back as the
// TOML inline table it was written as, in a fixed order (the source key in
// credentialSourceKeys order, then its companion), since go randomizes map
// iteration and an operator is told to copy-paste this text. Unrecognized keys
// are dropped so the stanza parses. Returns "" for a route with no credential.
func retiredRouteCredentialInline(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	var pairs []string
	for _, kind := range credresolver.Kinds() {
		v, ok := m[kind.SourceKey]
		if !ok {
			continue
		}
		pairs = append(pairs, fmt.Sprintf("%s = %s", kind.SourceKey, tomlValue(v)))
		if kind.CompanionKey == "" {
			continue
		}
		if cv, ok := m[kind.CompanionKey]; ok {
			pairs = append(pairs, fmt.Sprintf("%s = %s", kind.CompanionKey, tomlValue(cv)))
		}
	}
	if len(pairs) == 0 {
		return ""
	}
	return "{ " + strings.Join(pairs, ", ") + " }"
}

// tomlBareKeyPattern is TOML's bare-key charset: a key outside it has to be
// quoted to appear in a document that parses.
var tomlBareKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlKey renders one operator-written key, an ecosystem name or a key within
// that ecosystem's block, back as TOML. Either may have been written quoted,
// and echoing it bare would render a stanza that no longer parses.
func tomlKey(key string) string {
	if tomlBareKeyPattern.MatchString(key) {
		return key
	}
	return fmt.Sprintf("%q", key)
}

// tomlValue renders one decoded free-form value back as TOML: a credential
// value or an ecosystem declaration's, each a string or an array of them.
// Anything else renders as a quoted Go rendering rather than being dropped
// silently, since parseCredential or the row's own RouteDeclaration hook names
// it precisely once the operator has migrated off the retired key.
func tomlValue(v any) string {
	switch t := v.(type) {
	case string:
		return fmt.Sprintf("%q", t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, tomlValue(e))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprintf("%q", fmt.Sprint(t))
	}
}

func tomlStringArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, fmt.Sprintf("%q", v))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// parseCredential validates a route's credential inline table and maps it onto
// credresolver.Config: exactly one of credentialSourceKeys must be present, and
// each companion key is valid only alongside its own source. upstreamURL and
// matchHost are carried through whichever source the route names, since netrc
// keys its host match on the first, and exec and npmrc both need the second.
func parseCredential(label, matchHost string, m map[string]any, upstreamURL string) (credresolver.Config, error) {
	// go-toml distinguishes an omitted credential key (nil) from an empty
	// credential = {}: the first is ADR 0045's unauthenticated pass-through,
	// the second falls through to the "names no source" error below.
	if m == nil {
		return credresolver.Config{}, nil
	}

	for key := range m {
		if credresolver.IsCompanionKey(key) {
			continue
		}
		if !isCredentialSourceKey(key) {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential has unknown key %q", label, key)
		}
	}

	for _, kind := range credresolver.Kinds() {
		if kind.CompanionKey == "" {
			continue
		}
		if _, ok := m[kind.CompanionKey]; ok {
			if _, ok := m[kind.SourceKey]; !ok {
				return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q is only valid alongside %q", label, kind.CompanionKey, kind.SourceKey)
			}
		}
	}

	// Every key's value must be a string except "exec", whose TOML array value
	// go-toml decodes into []interface{}, so it cannot share the string check
	// below. seenSource records which sources the table named here, where each
	// key is already classified, rather than repeating the exec special case.
	var execArgv []string
	strs := make(map[string]string, len(m))
	seenSource := make(map[string]bool, len(m))
	for key, v := range m {
		if kind, ok := credresolver.KindBySourceKey(key); ok && kind.ArgvValue {
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

	// Rebuilt in credresolver.Kinds() order rather than m's: go randomizes map
	// iteration, and this order feeds the "names more than one source" error
	// text below, which must stay deterministic for the same input.
	var present []credresolver.Kind
	for _, kind := range credresolver.Kinds() {
		if seenSource[kind.SourceKey] {
			present = append(present, kind)
		}
	}
	switch len(present) {
	case 0:
		return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential names no source; exactly one of %s is required", label, strings.Join(credentialSourceKeys, ", "))
	case 1:
		// exactly one source: proceed below.
	default:
		keys := make([]string, len(present))
		for i, k := range present {
			keys[i] = k.SourceKey
		}
		return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential names more than one source: %s", label, strings.Join(keys, ", "))
	}

	kind := present[0]

	cfg := credresolver.Config{UpstreamURL: upstreamURL, MatchHost: matchHost}
	if kind.CompanionKey != "" {
		// A present-but-empty companion value was already rejected above, so
		// this "" only ever means the companion key is missing.
		if strs[kind.CompanionKey] == "" {
			return credresolver.Config{}, fmt.Errorf("registryroutes: %s: credential key %q requires companion key %q", label, kind.SourceKey, kind.CompanionKey)
		}
	}
	if kind.ArgvValue {
		cfg.ExecArgv = execArgv
	} else {
		*kind.ValueField(&cfg) = strs[kind.SourceKey]
		cfg.FileFormat = kind.FileFormat
	}
	if kind.CompanionKey != "" {
		*kind.CompanionField(&cfg) = strs[kind.CompanionKey]
	}
	return cfg, nil
}

// parseExecArgv validates the "exec" credential value: a non-empty TOML array
// of strings whose argv[0] is itself non-empty. An empty argv[0] would reach
// exec.Command as an empty program name and fail with an OS error that never
// names the offending route.
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

// ValidateUpstreamOrigin reports an error unless raw is an absolute http(s)
// URL with no userinfo, path, query, or fragment (ADR 0047, issue #3261): a
// host-rooted route has no way to join a base path. A single trailing "/" is
// tolerated and stripped by the caller. Exported so cmd/launcher's per-route
// doctor row reuses this instead of drifting from what Parse accepts.
func ValidateUpstreamOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		// *url.Error echoes the full raw URL, which may embed userinfo; unwrap
		// to the inner error so a malformed URL never echoes a credential back.
		if uerr, ok := err.(*url.Error); ok {
			err = uerr.Err
		}
		return fmt.Errorf("upstream-origin is malformed: %w", err)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("upstream-origin %q must be an absolute http(s) URL", raw)
	}
	if u.User != nil {
		// raw is omitted here, unlike the errors above: it may embed a
		// credential, and this error must not echo one back.
		return errors.New("upstream-origin must not contain userinfo")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("upstream-origin %q must be a bare origin (scheme://host[:port]) with no path, query, or fragment", raw)
	}
	return nil
}

// UpstreamOriginFor renders an upstream URL as the upstream-origin a route
// should declare, or "" when none is needed: plain https on the default port
// is what a host-rooted route derives from match-host (ADR 0047, issue #3261).
// It rebuilds from u.Host, so an embedded credential is stripped rather than
// echoed. Exported so the migration remedies and registrydiscover.Render agree.
func UpstreamOriginFor(upstreamURL string) string {
	u, err := url.Parse(upstreamURL)
	// A scheme-less "user:s3cr3t@host" parses into an opaque body with an empty
	// Host, so this returns "" rather than falling back to the raw value, which
	// would echo the credential into an error, a log, or a generated file.
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme == "https" && u.Port() == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// validateAuthScheme reports an error unless scheme is "bearer", "basic", or
// "header:<Name>" with Name a valid RFC 7230 header field name (ADR 0045).
func validateAuthScheme(label, scheme string) error {
	if scheme == "bearer" || scheme == "basic" {
		return nil
	}
	if name, ok := strings.CutPrefix(scheme, "header:"); ok && name != "" {
		if registryvocab.IsValidHeaderFieldName(name) {
			return nil
		}
		return fmt.Errorf("registryroutes: %s: auth-scheme %q names an invalid header field name", label, scheme)
	}
	return fmt.Errorf("registryroutes: %s: auth-scheme %q is not one of \"bearer\", \"basic\", or \"header:<Name>\"", label, scheme)
}

// validateAllowPatterns rejects any pattern not already in the canonical
// subtree-root form registrypathset derives (ADR 0047, issue #3258). It checks
// with path.Clean rather than normalizing, so a mistyped pattern fails at parse
// time instead of silently mismatching a request path. "/" is rejected too:
// PathSet.Admits reads it as "admit every path", the off switch ADR 0047 bans.
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

// validateDeclaredPath validates one non-empty operator-declared path field,
// shared so gradle-path (issue #3259) and go-path (issue #3260) cannot drift.
// Both ban "$", "`", and "\": gradle's value lands in a Groovy double-quoted
// literal where "$" interpolates at load time, go's in a shell-sourced export
// line. It strips every trailing "/", so "//" cannot pass the bare-root check.
func validateDeclaredPath(label, field, value string) (string, error) {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, " \t\r\n") {
		return "", fmt.Errorf("registryroutes: %s: %s %q must not contain whitespace", label, field, value)
	}
	if strings.ContainsAny(value, "$`\\") {
		return "", fmt.Errorf("registryroutes: %s: %s %q must not contain %q, %q, or %q", label, field, value, "$", "`", "\\")
	}
	if !strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("registryroutes: %s: %s %q must start with \"/\"", label, field, value)
	}
	normalized := strings.TrimRight(value, "/")
	if normalized == "" {
		return "", fmt.Errorf("registryroutes: %s: %s %q must name a specific path, not the whole host", label, field, value)
	}
	for i, seg := range strings.Split(normalized, "/") {
		if i == 0 {
			// normalized always starts with "/", so the split always yields a
			// leading "" for that prefix: not a real segment, and not the
			// doubled slash the empty-segment check below catches.
			continue
		}
		switch seg {
		case "":
			return "", fmt.Errorf("registryroutes: %s: %s %q must not contain an empty segment (doubled slash)", label, field, value)
		case ".":
			return "", fmt.Errorf("registryroutes: %s: %s %q must not contain a %q segment", label, field, value, ".")
		case "..":
			return "", fmt.Errorf("registryroutes: %s: %s %q must not contain a %q segment", label, field, value, "..")
		}
	}
	return normalized, nil
}

// routeLabel names a route for an error message: by its match-host when it has
// one, or by its 1-based position when match-host itself is unusable as a label.
func routeLabel(matchHost string, index int) string {
	if matchHost != "" {
		return fmt.Sprintf("route %q", matchHost)
	}
	return fmt.Sprintf("route %d", index+1)
}
