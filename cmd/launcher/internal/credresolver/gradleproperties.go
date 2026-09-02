package credresolver

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// gradlePropertiesValue parses content as Java-properties-format text (the
// shape of a Gradle ~/.gradle/gradle.properties file) and returns the value
// of the first line naming key. Pure: does no I/O itself -- callers own
// reading the file; sourceName is used only to name the source in the
// returned error, never logged or echoed alongside a credential value.
//
// This implements only the subset of java.util.Properties syntax a
// single-line credential token needs: "#" or "!" prefixed comment lines and
// blank lines are skipped, and a line's key and value are split at the
// earliest of "=", ":", or whitespace -- see splitGradleProperty for the
// exact rule. Line continuations (a trailing "\" joining a logical line
// across physical lines) and "\uXXXX"/"\t"-style escape sequences are
// deliberately NOT implemented -- a registry credential is a single-line
// token, and fail-closed simplicity here beats emulating the rest of
// java.util.Properties.
//
// Key comparison is exact (properties keys are case-sensitive), and the
// first matching line wins. When no line names key, or the matching line's
// value is empty after trimming, this returns an error naming sourceName
// and key rather than an empty string with a nil error -- a proxy that goes
// on to run unauthenticated because of a silent miss is the failure mode
// this guards against.
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
		// A mid-value "\r" would reach the HTTP proxy at header-write time --
		// never print v here, mirroring rawFileResolver and execResolver's
		// embedded-newline guards.
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

// splitGradleProperty splits a single properties line into its key and raw
// (not-yet-trimmed) value, matching java.util.Properties: the key ends at
// the *earliest* of "=", ":", or a run of whitespace -- never at whichever
// of "="/":" happens to occur first in the whole line, since either could
// legitimately appear later, inside a whitespace-separated value (e.g. a
// password containing ":"). When the key ends at whitespace, that run of
// whitespace is consumed, then at most one immediately-following "="/":" is
// also consumed, before the rest of the line becomes the value -- so
// "key = value" and "key value" both resolve to "value", but a "="/":"
// inside the value itself (past that first one) is left alone. A line with
// no separator of any kind is not a valid key=value entry and is reported
// via ok=false.
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
