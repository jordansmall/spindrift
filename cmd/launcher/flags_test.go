package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseFlags_SetEnv: a recognized flag is injected into the environment.
func TestParseFlags_SetEnv(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "")
	remaining, err := parseFlags([]string{"--issue-number", "215"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
	if got := os.Getenv("ISSUE_NUMBER"); got != "215" {
		t.Errorf("ISSUE_NUMBER = %q, want %q", got, "215")
	}
}

// TestParseFlags_FlagWinsOverEnv: flag > env precedence.
func TestParseFlags_FlagWinsOverEnv(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "1")
	_, err := parseFlags([]string{"--issue-number", "999"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("ISSUE_NUMBER"); got != "999" {
		t.Errorf("ISSUE_NUMBER = %q, want %q (flag must win over env)", got, "999")
	}
}

// TestParseFlags_EnvFallback: env is used when no flag is supplied.
func TestParseFlags_EnvFallback(t *testing.T) {
	t.Setenv("MAX_JOBS", "7")
	_, err := parseFlags([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("MAX_JOBS"); got != "7" {
		t.Errorf("MAX_JOBS = %q, want %q (env must survive when no flag given)", got, "7")
	}
}

// TestParseFlags_UnknownFlag: unrecognised --flag returns an error.
func TestParseFlags_UnknownFlag(t *testing.T) {
	_, err := parseFlags([]string{"--not-a-schema-flag", "value"})
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
}

// TestParseFlags_PassthroughPositional: positional args are returned unchanged.
func TestParseFlags_PassthroughPositional(t *testing.T) {
	remaining, err := parseFlags([]string{"build", "--max-jobs", "2", "extra"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[0] != "build" || remaining[1] != "extra" {
		t.Errorf("remaining = %v, want [build extra]", remaining)
	}
}

// TestParseFlags_DoubleDash: args after "--" are passed through unchanged.
func TestParseFlags_DoubleDash(t *testing.T) {
	remaining, err := parseFlags([]string{"--", "--not-parsed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 1 || remaining[0] != "--not-parsed" {
		t.Errorf("remaining = %v, want [--not-parsed]", remaining)
	}
}

// TestParseFlags_MissingValue: flag with no value returns an error.
func TestParseFlags_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--issue-number"})
	if err == nil {
		t.Fatal("expected error when flag value is missing, got nil")
	}
}

// TestParseFlags_SecretsExcluded: secret knobs must not appear in schemaFlags.
func TestParseFlags_SecretsExcluded(t *testing.T) {
	secrets := []string{"GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}
	for _, env := range secrets {
		for _, entry := range schemaFlags {
			if entry.env == env {
				t.Errorf("secret knob %s must not appear in schemaFlags (would expose secrets in ps output)", env)
			}
		}
	}
}

// TestParseFlags_MultipleFlags: multiple flags are all injected.
func TestParseFlags_MultipleFlags(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "")
	t.Setenv("MAX_JOBS", "")
	_, err := parseFlags([]string{"--issue-number", "215", "--max-jobs", "1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("ISSUE_NUMBER"); got != "215" {
		t.Errorf("ISSUE_NUMBER = %q, want %q", got, "215")
	}
	if got := os.Getenv("MAX_JOBS"); got != "1" {
		t.Errorf("MAX_JOBS = %q, want %q", got, "1")
	}
}

// TestDispatchIssueArg_Numeric: a numeric arg is returned as the issue number.
func TestDispatchIssueArg_Numeric(t *testing.T) {
	got := dispatchIssueArg([]string{"123"})
	if got != "123" {
		t.Errorf("dispatchIssueArg([\"123\"]) = %q, want %q", got, "123")
	}
}

// TestDispatchIssueArg_Empty: empty args return empty string.
func TestDispatchIssueArg_Empty(t *testing.T) {
	got := dispatchIssueArg([]string{})
	if got != "" {
		t.Errorf("dispatchIssueArg([]) = %q, want %q", got, "")
	}
}

// TestDispatchIssueArg_NonNumeric: non-numeric first arg returns empty string.
func TestDispatchIssueArg_NonNumeric(t *testing.T) {
	got := dispatchIssueArg([]string{"not-an-issue"})
	if got != "" {
		t.Errorf("dispatchIssueArg([\"not-an-issue\"]) = %q, want empty (non-numeric ignored)", got)
	}
}

// TestPrintVersion_Format: version output starts with "spindrift" and includes a rev.
func TestPrintVersion_Format(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf)
	got := buf.String()
	if !strings.HasPrefix(got, "spindrift ") {
		t.Errorf("printVersion must start with 'spindrift ', got: %q", got)
	}
	if !strings.Contains(got, "(rev ") {
		t.Errorf("printVersion must contain '(rev ...)', got: %q", got)
	}
}

// TestPrintHelp_UsageLineNamesSpindrift: the first usage line must name the binary "spindrift".
func TestPrintHelp_UsageLineNamesSpindrift(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	firstLine := strings.SplitN(buf.String(), "\n", 2)[0]
	if !strings.HasPrefix(firstLine, "Usage: spindrift") {
		t.Errorf("first line must start with 'Usage: spindrift', got: %q", firstLine)
	}
}

// TestPrintHelp_ShowsDispatchSubcommand: help output names dispatch as a subcommand (not just a flag doc).
func TestPrintHelp_ShowsDispatchSubcommand(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	// "dispatch" must appear as a standalone subcommand entry, not buried in a flag doc.
	if !strings.Contains(out, "dispatch") {
		t.Errorf("help output must show 'dispatch' subcommand, got:\n%s", out)
	}
}

// TestPrintHelp_ContainsLabelEntry: help output includes --label flag with its doc.
func TestPrintHelp_ContainsLabelEntry(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	if !strings.Contains(out, "--label") {
		t.Error("help output missing --label flag")
	}
	if !strings.Contains(out, "issues carrying this label are dispatchable") {
		t.Error("help output missing label doc string")
	}
}

// TestParseFlags_AliasSetEnv: an alias flag resolves to the same env var as the long form.
func TestParseFlags_AliasSetEnv(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "")
	remaining, err := parseFlags([]string{"--issue", "42"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
	if got := os.Getenv("ISSUE_NUMBER"); got != "42" {
		t.Errorf("ISSUE_NUMBER = %q, want %q (alias must set same env var)", got, "42")
	}
}

// TestPrintHelp_ShowsAlias: aliased knobs show the alias next to the long form.
func TestPrintHelp_ShowsAlias(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	if !strings.Contains(out, "--issue-number, --issue") {
		t.Errorf("help output missing alias display; want --issue-number, --issue in:\n%s", out)
	}
}

// TestParseFlags_FileFlag_ReadsToken: --<name>-file reads the file and sets the env var.
func TestParseFlags_FileFlag_ReadsToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("secret-value"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	_, err := parseFlags([]string{"--gh-token-file", tokenFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "secret-value" {
		t.Errorf("GH_TOKEN = %q, want %q", got, "secret-value")
	}
}

// TestParseFlags_FileFlag_WinsOverEnv: file flag takes precedence over env var.
func TestParseFlags_FileFlag_WinsOverEnv(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("file-value"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "env-value")
	_, err := parseFlags([]string{"--gh-token-file", tokenFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "file-value" {
		t.Errorf("GH_TOKEN = %q, want %q (file flag must win over env)", got, "file-value")
	}
}

// TestParseFlags_FileFlag_MissingFile: --<name>-file with non-existent path returns an error.
func TestParseFlags_FileFlag_MissingFile(t *testing.T) {
	_, err := parseFlags([]string{"--gh-token-file", "/nonexistent/path/token.txt"})
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "/nonexistent/path/token.txt") {
		t.Errorf("error should mention the path, got: %v", err)
	}
}

// TestParseFlags_FileFlag_MissingValue: --<name>-file with no following arg returns an error.
func TestParseFlags_FileFlag_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--gh-token-file"})
	if err == nil {
		t.Fatal("expected error when file flag has no path argument, got nil")
	}
}

// TestPrintHelp_SecretKnobEnvOnly: secret knobs appear as env-only (no --flag prefix).
func TestPrintHelp_SecretKnobEnvOnly(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	if !strings.Contains(out, "GH_TOKEN") {
		t.Error("help output missing GH_TOKEN env-only listing")
	}
	if !strings.Contains(out, "env-only") {
		t.Error("help output missing 'env-only' marker for secret knobs")
	}
}

// TestParseFlags_FileFlag_StripsNewline: trailing newline is stripped from file content.
func TestParseFlags_FileFlag_StripsNewline(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("stripped-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	_, err := parseFlags([]string{"--gh-token-file", tokenFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("GH_TOKEN"); got != "stripped-value" {
		t.Errorf("GH_TOKEN = %q, want %q (trailing newline must be stripped)", got, "stripped-value")
	}
}

// TestPrintHelp_ShowsSecretFileFlags: help output lists --<name>-file flags for secret knobs.
func TestPrintHelp_ShowsSecretFileFlags(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	for _, want := range []string{"--gh-token-file", "--anthropic-api-key-file", "--claude-code-oauth-token-file"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %s", want)
		}
	}
}

// TestParseFlags_NoBuildPassthrough: --no-build is returned as a remaining arg,
// not treated as an unknown flag error.
func TestParseFlags_NoBuildPassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--no-build"})
	if err != nil {
		t.Fatalf("parseFlags with --no-build: unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[0] != "dispatch" || remaining[1] != "--no-build" {
		t.Errorf("remaining = %v, want [dispatch --no-build]", remaining)
	}
}

// TestParseFlags_NoBuildWithIssue: --no-build passes through with an issue number.
func TestParseFlags_NoBuildWithIssue(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--no-build", "42"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 3 || remaining[1] != "--no-build" || remaining[2] != "42" {
		t.Errorf("remaining = %v, want [dispatch --no-build 42]", remaining)
	}
}

// TestDispatchNoBuildArgs: dispatch --no-build arg extraction.
func TestDispatchNoBuildArgs(t *testing.T) {
	noBuild, rest := dispatchNoBuildArgs([]string{"--no-build", "123"})
	if !noBuild {
		t.Error("want noBuild=true, got false")
	}
	if len(rest) != 1 || rest[0] != "123" {
		t.Errorf("rest = %v, want [123]", rest)
	}
}

// TestDispatchNoBuildArgs_AbsentFlag: no --no-build flag leaves noBuild false.
func TestDispatchNoBuildArgs_AbsentFlag(t *testing.T) {
	noBuild, rest := dispatchNoBuildArgs([]string{"42"})
	if noBuild {
		t.Error("want noBuild=false, got true")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

// TestDispatchIssueArgs_Variadic: multiple numeric args all returned in order.
func TestDispatchIssueArgs_Variadic(t *testing.T) {
	got := dispatchIssueArgs([]string{"12", "15", "18"})
	want := []string{"12", "15", "18"}
	if len(got) != len(want) {
		t.Fatalf("dispatchIssueArgs: got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("pos %d: got %q, want %q", i, got[i], w)
		}
	}
}

// TestDispatchIssueArgs_Empty: empty args return nil.
func TestDispatchIssueArgs_Empty(t *testing.T) {
	got := dispatchIssueArgs([]string{})
	if len(got) != 0 {
		t.Errorf("dispatchIssueArgs([]): got %v, want empty", got)
	}
}

// TestDispatchIssueArgs_SkipsNonNumeric: non-numeric args are ignored.
func TestDispatchIssueArgs_SkipsNonNumeric(t *testing.T) {
	got := dispatchIssueArgs([]string{"--no-build", "42", "foo"})
	if len(got) != 1 || got[0] != "42" {
		t.Errorf("dispatchIssueArgs: got %v, want [42]", got)
	}
}

// TestDispatchYesArgs_YesFlag: --yes sets yes=true and is removed from remaining.
func TestDispatchYesArgs_YesFlag(t *testing.T) {
	yes, rest := dispatchYesArgs([]string{"--yes", "42"})
	if !yes {
		t.Error("want yes=true, got false")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

// TestDispatchYesArgs_ForceAlias: --force is an alias for --yes.
func TestDispatchYesArgs_ForceAlias(t *testing.T) {
	yes, _ := dispatchYesArgs([]string{"--force"})
	if !yes {
		t.Error("--force must set yes=true")
	}
}

// TestDispatchYesArgs_Absent: no --yes/--force flag leaves yes=false.
func TestDispatchYesArgs_Absent(t *testing.T) {
	yes, rest := dispatchYesArgs([]string{"42"})
	if yes {
		t.Error("want yes=false, got true")
	}
	if len(rest) != 1 || rest[0] != "42" {
		t.Errorf("rest = %v, want [42]", rest)
	}
}

// TestParseFlags_YesPassthrough: --yes passes through like --no-build.
func TestParseFlags_YesPassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--yes", "42"})
	if err != nil {
		t.Fatalf("parseFlags with --yes: unexpected error: %v", err)
	}
	if len(remaining) != 3 || remaining[1] != "--yes" || remaining[2] != "42" {
		t.Errorf("remaining = %v, want [dispatch --yes 42]", remaining)
	}
}

// TestParseFlags_ForcePassthrough: --force passes through like --no-build.
func TestParseFlags_ForcePassthrough(t *testing.T) {
	remaining, err := parseFlags([]string{"dispatch", "--force"})
	if err != nil {
		t.Fatalf("parseFlags with --force: unexpected error: %v", err)
	}
	if len(remaining) != 2 || remaining[1] != "--force" {
		t.Errorf("remaining = %v, want [dispatch --force]", remaining)
	}
}

// TestPrintHelp_ShowsNoBuildFlag: help output documents --no-build on dispatch.
func TestPrintHelp_ShowsNoBuildFlag(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	if !strings.Contains(buf.String(), "--no-build") {
		t.Error("help output missing --no-build flag")
	}
}
