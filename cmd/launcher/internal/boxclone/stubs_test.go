package boxclone_test

import (
	"errors"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
)

// Library tests never import the driver bundle (ADR 0066 decision 8), so these
// stand in for the bundled backends under their real names. Each returns a
// fixed URL (git echoes its knob) and keeps only the required-knob failure a
// boxclone test relies on; the real URL logic is tested in each backend's own
// package. forgejo's URL carries its token so the banner-redaction test still
// has something to redact.
func init() {
	stub := func(name string, clone func(backendkit.Shared, backendkit.Lookup, string) (string, error)) {
		backendkit.Register(backendkit.Strategy{Descriptor: backend.Descriptor{Name: name}, CloneURL: clone})
	}
	stub("github", func(s backendkit.Shared, _ backendkit.Lookup, _ string) (string, error) {
		return "https://github.com/" + s.RepoSlug + ".git", nil
	})
	stub("git", func(_ backendkit.Shared, l backendkit.Lookup, _ string) (string, error) {
		u, _ := l("CODE_FORGE_REMOTE_URL")
		if u == "" {
			return "", errors.New("CODE_FORGE_REMOTE_URL is required when CODE_FORGE=git")
		}
		return u, nil
	})
	stub("forgejo", func(s backendkit.Shared, l backendkit.Lookup, _ string) (string, error) {
		tok, _ := l("FORGEJO_TOKEN")
		if tok == "" {
			return "", errors.New("FORGEJO_TOKEN is required when CODE_FORGE=forgejo")
		}
		return "https://" + tok + "@forgejo.invalid/" + s.RepoSlug + ".git", nil
	})
	stub("local", func(_ backendkit.Shared, _ backendkit.Lookup, mount string) (string, error) {
		return mount, nil
	})
	stub("jira", nil)
}
