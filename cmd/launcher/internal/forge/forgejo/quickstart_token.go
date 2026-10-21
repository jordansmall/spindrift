package forgejo

import (
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/rest"
)

// acquireToken validates the pasted token with a live Probe rather than a
// prefix audit: Forgejo PATs have no sniffable prefix the way GitHub's do.
// There is no retry loop, so every failure path here aborts the run and tells
// the operator to rerun the wizard.
func acquireToken(s backendkit.Shared, l backendkit.Lookup, p backendkit.TokenPrompt) (string, error) {
	k, err := ReadKnobs(l)
	if err != nil {
		return "", err
	}
	baseURL, repoSlug := k.ForgejoBaseURL, s.RepoSlug
	token := p.PromptMasked("Forgejo token (paste a Forgejo personal access token)")
	if token == "" {
		return "", fmt.Errorf("no Forgejo token provided — rerun `%s` and paste a token when prompted", p.RerunCmd)
	}
	if err := p.Probe(token); err != nil {
		if errors.Is(err, forge.ErrAuthFailure) {
			return "", fmt.Errorf("Forgejo token rejected by the API at %s — the instance is derived from the git remote, so if this is the wrong instance, `git remote set-url` to point at the right one before you rerun `%s` and paste a valid token: %w", baseURL, p.RerunCmd, err)
		}
		if !errors.Is(err, forge.ErrNotFound) {
			var statusErr rest.StatusError
			if errors.As(err, &statusErr) {
				cause := fmt.Sprintf("the instance responded with HTTP status %d", statusErr.Status)
				return "", connectivityError(baseURL, repoSlug, p.RerunCmd, cause, "check the Forgejo instance's health/logs", err)
			}
			var decodeErr rest.DecodeError
			if errors.As(err, &decodeErr) {
				return "", connectivityError(baseURL, repoSlug, p.RerunCmd, "the instance's response could not be parsed", "check the Forgejo instance's version/API compatibility", err)
			}
		}
		return "", connectivityError(baseURL, repoSlug, p.RerunCmd, "either the instance is unreachable or the repo slug is wrong", "check both", err)
	}
	fmt.Fprintln(p.Out, "ok: Forgejo token validated via live API ping")
	return token, nil
}

// connectivityError builds the shared error skeleton for acquireToken's three
// post-Probe failure branches, which differ only in the cause and remedy
// clauses.
func connectivityError(baseURL, repoSlug, rerunCmd, cause, remedy string, err error) error {
	return fmt.Errorf("Forgejo connectivity check to %s (repo slug %q) failed — %s; %s, then rerun `%s`: %w", baseURL, repoSlug, cause, remedy, rerunCmd, err)
}
