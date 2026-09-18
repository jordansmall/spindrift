package credresolver

import (
	"strings"
	"testing"
)

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

// Three tables guard against an implementation that stops at the first
// [registries.*] header or only remembers the last token it saw.
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

// A missing table must fail closed, never return an empty string with a nil
// error, which would let a proxy run unauthenticated without any signal.
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

// A table missing its token field is a different problem from no table at
// all, and callers (doctor, error logs) need to tell the two apart.
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

// The old hand-rolled scanner took first-match-wins. The TOML spec makes
// redefining a table a hard error and go-toml/v2 enforces that, so neither
// token is picked.
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

// A lookup miss must not interpolate another entry's token into the
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

// A header line with a trailing "#" comment must still count as a header,
// or the next table's token leaks into the lookup for the previous one.
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

// A hand-edited credentials.toml often carries a note line between the
// header and the token assignment.
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

// The truncation fix that closed the table-header-with-trailing-comment bug
// also covers a trailing comment on the token assignment itself.
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

// A quoted table name holding a literal "#" must still count as a header,
// or the next table's token leaks into the lookup for the previous one.
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

// The old hand-rolled scanner tolerated each of these broken headers as
// merely ending the previous table's section. A real TOML parser rejects the
// document outright, so the other table's token can never leak into a lookup
// for "myreg", either as the result or inside the error.
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

// TOML allows both single- and double-quoted strings.
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

// The old hand-rolled scanner never resolved a token containing a backslash,
// and the "behaves identically" acceptance criterion pins the set of tokens
// it accepted.
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

// go-toml/v2 escape decoding can produce control characters, and letting one
// through would blow up at HTTP header-write time instead of failing closed
// here.
func TestCargoCredentialsToken_ControlCharacterInTokenIsRejected(t *testing.T) {
	cases := []struct {
		name       string
		tomlEscape string // go-toml decodes this basic-string escape into the control character
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

// The token field is present here and only its value is rejected, so the
// "but no token field" wording would be false.
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

// Resolving a multi-line basic string would let an embedded newline flow
// from the token into an HTTP header value. The old hand-rolled scanner
// failed closed here, and the "behaves identically" acceptance criterion
// forbids reintroducing that hole.
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
