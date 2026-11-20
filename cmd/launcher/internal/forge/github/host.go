package github

import (
	"os"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
)

// DefaultHost is the host Host returns when GH_HOST is unset.
const DefaultHost = "github.com"

// Host returns GH_HOST verbatim when set, else github.com. Doctor's
// registry-route-drift check shares it with the Ledger remote so both agree
// on a GitHub Enterprise host (issue #3912).
func Host() string { return HostFrom(os.Getenv) }

// HostFrom is Host with an injected getenv, for hermetic callers.
func HostFrom(getenv func(string) string) string {
	if host := getenv("GH_HOST"); host != "" {
		return host
	}
	return DefaultHost
}

func init() {
	// The row's Routes pin api.github.com. Under any other GH_HOST no row
	// applies: every github call fails with ErrNoRoute rather than going through
	// gh. The row's credential is neither fetched nor sent to a host it never
	// targeted.
	backendrows.RegisterGate(backend.GitHub.Name, func(lookup func(string) string) bool {
		return strings.EqualFold(HostFrom(lookup), DefaultHost)
	})
}
