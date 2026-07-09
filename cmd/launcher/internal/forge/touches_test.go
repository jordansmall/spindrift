package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestParseTouchPaths_HeaderListFormat(t *testing.T) {
	body := "## Touches\n- cmd/launcher/*.go\n- lib/env-schema.nix"
	paths := forge.ParseTouchPaths(body)
	if len(paths) != 2 || paths[0] != "cmd/launcher/*.go" || paths[1] != "lib/env-schema.nix" {
		t.Errorf("expected [cmd/launcher/*.go lib/env-schema.nix], got %v", paths)
	}
}

func TestParseTouchPaths_Empty(t *testing.T) {
	if paths := forge.ParseTouchPaths(""); len(paths) != 0 {
		t.Errorf("expected [], got %v", paths)
	}
}

func TestParseTouchPaths_NoSection(t *testing.T) {
	body := "Just a regular issue body with no Touches section."
	if paths := forge.ParseTouchPaths(body); len(paths) != 0 {
		t.Errorf("expected [], got %v", paths)
	}
}

func TestParseTouchPaths_HeaderWithColon(t *testing.T) {
	body := "## Touches:\n- docs/reference.md"
	paths := forge.ParseTouchPaths(body)
	if len(paths) != 1 || paths[0] != "docs/reference.md" {
		t.Errorf("expected [docs/reference.md], got %v", paths)
	}
}

func TestParseTouchPaths_SectionEndsOnNextHeading(t *testing.T) {
	body := "## Touches\n- cmd/launcher/*.go\n## Other section\n- README.md"
	paths := forge.ParseTouchPaths(body)
	if len(paths) != 1 || paths[0] != "cmd/launcher/*.go" {
		t.Errorf("expected [cmd/launcher/*.go], got %v", paths)
	}
}

func TestParseTouchPaths_NoDuplicates(t *testing.T) {
	body := "## Touches\n- cmd/launcher/*.go\n- cmd/launcher/*.go"
	paths := forge.ParseTouchPaths(body)
	if len(paths) != 1 || paths[0] != "cmd/launcher/*.go" {
		t.Errorf("expected [cmd/launcher/*.go] (deduplicated), got %v", paths)
	}
}
