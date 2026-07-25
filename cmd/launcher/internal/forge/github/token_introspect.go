package github

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// TokenOAuthScopes reads the X-OAuth-Scopes response header `gh api -i`
// returns for a classic/OAuth token (issue #1950's read-only token gate;
// mirrors quickstart's own hostEnvironment.TokenScopes, ADR 0027 -- kept
// separate rather than shared since quickstart is its own `package main`
// that predates any forge client and cannot import this package). token is
// passed via GH_TOKEN so the probe reflects the token under audit, not
// whatever credential the caller's own environment already carries.
func TokenOAuthScopes(token string) ([]string, error) {
	cmd := exec.Command("gh", "api", "-i", "user")
	cmd.Env = append(os.Environ(), "GH_TOKEN="+token)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api -i user: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "x-oauth-scopes") {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, nil
		}
		var scopes []string
		for _, s := range strings.Split(value, ",") {
			scopes = append(scopes, strings.TrimSpace(s))
		}
		return scopes, nil
	}
	return nil, nil
}

// TokenRepoPushPermission reports whether token (typically a GitHub App
// installation token) carries push access to repoSlug, per the repo
// endpoint's `permissions.push` field -- an App identity has no ambient user
// role to blur the result the way a fine-grained PAT's underlying account
// would, so this field accurately reflects the installation's own grant.
func TokenRepoPushPermission(token, repoSlug string) (bool, error) {
	cmd := exec.Command("gh", "api", "repos/"+repoSlug)
	cmd.Env = append(os.Environ(), "GH_TOKEN="+token)
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("gh api repos/%s: %w", repoSlug, err)
	}
	var resp struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return false, fmt.Errorf("parse repo permissions for %s: %w", repoSlug, err)
	}
	return resp.Permissions.Push, nil
}
