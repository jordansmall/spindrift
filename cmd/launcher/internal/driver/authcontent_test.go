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
	copilot := []string{"github-copilot/gpt-5"}
	const noEntry = "holds no OAuth entry"
	tests := []struct {
		name       string
		content    string
		models     []string
		wantErr    []string
		wantAbsent []string
	}{
		{name: "allowed oauth", content: `{"github-copilot":` + oauth + `}`},
		{name: "api_key for any provider", content: `{"anthropic":{"type":"api_key","key":"SECRET-K"}}`},
		{name: "empty object", content: `{}`},
		{name: "anthropic oauth rejected", content: `{"anthropic":` + oauth + `}`,
			wantErr: []string{"PI_AUTH_CONTENT", "anthropic", "github-copilot", "set ANTHROPIC_API_KEY for anthropic"}},
		{name: "mapped oauth id names the api-key provider", content: `{"codex-sub":` + oauth + `}`,
			wantErr: []string{"codex-sub", "set OPENAI_API_KEY for openai instead"}},
		{name: "mapped oauth id, other provider", content: `{"gemini-sub":` + oauth + `}`,
			wantErr: []string{"set GEMINI_API_KEY for google instead"}},
		{name: "mapped to a provider with no rule", content: `{"orphan-sub":` + oauth + `}`,
			wantErr: []string{"orphan-sub", "spindrift carries no API key for the nowhere Provider"}},
		{name: "duplicate type key differing in case", content: `{"anthropic":{"type":"oauth","Type":"api_key"}}`,
			wantErr: []string{"anthropic", "OAuth entry"}},
		{name: "type key only in the wrong case", content: `{"anthropic":{"Type":"api_key"}}`,
			wantErr: []string{"anthropic", "\"type\""}},
		{name: "non-string type", content: `{"anthropic":{"type":1}}`, wantErr: []string{"anthropic", "\"type\""}},
		{name: "unknown provider oauth", content: `{"zed":` + oauth + `}`,
			wantErr: []string{"zed", "spindrift carries no API key for the zed Provider"}},
		{name: "two providers", content: `{"zed":` + oauth + `,"anthropic":` + oauth + `}`,
			wantErr: []string{"anthropic", "zed"}},
		{name: "invalid json", content: `{not json SECRET-X`, wantErr: []string{"PI_AUTH_CONTENT", "JSON object"}},
		{name: "non-object", content: `["SECRET-X"]`, wantErr: []string{"PI_AUTH_CONTENT", "JSON object"}},
		{name: "entry not object", content: `{"anthropic":"SECRET-X"}`, wantErr: []string{"anthropic", "\"type\""}},
		{name: "entry without type", content: `{"anthropic":{"key":"SECRET-X"}}`, wantErr: []string{"anthropic", "\"type\""}},

		{name: "empty store, copilot model", content: `{}`, models: copilot,
			wantErr: []string{"PI_AUTH_CONTENT", "github-copilot", noEntry}},
		{name: "api_key-only store, copilot model", content: `{"github-copilot":{"type":"api_key","key":"SECRET-K"}}`, models: copilot,
			wantErr: []string{"PI_AUTH_CONTENT", "github-copilot", noEntry}},
		{name: "other provider only, copilot model", content: `{"anthropic":{"type":"api_key","key":"SECRET-K"}}`, models: copilot,
			wantErr: []string{"PI_AUTH_CONTENT", "github-copilot", noEntry}},
		{name: "copilot model among tiers", content: `{}`, models: []string{"openai/gpt-5", "", "github-copilot/x"},
			wantErr: []string{"github-copilot", noEntry}},
		{name: "malformed copilot entry reported once", content: `{"github-copilot":"SECRET-X"}`, models: copilot,
			wantErr: []string{"github-copilot", "\"type\""}, wantAbsent: []string{noEntry}},
		{name: "oauth entry, copilot model", content: `{"github-copilot":` + oauth + `}`, models: copilot},
		{name: "empty store, other model", content: `{}`, models: []string{"openai/gpt-5"}},
		{name: "api_key-only store, other model", content: `{"github-copilot":{"type":"api_key","key":"SECRET-K"}}`, models: []string{"openai/gpt-5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := testAuth.Check(tt.content, testRules, tt.models)
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
			for _, w := range tt.wantAbsent {
				if strings.Contains(msg, w) {
					t.Errorf("error %q contains %q", msg, w)
				}
			}
			if strings.Contains(msg, "SECRET") {
				t.Errorf("error echoes secret content: %q", msg)
			}
		})
	}
}

func TestAuthContentCheckSortsProviders(t *testing.T) {
	err := testAuth.Check(`{"zed":{"type":"oauth"},"anthropic":{"type":"oauth"}}`, testRules, nil)
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
			err := a.Check(`{"`+id+`":{"type":"oauth"}}`, rules, nil)
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
