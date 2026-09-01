package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// netrcCredential parses content as netrc-format text and returns the
// password of the first entry whose machine token case-insensitively
// equals host -- matching first-match-wins semantics of real netrc
// consumers like curl and git, rather than resolving to whichever duplicate
// entry for the same host happens to appear last. Pure: does no I/O itself
// -- callers own reading the file; sourceName is used only to name the
// source in the returned error, never logged or echoed alongside a
// credential value. When no entry's machine matches host, this returns an
// error rather than an empty string with a nil error -- a proxy that goes
// on to run unauthenticated because of a silent miss is the failure mode
// this guards against.
//
// A "default" token ends whatever machine stanza precedes it, so fields
// following it are never attributed to an earlier machine. Unlike some
// netrc implementations, "default" here is never treated as a fallback
// credential source -- a file whose only stanza is "default", with no
// "machine" entries at all, always fails closed with a "no entry for host"
// error. A "macdef" token starts a macro definition whose body -- up to the
// next blank line or EOF -- is skipped verbatim rather than tokenized,
// since a macro body can contain arbitrary text (including strings that
// look like netrc fields) that must never be mistaken for real
// login/password tokens.
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
			// Macro bodies end at the first blank line (or EOF); every
			// line up to that point is macro content, never netrc
			// fields, so it is never passed to strings.Fields below.
			if strings.TrimSpace(line) == "" {
				inMacro = false
			}
			continue
		}

		fields := strings.Fields(line)

		// A hand-maintained netrc accumulates commented-out entries over
		// time (e.g. a revoked password left as a "#"-prefixed record, or a
		// trailing "# ..." annotation after real fields on the same line);
		// truncate at the first "#" token -- start of line or mid-line --
		// before tokenizing, so its words are never mistaken for a live
		// machine/login/password stanza. A "#" as the first field naturally
		// truncates fields to empty, a no-op through the rest of the loop.
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
						// Recorded as soon as the machine token matches,
						// independent of whether a password token ever
						// follows -- lets the final error distinguish "no
						// entry for host" from "entry exists but has no
						// password".
						hostMatched = true
					}
				} else {
					// A "machine" token with no value on its line is
					// malformed -- without this, currentMachine/inMachine
					// would keep pointing at whatever stanza preceded it,
					// misattributing a later, unrelated password to it.
					currentMachine = ""
					inMachine = false
				}
			case "default":
				// Ends the preceding machine stanza -- nothing after
				// this token belongs to currentMachine anymore.
				inMachine = false
			case "macdef":
				// Skip the macro name field, then stop tokenizing this
				// line entirely: any trailing fields on the same line
				// (a macro body crammed after the name) must never be
				// mistaken for real login/password tokens. The macro
				// body's remaining lines are consumed verbatim by the
				// outer loop.
				inMacro = true
				i = len(fields)
			case "password":
				if i+1 < len(fields) {
					i++
					if inMachine && strings.EqualFold(currentMachine, host) && password == "" {
						// First password token wins, even mid-line -- a
						// second "password" token for the same machine
						// stanza on the same line must never clobber the
						// one already resolved; the end-of-line break below
						// only guards against later lines.
						password = fields[i]
					}
				}
			case "login", "account":
				// Skip the associated value token -- these fields are
				// recognized only so the parse doesn't desync; their
				// values are never acted on (out of scope for this
				// issue).
				i++
			}
		}

		if password != "" {
			// First match wins: stop scanning entirely so nothing
			// later in the file -- a duplicate machine entry, a
			// default stanza, or a macro body -- can ever overwrite
			// the password already resolved for host.
			break
		}
	}

	if err := scanner.Err(); err != nil {
		// scanner.Err() is nil when Scan() stopped because it reached EOF
		// cleanly; a non-nil error here means a read/token-size failure cut
		// the scan short, which must not be misreported as an ordinary
		// lookup miss below.
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
