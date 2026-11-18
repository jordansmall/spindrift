package forge

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var credentialInURL = regexp.MustCompile(`://[^/\s]+@`)

// RedactURLCredentials strips embedded userinfo (user:pass@) from URLs in s.
// Git error text echoes CODE_FORGE_REMOTE_URL, which commonly carries a token,
// and those errors reach public issue comments (settle.mergeImmediate), so run
// this before an error crosses that boundary. The regex is greedy to the last @
// before the next / or whitespace, so it over-redacts rather than leaking.
func RedactURLCredentials(s string) string {
	return credentialInURL.ReplaceAllString(s, "://")
}

// RedactedOutputTail renders the tail of git's output as ": <text>" so a
// clone failure names its cause (auth, not-found, network). It is redacted
// because git echoes the remote URL and the error reaches issue comments.
func RedactedOutputTail(out []byte) string {
	const maxTail = 512
	msg := strings.TrimSpace(RedactURLCredentials(string(out)))
	if msg == "" {
		return ""
	}
	if len(msg) > maxTail {
		cut := len(msg) - maxTail
		for cut < len(msg) && !utf8.RuneStart(msg[cut]) {
			cut++
		}
		msg = "..." + msg[cut:]
	}
	return ": " + msg
}
