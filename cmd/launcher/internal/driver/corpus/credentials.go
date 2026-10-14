// Package corpus is the credential guard and scrubber for the per-Driver
// Pass-log corpus (issue #5180). Find reports credential-shaped strings,
// emails and host home paths in a corpus file; Scrub redacts them, and also
// blanks the tool output, file bodies, inline code and agent prose that hold
// repo content (issue #5181), so a maintainer can prepare an entry.
package corpus

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Finding is one credential-shaped match. It carries no secret text, so
// callers can print it freely.
type Finding struct {
	Pattern string
	Line    int // 1-based line the match starts on
}

type pattern struct {
	name string
	re   *regexp.Regexp
	// group, when non-zero, is the submatch to redact; the rest of the match
	// is context worth keeping (the "Bearer " prefix).
	group int
	// valid, when set, rejects a match that is shaped right but not random
	// enough to be a credential. It sees only the match, so no pattern comes to
	// depend on bytes outside it.
	valid func(match []byte) bool
}

// patterns is the single source of truth for what the guard rejects:
// credentials, plus emails and host home paths.
// No pattern is anchored (\b) or fixed-length: a match depends on no byte
// outside itself, and every unbounded body class is greedy (private-key-block
// ends at its END line or end of input), so a neighbouring credential can
// never hide one, and over-redacting trailing text is the safe error.
// Overlapping matches merge under the earliest-starting one's name; table
// order only breaks same-start ties (sk-ant- keys also fit the OpenAI shape).
var patterns = []pattern{
	{name: "github-token", re: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`)},
	{name: "github-fine-grained", re: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`)},
	{name: "gitlab-token", re: regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`)},
	{name: "anthropic-api-key", re: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	{name: "openai-api-key", re: regexp.MustCompile(`sk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{32,}`), valid: hasDigitOrUpper},
	{name: "bearer-token", re: regexp.MustCompile(`(?i)bearer[ \t]+([A-Za-z0-9._~+/=-]{20,})`), group: 1},
	{name: "private-key-block", re: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`)},
	{name: "aws-access-key-id", re: regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16,}`)},
	{name: "google-api-key", re: regexp.MustCompile(`AIza[0-9A-Za-z_-]{35,}`)},
	{name: "slack-token", re: regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
	{name: "npm-token", re: regexp.MustCompile(`npm_[A-Za-z0-9]{36,}`)},
	// Not credentials, but they name a person or their machine.
	{name: "email", re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)},
	{name: "home-path", re: regexp.MustCompile(`/(?:home|Users)/[A-Za-z0-9._-]+(?:/[A-Za-z0-9._+-]+)*`), valid: notBoxHome},
}

// notBoxHome spares /home/agent, the Box user's home, which names no one.
func notBoxHome(match []byte) bool {
	user, _, _ := bytes.Cut(match[bytes.IndexByte(match[1:], '/')+2:], []byte("/"))
	return string(user) != "agent"
}

// hasDigitOrUpper rejects kebab-case prose that fits the OpenAI shape: a real
// random body lacks both digits and capitals with probability about (26/62)^32,
// a lowercase slug never has either. The greedy match is the whole class run,
// so rejecting it hides no shorter match inside it.
func hasDigitOrUpper(match []byte) bool {
	return bytes.ContainsAny(match[len("sk-"):], "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

type span struct {
	start, end int
	name       string
}

// spans returns the credential spans in start order, overlapping matches merged.
func spans(data []byte) []span {
	var all []span
	for _, p := range patterns {
		// Resume one byte past each match's start, not at its end: a credential
		// swallowed by a greedy neighbour of the same shape must still be found.
		// Worst case is quadratic in a long run holding many prefixes (sk-sk-sk-...),
		// which is fine at corpus and Pass-log sizes.
		for from := 0; from < len(data); {
			m := p.re.FindSubmatchIndex(data[from:])
			if m == nil {
				break
			}
			if m[2*p.group] >= 0 && (p.valid == nil || p.valid(data[from+m[0]:from+m[1]])) {
				all = append(all, span{from + m[2*p.group], from + m[2*p.group+1], p.name})
			}
			from += m[0] + 1
		}
	}
	// Stable keeps table priority between same-start matches.
	sort.SliceStable(all, func(i, j int) bool { return all[i].start < all[j].start })
	var out []span
	for _, s := range all {
		if n := len(out); n > 0 && s.start < out[n-1].end {
			if s.end > out[n-1].end {
				out[n-1].end = s.end
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// Find reports every credential-shaped string in data.
func Find(data []byte) []Finding {
	var out []Finding
	line, pos := 1, 0
	for _, s := range spans(data) {
		line += bytes.Count(data[pos:s.start], []byte{'\n'})
		pos = s.start
		out = append(out, Finding{Pattern: s.name, Line: line})
	}
	return out
}

// Scrub blanks JSON bodies (see redactBodies), then replaces each pattern
// match with [REDACTED:<pattern>]. Its output has no Find hits and no bodies
// left to blank, so Scrub is idempotent.
func Scrub(data []byte) []byte {
	data, _ = redactBodies(data)
	// One round already redacts every span; the loop keeps idempotence true by
	// construction should a pattern ever regain context dependence. It ends
	// because "[", ":" and "]" sit in no pattern's character class, so a
	// placeholder never forms a new match.
	for {
		ss := spans(data)
		if len(ss) == 0 {
			return data
		}
		data = redact(data, ss)
	}
}

// Tally counts what Scrub would redact, keyed by placeholder name.
func Tally(data []byte) map[string]int {
	out, counts := redactBodies(data)
	for _, f := range Find(out) {
		counts[f.Pattern]++
	}
	return counts
}

func redact(data []byte, ss []span) []byte {
	var out []byte
	pos := 0
	for _, s := range ss {
		out = append(out, data[pos:s.start]...)
		out = append(out, "[REDACTED:"+s.name+"]"...)
		pos = s.end
	}
	return append(out, data[pos:]...)
}

// Scan walks root and returns one "<path>:<line>: unscrubbed <pattern>"
// violation per hit across every file beneath it. Violations name the pattern,
// never the secret.
func Scan(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, f := range Find(data) {
			out = append(out, fmt.Sprintf("%s:%d: unscrubbed %s", path, f.Line, f.Pattern))
		}
		return nil
	})
	return out, err
}
