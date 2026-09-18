package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// runNoncePattern matches a RUN_NONCE reference in any spelling a fragment or
// the registry might use (`${RUN_NONCE}`, `$RUN_NONCE`). fieldShapePattern and
// emitsMarker share it so the two never drift apart.
const runNoncePattern = `\$(?:\{RUN_NONCE\}|RUN_NONCE\b)`

// emitsMarker reports whether content emits token followed by any RUN_NONCE
// spelling. It is deliberately looser than fieldShapePattern: it ignores the
// payload placeholder, so the walk reports a fragment with a wrong field shape
// as undeclared instead of letting it pass silently.
func emitsMarker(content, token string) bool {
	pattern := regexp.MustCompile(regexp.QuoteMeta(token) + `\s+` + runNoncePattern)
	return pattern.MatchString(content)
}

// TestMarkerFragmentParity pins the marker-emitting prompt fragments (issue
// #2996) against outcome.MarkerChannelFieldShapes, the Go copy of
// lib/prompt-contract.nix's markerChannels registry. A fragment's marker line
// is a template with placeholders, so the check derives an expected shape from
// the fieldShape's placeholder fields, not from the fieldShape string itself.
func TestMarkerFragmentParity(t *testing.T) {
	fragments := []struct {
		file  string
		token string
	}{
		{"research-verdict-local.md", "SPINDRIFT_COMMENT"},
		{"research-verdict-github-readonly.md", "SPINDRIFT_COMMENT"},
		{"research-verdict-forgejo-readonly.md", "SPINDRIFT_COMMENT"},
		{"filer-file-relay.md", "SPINDRIFT_ISSUE_INTENT"},
	}

	repoRoot := filepath.Join("..", "..", "..")

	for _, f := range fragments {
		t.Run(f.file, func(t *testing.T) {
			shape, ok := outcome.MarkerChannelFieldShapes[f.token]
			if !ok {
				t.Fatalf("no MarkerChannelFieldShapes entry for token %q", f.token)
			}
			pattern := fieldShapePattern(t, f.token, shape)
			content := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/"+f.file))
			if !pattern.MatchString(content) {
				t.Errorf("%s does not contain a %q emission matching field shape %q (pattern: %s)",
					f.file, f.token, shape, pattern.String())
			}
		})
	}

	t.Run("exhaustiveness", func(t *testing.T) {
		// Keyed by path relative to templates/, not basename, so a
		// same-named fragment added under another template set still
		// registers as undeclared.
		declared := map[string]bool{}
		for _, f := range fragments {
			declared["default/prompts/fragments/"+f.file] = true
		}
		// Derived from fragments, not hand-listed, so a new fragment
		// row's token is automatically walked too.
		seenToken := map[string]bool{}
		var tokens []string
		for _, f := range fragments {
			if !seenToken[f.token] {
				seenToken[f.token] = true
				tokens = append(tokens, f.token)
			}
		}

		templatesRoot := filepath.Join(repoRoot, "templates")
		err := filepath.WalkDir(templatesRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			normalized := normalizeWhitespace(string(raw))
			rel, relErr := filepath.Rel(templatesRoot, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			for _, tok := range tokens {
				if !emitsMarker(normalized, tok) {
					continue
				}
				if !declared[rel] {
					t.Errorf("%s emits %q with a run nonce but has no marker_fragment_parity_test.go fixture row", rel, tok)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", templatesRoot, err)
		}

		// Every declared row must still point at a real file that emits its
		// token, catching a stale row left behind by a fragment rename or edit.
		for _, f := range fragments {
			content := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/"+f.file))
			if !emitsMarker(content, f.token) {
				t.Errorf("declared fixture row %s no longer emits %q with a run nonce", f.file, f.token)
			}
		}
	})
}

// fieldShapePattern derives a regexp matching a token's rendered emission from
// its markerChannels fieldShape (e.g. "<nonce> <base64-payload>"). It calls
// t.Fatalf on an unrecognized placeholder rather than skipping it, so a
// registry edit that introduces a new field shape fails loudly here instead of
// passing a fixture that never checked it.
func fieldShapePattern(t *testing.T, token, fieldShape string) *regexp.Regexp {
	t.Helper()
	parts := []string{regexp.QuoteMeta(token)}
	for _, field := range strings.Fields(fieldShape) {
		switch field {
		case "<nonce>":
			parts = append(parts, runNoncePattern)
		case "<base64-payload>":
			// Fragments spell this placeholder differently (one writes
			// `<base64-encoded verdict comment body, structured per below>`),
			// so match any angle-bracket placeholder starting with "base64"
			// rather than the exact wording.
			parts = append(parts, `<base64[^>]*>`)
		default:
			t.Fatalf("fieldShapePattern: field shape %q for token %q has no derivation rule for placeholder %q — add one",
				fieldShape, token, field)
		}
	}
	return regexp.MustCompile(strings.Join(parts, `\s+`))
}
