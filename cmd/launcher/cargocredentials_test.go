package main

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

// TestCargoCredentialsToken_DuplicateTableFirstMatchWins verifies that when
// two [registries.<name>] tables exist for the same registryName, the first
// one's token wins -- mirroring netrc.go's first-match-wins precedent for a
// pathological file that repeats the same host/table.
func TestCargoCredentialsToken_DuplicateTableFirstMatchWins(t *testing.T) {
	content := []byte(
		"[registries.acme]\ntoken = \"first-tok\"\n" +
			"[registries.acme]\ntoken = \"second-tok\"\n",
	)

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "first-tok" {
		t.Errorf("got %q, want %q", got, "first-tok")
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

// TestCargoCredentialsToken_MalformedHeaderMissingClosingBracketDoesNotLeakSection
// verifies that a line starting with "[" but missing its closing "]" (a
// dropped bracket from a hand edit) still exits the previously active
// section rather than falling through into the key-parsing branch as if it
// were still inside "myreg". Same tokenFound-short-circuit pattern as
// TestCargoCredentialsToken_HeaderWithHashInQuotedNameDoesNotLeakSection:
// "myreg" carries no token of its own so the leak can't hide behind an
// early return.
// TestCargoCredentialsToken_MalformedHeaderDoesNotLeakSection covers the
// fail-closed fix for a header line that fails the HasSuffix(trimmed, "]")
// check -- a dropped closing bracket, trailing junk after it, or an
// unterminated quoted name that leaves stripTOMLComment's quote tracking
// never closed -- each of which must still end the previously active
// section rather than leaking its token into the lookup for "myreg".
func TestCargoCredentialsToken_MalformedHeaderDoesNotLeakSection(t *testing.T) {
	cases := []struct {
		name       string
		headerLine string
	}{
		{"MissingClosingBracket", "[registries.other"},
		{"TrailingJunk", "[registries.other] junk"},
		{"UnterminatedQuotedName", "[registries.\"other] # x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte(
				"[registries.myreg]\n" + tc.headerLine + "\ntoken = \"OTHERSECRET\"\n",
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
// token value containing a backslash-escaped quote is rejected rather than
// silently truncated at the escaped quote -- unquoteTOMLString does not
// process backslash escapes, so treating the result as valid would forward
// a corrupted credential (e.g. "abc\" instead of "abc\"#def") with a nil
// error.
func TestCargoCredentialsToken_EscapedQuoteInTokenIsRejected(t *testing.T) {
	content := []byte("[registries.acme]\ntoken = \"abc\\\"#def\"\n")

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
	if err == nil {
		t.Fatalf("expected the table-exists-but-no-token error, got token %q", got)
	}
	if strings.Contains(err.Error(), "no [registries.") {
		t.Errorf("expected the table-exists-but-no-token error, got: %v", err)
	}
}

// TestCargoCredentialsToken_TripleQuotedTokenIsRejected verifies that a TOML
// multi-line basic string ("""...""") is rejected rather than resolved to
// the corrupted single-character result of stripping only the outer quote
// pair.
func TestCargoCredentialsToken_TripleQuotedTokenIsRejected(t *testing.T) {
	content := []byte("[registries.acme]\ntoken = \"\"\"\nSECRET\n\"\"\"\n")

	got, err := cargoCredentialsToken(content, "/some/credentials.toml", "acme")
	if err == nil {
		t.Fatalf("expected the table-exists-but-no-token error, got token %q", got)
	}
	if strings.Contains(err.Error(), "no [registries.") {
		t.Errorf("expected the table-exists-but-no-token error, got: %v", err)
	}
}
