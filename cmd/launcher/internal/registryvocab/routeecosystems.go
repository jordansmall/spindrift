package registryvocab

// RouteEcosystems is a route's per-ecosystem declaration block, keyed by an
// ecosystem.Table row's Name ("go", "gradle", "cargo", ...). It is the wire
// shape both the routes-file `[routes.ecosystems.<name>]` table and the
// manifest's projection of it share (issue #3403), so a block decoded from
// TOML and a block round-tripped through JSON must read back identically --
// see RouteDeclaration and Strings for how that constrains the value shape.
type RouteEcosystems map[string]RouteDeclaration

// RouteDeclaration is one ecosystem's block: an open map from key to value,
// mirroring the free-form sub-table precedent already used for a route's
// credential block. Only "path" is a typed, package-wide key (see
// RouteDeclarationPathKey); every other key is ecosystem-specific and is
// read with Strings.
type RouteDeclaration map[string]any

// RouteDeclarationPathKey is the one RouteDeclaration key every ecosystem
// shares: the URL subtree the ecosystem is served from, in the same sense
// as Subtree.Path.
const RouteDeclarationPathKey = "path"

// RouteDeclarationKeyLabel spells one ecosystem's declaration key the way an
// operator wrote it in the routes file, for error text. Every hop that names
// a block key back to an operator (the routes-file parser, the launcher's
// route resolution) goes through here, so the spelling can never drift
// between the message that rejects a key and the message that asks for one.
func RouteDeclarationKeyLabel(ecosystem, key string) string {
	return "ecosystems." + ecosystem + "." + key
}

// Path returns ecosystem's "path" value, or "" if r is nil, ecosystem has no
// block, the block has no "path" key, or the value is not a non-empty
// string. A wrong-typed or missing value reads as absent rather than as an
// error because by the time anything calls this accessor the routes-file
// parser has already validated the block's shape up front; Path is a
// read-side convenience over an already-trusted value, not a second
// validation pass.
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

// Strings returns ecosystem's key value as a []string, or nil if r is nil,
// ecosystem has no block, the block has no key, or the value is not a
// []any whose every element is a string. []any is the one representation
// Strings accepts because it is what go-toml decodes a TOML array into
// inside a map[string]any block, and what that same value comes back as
// after a JSON marshal/unmarshal round trip through the manifest -- so a
// caller that always writes through StringsValue gets an identical block
// whichever hop produced it. As with Path, a wrong-typed value reads as
// absent rather than as an error: validation is the parser's job, not this
// accessor's.
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
// reads back, so a caller building a RouteDeclaration by hand -- rather than
// decoding one from TOML -- never has to hand-roll the []any conversion
// itself. An empty or nil values returns nil rather than an empty []any, so
// a legacy-key translation that finds nothing declared omits the key
// entirely instead of writing an empty array.
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
