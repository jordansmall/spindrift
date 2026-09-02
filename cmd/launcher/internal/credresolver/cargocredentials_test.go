package credresolver

import (
	"strings"
	"testing"
)

// TestCargoCredentialsToken_SingleTableExactMatch verifies that a
// credentials.toml with a single [registries.NAME] table resolves the
// token for an exact registryName match.
func TestCargoCredentialsToken_SingleTableExactMatch(t *testing.T) {
	content := []byte("[registries.acme]\ntoken = \"s3kr3t\"\n")

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// TestCargoCredentialsToken_MultipleTablesResolvesCorrectOne verifies that a
// credentials.toml holding tables for several distinct registries resolves
// the token of the requested registryName, not just the first table in the
// file -- guards against an implementation that stops at the first
// [registries.*] header or only remembers the last token seen.
func TestCargoCredentialsToken_MultipleTablesResolvesCorrectOne(t *testing.T) {
	content := []byte(
		"[registries.first]\ntoken = \"first-tok\"\n" +
			"[registries.second]\ntoken = \"second-tok\"\n" +
			"[registries.third]\ntoken = \"third-tok\"\n",
	)

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "second")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "second-tok" {
		t.Errorf("got %q, want %q", got, "second-tok")
	}
}

// TestCargoCredentialsToken_TableNotPresentIsError verifies that a
// credentials.toml with no [registries.<name>] header at all fails closed
// with an error naming both the source and the registry name that was
// looked for -- never an empty string with a nil error, since that would
// let a proxy run unauthenticated without any signal.
func TestCargoCredentialsToken_TableNotPresentIsError(t *testing.T) {
	content := []byte("[registries.other]\ntoken = \"s3kr3t\"\n")
	const source = "/some/credentials.toml"
	const registryName = "missing-registry"

	_, err := cargoCredentialsToken(content, source, registryName)
	if err == nil {
		t.Fatal("expected error for registry with no matching table, got nil")
	}
	if !strings.Contains(err.Error(), source) {
		t.Errorf("expected error to mention the source %q, got: %v", source, err)
	}
	if !strings.Contains(err.Error(), registryName) {
		t.Errorf("expected error to mention the registry name %q, got: %v", registryName, err)
	}
}

// TestCargoCredentialsToken_TablePresentNoTokenIsError verifies that a
// matching [registries.<name>] table with no "token" key inside it produces
// a distinct error from the table-not-present case -- the registry does
// have a table, it is just missing a token field, which is a different
// problem than no table existing at all, and callers (doctor, error logs)
// need to tell the two apart.
func TestCargoCredentialsToken_TablePresentNoTokenIsError(t *testing.T) {
	content := []byte("[registries.acme]\n")
	const source = "/some/credentials.toml"
	const registryName = "acme"

	_, err := cargoCredentialsToken(content, source, registryName)
	if err == nil {
		t.Fatal("expected error for a table with no token field, got nil")
	}
	if strings.Contains(err.Error(), "no [registries.") {
		t.Errorf("expected a distinct error from the missing-table case, got: %v", err)
	}
	if !strings.Contains(err.Error(), source) {
		t.Errorf("expected error to mention the source %q, got: %v", source, err)
	}
	if !strings.Contains(err.Error(), registryName) {
		t.Errorf("expected error to mention the registry name %q, got: %v", registryName, err)
	}
}

// TestCargoCredentialsToken_DuplicateTableIsError verifies that when two
// [registries.<name>] tables exist for the same registryName, this errors
// rather than picking either one -- unlike the old hand-rolled scanner
// (which took a first-match-wins reading), the TOML spec makes redefining
// the same table a hard error, and go-toml/v2 enforces that.
func TestCargoCredentialsToken_DuplicateTableIsError(t *testing.T) {
	content := []byte(
		"[registries.acme]\ntoken = \"first-tok\"\n" +
			"[registries.acme]\ntoken = \"second-tok\"\n",
	)
	const source = "/some/credentials.toml"

	_, err := cargoCredentialsToken(content, source, "acme")
	if err == nil {
		t.Fatal("expected error for a duplicated table, got nil")
	}
	if !strings.Contains(err.Error(), source) {
		t.Errorf("expected error to mention the source %q, got: %v", source, err)
	}
	if strings.Contains(err.Error(), "first-tok") {
		t.Errorf("error must never echo a token value, got: %v", err)
	}
	if strings.Contains(err.Error(), "second-tok") {
		t.Errorf("error must never echo a token value, got: %v", err)
	}
}

// TestCargoCredentialsToken_NeverEchoesSecret proves that a lookup-miss
// error never contains a real token value that happens to be in scope
// elsewhere in the credentials.toml content -- guards against a future
// change accidentally interpolating an entry's token into the
// no-matching-table error message.
func TestCargoCredentialsToken_NeverEchoesSecret(t *testing.T) {
	const secret = "s3kr3t-do-not-echo"
	content := []byte("[registries.other]\ntoken = \"" + secret + "\"\n")

	_, err := cargoCredentialsToken(content, "/some/credentials.toml", "missing-registry")
	if err == nil {
		t.Fatal("expected error for registry with no matching table, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must never echo a token value, got: %v", err)
	}
}

// TestCargoCredentialsToken_EmptyTokenValueIsError verifies that a matching
// table with an explicit but empty "token = \"\"" produces the same
// table-exists-but-no-token error as a missing token field entirely --
// TOML makes an empty string representable, so treating it as a found
// credential would let a proxy run unauthenticated with a nil error.
func TestCargoCredentialsToken_EmptyTokenValueIsError(t *testing.T) {
	content := []byte("[registries.acme]\ntoken = \"\"\n")
	const source = "/some/credentials.toml"
	const registryName = "acme"

	_, err := cargoCredentialsToken(content, source, registryName)
	if err == nil {
		t.Fatal("expected error for an empty token value, got nil")
	}
	if strings.Contains(err.Error(), "no [registries.") {
		t.Errorf("expected the table-exists-but-no-token error, got: %v", err)
	}
	if !strings.Contains(err.Error(), source) {
		t.Errorf("expected error to mention the source %q, got: %v", source, err)
	}
	if !strings.Contains(err.Error(), registryName) {
		t.Errorf("expected error to mention the registry name %q, got: %v", registryName, err)
	}
}

// TestCargoCredentialsToken_HeaderTrailingCommentDoesNotLeakSection verifies
// that a table header with a trailing "#" comment (e.g. "[registries.other]
// # personal") is still recognized as a header line, rather than leaking the
// prior table's token into the lookup for a different registryName.
func TestCargoCredentialsToken_HeaderTrailingCommentDoesNotLeakSection(t *testing.T) {
	content := []byte(
		"[registries.mine]\n" +
			"[registries.other] # personal\ntoken = \"OTHER-SECRET\"\n",
	)

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "mine")
	if err == nil {
		t.Fatalf("expected the table-exists-but-no-token error for \"mine\", got token %q", got)
	}
	if !strings.Contains(err.Error(), "but no token field") {
		t.Errorf("expected the table-exists-but-no-token error, got: %v", err)
	}
	if strings.Contains(err.Error(), "OTHER-SECRET") {
		t.Errorf("leaked the other table's secret into the error: %v", err)
	}
}

// TestCargoCredentialsToken_StandaloneCommentLineInTableBodyDoesNotBreakParsing
// verifies that a full-line "#" comment inside a target table's body, sitting
// between the header and the "token = ..." line, is skipped rather than
// confusing the scanner -- e.g. a hand-edited credentials.toml with a note
// above the token assignment.
func TestCargoCredentialsToken_StandaloneCommentLineInTableBodyDoesNotBreakParsing(t *testing.T) {
	content := []byte("[registries.acme]\n# a note about this registry\ntoken = \"s3kr3t\"\n")

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// TestCargoCredentialsToken_TokenLineTrailingCommentIsStripped verifies that
// a trailing "#" comment on the "token = ..." assignment line itself is
// stripped before the value is parsed -- the same truncation fix that closed
// the table-header-with-trailing-comment bug also resolves this case.
func TestCargoCredentialsToken_TokenLineTrailingCommentIsStripped(t *testing.T) {
	content := []byte("[registries.acme]\ntoken = \"s3kr3t\" # prod credential\n")

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// TestCargoCredentialsToken_HeaderWithHashInQuotedNameDoesNotLeakSection
// verifies that a table header whose quoted name contains a literal "#"
// (e.g. [registries."other#x"]) is still recognized as a header line, rather
// than leaking the prior table's token into the lookup for a different
// registryName.
func TestCargoCredentialsToken_HeaderWithHashInQuotedNameDoesNotLeakSection(t *testing.T) {
	content := []byte(
		"[registries.myreg]\n" +
			"[registries.\"other#x\"]\ntoken = \"OTHERSECRET\"\n",
	)

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "myreg")
	if err == nil {
		t.Fatalf("expected the table-exists-but-no-token error for \"myreg\", got token %q", got)
	}
	if got == "OTHERSECRET" {
		t.Fatalf("leaked the other table's token as the result: %q", got)
	}
	if !strings.Contains(err.Error(), "but no token field") {
		t.Errorf("expected the table-exists-but-no-token error, got: %v", err)
	}
	if strings.Contains(err.Error(), "OTHERSECRET") {
		t.Errorf("leaked the other table's secret into the error: %v", err)
	}
}

// TestCargoCredentialsToken_MalformedHeaderIsError verifies that a header
// line broken by a dropped closing bracket, trailing junk after it, or an
// unterminated quoted name is invalid TOML and fails the whole parse --
// unlike the old hand-rolled scanner (which tolerated each of these forms
// as merely ending the previous table's section), a real TOML parser
// rejects the document outright, so the other table's token can never leak
// into a lookup for "myreg" either as the result or inside the error.
func TestCargoCredentialsToken_MalformedHeaderIsError(t *testing.T) {
	cases := []struct {
		name       string
		headerLine string
	}{
		{"MissingClosingBracket", "[registries.other"},
		{"TrailingJunk", "[registries.other] junk"},
		{"UnterminatedQuotedName", "[registries.\"other] # x"},
	}

	const source = "/some/credentials.toml"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte(
				"[registries.myreg]\n" + tc.headerLine + "\ntoken = \"OTHERSECRET\"\n",
			)

			got, err := cargoCredentialsToken(content, source, "myreg")
			if err == nil {
				t.Fatalf("expected a parse error for malformed TOML, got token %q", got)
			}
			if got == "OTHERSECRET" {
				t.Fatalf("leaked the other table's token as the result: %q", got)
			}
			if !strings.Contains(err.Error(), source) {
				t.Errorf("expected error to mention the source %q, got: %v", source, err)
			}
			if strings.Contains(err.Error(), "OTHERSECRET") {
				t.Errorf("leaked the other table's secret into the error: %v", err)
			}
		})
	}
}

// TestCargoCredentialsToken_SingleQuotedTokenWorks verifies that a
// single-quoted "token = '...'" assignment resolves the same as a
// double-quoted one -- TOML allows both string forms.
func TestCargoCredentialsToken_SingleQuotedTokenWorks(t *testing.T) {
	content := []byte("[registries.acme]\ntoken = 's3kr3t'\n")

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3kr3t" {
		t.Errorf("got %q, want %q", got, "s3kr3t")
	}
}

// TestCargoCredentialsToken_EscapedQuoteInTokenIsRejected verifies that a
// token value containing a backslash-escaped quote is rejected, matching the
// old hand-rolled scanner's fail-closed behavior -- the "behaves identically"
// acceptance criterion pins the old scanner's accepted-token surface, which
// never resolved a token containing a backslash.
func TestCargoCredentialsToken_EscapedQuoteInTokenIsRejected(t *testing.T) {
	const secret = "abc\"#def"
	content := []byte("[registries.acme]\ntoken = \"abc\\\"#def\"\n")

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
	if err == nil {
		t.Fatalf("expected the table-exists-but-no-token error, got token %q", got)
	}
	if strings.Contains(err.Error(), "no [registries.") {
		t.Errorf("expected the table-exists-but-no-token error, got: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must never echo a token value, got: %v", err)
	}
}

// TestCargoCredentialsToken_ControlCharacterInTokenIsRejected verifies that a
// token value containing a control character (NUL, tab, ...) is rejected --
// go-toml/v2 escape decoding can produce these (e.g. "a\tb"), and letting one
// through would blow up at HTTP header-write time instead of failing closed
// here.
func TestCargoCredentialsToken_ControlCharacterInTokenIsRejected(t *testing.T) {
	cases := []struct {
		name       string
		tomlEscape string // TOML basic-string escape sequence go-toml decodes into the control char
	}{
		{"NUL", `\u0000`},
		{"Tab", `\t`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte("[registries.acme]\ntoken = \"a" + tc.tomlEscape + "b\"\n")

			got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
			if err == nil {
				t.Fatalf("expected an error for a control character in the token, got token %q", got)
			}
			if strings.Contains(err.Error(), "no [registries.") {
				t.Errorf("expected the table-exists error, got: %v", err)
			}
		})
	}
}

// TestCargoCredentialsToken_DisallowedCharacterErrorIsDistinctFromMissingToken
// verifies that a token rejected for containing a disallowed character (here,
// an embedded quote) gets its own accurate error, distinct from the
// genuinely-missing-token case -- the token field IS present in this case, so
// the "but no token field" wording would be false. The error must still name
// the source and registry, and must never echo the rejected token value.
func TestCargoCredentialsToken_DisallowedCharacterErrorIsDistinctFromMissingToken(t *testing.T) {
	const secret = "abc\"#def"
	content := []byte("[registries.acme]\ntoken = \"abc\\\"#def\"\n")
	const source = "/some/credentials.toml"
	const registryName = "acme"

	_, err := cargoCredentialsToken(content, source, registryName)
	if err == nil {
		t.Fatal("expected an error for a disallowed character in the token, got nil")
	}
	if strings.Contains(err.Error(), "no token field") {
		t.Errorf("expected a distinct error from the missing-token-field case, got: %v", err)
	}
	if !strings.Contains(err.Error(), source) {
		t.Errorf("expected error to mention the source %q, got: %v", source, err)
	}
	if !strings.Contains(err.Error(), registryName) {
		t.Errorf("expected error to mention the registry name %q, got: %v", registryName, err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must never echo a token value, got: %v", err)
	}
}

// TestCargoCredentialsToken_TripleQuotedTokenIsRejected verifies that a TOML
// multi-line basic string ("""...""") is rejected, matching the old
// hand-rolled scanner's fail-closed behavior -- resolving it would let an
// embedded newline flow from the token into an HTTP header value, which the
// "behaves identically" acceptance criterion forbids reintroducing.
func TestCargoCredentialsToken_TripleQuotedTokenIsRejected(t *testing.T) {
	const secret = "SECRET"
	content := []byte("[registries.acme]\ntoken = \"\"\"\nSECRET\n\"\"\"\n")

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
	if err == nil {
		t.Fatalf("expected the table-exists-but-no-token error, got token %q", got)
	}
	if strings.Contains(err.Error(), "no [registries.") {
		t.Errorf("expected the table-exists-but-no-token error, got: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error must never echo a token value, got: %v", err)
	}
}
