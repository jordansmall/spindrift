// Package tokenrefresh keeps GH_TOKEN current across a launcher run that
// outlives a GitHub App installation token's ~1h lifetime (issue #1027). An
// external minter replaces a file with a fresh token, and the launcher reads
// that file instead of the value it captured at startup. The minter replaces
// the file by rename on each rotation, so a read sees a whole token.
package tokenrefresh

import (
	"os"
	"strings"
	"sync/atomic"
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

// Follow returns a func yielding the current token. With path empty it always
// returns static and reads nothing. Otherwise every call re-reads path, so the
// next request after a rotation carries the new token; static is returned until
// the file first holds a token, and the last good token is kept thereafter. The
// returned func is safe for concurrent use.
func Follow(static, path string) func() string {
	if path == "" {
		return func() string { return static }
	}
	var last atomic.Pointer[string]
	last.Store(&static)
	return func() string {
		// No lock across the read, so concurrent requests do not serialise. A
		// racing store can leave an older fallback, harmless as every call re-reads.
		next, _ := ReadIfChanged(path, *last.Load())
		last.Store(&next)
		return next
	}
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
