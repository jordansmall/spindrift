package registryvocab

// RouteEcosystems is a route's per-ecosystem declaration block, keyed by an
// ecosystem.Table row's Name ("go", "gradle", "cargo", ...). The routes-file
// `[routes.ecosystems.<name>]` table and the manifest's projection of it
// share this wire shape (issue #3403), so a block decoded from TOML and one
// round-tripped through JSON must read back identically.
type RouteEcosystems map[string]RouteDeclaration

// RouteDeclaration is one ecosystem's block, an open map from key to value.
// Only "path" is a typed, package-wide key; every other key is
// ecosystem-specific and is read with Strings.
type RouteDeclaration map[string]any

// RouteDeclarationPathKey is the one RouteDeclaration key every ecosystem
// shares: the URL subtree the ecosystem is served from, in the same sense
// as Subtree.Path.
const RouteDeclarationPathKey = "path"

// RouteDeclarationKeyLabel spells one ecosystem's declaration key the way an
// operator wrote it in the routes file, for error text. Every hop that names
// a block key back to an operator goes through here, so the message that
// rejects a key and the message that asks for one cannot drift apart.
func RouteDeclarationKeyLabel(ecosystem, key string) string {
	return "ecosystems." + ecosystem + "." + key
}

// Path returns ecosystem's "path" value, or "" if r is nil, ecosystem has no
// block, the block has no "path" key, or the value is not a string. A
// wrong-typed value reads as absent rather than as an error because the
// routes-file parser has already validated the block's shape by the time
// anything calls this.
func (r RouteEcosystems) Path(ecosystem string) string {
	v, ok := r.value(ecosystem, RouteDeclarationPathKey)
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// Strings returns ecosystem's key value as a []string, or nil if the value
// is missing or is not a []any of strings. []any is the only accepted
// representation because it is what go-toml decodes a TOML array into inside
// a map[string]any block, and what that value comes back as after a JSON
// round trip through the manifest.
func (r RouteEcosystems) Strings(ecosystem, key string) []string {
	v, ok := r.value(ecosystem, key)
	if !ok {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(raw))
	for i, elem := range raw {
		s, ok := elem.(string)
		if !ok {
			return nil
		}
		out[i] = s
	}
	return out
}

func (r RouteEcosystems) value(ecosystem, key string) (any, bool) {
	if r == nil {
		return nil, false
	}
	block, ok := r[ecosystem]
	if !ok {
		return nil, false
	}
	v, ok := block[key]
	return v, ok
}

// StringsValue converts values into the canonical []any block value Strings
// reads back. An empty or nil slice returns nil rather than an empty []any,
// so a legacy-key translation that finds nothing declared omits the key
// instead of writing an empty array.
func StringsValue(values []string) []any {
	if len(values) == 0 {
		return nil
	}
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
