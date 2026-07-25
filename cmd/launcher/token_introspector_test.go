package main

import (
	"errors"
	"testing"
)

// TestNewGHTokenIntrospector table-drives the prefix-dispatch and
// write-scope-matching logic newGHTokenIntrospector builds (issue #1950):
// which token shapes are introspectable, which signal each shape uses, and
// which scopes/permissions count as write-capable. Exercised directly
// against fake oauthScopes/repoPush functions rather than a live gh call, so
// this is the one place the classification decision itself is unit-tested
// (ghTokenIntrospector, the production wiring, is exercised end-to-end by
// internal/forge/github's own TokenOAuthScopes/TokenRepoPushPermission
// tests).
func TestNewGHTokenIntrospector(t *testing.T) {
	errBoom := errors.New("boom")

	cases := []struct {
		name           string
		token          string
		scopes         []string
		scopesErr      error
		push           bool
		pushErr        error
		wantIntro      bool
		wantWriteCap   bool
		wantErr        bool
		wantScopesCall bool
		wantPushCall   bool
	}{
		{
			name:      "fine-grained PAT is not introspectable",
			token:     "github_pat_abc123",
			wantIntro: false,
		},
		{
			name:      "unknown prefix is not introspectable",
			token:     "some-opaque-token",
			wantIntro: false,
		},
		{
			name:           "classic PAT with repo scope is write-capable",
			token:          "ghp_abc123",
			scopes:         []string{"read:org", "repo"},
			wantIntro:      true,
			wantWriteCap:   true,
			wantScopesCall: true,
		},
		{
			name:           "classic PAT with public_repo scope is write-capable",
			token:          "ghp_abc123",
			scopes:         []string{"public_repo"},
			wantIntro:      true,
			wantWriteCap:   true,
			wantScopesCall: true,
		},
		{
			name:           "OAuth token with only read scopes is not write-capable",
			token:          "gho_abc123",
			scopes:         []string{"read:org", "notifications"},
			wantIntro:      true,
			wantWriteCap:   false,
			wantScopesCall: true,
		},
		{
			name:           "classic PAT scope lookup error propagates",
			token:          "ghp_abc123",
			scopesErr:      errBoom,
			wantErr:        true,
			wantScopesCall: true,
		},
		{
			name:         "App installation token with push is write-capable",
			token:        "ghs_abc123",
			push:         true,
			wantIntro:    true,
			wantWriteCap: true,
			wantPushCall: true,
		},
		{
			name:         "App installation token without push is not write-capable",
			token:        "ghs_abc123",
			push:         false,
			wantIntro:    true,
			wantWriteCap: false,
			wantPushCall: true,
		},
		{
			name:         "App installation token push lookup error propagates",
			token:        "ghs_abc123",
			pushErr:      errBoom,
			wantErr:      true,
			wantPushCall: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scopesCalled, pushCalled := false, false
			introspect := newGHTokenIntrospector(
				func(token string) ([]string, error) {
					scopesCalled = true
					return tc.scopes, tc.scopesErr
				},
				func(token, repoSlug string) (bool, error) {
					pushCalled = true
					return tc.push, tc.pushErr
				},
			)

			result, err := introspect(tc.token, "owner/repo")

			if scopesCalled != tc.wantScopesCall {
				t.Errorf("oauthScopes called = %v, want %v", scopesCalled, tc.wantScopesCall)
			}
			if pushCalled != tc.wantPushCall {
				t.Errorf("repoPush called = %v, want %v", pushCalled, tc.wantPushCall)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("introspect() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("introspect() error = %v, want nil", err)
			}
			if result.Introspectable != tc.wantIntro {
				t.Errorf("Introspectable = %v, want %v", result.Introspectable, tc.wantIntro)
			}
			if tc.wantIntro && result.WriteCapable != tc.wantWriteCap {
				t.Errorf("WriteCapable = %v, want %v", result.WriteCapable, tc.wantWriteCap)
			}
		})
	}
}
