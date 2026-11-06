package credresolver

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strings"

	"spindrift.dev/launcher/internal/npmrcvalue"
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

// npmrcAuthToken returns the value of the first "//<registry>/:_authToken"
// line whose registry hostname equals host, ignoring case, any path after the
// host, any port on either side, and whitespace around "=". sourceName names
// the file in errors, never alongside a credential value. A miss or an empty
// value is an error rather than a silent skip, so the proxy never runs
// unauthenticated.
func npmrcAuthToken(content []byte, sourceName, host string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	normalizedHost := npmrcHostname(host)
	const keySuffix = ":_authToken"

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || !strings.HasPrefix(line, "//") {
			continue
		}

		// Split at the first "=" and trim the key, as npm's ini parser does.
		// line is already trimmed and "//"-prefixed, so only the key's right
		// side can carry whitespace and the "//" slice below is in bounds.
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		rest := strings.TrimSpace(key)[len("//"):]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			continue
		}
		registrySpec, pathAndKey := rest[:slash], rest[slash+1:]

		if !strings.HasSuffix(pathAndKey, keySuffix) || !strings.EqualFold(npmrcHostname(registrySpec), normalizedHost) {
			continue
		}

		value := npmrcvalue.Decode(rawValue)
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
