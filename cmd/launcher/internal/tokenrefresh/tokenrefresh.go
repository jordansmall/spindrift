// Package tokenrefresh keeps GH_TOKEN current across a launcher run that
// outlives a GitHub App installation token's ~1h lifetime (issue #1027). An
// external minter replaces a file with a fresh token, and the launcher reads
// that file instead of the value it captured at startup. The minter replaces
// the file by rename on each rotation, so a read sees a whole token.
package tokenrefresh

import (
	"os"
	"strings"
	"time"
)

// Interval is how often a caller of Watch polls GH_TOKEN_REFRESH_FILE for a
// freshly minted token. An installation token lives about an hour (issue #1027).
const Interval = 60 * time.Second

// ReadIfChanged reports whether path holds a non-empty token differing from prev.
// On a read error, or on empty or unchanged contents, it returns prev, so the
// caller never clears GH_TOKEN out from under an in-flight gh call.
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

// Watch polls path every interval and calls setenv on each change, checking once
// before the first tick, until stop is closed. A setenv error leaves prev
// unchanged so the next poll retries the same token.
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

// WatchGHToken is Watch applying each token to GH_TOKEN. The launcher and the
// daemon both start it, and the github row's credential reads GH_TOKEN at each
// use, so this is what carries a rotation to Connector requests and core's git.
func WatchGHToken(path string, interval time.Duration, stop <-chan struct{}) {
	Watch(path, interval, stop, func(v string) error { return os.Setenv("GH_TOKEN", v) })
}
