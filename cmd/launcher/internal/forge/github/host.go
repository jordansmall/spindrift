package github

import "os"

// Host returns GH_HOST verbatim when set, else github.com. Doctor's
// registry-route-drift check shares it with the Ledger remote so both agree
// on a GitHub Enterprise host (issue #3912).
func Host() string {
	if host := os.Getenv("GH_HOST"); host != "" {
		return host
	}
	return "github.com"
}
