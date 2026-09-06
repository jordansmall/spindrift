package ecosystem

import "testing"

// TestParsePnpmRegistryConfig_RegistryLine verifies that
// pnpm-workspace.yaml's top-level "registry:" key and a quoted
// "@scope:registry" catalog key both parse into their own Declaration.
func TestParsePnpmRegistryConfig_RegistryLine(t *testing.T) {
	content := `
packages:
  - "packages/*"
registry: "https://pnpm.example.com/registry"
catalog:
  "@myorg:registry": 'https://scoped-pnpm.example.com/registry/'
`
	decls, namedAny, err := pnpmRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if !namedAny {
		t.Error("namedAny = false, want true")
	}
	want := []Declaration{
		{Host: "pnpm.example.com", UpstreamBaseURL: "https://pnpm.example.com/registry"},
		{Host: "scoped-pnpm.example.com", UpstreamBaseURL: "https://scoped-pnpm.example.com/registry"},
	}
	if len(decls) != len(want) {
		t.Fatalf("decls = %+v, want %+v", decls, want)
	}
	for i, w := range want {
		if decls[i] != w {
			t.Errorf("decls[%d] = %+v, want %+v", i, decls[i], w)
		}
	}
}

// TestParsePnpmRegistryConfig_RepeatedURLDeduped verifies that the same
// registry URL declared twice yields only one Declaration.
func TestParsePnpmRegistryConfig_RepeatedURLDeduped(t *testing.T) {
	content := `
registry: https://pnpm.example.com/registry
catalog:
  "@myorg:registry": https://pnpm.example.com/registry
`
	decls, _, err := pnpmRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1 (repeated URL deduped)", decls)
	}
}

// TestParsePnpmRegistryConfig_NonRegistrySuffixKeyNotDeclared verifies that
// a key merely ending in "registry" (not the literal "registry" key or a
// "@scope:registry" scoped key) is not mistaken for a registry declaration.
func TestParsePnpmRegistryConfig_NonRegistrySuffixKeyNotDeclared(t *testing.T) {
	decls, namedAny, err := pnpmRow.ConfigParser("myregistry: https://sneaky.example.com\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none (\"myregistry\" is not a real pnpm registry key)", decls)
	}
	if namedAny {
		t.Error("namedAny = true, want false")
	}
}

// TestParsePnpmRegistryConfig_ListItemRegistryKeyNotDeclared verifies that a
// "registry:" key nested under a YAML list item (not a top-level or scoped
// key) is not parsed as a declaration.
func TestParsePnpmRegistryConfig_ListItemRegistryKeyNotDeclared(t *testing.T) {
	content := `
mirrors:
  - registry: https://listitem.example.com
`
	decls, namedAny, err := pnpmRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none (a YAML list item is not a top-level or scoped registry key)", decls)
	}
	if namedAny {
		t.Error("namedAny = true, want false")
	}
}

// TestParsePnpmRegistryConfig_FullLineCommentYieldsNoDeclaration verifies
// that a line that is entirely a "#" comment is never mistaken for a
// registry declaration.
func TestParsePnpmRegistryConfig_FullLineCommentYieldsNoDeclaration(t *testing.T) {
	content := `
packages:
  - "packages/*"
# registry: https://commented-out.example.com/
`
	decls, namedAny, err := pnpmRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none (a commented-out registry line must not leak a Declaration)", decls)
	}
	if namedAny {
		t.Error("namedAny = true, want false")
	}
}

// TestParsePnpmRegistryConfig_TrailingInlineCommentStripped verifies that a
// space-then-"#" trailing comment after a registry value is stripped from
// the extracted URL.
func TestParsePnpmRegistryConfig_TrailingInlineCommentStripped(t *testing.T) {
	decls, _, err := pnpmRow.ConfigParser("registry: https://pnpm.example.com/registry # our mirror\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := Declaration{Host: "pnpm.example.com", UpstreamBaseURL: "https://pnpm.example.com/registry"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v", decls[0], want)
	}
}

// TestParsePnpmRegistryConfig_NeverStampsEcosystemOrConfigPath verifies the
// pure-hook contract directly.
func TestParsePnpmRegistryConfig_NeverStampsEcosystemOrConfigPath(t *testing.T) {
	decls, _, err := pnpmRow.ConfigParser("registry: https://pnpm.example.com/registry\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	if decls[0].Ecosystem != "" || decls[0].ConfigPath != "" {
		t.Errorf("decls[0] = %+v, want Ecosystem and ConfigPath both unset", decls[0])
	}
}
