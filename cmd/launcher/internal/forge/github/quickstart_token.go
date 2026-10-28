package github

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
)

// acquireToken is the wizard's github token setup: acquire, then audit for
// least privilege.
func acquireToken(_ backendkit.Shared, _ backendkit.Lookup, p backendkit.TokenPrompt) (string, error) {
	token, err := acquireGHToken(p)
	if err != nil {
		return "", err
	}
	if err := auditGHToken(token, p); err != nil {
		return "", err
	}
	return token, nil
}

// acquireGHToken reuses an ambient GH_TOKEN without prompting, otherwise it
// guides the operator toward a fine-grained single-repo PAT. The `gh auth
// token` fallback warns, because that token is typically repo-wide.
func acquireGHToken(p backendkit.TokenPrompt) (string, error) {
	tokenEnvVar := backend.GitHub.TokenEnvVar
	if token := p.Getenv(tokenEnvVar); token != "" {
		return token, nil
	}
	fmt.Fprintf(p.Out, "No ambient %s found.\n", tokenEnvVar)
	fmt.Fprint(p.Out, "Create a fine-grained personal access token scoped to only this repo, with:\n"+requiredGHPermissions)
	token := p.PromptMasked("GitHub token (paste a fine-grained PAT, or leave blank to fall back to `gh auth token` — broader scope warning)")
	if token != "" {
		return token, nil
	}
	fmt.Fprintln(p.Out, "WARNING: gh auth token typically returns a repo-wide OAuth token, broader than the single-repo scope quickstart recommends.")
	token, err := p.AmbientToken()
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("gh auth token returned no token — run `gh auth login` and retry")
	}
	return token, nil
}

// requiredGHPermissions are the permissions a token must carry on the single
// target repo. GitHub exposes no endpoint to introspect a fine-grained PAT,
// so quickstart prints these for the operator to check (ADR 0027).
const requiredGHPermissions = `  Issues: Read and write
  Contents: Read and write
  Pull requests: Read and write
  Metadata: Read
`

// auditGHToken checks a GitHub token for least privilege, branching on its
// prefix. A fine-grained PAT cannot be introspected, so it passes ungated.
func auditGHToken(token string, p backendkit.TokenPrompt) error {
	w := p.Out
	if strings.HasPrefix(token, "github_pat_") {
		fmt.Fprintln(w, "fine-grained PAT detected — GitHub exposes no endpoint to introspect it.")
		fmt.Fprint(w, "It should carry only these permissions, on the single target repo:\n"+requiredGHPermissions)
		return nil
	}
	if strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "gho_") {
		scopes, err := p.TokenScopes(token)
		if err != nil {
			return fmt.Errorf("read token scopes: %w", err)
		}
		fmt.Fprintf(w, "token scopes: %s\n", strings.Join(scopes, ", "))
		excess := excessGHScopes(scopes)
		if len(excess) == 0 {
			fmt.Fprintln(w, "ok: scopes are least-privilege")
			return nil
		}
		fmt.Fprintf(w, "WARNING: token grants broader-than-needed scope(s): %s\n", strings.Join(excess, ", "))
		fmt.Fprintln(w, "quickstart only needs single-repo Issues/Contents/Pull requests RW + Metadata R — mint a fine-grained PAT instead for least privilege.")
		answer := p.Prompt("Type ACCEPT to continue with this over-broad token, anything else aborts")
		if answer != "ACCEPT" {
			return fmt.Errorf("aborted: GitHub token grants broader access than needed (%s) — mint a fine-grained single-repo PAT instead", strings.Join(excess, ", "))
		}
		return nil
	}
	// Any other prefix (ghs_ app-installation tokens, for one) is neither a
	// fine-grained PAT nor a classic token, so there is nothing to audit.
	return nil
}

// leastPrivilegeGHScope is the only classic-token scope the audit accepts.
const leastPrivilegeGHScope = "public_repo"

// excessGHScopes returns every scope but leastPrivilegeGHScope, in input order:
// an allow-list flags a scope GitHub adds later rather than silently missing it.
func excessGHScopes(scopes []string) []string {
	var excess []string
	for _, s := range scopes {
		if s != leastPrivilegeGHScope {
			excess = append(excess, s)
		}
	}
	return excess
}
