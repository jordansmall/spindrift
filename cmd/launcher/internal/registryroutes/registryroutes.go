// Package registryroutes parses and validates a registry proxy routes file
// (ADR 0045): a TOML document declaring one or more Registry routes, each
// binding a match host, an auth scheme, and a
// credential reference in a single record -- the property ADR 0045 calls
// load-bearing, since it leaves no Box-reachable way to pair a credential
// meant for one host with a different one.
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
	MatchHost  string
	AuthScheme string
	// UpstreamOrigin is the operator-declared scheme://host[:port] this
	// route forwards to (ADR 0047, issue #3261), overriding the origin the
	// Target repo's committed config would otherwise imply. It covers the
	// two things that config can't always supply on its own: a non-default
	// scheme or port, and a host serving only ecosystems with nothing
	// committed for the launcher to scan. Optional -- "" when the field is
	// omitted, the common case -- and never carries a path.
	UpstreamOrigin string
	Credential     credresolver.Config
	// Ecosystems is the route's per-ecosystem [routes.ecosystems.<name>]
	// declaration block (issue #3403), keyed by ecosystem.Table row name --
	// the single source of truth Parse builds CargoRegistries, GradlePath,
	// and GoPath back out of below, whether the route declared its block the
	// new way or via one of those three retired top-level keys. Nil when the
	// route declares nothing per-ecosystem, never an empty map.
	Ecosystems registryvocab.RouteEcosystems
	// CargoRegistries names the Target repo's [registries.NAME] entries this
	// route serves (ADR 0045), read back from Ecosystems' "cargo" block's
	// "registries" key. Nil when the routes file declares neither the
	// retired cargo-registries key nor a [routes.ecosystems.cargo] block
	// naming any (back-compat, or discovery (issue #3143) hasn't populated
	// it yet); an operator may also hand-write either spelling.
	CargoRegistries []string
	// Allow names extra path patterns that extend a host-rooted route's
	// derived enforced path-set (ADR 0047, issue #3258) -- for a path shape
	// the Target repo's own manifests don't expose (e.g. an Artifactory
	// sibling download endpoint), rather than gating enforcement itself. Nil
	// or empty is valid and the common case. Since every route is now
	// host-rooted (ADR 0047, issue #3261), extending the derived path-set is
	// the only recourse a route has -- there is no opt-out.
	Allow []string
	// GradlePath is the operator-declared path gradle should bind to under a
	// host-rooted route (issue #3259), read back from Ecosystems' "gradle"
	// block's "path" key. Gradle has no committed in-tree config spindrift
	// can scan (no InTreeConfigPath in ecosystem.Table), so unlike every
	// other entry in the enforced path-set, this one path comes from
	// operator declaration in the routes file rather than repo scanning. ""
	// when the route declares neither the retired gradle-path key nor a
	// [routes.ecosystems.gradle] block naming a path.
	GradlePath string
	// GoPath is the operator-declared path go should bind to (as GOPROXY)
	// under a host-rooted route (issue #3260), read back from Ecosystems'
	// "go" block's "path" key -- the same shape as GradlePath and for the
	// same reason: go, like gradle, has no committed in-tree config
	// spindrift can scan (no InTreeConfigPath in ecosystem.Table's go row),
	// so this one path comes from operator declaration in the routes file
	// rather than repo scanning. "" when the route declares neither the
	// retired go-path key nor a [routes.ecosystems.go] block naming a path.
	GoPath string
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

// The two retired keys (ADR 0047, issue #3261) stay decodable fields, as
// pointers: the decoder is strict, so dropping them outright would report a
// routes file that still declares one with a bare go-toml unknown-key error
// instead of retiredRouteKeysError's migration remedy, and a pointer
// distinguishes "declared" from "declared with the zero value" -- an
// explicit enforce-allowlist = false is as retired as a true one.
type rawRoute struct {
	MatchHost        string         `toml:"match-host"`
	UpstreamBaseURL  *string        `toml:"upstream-base-url"`
	UpstreamOrigin   string         `toml:"upstream-origin"`
	AuthScheme       string         `toml:"auth-scheme"`
	Credential       map[string]any `toml:"credential"`
	EnforceAllowlist *bool          `toml:"enforce-allowlist"`
	CargoRegistries  []string       `toml:"cargo-registries"`
	Allow            []string       `toml:"allow"`
	GradlePath       string         `toml:"gradle-path"`
	GoPath           string         `toml:"go-path"`
	// Ecosystems decodes [routes.ecosystems.<name>] (issue #3403), keyed by
	// the ecosystem name the operator wrote. Each block is left as a bare
	// map, the same free-form-sub-table precedent Credential above uses, so
	// the per-ecosystem key checks (path's shared rules, everything else the
	// row's own RouteDeclaration hook) can be done by hand against
	// ecosystem.Table and reported against the offending route, name, and
	// key -- a fixed struct with DisallowUnknownFields would only ever know
	// about "path", never a row-specific key like cargo's "registries".
	Ecosystems map[string]map[string]any `toml:"ecosystems"`
}

// Parse decodes, validates, and normalizes a routes file (ADR 0045) from
// data. Every returned error names the offending route (by its match-host,
// or "route N" when match-host itself is the problem) and field. It is a
// thin wrapper over parseRoutes, fixing rows to ecosystem.Table -- the real
// registries a build of this package knows about -- so that a test wanting
// a fake row (e.g. one exercising an unknown-key rejection without touching
// a real ecosystem's rules) can drive parseRoutes directly instead
// (mirroring registrydiscover's Extract/extractRows split).
func Parse(data []byte) ([]Route, error) {
	return parseRoutes(data, ecosystem.Table)
}

// parseRoutes is Parse's implementation, taking rows rather than reading
// ecosystem.Table directly; see Parse's own doc for why.
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

		// upstream-origin is optional: a route that omits it derives its
		// origin from the Target repo's committed config instead, and stores
		// "" all the way through Route -- ValidateUpstreamOrigin rejects an
		// empty string, so the validate-and-normalize only runs when the
		// field is actually present.
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

		// credentialUpstreamURL is what this route wants netrc's host match
		// keyed on: the netrc source (credresolver's netrcFileResolver)
		// parses Credential.UpstreamURL only to pull out its bare host for
		// the machine-name match, so "https://" + match-host carries exactly
		// the host the route already commits to when no origin is declared,
		// without inventing a path that doesn't exist.
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
			MatchHost:       rr.MatchHost,
			UpstreamOrigin:  upstreamOrigin,
			AuthScheme:      authScheme,
			Credential:      cred,
			Ecosystems:      ecosystems,
			CargoRegistries: ecosystems.Strings(nameCargo, cargoRegistriesKey),
			Allow:           rr.Allow,
			GradlePath:      ecosystems.Path(nameGradle),
			GoPath:          ecosystems.Path(nameGo),
		})
	}
	return routes, nil
}

// nameCargo, nameGradle, and nameGo are the three ecosystem.Table row names
// buildRouteEcosystems's legacy-key translation targets. registryroutes is
// not one of the packages ecosystem.containment_test.go's bare-name scan
// covers (see the scan's own doc), so spelling them out here -- rather than
// looking a row up by iterating rows for some other identifying field --
// is in bounds.
const (
	nameCargo          = "cargo"
	nameGradle         = "gradle"
	nameGo             = "go"
	pathKey            = registryvocab.RouteDeclarationPathKey
	cargoRegistriesKey = "registries"
)

// legacyRouteKeys names, for each ecosystem buildRouteEcosystems can
// translate a retired top-level key for, the key an operator who hasn't
// migrated yet still writes -- the spelling every error about a translated
// declaration uses, from the legacy/block conflict to whatever the
// declaration's own validation rejects, so the error names the key the
// operator actually typed rather than the block key it was translated into
// (issue #3403).
var legacyRouteKeys = map[string]string{
	nameCargo:  "cargo-registries",
	nameGradle: "gradle-path",
	nameGo:     "go-path",
}

// legacyDeclaration is one retired top-level key translated into the
// [routes.ecosystems.<name>] block it stands for, so both spellings reach
// exactly the same validation.
type legacyDeclaration struct {
	name string
	raw  map[string]any
}

// legacyDeclarations translates whichever of the three retired top-level
// keys rr declares into the blocks they stand for, gradle then go then
// cargo -- a fixed order, since a route declaring two bad legacy keys must
// always report the same one first.
func legacyDeclarations(rr rawRoute) []legacyDeclaration {
	var out []legacyDeclaration
	if rr.GradlePath != "" {
		out = append(out, legacyDeclaration{name: nameGradle, raw: map[string]any{pathKey: rr.GradlePath}})
	}
	if rr.GoPath != "" {
		out = append(out, legacyDeclaration{name: nameGo, raw: map[string]any{pathKey: rr.GoPath}})
	}
	if len(rr.CargoRegistries) > 0 {
		out = append(out, legacyDeclaration{name: nameCargo, raw: map[string]any{cargoRegistriesKey: registryvocab.StringsValue(rr.CargoRegistries)}})
	}
	return out
}

// buildRouteEcosystems builds rr's Ecosystems block: it seeds the block from
// whichever of the three retired top-level keys (cargo-registries,
// gradle-path, go-path) rr declares, running each through the very same
// validation a declared block gets -- so a rule lives in exactly one place
// (the shared path rules, or the row's own RouteDeclaration hook) and the
// two spellings cannot drift apart; then walks rr.Ecosystems in rows'
// order (falling back to any name rows doesn't know, sorted, so an unknown
// name is still reported deterministically) validating each declared block
// against the same row. A route naming the same ecosystem both ways (a
// retired key and a [routes.ecosystems.<name>] block) is rejected up front,
// before either side is otherwise validated, naming the route, the retired
// key, and the block -- there is no rule for merging the two, so accepting
// both silently would leave whichever key registryroutes checked last
// winning by accident. The returned block is nil, not empty, when rr
// declares nothing per-ecosystem at all (registryvocab.RouteEcosystems'
// documented "absent is nil" convention).
func buildRouteEcosystems(label string, rr rawRoute, rows []ecosystem.Row) (registryvocab.RouteEcosystems, error) {
	rowByName := make(map[string]ecosystem.Row, len(rows))
	for _, row := range rows {
		rowByName[row.Name] = row
	}

	blocks := make(registryvocab.RouteEcosystems)

	for _, legacy := range legacyDeclarations(rr) {
		if _, declared := rr.Ecosystems[legacy.name]; declared {
			return nil, legacyBlockConflictError(label, legacyRouteKeys[legacy.name], legacy.name)
		}
		row := rowByName[legacy.name]
		// A name rows doesn't know yields the zero Row, whose nil hook lands
		// the translated key in buildRouteDeclarationBlock's rejection rather
		// than accepting it unchecked -- but that rejection names row.Name, so
		// the zero Row still needs one.
		row.Name = legacy.name
		legacyKey := legacyRouteKeys[legacy.name]
		block, err := buildRouteDeclarationBlock(label, row, legacy.raw, func(string) string { return legacyKey })
		if err != nil {
			return nil, err
		}
		blocks[legacy.name] = block
	}

	handled := make(map[string]bool, len(rr.Ecosystems))
	for _, row := range rows {
		raw, ok := rr.Ecosystems[row.Name]
		if !ok {
			continue
		}
		handled[row.Name] = true
		block, err := buildRouteDeclarationBlock(label, row, raw, func(key string) string {
			return registryvocab.RouteDeclarationKeyLabel(row.Name, key)
		})
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

// legacyBlockConflictError reports a route declaring the same ecosystem
// both via a retired top-level key and via its [routes.ecosystems.<name>]
// block -- there is no rule for merging the two, so this is always an
// error, independent of which keys either side actually names.
func legacyBlockConflictError(label, legacyKey, name string) error {
	return fmt.Errorf("registryroutes: %s: %s and [routes.ecosystems.%s] both declare %s's route; keep only one", label, legacyKey, name, name)
}

// buildRouteDeclarationBlock validates one ecosystem's declaration block
// (issue #3403) against row -- "path" via the shared canonical-path rules
// every ecosystem uses (validateDeclaredPath), every other key via row's own
// RouteDeclaration hook, a nil hook rejecting every such key since it means
// "this row's block accepts no key beyond path" (RouteDeclarationValidator's
// own contract). Keys are walked in sorted order so a block declaring two
// problems always reports the same one first. keyLabel spells a key back to
// the operator: a block written as [routes.ecosystems.<name>] names it that
// way, while one translated from a retired top-level key names that key
// instead, which is what the operator can actually go and edit.
func buildRouteDeclarationBlock(label string, row ecosystem.Row, raw map[string]any, keyLabel func(key string) string) (registryvocab.RouteDeclaration, error) {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	block := make(registryvocab.RouteDeclaration, len(raw))
	for _, key := range keys {
		value := raw[key]
		if key == pathKey {
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("registryroutes: %s: %s must be a string", label, keyLabel(key))
			}
			normalized, err := validateDeclaredPath(label, keyLabel(key), s)
			if err != nil {
				return nil, err
			}
			block[key] = normalized
			continue
		}
		if row.RouteDeclaration == nil {
			return nil, fmt.Errorf("registryroutes: %s: %s is not a key %s's route declaration accepts", label, keyLabel(key), row.Name)
		}
		if err := row.RouteDeclaration(key, value); err != nil {
			return nil, fmt.Errorf("registryroutes: %s: %s %w", label, keyLabel(key), err)
		}
		block[key] = value
	}
	return block, nil
}

// retiredRouteKeysError reports a configuration error when rr declares
// either key ADR 0047 (issue #3261) retired -- upstream-base-url or
// enforce-allowlist -- naming the offending route, the key(s), the
// migration, and a copy-pasteable replacement stanza, the same
// retired-scalar-knob shape ADR 0045 used when it deleted the five
// REGISTRY_PROXY_* env knobs (see cmd/launcher's
// validateRetiredRegistryProxyKnobs). Detection is by presence, not
// truthiness: enforce-allowlist = false named an off switch that no longer
// exists, so it is as retired as a true one.
func retiredRouteKeysError(label string, rr rawRoute) error {
	var set []string
	if rr.UpstreamBaseURL != nil {
		set = append(set, "upstream-base-url")
	}
	if rr.EnforceAllowlist != nil {
		set = append(set, "enforce-allowlist")
	}
	if len(set) == 0 {
		return nil
	}

	verb := "is"
	if len(set) > 1 {
		verb = "are"
	}
	return fmt.Errorf(
		"registryroutes: %s: %s %s retired (ADR 0047, issue #3261): every route is now host-rooted, and enforcement against the derived path-set is unconditional -- there is no off switch, and allow is the only recourse for a path that set misses; equivalent routes-file stanza:\n\n%s",
		label, strings.Join(set, ", "), verb,
		retiredRouteStanza(rr),
	)
}

// retiredRouteStanza builds the replacement [[routes]] entry for a route
// that still declares a retired key: the route's own remaining declared
// keys, minus both retired ones, so migrating is "paste this stanza back"
// rather than "re-derive the route from ADR 0047". upstream-origin appears
// only when the retired upstream-base-url said something match-host alone
// cannot -- a non-default scheme or an explicit port; its path is dropped,
// since a host-rooted route derives the paths it serves rather than joining
// a base path.
func retiredRouteStanza(rr rawRoute) string {
	var b strings.Builder
	b.WriteString("[[routes]]\n")
	fmt.Fprintf(&b, "match-host = %q\n", rr.MatchHost)
	if rr.UpstreamBaseURL != nil {
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
	if len(rr.CargoRegistries) > 0 {
		fmt.Fprintf(&b, "cargo-registries = %s\n", tomlStringArray(rr.CargoRegistries))
	}
	if len(rr.Allow) > 0 {
		fmt.Fprintf(&b, "allow = %s\n", tomlStringArray(rr.Allow))
	}
	if rr.GradlePath != "" {
		fmt.Fprintf(&b, "gradle-path = %q\n", rr.GradlePath)
	}
	if rr.GoPath != "" {
		fmt.Fprintf(&b, "go-path = %q\n", rr.GoPath)
	}
	b.WriteString(retiredRouteEcosystemBlocks(rr.Ecosystems))
	return b.String()
}

// retiredRouteEcosystemBlocks renders a route's [routes.ecosystems.<name>]
// blocks (issue #3403) back as TOML sub-tables, which is why
// retiredRouteStanza appends them after every top-level key: a sub-table
// inside a [[routes]] entry ends that entry's top-level keys. Names and the
// keys within each block are sorted, since go's map iteration is randomized
// and this text lands in an error an operator is told to copy-paste (the
// same reason retiredRouteCredentialInline fixes its order). Blocks are
// echoed verbatim: this stanza is built before buildRouteEcosystems runs,
// so there is nothing validated or normalized to render yet.
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
// TOML inline table it was written as, in a fixed order -- the source key in
// credentialSourceKeys order, then its companion -- since go's map iteration
// is randomized and this text lands in an error an operator is told to
// copy-paste. Keys the credential grammar doesn't recognize are dropped:
// parseCredential would reject them anyway, and this stanza is meant to
// parse. Returns "" for a route with no credential key at all (ADR 0045's
// unauthenticated pass-through), which is a missing key rather than an
// empty one.
func retiredRouteCredentialInline(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	var pairs []string
	for _, key := range credentialSourceKeys {
		v, ok := m[key]
		if !ok {
			continue
		}
		pairs = append(pairs, fmt.Sprintf("%s = %s", key, tomlValue(v)))
		companion := ""
		switch key {
		case "cargo-credentials":
			companion = "registry-name"
		case "gradle-properties":
			companion = "key"
		}
		if companion == "" {
			continue
		}
		if cv, ok := m[companion]; ok {
			pairs = append(pairs, fmt.Sprintf("%s = %s", companion, tomlValue(cv)))
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

// tomlKey renders one operator-written key -- an ecosystem name, or a key
// within that ecosystem's block -- back as TOML. Either may have been
// written as a quoted key, and echoing such a name bare would render a
// stanza that no longer parses, defeating retiredRouteStanza's "paste this
// stanza back" promise.
func tomlKey(key string) string {
	if tomlBareKeyPattern.MatchString(key) {
		return key
	}
	return fmt.Sprintf("%q", key)
}

// tomlValue renders one decoded free-form value back as TOML -- a credential
// value or an ecosystem declaration's, both of which are a string or an
// array of them (the exec source's argv, cargo's registries). Anything else
// renders as a quoted Go rendering rather than being dropped silently -- it
// is malformed input either way, and parseCredential or the row's own
// RouteDeclaration hook names it precisely once the operator has migrated
// off the retired key.
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

// parseCredential validates a route's credential inline table and maps it
// onto credresolver.Config: exactly one of credentialSourceKeys must be
// present, "registry-name" is accepted only as cargo-credentials' companion,
// "key" only as gradle-properties' companion, and any other key is an
// error. upstreamURL is always carried through as Credential.UpstreamURL,
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
// upstreamURL here is really "whatever this route wants netrc's host
// match keyed on" -- Parse passes the route's declared upstream-origin, or
// its "https://" + match-host stand-in when the route declares none.
func parseCredential(label, matchHost string, m map[string]any, upstreamURL string) (credresolver.Config, error) {
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

	cfg := credresolver.Config{UpstreamURL: upstreamURL, MatchHost: matchHost}
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

// ValidateUpstreamOrigin reports an error unless raw is an absolute http(s)
// URL with no userinfo and no path, query, or fragment -- an origin, not a
// URL (ADR 0047, issue #3261): a route matches a host and serves the paths
// its enforced path-set admits, so an origin carrying a path would silently
// name a base path the host-rooted serving path has no way to join. A single
// trailing "/" is tolerated (url.Parse's Path for "https://host/") and
// stripped by the caller, so the bare and trailing-slash forms store
// identically. Exported so a per-route doctor row (cmd/launcher's
// registryRouteChecks) can reuse this package's own validation instead of
// reproducing it clause-for-clause -- a second, hand-copied version could
// silently drift out of sync with what Parse actually accepts.
func ValidateUpstreamOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse's *url.Error echoes the full raw URL, which may embed
		// userinfo; unwrap to the inner error so a malformed URL never
		// echoes a credential back (matching the userinfo branch below).
		if uerr, ok := err.(*url.Error); ok {
			err = uerr.Err
		}
		return fmt.Errorf("upstream-origin is malformed: %w", err)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("upstream-origin %q must be an absolute http(s) URL", raw)
	}
	if u.User != nil {
		// raw is omitted here, unlike the two errors above: it may embed a
		// credential (e.g. https://user:pass@host/), and this error must not
		// echo one back.
		return errors.New("upstream-origin must not contain userinfo")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("upstream-origin %q must be a bare origin (scheme://host[:port]) with no path, query, or fragment", raw)
	}
	return nil
}

// UpstreamOriginFor renders an upstream URL as the upstream-origin value a
// route should declare, or "" when the route needs no upstream-origin key at
// all: plain https on the default port is exactly what a host-rooted route
// derives from match-host on its own (ADR 0047, issue #3261). Only the origin
// is returned -- the URL's path, query, and fragment are dropped, since a
// host-rooted route derives the paths it serves rather than joining a base
// path -- and it is rebuilt from u.Host, which carries host[:port] and never
// userinfo, so a credential the URL embedded is stripped rather than echoed.
// A URL that doesn't parse, or parses without a host (a scheme-less
// "user:s3cr3t@host" parses into an opaque body with an empty Host), yields
// "" rather than risking the caller's raw value reaching an error printed to
// stderr and CI logs, or a generated file.
//
// The rule lives here, exported, because two kinds of caller must agree on
// it exactly: the migration remedies that tell an operator what to write
// (Parse's retired-key stanza, and cmd/launcher's stanza for the retired
// REGISTRY_PROXY_* scalar knobs) and the generator that writes it for them
// (registrydiscover.Render). Were they to drift, an operator following a
// remedy would end up with a route that disagrees with what "spindrift
// registry discover" produces for the same upstream.
func UpstreamOriginFor(upstreamURL string) string {
	u, err := url.Parse(upstreamURL)
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
// subtree-root form registrypathset derives (leading "/", no trailing "/",
// no "." or ".." segment) -- checked via path.Clean rather than silently
// normalized, so a mistyped pattern fails loudly at parse time instead of
// matching (or failing to match) a request path for a reason that's purely
// a formatting mismatch once merged into an enforced path-set (ADR 0047,
// issue #3258). The literal pattern "/" is rejected too, even though it
// passes the canonical-form check (path.Clean("/") == "/"):
// registryvocab.PathSet.Admits treats a "/" entry in EnforcedPaths as
// "admit every path", which is a legitimate *derived* entry when a
// registry's whole host is one endpoint with no subpath, but as an
// operator-supplied allow override it is indistinguishable from disabling
// host-rooted enforcement outright -- the off switch ADR 0047 forbids. Nil
// or empty patterns are valid (the field is optional).
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

// validateDeclaredPath validates a non-empty operator-declared path field --
// gradle-path (issue #3259) or go-path (issue #3260), named by field for the
// error text -- shared so the two fields' rules can never drift apart: it
// must start with "/", contain no whitespace, contain no "$", "`", or "\",
// contain no "..", ".", or empty (doubled-slash) segment, and not be the
// bare root "/" -- a route-level declared path only ever ADDS a subtree on
// top of an already-resolved host-rooted route, so declaring "the whole
// host" needs no special field and is rejected outright. The "$"/"`"/"\"
// ban applies to both fields, not just gradle-path's Groovy hazard: gradle's
// value flows into gradleRedirectScript's Groovy double-quoted string
// literal (ecosystem.GradleInitScript), where an unescaped "$" triggers
// GString interpolation at init-script load time, while go's flows into a
// shell-sourced `export GOPROXY='<value>'` line rendered in POSIX single
// quotes (driver-exec/bindregistry_cmd.go, registrymanifest.go) -- a GOPROXY
// URL path has no legitimate use for any of those three bytes either, so
// both fields ban them rather than special-casing gradle. On success it
// returns value with all trailing "/" stripped (not just one -- otherwise
// "//" would normalize to "/" and slip past the bare-root check below as
// the very whole-host value it's meant to catch), the same normalization
// Parse applies to upstream-origin. Callers must gate
// on value != "" themselves -- "" (the field omitted) is valid and never
// reaches this function.
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
			// normalized always starts with "/" (checked above), so
			// splitting on "/" always yields a leading "" for that prefix
			// -- not a real segment, and not the doubled-slash case the
			// empty-segment check below exists to catch.
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

// routeLabel names a route for an error message: by its match-host when it
// has one, or by its 1-based position in the file when match-host itself is
// what's missing or otherwise unusable as a label.
func routeLabel(matchHost string, index int) string {
	if matchHost != "" {
		return fmt.Sprintf("route %q", matchHost)
	}
	return fmt.Sprintf("route %d", index+1)
}
