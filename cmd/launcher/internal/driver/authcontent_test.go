package driver

import (
	"strings"
	"testing"
)

var testAuth = AuthContent{
	Env:            "PI_AUTH_CONTENT",
	OAuthProviders: []string{"github-copilot"},
	APIKeyProviderFor: map[string]string{
		"codex-sub":  "openai",
		"gemini-sub": "google",
		"orphan-sub": "nowhere",
	},
}

var testRules = []CredentialRule{
	{Env: []string{"ANTHROPIC_API_KEY"}, ModelPrefix: "anthropic/"},
	{Env: []string{"OPENAI_API_KEY"}, ModelPrefix: "openai/"},
	{Env: []string{"GEMINI_API_KEY"}, ModelPrefix: "google/"},
	{Env: []string{"PI_AUTH_CONTENT"}, ModelPrefix: "github-copilot/"},
}

func TestAuthContentCheck(t *testing.T) {
	const oauth = `{"type":"oauth","access":"SECRET-A","refresh":"SECRET-R","expires":1}`
	tests := []struct {
		name    string
		content string
		wantErr []string
	}{
		{"allowed oauth", `{"github-copilot":` + oauth + `}`, nil},
		{"api_key for any provider", `{"anthropic":{"type":"api_key","key":"SECRET-K"}}`, nil},
		{"empty object", `{}`, nil},
		{"anthropic oauth rejected", `{"anthropic":` + oauth + `}`,
			[]string{"PI_AUTH_CONTENT", "anthropic", "github-copilot", "set ANTHROPIC_API_KEY for anthropic"}},
		{"mapped oauth id names the api-key provider", `{"codex-sub":` + oauth + `}`,
			[]string{"codex-sub", "set OPENAI_API_KEY for openai instead"}},
		{"mapped oauth id, other provider", `{"gemini-sub":` + oauth + `}`,
			[]string{"set GEMINI_API_KEY for google instead"}},
		{"mapped to a provider with no rule", `{"orphan-sub":` + oauth + `}`,
			[]string{"orphan-sub", "spindrift carries no API key for the nowhere Provider"}},
		{"duplicate type key differing in case", `{"anthropic":{"type":"oauth","Type":"api_key"}}`,
			[]string{"anthropic", "OAuth entry"}},
		{"type key only in the wrong case", `{"anthropic":{"Type":"api_key"}}`,
			[]string{"anthropic", "\"type\""}},
		{"non-string type", `{"anthropic":{"type":1}}`, []string{"anthropic", "\"type\""}},
		{"unknown provider oauth", `{"zed":` + oauth + `}`,
			[]string{"zed", "spindrift carries no API key for the zed Provider"}},
		{"two providers", `{"zed":` + oauth + `,"anthropic":` + oauth + `}`,
			[]string{"anthropic", "zed"}},
		{"invalid json", `{not json SECRET-X`, []string{"PI_AUTH_CONTENT", "JSON object"}},
		{"non-object", `["SECRET-X"]`, []string{"PI_AUTH_CONTENT", "JSON object"}},
		{"entry not object", `{"anthropic":"SECRET-X"}`, []string{"anthropic", "\"type\""}},
		{"entry without type", `{"anthropic":{"key":"SECRET-X"}}`, []string{"anthropic", "\"type\""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := testAuth.Check(tt.content, testRules)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want error, got nil")
			}
			msg := err.Error()
			for _, w := range tt.wantErr {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q lacks %q", msg, w)
				}
			}
			if strings.Contains(msg, "SECRET") {
				t.Errorf("error echoes secret content: %q", msg)
			}
		})
	}
}

func TestAuthContentCheckSortsProviders(t *testing.T) {
	err := testAuth.Check(`{"zed":{"type":"oauth"},"anthropic":{"type":"oauth"}}`, testRules)
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if strings.Index(msg, "anthropic") > strings.Index(msg, "zed") {
		t.Errorf("providers not sorted: %q", msg)
	}
}

func TestAuthContentFor(t *testing.T) {
	a, ok := AuthContentFor("pi")
	if !ok || a.Env != "PI_AUTH_CONTENT" || len(a.OAuthProviders) != 1 || a.OAuthProviders[0] != "github-copilot" {
		t.Errorf("pi AuthContentFor = %+v, %v", a, ok)
	}
	for _, name := range []string{"claude", "opencode", "", "nope"} {
		if _, ok := AuthContentFor(name); ok {
			t.Errorf("AuthContentFor(%q) ok, want none", name)
		}
	}
}

func TestPiAPIKeyProvidersResolveToNamedVariable(t *testing.T) {
	a, ok := AuthContentFor("pi")
	if !ok {
		t.Fatal("pi has no AuthContent")
	}
	if len(a.APIKeyProviderFor) == 0 {
		t.Fatal("pi maps no OAuth id to an API-key Provider")
	}
	rules, rulesOK := CredentialRules("pi")
	if !rulesOK {
		t.Fatal("pi has no credential rules")
	}
	for id, provider := range a.APIKeyProviderFor {
		t.Run(id, func(t *testing.T) {
			err := a.Check(`{"`+id+`":{"type":"oauth"}}`, rules)
			if err == nil {
				t.Fatalf("OAuth entry for %s accepted", id)
			}
			want := "for " + provider + " instead"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q lacks a \"set <ENV> %s\" pointer", err, want)
			}
		})
	}
}
