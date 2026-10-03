package credresolver

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// netrcCredential returns the password of the first netrc entry whose machine
// token case-insensitively equals host, matching the first-match-wins rule of
// curl and git. Quoting and escapes follow curl 7.84+ (git-credential-netrc's
// Perl Net::Netrc differs). A miss returns an error rather than an empty
// string, so a caller cannot silently run unauthenticated; so does an
// unterminated quote on or before the matching line (the scan stops after the
// line holding the first matching password, so later lines are never seen).
// sourceName only names the source in errors; never log it beside a
// credential value.
func netrcCredential(content []byte, sourceName, host string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))

	var (
		currentMachine string
		inMachine      bool
		password       string
		inMacro        bool
		hostMatched    bool
	)

	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := scanner.Text()

		if inMacro {
			// A macro body ends at the first blank line or EOF; until
			// then its lines are arbitrary text, never netrc fields.
			if strings.TrimSpace(line) == "" {
				inMacro = false
			}
			continue
		}

		fields, ok := netrcFields(line)
		if !ok {
			// Fail closed: a partial token would resolve as a wrong password.
			// The token text is deliberately left out of the error.
			return "", fmt.Errorf("netrc file %s line %d has an unterminated quote (looking up host %s)", sourceName, lineNo, host)
		}
		for i := 0; i < len(fields); i++ {
			switch fields[i] {
			case "machine":
				if i+1 < len(fields) {
					i++
					currentMachine = fields[i]
					inMachine = true
					if strings.EqualFold(currentMachine, host) {
						// Set on the machine token alone so the final
						// error can distinguish "no entry for host" from
						// "entry exists but has no password".
						hostMatched = true
					}
				} else {
					// A "machine" with no value is malformed; clearing
					// the stanza stops a later password from being
					// misattributed to the preceding machine.
					currentMachine = ""
					inMachine = false
				}
			case "default":
				// Ends the preceding machine stanza. "default" is never a
				// fallback credential here, so a file with no machine
				// entry always fails closed.
				inMachine = false
			case "macdef":
				// Skip the rest of the line: tokens after the macro name
				// are never login/password tokens (an unbalanced quote
				// among them has already failed the lookup above).
				inMacro = true
				i = len(fields)
			case "password":
				if i+1 < len(fields) {
					i++
					if inMachine && strings.EqualFold(currentMachine, host) && password == "" {
						// The first password token wins: a second one
						// later on the same line must not clobber it,
						// and the break below only guards later lines.
						password = fields[i]
					}
				}
			case "login", "account":
				// Skip the value token so the parse stays aligned; the
				// values themselves are unused.
				i++
			}
		}

		if password != "" {
			// First match wins: stop scanning so no later duplicate
			// entry or macro body can overwrite it.
			break
		}
	}

	if err := scanner.Err(); err != nil {
		// A non-nil error means a read or token-size failure cut the scan
		// short rather than a clean EOF, so it must not fall through to
		// the lookup-miss errors below.
		return "", fmt.Errorf("reading netrc file %s: %w", sourceName, err)
	}

	if password != "" {
		return password, nil
	}
	if hostMatched {
		return "", fmt.Errorf("netrc file %s has entry for host %s but no password field", sourceName, host)
	}
	return "", fmt.Errorf("netrc file %s has no entry for host %s", sourceName, host)
}

// isNetrcSpace is ASCII-only like curl's ISSPACE, deliberately narrower than
// strings.Fields' Unicode whitespace (so U+00A0 stays inside a token).
func isNetrcSpace(c byte) bool {
	return strings.IndexByte(" \t\r\v\f", c) >= 0
}

// netrcFields splits a line into netrc tokens on whitespace outside quotes and
// truncates at the first unquoted token starting with "#", so a commented-out
// record is never read as a live stanza. A token opening with `"` runs to the
// next unescaped `"` and is unwrapped with curl's escapes (\" \\ \n \r \t; any
// other escaped byte maps to itself); unquoted tokens stay verbatim. ok is false
// when a quote is never closed.
func netrcFields(line string) (fields []string, ok bool) {
	for i := 0; i < len(line); {
		switch {
		case isNetrcSpace(line[i]):
			i++
		case line[i] == '"':
			var b strings.Builder
			closed := false
			for i++; i < len(line) && !closed; i++ {
				switch c := line[i]; {
				case c == '"':
					closed = true
				case c == '\\' && i+1 < len(line):
					i++
					switch line[i] {
					case 'n':
						b.WriteByte('\n')
					case 'r':
						b.WriteByte('\r')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(line[i])
					}
				default:
					b.WriteByte(c)
				}
			}
			if !closed {
				return nil, false
			}
			fields = append(fields, b.String())
		default:
			start := i
			for i < len(line) && !isNetrcSpace(line[i]) {
				i++
			}
			if line[start] == '#' {
				return fields, true
			}
			fields = append(fields, line[start:i])
		}
	}
	return fields, true
}
