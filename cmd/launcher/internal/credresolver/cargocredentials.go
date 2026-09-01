package credresolver

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// cargoCredentialsToken parses content as a cargo credentials.toml file and
// returns the token of the first "[registries.NAME]" table whose NAME
// exactly equals registryName -- matching first-match-wins semantics
// (mirroring netrc.go's own precedent) rather than resolving to whichever
// duplicate table for the same name happens to appear last. Pure: does no
// I/O itself -- callers own reading the file; sourceName is used only to
// name the source in the returned error, never logged or echoed alongside a
// credential value. When no table matches registryName, or a matching table
// has no "token" key, this returns an error rather than an empty string
// with a nil error -- a proxy that goes on to run unauthenticated because
// of a silent miss is the failure mode this guards against.
//
// registryName is matched against a table's header by plain string
// equality, not internal/bindregistry/cargoplaceholder.go's
// cargoBareKeyPattern: registryName here only ever feeds a Go string
// comparison, so there is no injection surface that regex guards against.
//
// This is a lightweight line-based scanner, deliberately not a TOML parser
// -- mirroring cargoplaceholder.go's own stated preference for this kind of
// file. A table's body runs from its header line to the next "[...]"
// header line (of any table, not just [registries.*]) or EOF.
func cargoCredentialsToken(content []byte, sourceName, registryName string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))

	targetHeader := "registries." + registryName

	var (
		inTargetSection bool
		tableFound      bool
		tokenFound      bool
		token           string
	)

	for scanner.Scan() {
		line := scanner.Text()

		// See stripTOMLComment's doc comment for why this must be
		// quote-aware rather than a blind byte truncation.
		line = stripTOMLComment(line)
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "[") {
			if strings.HasSuffix(trimmed, "]") {
				header := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
				if header == targetHeader {
					inTargetSection = true
					// Recorded as soon as the header matches, independent of
					// whether a token assignment ever follows -- lets the
					// final error distinguish "no table for registryName"
					// from "table exists but has no token".
					tableFound = true
				} else {
					inTargetSection = false
				}
			} else {
				// A dropped "]" is an easy hand-edit mistake -- it must
				// still end the previous table's section rather than
				// silently extending that table's authority (and its
				// secret) to whatever comes next.
				inTargetSection = false
			}
			continue
		}

		if !inTargetSection || tokenFound {
			continue
		}

		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "token" {
			continue
		}

		if unquoted, ok := unquoteTOMLString(strings.TrimSpace(value)); ok && unquoted != "" {
			// An empty quoted string ("" or '') must not count as found --
			// unlike netrc.go's strings.Fields(), TOML makes an empty value
			// representable, so this must fail closed explicitly.
			// First token wins, even across a pathologically duplicated
			// header -- once found here, nothing later in the file (a
			// duplicate table, or a stray "token" line elsewhere) can
			// ever overwrite it.
			token = unquoted
			tokenFound = true
		}
	}

	if err := scanner.Err(); err != nil {
		// scanner.Err() is nil when Scan() stopped because it reached EOF
		// cleanly; a non-nil error here means a read/token-size failure cut
		// the scan short, which must not be misreported as an ordinary
		// lookup miss below.
		return "", fmt.Errorf("reading cargo credentials file %s: %w", sourceName, err)
	}

	if tokenFound {
		return token, nil
	}
	if tableFound {
		return "", fmt.Errorf("cargo credentials file %s has table [registries.%s] but no token field", sourceName, registryName)
	}
	return "", fmt.Errorf("cargo credentials file %s has no [registries.%s] table", sourceName, registryName)
}

// stripTOMLComment truncates line at the first "#" that appears outside of a
// '...' or "..." quoted string -- a quote-aware replacement for a blind
// byte truncation, which would mistake a "#" inside a quoted table name
// (e.g. [registries."other#x"]) or a quoted token value for a comment
// marker and corrupt the line before header/value parsing ever sees it.
// It does not process backslash escapes, matching unquoteTOMLString's own
// stated limitation.
func stripTOMLComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '#':
			return line[:i]
		}
	}
	return line
}

// unquoteTOMLString strips a matching pair of leading/trailing double or
// single quotes from s and reports whether s was quoted. It does not
// process backslash escapes -- an unescaped-is-fine value is all this
// lightweight scanner needs, since it is not a general TOML parser -- so it
// rejects a result that still contains a quote or backslash rather than
// returning a silently corrupted credential (e.g. an escaped quote or a
// triple-quoted multi-line string, both mishandled by this stripper).
func unquoteTOMLString(s string) (string, bool) {
	if len(s) < 2 {
		return "", false
	}
	quote := s[0]
	if quote != '"' && quote != '\'' {
		return "", false
	}
	if s[len(s)-1] != quote {
		return "", false
	}
	unquoted := s[1 : len(s)-1]
	if strings.ContainsAny(unquoted, `"'\`) {
		return "", false
	}
	return unquoted, true
}
