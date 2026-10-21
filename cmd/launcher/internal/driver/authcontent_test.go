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

func store(v string) func(string) string {
	return func(env string) string {
		if env == "PI_AUTH_CONTENT" {
			return v
		}
		return ""
	}
}

func TestCheckClosedCredentials(t *testing.T) {
	tests := []struct {
		name   string
		driver string
		models []string
		store  string
		want   []string // substrings of the error; nil means no error
	}{
		{"pi unlisted", "pi", []string{"cerebras/x"}, "", []string{"no credential covers the cerebras Provider under the pi Driver", "PI_AUTH_CONTENT", "anthropic/"}},
		{"pi listed", "pi", []string{"openai/gpt-5"}, "", nil},
		{"pi store entry", "pi", []string{"cerebras/x"}, `{"cerebras":{"type":"api_key","key":"k"}}`, nil},
		{"pi other store entry", "pi", []string{"cerebras/x"}, `{"cohere":{"type":"api_key","key":"k"}}`, []string{"cerebras"}},
		{"pi non-api_key store entry", "pi", []string{"cerebras/x"}, `{"cerebras":{"type":"apikey","key":"k"}}`, []string{"cerebras"}},
		{"pi oauth store entry", "pi", []string{"cerebras/x"}, `{"cerebras":{"type":"oauth"}}`, []string{"cerebras"}},
		{"pi empty Provider", "pi", []string{"/x"}, "", []string{`model "/x" names no Provider before the "/"`, "write it as <provider>/<model>", "anthropic/"}},
		{"pi bare and empty", "pi", []string{"", "bare-model"}, "", nil},
		{"opencode open-ended", "opencode", []string{"cerebras/x"}, "", nil},
		{"claude open-ended", "claude", []string{"cerebras/x"}, "", nil},
		{"unknown driver", "nope", []string{"cerebras/x"}, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckClosedCredentials(tt.driver, tt.models, store(tt.store))
			if tt.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q missing %q", err, w)
				}
			}
		})
	}
}

func TestCheckClosedCredentialsReportsEachProviderOnce(t *testing.T) {
	err := CheckClosedCredentials("pi", []string{"cerebras/x", "cohere/y", "cerebras/z"}, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := strings.Count(err.Error(), "covers the cerebras Provider"); n != 1 {
		t.Errorf("cerebras reported %d times: %v", n, err)
	}
	if !strings.Contains(err.Error(), "covers the cohere Provider") {
		t.Errorf("cohere not reported: %v", err)
	}
}

func TestCheckClosedCredentialsLeavesUnparseableStoreToCheck(t *testing.T) {
	for _, content := range []string{"not json", "null", `["x"]`} {
		t.Run(content, func(t *testing.T) {
			if err := CheckClosedCredentials("pi", []string{"cerebras/x"}, store(content)); err != nil {
				t.Errorf("unparseable store reported here as well as by AuthContent.Check: %v", err)
			}
		})
	}
}
