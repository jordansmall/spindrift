package npmfamily

import (
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

func TestParseYarnRegistryConfig_NpmRegistryServerQuoted(t *testing.T) {
	content := `
npmRegistryServer: "https://yarn.example.com/registry"
npmScopes:
  myorg:
    npmRegistryServer: 'https://scoped-yarn.example.com/registry/'
`
	decls, namedAny, err := Yarn.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if !namedAny {
		t.Error("namedAny = false, want true")
	}
	want := []ecosystem.Declaration{
		{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/registry"},
		{Host: "scoped-yarn.example.com", UpstreamBaseURL: "https://scoped-yarn.example.com/registry", Scoped: true},
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

func TestParseYarnRegistryConfig_RepeatedURLDeduped(t *testing.T) {
	content := `
npmRegistryServer: https://yarn.example.com
npmScopes:
  myorg:
    npmRegistryServer: https://yarn.example.com
`
	decls, _, err := Yarn.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1 (repeated URL deduped)", decls)
	}
}

func TestParseYarnRegistryConfig_NonHTTPRegistryIsSkippedButNamed(t *testing.T) {
	decls, namedAny, err := Yarn.ConfigParser("npmRegistryServer: /local/path\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none", decls)
	}
	if !namedAny {
		t.Error("namedAny = false, want true")
	}
}

func TestParseYarnRegistryConfig_FullLineCommentYieldsNoDeclaration(t *testing.T) {
	decls, namedAny, err := Yarn.ConfigParser("# npmRegistryServer: https://commented-out.example.com/\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("decls = %+v, want none (a commented-out registry line must not leak a Declaration)", decls)
	}
	if namedAny {
		t.Error("namedAny = true, want false (no real declaration, just a comment)")
	}
}

func TestParseYarnRegistryConfig_TrailingInlineCommentStripped(t *testing.T) {
	decls, _, err := Yarn.ConfigParser("npmRegistryServer: https://yarn.example.com # our mirror\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := ecosystem.Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v", decls[0], want)
	}
}

func TestParseYarnRegistryConfig_TabBeforeTrailingInlineCommentStripped(t *testing.T) {
	decls, _, err := Yarn.ConfigParser("npmRegistryServer: https://yarn.example.com\t# our mirror\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := ecosystem.Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v", decls[0], want)
	}
}

func TestParseYarnRegistryConfig_QuotedValueHashFragmentNotTreatedAsComment(t *testing.T) {
	decls, _, err := Yarn.ConfigParser("npmRegistryServer: \"https://yarn.example.com/#frag\"\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := ecosystem.Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/#frag"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v (a quoted \"#\" must not be mistaken for a trailing comment)", decls[0], want)
	}
}

// The fixture's first "#" has no whitespace before it, so it is part of the
// URL and not a comment marker. Only a whitespace-then-"#" starts a comment.
func TestParseYarnRegistryConfig_UnquotedHashFragmentPlusTrailingCommentStripped(t *testing.T) {
	decls, _, err := Yarn.ConfigParser("npmRegistryServer: https://yarn.example.com/#frag # our mirror\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := ecosystem.Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/#frag"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v (a trailing comment after a \"#\" fragment must still be stripped)", decls[0], want)
	}
}

func TestParseYarnRegistryConfig_UnquotedHashFragmentPlusTabTrailingCommentStripped(t *testing.T) {
	decls, _, err := Yarn.ConfigParser("npmRegistryServer: https://yarn.example.com/#frag\t# our mirror\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := ecosystem.Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/#frag"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v (a trailing comment after a \"#\" fragment must still be stripped)", decls[0], want)
	}
}

// ConfigParser is a pure hook: its caller stamps Ecosystem and ConfigPath, so
// the hook itself must leave both unset.
func TestParseYarnRegistryConfig_NeverStampsEcosystemOrConfigPath(t *testing.T) {
	decls, _, err := Yarn.ConfigParser("npmRegistryServer: https://yarn.example.com\n")
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

func yarnExports(getenv map[string]string, routes []registrymanifest.Route) []ecosystem.EnvExport {
	got, _ := Yarn.EnvExports(27182, "", stubGetenv(getenv), routes)
	return got
}

func TestYarnEnvExports_UnscopedBindingAlsoWhitelistsLoopback(t *testing.T) {
	got := yarnExports(nil, taggedRoute("r0", registryvocab.Subtree{Ecosystem: "yarn", Path: "/yarn"}))
	want := []ecosystem.EnvExport{
		{Name: yarnBindingEnvVar, Value: "http://127.0.0.1:27182/r0/yarn/"},
		{Name: yarnUnsafeHTTPWhitelistVar, Value: "127.0.0.1"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("exports = %v, want %v", got, want)
	}
}

func TestYarnEnvExports_ScopedOnlyExportsOnlyWhitelist(t *testing.T) {
	got := yarnExports(nil, taggedRoute("r0", registryvocab.Subtree{Ecosystem: "yarn", Path: "/acme", Scoped: true}))
	want := ecosystem.EnvExport{Name: yarnUnsafeHTTPWhitelistVar, Value: "127.0.0.1"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("exports = %v, want [%v]", got, want)
	}
}

func TestYarnEnvExports_AmbiguousStillWhitelists(t *testing.T) {
	routes := taggedRoute("r0",
		registryvocab.Subtree{Ecosystem: "yarn", Path: "/a"},
		registryvocab.Subtree{Ecosystem: "yarn", Path: "/b"},
	)
	got := yarnExports(nil, routes)
	if v, ok := ecosystem.ExportValue(got, yarnUnsafeHTTPWhitelistVar); !ok || v != "127.0.0.1" {
		t.Errorf("whitelist = (%q, %v), want (127.0.0.1, true)", v, ok)
	}
}

func TestYarnEnvExports_PreservesExistingWhitelist(t *testing.T) {
	routes := taggedRoute("r0", registryvocab.Subtree{Ecosystem: "yarn", Path: "/yarn"})
	tests := []struct{ name, existing, want string }{
		{"appended", "registry.corp.example", "registry.corp.example,127.0.0.1"},
		{"already listed", "a.example,127.0.0.1", "a.example,127.0.0.1"},
		{"substring is not a match", "127.0.0.10", "127.0.0.10,127.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := yarnExports(map[string]string{yarnUnsafeHTTPWhitelistVar: tc.existing}, routes)
			if v, ok := ecosystem.ExportValue(got, yarnUnsafeHTTPWhitelistVar); !ok || v != tc.want {
				t.Errorf("whitelist = (%q, %v), want (%q, true)", v, ok, tc.want)
			}
		})
	}
}

func TestYarnEnvExports_NoYarnPathNoWhitelist(t *testing.T) {
	tests := []struct {
		name   string
		routes []registrymanifest.Route
	}{
		{"other ecosystems only", taggedRoute("r0", registryvocab.Subtree{Ecosystem: "npm", Path: "/npm"})},
		{"no paths", []registrymanifest.Route{{Prefix: "r0"}}},
		{"empty prefix", taggedRoute("", registryvocab.Subtree{Ecosystem: "yarn", Path: "/yarn"})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := yarnExports(nil, tc.routes); len(got) != 0 {
				t.Errorf("exports = %v, want none", got)
			}
		})
	}
}
