package credresolver

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strings"
)

// npmrcHostname strips any trailing ":port", mirroring netrc.go's host-only
// match. It uses net.SplitHostPort rather than cutting at the first colon,
// because a bracketed IPv6 literal carries colons of its own: cutting there
// would collapse "[fe80::1]" and "[fe80::2]" onto one key, letting one host's
// token answer for another.
func npmrcHostname(s string) string {
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s[1 : len(s)-1]
	}
	return s
}

// npmrcUnquoteValue strips a matching pair of surrounding double quotes, which
// npm accepts and `npm config set` sometimes writes.
func npmrcUnquoteValue(v string) string {
	if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		return v[1 : len(v)-1]
	}
	return v
}

// npmrcAuthToken returns the value of the first "//<registry>/:_authToken="
// line whose registry hostname equals host, ignoring case, any path after the
// host, and any port on either side. sourceName names the file in errors,
// never alongside a credential value. A miss or an empty value is an error
// rather than a silent skip, so the proxy never runs unauthenticated.
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
		// A mid-value "\r" would reach the HTTP proxy at header-write time.
		// Never print value here.
		if strings.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("registry proxy credential file %s has npmrc _authToken entry for host %s with an embedded newline", sourceName, host)
		}
		// npm expands "${VAR}" references in .npmrc values; this parser does
		// not, so it fails closed rather than resolving to the literal
		// placeholder.
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
