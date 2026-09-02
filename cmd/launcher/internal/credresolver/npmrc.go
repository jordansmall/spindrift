package credresolver

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strings"
)

// npmrcHostname strips any trailing ":port" from an npmrc registry spec or a
// route's match host, so "registry.example.com:8080" and
// "registry.example.com" compare equal to "registry.example.com" -- mirrors
// netrc.go's host-only match (via url.Hostname()) rather than requiring the
// port to agree too. Uses net.SplitHostPort rather than a naive first-":"
// split: a bracketed IPv6 literal like "[fe80::1]" carries several ":"
// characters of its own, and truncating at the first one would collapse
// every distinct address sharing a prefix (e.g. "[fe80::1]" and "[fe80::2]")
// onto the same "[fe80" string, letting one host's token answer for
// another. When s has no port, SplitHostPort fails and s is used as-is,
// still stripping a single enclosing "[" "]" bracket pair so a bare
// bracketed literal (no port) also normalizes to the bracket-free address.
func npmrcHostname(s string) string {
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s[1 : len(s)-1]
	}
	return s
}

// npmrcUnquoteValue strips a matching pair of surrounding double quotes from
// an npmrc value -- npm accepts (and `npm config set` sometimes writes)
// quoted values, e.g. //host/:_authToken="tok en".
func npmrcUnquoteValue(v string) string {
	if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		return v[1 : len(v)-1]
	}
	return v
}

// npmrcAuthToken parses content as npmrc-format text and returns the value
// of the first "//<registry>/:_authToken=<value>" line whose registry
// hostname case-insensitively equals host. Pure: does no I/O itself --
// callers own reading the file; sourceName is used only to name the source
// in the returned error, never logged or echoed alongside a credential
// value.
//
// A line's registry spec is everything between the leading "//" and the
// next "/" -- this is the part compared against host, so a scoped-registry
// entry with a path after the host (e.g.
// "//artifactory.example.com/api/npm/npm/:_authToken=...") still keys on
// "artifactory.example.com", with the path ignored. Both sides of the
// comparison are stripped of any trailing ":port" first (npmrcHostname),
// same as netrc's host-only match, so a registry spec carrying a port never
// fails to match a match host that doesn't (or vice versa).
//
// When no line's registry hostname matches host, this returns an error
// rather than an empty string with a nil error -- a proxy that goes on to
// run unauthenticated because of a silent miss is the failure mode this
// guards against. A matching line whose value is empty is also an error,
// not a silent skip to the next line -- first-match-wins applies to the
// match, not to whether that match happens to carry a usable value.
func npmrcAuthToken(content []byte, sourceName, host string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	normalizedHost := npmrcHostname(host)
	const marker = ":_authToken="

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || !strings.HasPrefix(line, "//") {
			continue
		}

		rest := line[len("//"):]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			continue
		}
		registrySpec, pathAndKey := rest[:slash], rest[slash+1:]

		keyIdx := strings.Index(pathAndKey, marker)
		if keyIdx < 0 || !strings.EqualFold(npmrcHostname(registrySpec), normalizedHost) {
			continue
		}

		value := npmrcUnquoteValue(strings.TrimSpace(pathAndKey[keyIdx+len(marker):]))
		if value == "" {
			return "", fmt.Errorf("registry proxy credential file %s has npmrc _authToken entry for host %s but the value is empty", sourceName, host)
		}
		// A mid-value "\r" would reach the HTTP proxy at header-write time --
		// never print value here, mirroring rawFileResolver and execResolver's
		// embedded-newline guards.
		if strings.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("registry proxy credential file %s has npmrc _authToken entry for host %s with an embedded newline", sourceName, host)
		}
		// npm expands "${VAR}" references in .npmrc values; this parser does
		// not, so failing closed here beats resolving to the literal
		// unexpanded placeholder string.
		if strings.Contains(value, "${") {
			return "", fmt.Errorf("registry proxy credential file %s has npmrc _authToken entry for host %s that uses npm variable expansion, which this resolver does not support", sourceName, host)
		}
		return value, nil
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading npmrc file %s: %w", sourceName, err)
	}

	return "", fmt.Errorf("registry proxy credential file %s has no npmrc _authToken entry for host %s", sourceName, host)
}
