package github_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge/github"
)

// noIntrospection fails the test when the wizard audits a token it should not.
func noIntrospection(t *testing.T) backendkit.TokenIntrospector {
	return func(string, string) (backendkit.TokenIntrospection, error) {
		t.Helper()
		t.Fatal("must not introspect the token")
		return backendkit.TokenIntrospection{}, nil
	}
}

func acquire(t *testing.T, p backendkit.TokenPrompt) (string, error) {
	t.Helper()
	return acquireWith(t, noIntrospection(t), p)
}

func acquireWith(t *testing.T, introspect backendkit.TokenIntrospector, p backendkit.TokenPrompt) (string, error) {
	t.Helper()
	return github.AcquireTokenWith(introspect)(backendkit.Shared{RepoSlug: "o/r"}, nil, p)
}

func scopesOf(scopes []string, err error) backendkit.TokenIntrospector {
	return func(string, string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{Introspectable: true, Scopes: scopes}, err
	}
}

func TestAcquireToken_AmbientTokenSkipsPromptAndPrintsAuditNote(t *testing.T) {
	var out bytes.Buffer
	token, err := acquire(t, backendkit.TokenPrompt{
		Out:          &out,
		Getenv:       func(k string) string { return map[string]string{"GH_TOKEN": "github_pat_x"}[k] },
		PromptMasked: func(string) string { t.Fatal("must not prompt"); return "" },
	})
	if err != nil || token != "github_pat_x" {
		t.Fatalf("got %q, %v", token, err)
	}
	if !strings.Contains(out.String(), "fine-grained PAT detected") {
		t.Errorf("missing fine-grained note: %q", out.String())
	}
}

func TestAcquireToken_PastedToken(t *testing.T) {
	var out bytes.Buffer
	token, err := acquire(t, backendkit.TokenPrompt{
		Out:          &out,
		Getenv:       func(string) string { return "" },
		PromptMasked: func(string) string { return "github_pat_pasted" },
	})
	if err != nil || token != "github_pat_pasted" {
		t.Fatalf("got %q, %v", token, err)
	}
	for _, want := range []string{"No ambient GH_TOKEN found.", "Issues: Read and write"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q: %q", want, out.String())
		}
	}
}

func TestAcquireToken_BlankFallsBackToAmbientGhToken(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		err     error
		wantErr string
	}{
		{"token", "gho_x", nil, ""},
		{"empty", "", nil, "gh auth token returned no token"},
		{"error", "", errors.New("boom"), "boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			// A gho_ token is audited; keep the scopes narrow.
			token, err := acquireWith(t, scopesOf([]string{"public_repo"}, nil), backendkit.TokenPrompt{
				Out:          &out,
				Getenv:       func(string) string { return "" },
				PromptMasked: func(string) string { return "" },
				AmbientToken: func() (string, error) { return c.token, c.err },
			})
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || token != c.token {
				t.Fatalf("got %q, %v", token, err)
			}
			if !strings.Contains(out.String(), "WARNING: gh auth token typically returns") {
				t.Errorf("missing broader-scope warning: %q", out.String())
			}
		})
	}
}

func TestAcquireToken_ClassicTokenAudit(t *testing.T) {
	cases := []struct {
		name       string
		scopes     []string
		scopesErr  error
		answer     string
		wantErr    string
		wantOutput string
	}{
		{"narrow", []string{"public_repo"}, nil, "", "", "ok: scopes are least-privilege"},
		{"broad accepted", []string{"repo", "admin:org"}, nil, "ACCEPT", "", "broader-than-needed scope(s): repo, admin:org"},
		{"broad declined", []string{"read:org"}, nil, "no", "aborted: GitHub token grants broader access than needed (read:org)", ""},
		{"extra scopes accepted", []string{"public_repo", "gist", "user", "write:packages"}, nil, "ACCEPT", "", "broader-than-needed scope(s): gist, user, write:packages"},
		{"extra scope declined", []string{"public_repo", "delete_repo"}, nil, "no", "aborted: GitHub token grants broader access than needed (delete_repo)", "WARNING: token grants broader-than-needed scope(s): delete_repo"},
		{"scopes error", nil, errors.New("net down"), "", "read token scopes: net down", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := acquireWith(t, scopesOf(c.scopes, c.scopesErr), backendkit.TokenPrompt{
				Out:          &out,
				Getenv:       func(k string) string { return map[string]string{"GH_TOKEN": "ghp_x"}[k] },
				Prompt:       func(string) string { return c.answer },
				PromptMasked: func(string) string { t.Fatal("must not prompt"); return "" },
			})
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if !strings.Contains(out.String(), c.wantOutput) {
				t.Errorf("output missing %q: %q", c.wantOutput, out.String())
			}
		})
	}
}

func TestAcquireToken_OtherPrefixIsNotAudited(t *testing.T) {
	token, err := acquire(t, backendkit.TokenPrompt{
		Out: new(bytes.Buffer),

		Getenv: func(k string) string { return map[string]string{"GH_TOKEN": "ghs_x"}[k] },
	})
	if err != nil || token != "ghs_x" {
		t.Fatalf("got %q, %v", token, err)
	}
}

func TestStrategyHasNoKnobsFromRemote(t *testing.T) {
	if github.Strategy.KnobsFromRemote != nil {
		t.Error("github implies no knobs from a remote")
	}
}

// The ambient lookup follows the Descriptor's TokenEnvVar, not a GH_TOKEN
// literal.
func TestAcquireToken_AmbientLookupFollowsDescriptorTokenEnvVar(t *testing.T) {
	orig := backend.GitHub.TokenEnvVar
	backend.GitHub.TokenEnvVar = "CUSTOM_GH_TOKEN"
	t.Cleanup(func() { backend.GitHub.TokenEnvVar = orig })

	token, err := acquire(t, backendkit.TokenPrompt{
		Out: new(bytes.Buffer),
		Getenv: func(k string) string {
			return map[string]string{"GH_TOKEN": "ghp_wrong", "CUSTOM_GH_TOKEN": "ghs_right"}[k]
		},
	})
	if err != nil || token != "ghs_right" {
		t.Fatalf("got %q, %v", token, err)
	}
}

// A host with no github Connector cannot read the scopes, which is not a reason
// to refuse the token: the operator is told and the wizard carries on.
func TestAcquireToken_UnsupportedIntrospectionWarnsAndContinues(t *testing.T) {
	var out bytes.Buffer
	unsupported := func(string, string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{}, fmt.Errorf("no github connector is configured: %w", backendkit.ErrUnsupported)
	}
	token, err := acquireWith(t, unsupported, backendkit.TokenPrompt{
		Out:    &out,
		Getenv: func(k string) string { return map[string]string{"GH_TOKEN": "ghp_x"}[k] },
	})
	if err != nil || token != "ghp_x" {
		t.Fatalf("got %q, %v", token, err)
	}
	if !strings.Contains(out.String(), "cannot audit the token's scopes") {
		t.Errorf("output missing the unaudited notice: %q", out.String())
	}
}

// The wizard audits the pasted token itself, scoped to the target repo.
func TestAcquireToken_AuditsThePastedTokenForTheTargetRepo(t *testing.T) {
	var gotToken, gotRepo string
	introspect := func(token, repo string) (backendkit.TokenIntrospection, error) {
		gotToken, gotRepo = token, repo
		return backendkit.TokenIntrospection{Introspectable: true, Scopes: []string{"public_repo"}}, nil
	}
	if _, err := acquireWith(t, introspect, backendkit.TokenPrompt{
		Out:          new(bytes.Buffer),
		Getenv:       func(string) string { return "" },
		PromptMasked: func(string) string { return "gho_pasted" },
	}); err != nil {
		t.Fatal(err)
	}
	if gotToken != "gho_pasted" || gotRepo != "o/r" {
		t.Errorf("introspected (%q, %q), want (gho_pasted, o/r)", gotToken, gotRepo)
	}
}
