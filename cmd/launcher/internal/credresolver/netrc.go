package credresolver

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// netrcCredential returns the password of the first netrc entry whose machine
// case-insensitively equals host, first match wins as in curl and git.
// Quoting follows curl 7.84+ (git-credential-netrc's Perl Net::Netrc
// differs). A miss, or an unterminated quote on or before the matching line,
// is an error, so a caller cannot run unauthenticated. Never log sourceName beside a credential value.
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
						// Lets the final error tell "no entry" from "no
						// password".
						hostMatched = true
					}
				} else {
					// Clear the stanza so a later password is not
					// misattributed to the preceding machine.
					currentMachine = ""
					inMachine = false
				}
			case "default":
				// "default" is never a fallback credential, so a file with
				// no machine entry fails closed.
				inMachine = false
			case "macdef":
				// Tokens after the macro name are never login/password.
				inMacro = true
				i = len(fields)
			case "password":
				if i+1 < len(fields) {
					i++
					if inMachine && strings.EqualFold(currentMachine, host) && password == "" {
						// A second password on the same line must not
						// clobber the first.
						password = fields[i]
					}
				}
			case "login", "account":
				// Skip the unused value so the parse stays aligned.
				i++
			}
		}

		if password != "" {
			// First match wins.
			break
		}
	}

	if err := scanner.Err(); err != nil {
		// A cut-short scan must not read as a lookup miss.
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

// netrcFields splits a line into netrc tokens, truncating at the first
// unquoted "#" token so a commented-out record is never live. A quoted token
// is unwrapped with curl's escapes (\" \\ \n \r \t; any other escaped
// byte maps to itself). ok is false when a quote is never closed.
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
