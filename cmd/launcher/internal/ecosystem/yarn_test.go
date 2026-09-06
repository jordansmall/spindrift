package ecosystem

import "testing"

// TestParseYarnRegistryConfig_NpmRegistryServerQuoted verifies that
// .yarnrc.yml's top-level and per-scope npmRegistryServer values parse
// correctly whether double- or single-quoted.
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

// TestParseYarnRegistryConfig_RepeatedURLDeduped verifies that the same
// registry URL declared twice yields only one Declaration.
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

// TestParseYarnRegistryConfig_NonHTTPRegistryIsSkippedButNamed verifies
// that a npmRegistryServer value that is a local path, not an http(s) URL,
// is skipped but still reports namedAny true.
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

// TestParseYarnRegistryConfig_FullLineCommentYieldsNoDeclaration verifies
// that a line that is entirely a "#" comment is never mistaken for a
// registry declaration.
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

// TestParseYarnRegistryConfig_TrailingInlineCommentStripped verifies that a
// space-then-"#" trailing comment after a registry value is stripped from
// the extracted URL.
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

// TestParseYarnRegistryConfig_TabBeforeTrailingInlineCommentStripped is the
// tab-separator variant of TestParseYarnRegistryConfig_TrailingInlineCommentStripped.
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

// TestParseYarnRegistryConfig_QuotedValueHashFragmentNotTreatedAsComment
// verifies that a "#" inside a quoted value (a URL fragment) is preserved,
// not mistaken for a comment marker.
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

// TestParseYarnRegistryConfig_UnquotedHashFragmentPlusTrailingCommentStripped
// covers a value with a "#" URL fragment (no preceding whitespace, so not a
// comment itself) followed by a whitespace-then-"#" trailing comment.
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

// TestParseYarnRegistryConfig_UnquotedHashFragmentPlusTabTrailingCommentStripped
// is the tab-separator variant of
// TestParseYarnRegistryConfig_UnquotedHashFragmentPlusTrailingCommentStripped.
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

// TestParseYarnRegistryConfig_NeverStampsEcosystemOrConfigPath verifies the
// pure-hook contract directly.
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
