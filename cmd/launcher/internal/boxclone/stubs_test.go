package boxclone_test

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
)

// Library tests never import the driver bundle (ADR 0066 decision 8), so these
// stand in for the bundled backends under their real names, keeping only the
// web URL the mirror clone's "forge" remote reads; the real URL logic is
// tested in each backend's own package.
func init() {
	stub := func(name string, web func(backendkit.Shared, backendkit.Lookup) string) {
		backendkit.Register(backendkit.Strategy{Descriptor: backend.Descriptor{Name: name}, WebURL: web})
	}
	stub("github", func(s backendkit.Shared, _ backendkit.Lookup) string { return "https://github.com/" + s.RepoSlug })
	stub("git", nil)
	stub("forgejo", func(s backendkit.Shared, _ backendkit.Lookup) string { return "https://forgejo.invalid/" + s.RepoSlug })
	stub("jira", nil)
}
