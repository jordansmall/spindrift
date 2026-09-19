package credresolver

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// gradlePropertiesValue returns the value of the first line naming key in
// Java-properties-format content, and does no I/O itself. It does not
// implement line continuations or "\uXXXX" escapes, because a registry
// credential is a single-line token. A missing key or empty value is an
// error, not an empty string, so the proxy never runs unauthenticated.
func gradlePropertiesValue(content []byte, sourceName, key string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}

		k, v, ok := splitGradleProperty(line)
		if !ok || k != key {
			continue
		}

		v = strings.TrimSpace(v)
		if v == "" {
			return "", fmt.Errorf("registry proxy credential file %s has property %q but the value is empty", sourceName, key)
		}
		// A mid-value "\r" would reach the HTTP proxy at header-write time.
		// Never print v here, matching the embedded-newline guards in
		// rawFileResolver and execResolver.
		if strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("registry proxy credential file %s has property %q with an embedded newline", sourceName, key)
		}
		return v, nil
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("registry proxy credential file %s: reading gradle properties: %w", sourceName, err)
	}

	return "", fmt.Errorf("registry proxy credential file %s has no property %q", sourceName, key)
}

// splitGradleProperty splits a line at the earliest of "=", ":", or a run of
// whitespace, matching java.util.Properties. Splitting at the first "=" or ":"
// anywhere in the line would cut inside a whitespace-separated value that
// contains one, such as a password with a colon. A line with no separator
// returns ok=false.
func splitGradleProperty(line string) (key, value string, ok bool) {
	idx := strings.IndexAny(line, "=: \t")
	if idx < 0 {
		return "", "", false
	}

	key = strings.TrimSpace(line[:idx])
	rest := line[idx+1:]

	if line[idx] == '=' || line[idx] == ':' {
		return key, rest, true
	}

	// idx was whitespace: skip the rest of the run, then at most one
	// trailing "=" or ":" before the value begins.
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && (rest[0] == '=' || rest[0] == ':') {
		rest = rest[1:]
	}
	return key, rest, true
}
