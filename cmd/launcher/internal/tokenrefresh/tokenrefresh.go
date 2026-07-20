// Package tokenrefresh keeps GH_TOKEN current across a launcher run that
// outlives a GitHub App installation token's ~1h lifetime (issue #1027): an
// external minter (a workflow step holding the App private key) rewrites a
// file in place with a freshly minted token, and the launcher polls that
// file rather than trusting the value it captured at startup.
package tokenrefresh

import (
	"os"
	"strings"
	"time"
)

// ReadIfChanged reads path and reports whether its trimmed contents differ
// from prev and are non-empty. On a read error, or when the contents are
// empty or unchanged, it returns prev unchanged and changed=false — the
// caller keeps using whatever token it already has rather than clearing
// GH_TOKEN out from under an in-flight gh call.
func ReadIfChanged(path, prev string) (next string, changed bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return prev, false
	}
	token := strings.TrimSpace(string(data))
	if token == "" || token == prev {
		return prev, false
	}
	return token, true
}

// Watch polls path every interval and calls setenv whenever its content
// changes (checking immediately, before the first tick), until stop is
// closed. A setenv error leaves prev at its old value so the next poll
// retries the same token rather than silently adopting it as seen.
func Watch(path string, interval time.Duration, stop <-chan struct{}, setenv func(string) error) {
	prev := ""
	apply := func() {
		if next, changed := ReadIfChanged(path, prev); changed {
			if setenv(next) == nil {
				prev = next
			}
		}
	}

	apply()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			apply()
		}
	}
}
