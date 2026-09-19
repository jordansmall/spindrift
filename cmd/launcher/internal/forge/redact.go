package forge

import "regexp"

var credentialInURL = regexp.MustCompile(`://[^/\s]+@`)

// RedactURLCredentials strips embedded userinfo (user:pass@) from URLs in s.
// Git error text echoes CODE_FORGE_REMOTE_URL, which commonly carries a token,
// and those errors reach public issue comments (settle.mergeImmediate), so run
// this before an error crosses that boundary. The regex is greedy to the last @
// before the next / or whitespace, so it over-redacts rather than leaking.
func RedactURLCredentials(s string) string {
	return credentialInURL.ReplaceAllString(s, "://")
}
