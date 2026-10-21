package driver

import (
	"reflect"
	"slices"
	"strings"
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

// TestEffortOnModelLevels pins the Go mirror of the Nix effortOnModelLevels
// fact: only a Driver that reads effort from a model id's `:<level>` suffix
// carries levels (issue #5625).
func TestEffortOnModelLevels(t *testing.T) {
	want := []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	if got := EffortOnModelLevels("pi"); !slices.Equal(got, want) {
		t.Errorf("EffortOnModelLevels(pi) = %v, want %v", got, want)
	}
	for _, name := range []string{"claude", "opencode", "", "nope"} {
		if got := EffortOnModelLevels(name); got != nil {
			t.Errorf("EffortOnModelLevels(%q) = %v, want nil", name, got)
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

func TestCredentialRulesPiOnePerProvider(t *testing.T) {
	rules, ok := CredentialRules("pi")
	if !ok {
		t.Fatal("CredentialRules(pi) not found")
	}
	want := map[string]string{
		"anthropic":      "ANTHROPIC_API_KEY",
		"openai":         "OPENAI_API_KEY",
		"google":         "GEMINI_API_KEY",
		"openrouter":     "OPENROUTER_API_KEY",
		"groq":           "GROQ_API_KEY",
		"xai":            "XAI_API_KEY",
		"mistral":        "MISTRAL_API_KEY",
		"deepseek":       "DEEPSEEK_API_KEY",
		"github-copilot": "PI_AUTH_CONTENT",
	}
	if len(rules) != len(want) {
		t.Fatalf("CredentialRules(pi) has %d rules, want %d", len(rules), len(want))
	}
	for provider, env := range want {
		model := provider + "/some-model"
		var hit []CredentialRule
		for _, r := range rules {
			if r.AppliesTo(model) {
				hit = append(hit, r)
			}
		}
		if len(hit) != 1 || !slices.Equal(hit[0].Env, []string{env}) {
			t.Errorf("%s: rules applying = %v, want exactly one over %s", model, hit, env)
			continue
		}
		if len(hit[0].SetupCommand) != 0 {
			t.Errorf("%s: SetupCommand = %v, want none", provider, hit[0].SetupCommand)
		}
		if !strings.Contains(hit[0].Remedy, env) {
			t.Errorf("%s: remedy %q does not name %s", provider, hit[0].Remedy, env)
		}
	}
	for _, r := range rules {
		if r.AppliesTo() || r.AppliesTo("bare-model", "cohere/command") {
			t.Errorf("rule over %v must not apply without its Provider's model", r.Env)
		}
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
