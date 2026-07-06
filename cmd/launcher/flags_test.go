package main

import (
	"bytes"
	"os"
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
