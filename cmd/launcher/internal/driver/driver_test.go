package driver

import (
	"reflect"
	"slices"
	"testing"
)

// TestNewUnknownDriverErrors verifies that an unrecognized DRIVER value is
// rejected rather than silently falling back.
func TestNewUnknownDriverErrors(t *testing.T) {
	if _, err := New("bogus"); err == nil {
		t.Fatal("New(\"bogus\"): expected error, got nil")
	}
}

// TestProviderNamespacedModels pins the Go mirror of the Nix
// providerNamespacedModels fact, including names New would reject.
func TestProviderNamespacedModels(t *testing.T) {
	for name, want := range map[string]bool{
		"opencode": true,
		"pi":       true,
		"claude":   false,
		"":         false,
		"nope":     false,
	} {
		if got := ProviderNamespacedModels(name); got != want {
			t.Errorf("ProviderNamespacedModels(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCredentialRulesClaudeAlwaysApplies(t *testing.T) {
	rules, ok := CredentialRules("claude")
	if !ok || len(rules) != 1 {
		t.Fatalf("CredentialRules(claude) = %v, %v; want one rule", rules, ok)
	}
	r := rules[0]
	if want := []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}; !slices.Equal(r.Env, want) {
		t.Errorf("Env = %v, want %v", r.Env, want)
	}
	if want := []string{"claude", "setup-token"}; !slices.Equal(r.SetupCommand, want) {
		t.Errorf("SetupCommand = %v, want %v", r.SetupCommand, want)
	}
	if !r.AppliesTo() || !r.AppliesTo("claude-sonnet-4-5") {
		t.Error("claude rule must apply regardless of models")
	}
}

func TestCredentialRulesOpencodeAppliesOnlyToCopilot(t *testing.T) {
	rules, ok := CredentialRules("opencode")
	if !ok || len(rules) != 1 {
		t.Fatalf("CredentialRules(opencode) = %v, %v; want one rule", rules, ok)
	}
	r := rules[0]
	if want := []string{"OPENCODE_AUTH_CONTENT"}; !slices.Equal(r.Env, want) {
		t.Errorf("Env = %v, want %v", r.Env, want)
	}
	if r.AppliesTo() || r.AppliesTo("anthropic/claude", "openai/gpt") {
		t.Error("opencode rule must not apply without a github-copilot model")
	}
	if !r.AppliesTo("anthropic/claude", "github-copilot/gpt-5") {
		t.Error("opencode rule must apply when any model is github-copilot")
	}
}

func TestCredentialRuleSatisfied(t *testing.T) {
	r := CredentialRule{Env: []string{"A", "B"}}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"none set", nil, false},
		{"empty value", map[string]string{"A": ""}, false},
		{"first set", map[string]string{"A": "x"}, true},
		{"later set", map[string]string{"B": "x"}, true},
		{"unrelated set", map[string]string{"C": "x"}, false},
	} {
		if got := r.Satisfied(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("%s: Satisfied = %v, want %v", tc.name, got, tc.want)
		}
	}
	if (CredentialRule{}).Satisfied(func(string) string { return "x" }) {
		t.Error("a rule with no env vars must not be satisfied")
	}
}

func TestCredentialRulesEmptyNameIsClaude(t *testing.T) {
	got, ok := CredentialRules("")
	want, _ := CredentialRules("claude")
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("CredentialRules(\"\") = %v, %v; want claude's %v", got, ok, want)
	}
}

func TestCredentialRulesUnknownDriver(t *testing.T) {
	if rules, ok := CredentialRules("nope"); ok || rules != nil {
		t.Errorf("CredentialRules(nope) = %v, %v; want nil, false", rules, ok)
	}
}
