package forge

import "regexp"

var credentialInURL = regexp.MustCompile(`://[^/\s]+@`)

// RedactURLCredentials strips any embedded userinfo (user:pass@) from URLs
// occurring in s, leaving the rest of s untouched. CODE_FORGE_REMOTE_URL
// commonly carries embedded credentials
// (https://oauth2:<token>@host/repo.git) for hosts without a credential
// helper (docs/reference.md), and git's own error text echoes that URL
// verbatim on auth/network failures. Those errors flow unmodified into
// public GitHub issue comments (settle.mergeImmediate) — this must run on
// every such error before it crosses that trust boundary. The regex is
// greedy through the last @ before the next / or whitespace, so a literal
// (un-encoded) @ inside the password is still consumed as part of userinfo.
func RedactURLCredentials(s string) string {
	return credentialInURL.ReplaceAllString(s, "://")
}
