package ecosystem

import "testing"

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

// The ConfigParser hook is pure: registrydiscover's walker stamps Ecosystem
// and ConfigPath after the call returns, so the hook must leave both unset.
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
