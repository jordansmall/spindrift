package ecosystem

import "testing"

func TestParseYarnRegistryConfig_NpmRegistryServerQuoted(t *testing.T) {
	content := `
npmRegistryServer: "https://yarn.example.com/registry"
npmScopes:
  myorg:
    npmRegistryServer: 'https://scoped-yarn.example.com/registry/'
`
	decls, namedAny, err := yarnRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if !namedAny {
		t.Error("namedAny = false, want true")
	}
	want := []Declaration{
		{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/registry"},
		{Host: "scoped-yarn.example.com", UpstreamBaseURL: "https://scoped-yarn.example.com/registry"},
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
	decls, _, err := yarnRow.ConfigParser(content)
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1 (repeated URL deduped)", decls)
	}
}

func TestParseYarnRegistryConfig_NonHTTPRegistryIsSkippedButNamed(t *testing.T) {
	decls, namedAny, err := yarnRow.ConfigParser("npmRegistryServer: /local/path\n")
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
	decls, namedAny, err := yarnRow.ConfigParser("# npmRegistryServer: https://commented-out.example.com/\n")
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
	decls, _, err := yarnRow.ConfigParser("npmRegistryServer: https://yarn.example.com # our mirror\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v", decls[0], want)
	}
}

func TestParseYarnRegistryConfig_TabBeforeTrailingInlineCommentStripped(t *testing.T) {
	decls, _, err := yarnRow.ConfigParser("npmRegistryServer: https://yarn.example.com\t# our mirror\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v", decls[0], want)
	}
}

func TestParseYarnRegistryConfig_QuotedValueHashFragmentNotTreatedAsComment(t *testing.T) {
	decls, _, err := yarnRow.ConfigParser("npmRegistryServer: \"https://yarn.example.com/#frag\"\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/#frag"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v (a quoted \"#\" must not be mistaken for a trailing comment)", decls[0], want)
	}
}

// The fixture's first "#" has no whitespace before it, so it is part of the
// URL and not a comment marker. Only a whitespace-then-"#" starts a comment.
func TestParseYarnRegistryConfig_UnquotedHashFragmentPlusTrailingCommentStripped(t *testing.T) {
	decls, _, err := yarnRow.ConfigParser("npmRegistryServer: https://yarn.example.com/#frag # our mirror\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/#frag"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v (a trailing comment after a \"#\" fragment must still be stripped)", decls[0], want)
	}
}

func TestParseYarnRegistryConfig_UnquotedHashFragmentPlusTabTrailingCommentStripped(t *testing.T) {
	decls, _, err := yarnRow.ConfigParser("npmRegistryServer: https://yarn.example.com/#frag\t# our mirror\n")
	if err != nil {
		t.Fatalf("ConfigParser: unexpected error: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("decls = %+v, want exactly 1", decls)
	}
	want := Declaration{Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/#frag"}
	if decls[0] != want {
		t.Errorf("decls[0] = %+v, want %+v (a trailing comment after a \"#\" fragment must still be stripped)", decls[0], want)
	}
}

// ConfigParser is a pure hook: its caller stamps Ecosystem and ConfigPath, so
// the hook itself must leave both unset.
func TestParseYarnRegistryConfig_NeverStampsEcosystemOrConfigPath(t *testing.T) {
	decls, _, err := yarnRow.ConfigParser("npmRegistryServer: https://yarn.example.com\n")
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
