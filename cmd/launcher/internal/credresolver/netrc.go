package credresolver

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// netrcCredential returns the password of the first netrc entry whose machine
// token case-insensitively equals host, matching the first-match-wins rule of
// curl and git. A miss returns an error rather than an empty string, so a
// caller cannot silently run unauthenticated. sourceName only names the source
// in errors; never log it beside a credential value.
func netrcCredential(content []byte, sourceName, host string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))

	var (
		currentMachine string
		inMachine      bool
		password       string
		inMacro        bool
		hostMatched    bool
	)

	for scanner.Scan() {
		line := scanner.Text()

		if inMacro {
			// A macro body ends at the first blank line or EOF; until
			// then its lines are arbitrary text, never netrc fields.
			if strings.TrimSpace(line) == "" {
				inMacro = false
			}
			continue
		}

		fields := strings.Fields(line)

		// Truncate at the first "#" token, start of line or mid-line, so a
		// commented-out record is never read as a live stanza. A leading "#"
		// leaves fields empty, a no-op through the rest of the loop.
		for i, f := range fields {
			if strings.HasPrefix(f, "#") {
				fields = fields[:i]
				break
			}
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
				// Stop tokenizing the line: fields after the macro name
				// are macro body, not login/password tokens.
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
