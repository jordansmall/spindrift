package promptassembly_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
)

// clauseContract is a named obligation on one prompt fragment: clause is
// prose the fragment's rendered text must still say (or, with absent, must
// not say), and reason is why. pattern is the alternative to clause, for an
// obligation no fixed literal covers; exactly one of the two must be set. from
// and to optionally scope the
// match to the text after from and before to within the fragment's span.
// verbatim skips whitespace normalization, for clauses whose whitespace is
// the obligation.
type clauseContract struct {
	fragment string
	clause   string
	pattern  *regexp.Regexp
	reason   string
	from, to string
	absent   bool
	verbatim bool
}

// what names the contract's matcher in errors and test names.
func (c clauseContract) what() string {
	if c.pattern != nil {
		return c.pattern.String()
	}
	return c.clause
}

// renderedFragments maps a cell name to fragment name to that fragment's
// rendered spans in the cell (promptassembly.FragmentRenders).
type renderedFragments map[string]map[string][]string

// hyphenLineBreak matches a line ending in a word-internal hyphen, plus the
// next line's indent. It runs before whitespace is collapsed, because only an
// actual line break marks a re-wrap: a suspended hyphen like "kernel- or
// namespace-level" keeps its space, and a bullet's "- " has no letter before
// the hyphen.
var hyphenLineBreak = regexp.MustCompile(`([\p{L}\p{N}_])-[ \t]*\r?\n[ \t]*`)

// normalizeWhitespace lets a clause survive a harmless re-wrap of prose
// across lines; a line break after a hyphen is a re-wrap too, so it is
// rejoined.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(hyphenLineBreak.ReplaceAllString(s, "${1}-")), " ")
}

// splicedFragment is the clause that matches where a nested fragment whose
// name starts with prefix was spliced into a span. promptassembly marks each
// interruption of a fragment's render with a delimited fragment name; this
// keeps that encoding out of the contract table.
func splicedFragment(prefix string) string { return promptassembly.FragmentRenderGap + prefix }

// checkClauseContract checks c against every span of c.fragment in every
// cell. A fragment that renders in no cell fails, so a contract cannot go
// vacuous when a fragment is gated out of every checked prompt.
func checkClauseContract(c clauseContract, renders renderedFragments) error {
	if c.pattern != nil && c.clause != "" {
		return fmt.Errorf("fragment %s, %q (%s): sets both a clause and a pattern; the clause would be ignored", c.fragment, c.what(), c.reason)
	}
	if c.pattern == nil && c.clause == "" {
		return fmt.Errorf("fragment %s (%s): sets neither a clause nor a pattern; it would match vacuously", c.fragment, c.reason)
	}
	cells := make([]string, 0, len(renders))
	for cell := range renders {
		cells = append(cells, cell)
	}
	slices.Sort(cells)

	rendered := false
	for _, cell := range cells {
		for _, span := range renders[cell][c.fragment] {
			rendered = true
			if err := checkSpan(c, span); err != nil {
				return fmt.Errorf("fragment %s, %q (%s): cell %s: %w", c.fragment, c.what(), c.reason, cell, err)
			}
		}
	}
	if !rendered {
		return fmt.Errorf("fragment %s, %q (%s): fragment renders in no checked prompt", c.fragment, c.what(), c.reason)
	}
	return nil
}

func checkSpan(c clauseContract, span string) error {
	norm := normalizeWhitespace
	if c.verbatim {
		norm = func(s string) string { return s }
	}
	region := norm(span)
	if c.from != "" {
		from := norm(c.from)
		i := strings.Index(region, from)
		if i < 0 {
			return fmt.Errorf("scope start %q not found", c.from)
		}
		region = region[i+len(from):]
	}
	if c.to != "" {
		to := norm(c.to)
		i := strings.Index(region, to)
		if i < 0 {
			return fmt.Errorf("scope end %q not found", c.to)
		}
		region = region[:i]
	}
	has := strings.Contains(region, norm(c.clause))
	if c.pattern != nil {
		has = c.pattern.MatchString(region)
	}
	switch {
	case c.absent && has:
		return fmt.Errorf("clause is present but must be absent")
	case !c.absent && !has:
		return fmt.Errorf("clause is missing")
	}
	return nil
}

// twinEdit rewrites one twin's normalized text before the pair is compared,
// so a legitimate difference (a mechanism clause, a FILE ISSUES mention)
// collapses to common text while any other difference still fails. count is
// the exact number of occurrences old must have; 0 replaces every one, but at
// least one must exist, so a stale edit cannot go vacuous.
type twinEdit struct {
	old, new string
	count    int
}

// twinContract binds two fragments that mirror prose by hand, because the
// carrier or tracker is chosen per dispatch and fragments share no include:
// their rendered text, within the scope, must be equal after whitespace
// normalization and the per-side edits. The pair is compared across every
// cell, since twins rarely render in the same one.
type twinContract struct {
	a, b, reason string
	// from and through scope both twins: from starts the compared text
	// (inclusive), through ends it (inclusive); empty means the span's start
	// or end.
	from, through string
	// cutB is the marker of a paragraph (through the next blank line) removed
	// from b before comparing, for a paragraph only b carries.
	cutB string
	// editA and editB rewrite each side after scoping.
	editA, editB []twinEdit
	// endsA requires a's text to end at through, so prose appended after the
	// shared span is caught.
	endsA bool
}

// checkTwinContract compares every distinct processed span of c.a with every
// distinct one of c.b. A twin that renders in no cell fails, naming it.
func checkTwinContract(c twinContract, renders renderedFragments) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("twins %s and %s (%s): %s", c.a, c.b, c.reason, fmt.Sprintf(format, args...))
	}
	side := func(fragment string, cut string, edits []twinEdit, ends bool) (map[string]string, error) {
		cells := make([]string, 0, len(renders))
		for cell := range renders {
			cells = append(cells, cell)
		}
		slices.Sort(cells)
		texts := map[string]string{}
		for _, cell := range cells {
			for _, span := range renders[cell][fragment] {
				text, err := twinText(c, span, cut, edits, ends)
				if err != nil {
					return nil, fail("%s, cell %s: %v", fragment, cell, err)
				}
				if _, seen := texts[text]; !seen {
					texts[text] = cell
				}
			}
		}
		if len(texts) == 0 {
			return nil, fail("%s renders in no checked prompt", fragment)
		}
		return texts, nil
	}
	as, err := side(c.a, "", c.editA, c.endsA)
	if err != nil {
		return err
	}
	bs, err := side(c.b, c.cutB, c.editB, false)
	if err != nil {
		return err
	}
	for aText, aCell := range as {
		for bText, bCell := range bs {
			if aText != bText {
				return fail("%s (cell %s) and %s (cell %s) diverge:\n%s", c.a, aCell, c.b, bCell, divergence(aText, bText))
			}
		}
	}
	return nil
}

// twinText cuts, normalizes, scopes and edits one twin's span.
func twinText(c twinContract, span, cut string, edits []twinEdit, ends bool) (string, error) {
	if cut != "" {
		i := strings.Index(span, cut)
		if i < 0 {
			return "", fmt.Errorf("cut marker %q not found", cut)
		}
		if n := strings.Index(span[i:], "\n\n"); n >= 0 {
			span = span[:i] + span[i+n:]
		} else {
			span = span[:i]
		}
	}
	text := normalizeWhitespace(span)
	if c.from != "" {
		from := normalizeWhitespace(c.from)
		i := strings.Index(text, from)
		if i < 0 {
			return "", fmt.Errorf("scope start %q not found", c.from)
		}
		text = text[i:]
	}
	if c.through != "" {
		through := normalizeWhitespace(c.through)
		i := strings.Index(text, through)
		if i < 0 {
			return "", fmt.Errorf("scope end %q not found", c.through)
		}
		if rest := text[i+len(through):]; ends && rest != "" {
			return "", fmt.Errorf("text follows %q: %q", c.through, rest)
		}
		text = text[:i+len(through)]
	}
	for _, e := range edits {
		old := normalizeEdit(e.old)
		n := strings.Count(text, old)
		if (e.count > 0 && n != e.count) || n == 0 {
			return "", fmt.Errorf("%q must occur exactly %d time(s) (0 means at least once), found %d", e.old, e.count, n)
		}
		text = strings.ReplaceAll(text, old, normalizeEdit(e.new))
	}
	return text, nil
}

// divergence quotes a short excerpt of each text around the first byte they
// differ at, so a failure shows the drift rather than two whole fragments.
func divergence(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	excerpt := func(s string) string {
		lo, hi := max(i-60, 0), min(i+80, len(s))
		return s[lo:hi]
	}
	return fmt.Sprintf("first difference at byte %d:\n  a: ...%q\n  b: ...%q", i, excerpt(a), excerpt(b))
}

func TestCheckClauseContract(t *testing.T) {
	const rewrapped = "fold each fix into the\n  commit it belongs to, then\nkernel-\n  level stop"
	renders := renderedFragments{
		"cell-a": {"wrap.md": {rewrapped}, "scope.md": {"alpha beta MARK gamma"}},
		"cell-b": {"wrap.md": {rewrapped + " again"}, "scope.md": {"alpha  beta\nMARK gamma"}},
		"cell-c": {"delim-ok.md": {"a\nX\nb"}, "delim-indented.md": {"a\n  X\nb"}},
		"cell-d": {"splice.md": {"head \x00kid-one.md\x00 tail \x00 end"}},
		"cell-e": {
			"heredoc-ok.md":    {"driver-exec signal x <<'SPINDRIFT_SIGNAL_EOF'\nbody\nSPINDRIFT_SIGNAL_EOF"},
			"heredoc-dash.md":  {"cat <<-'EOF'\nbody\nEOF"},
			"heredoc-space.md": {"cat << EOF\nbody\nEOF"},
		},
	}
	eofPattern := regexp.MustCompile(`<<-?\s*['"]?EOF\b`)
	tests := []struct {
		name    string
		c       clauseContract
		wantErr []string
	}{
		{
			name: "splice between scope marks passes",
			c:    clauseContract{fragment: "splice.md", clause: splicedFragment("kid-"), from: "head", to: "tail", reason: "r"},
		},
		{
			name:    "splice outside scope fails",
			c:       clauseContract{fragment: "splice.md", clause: splicedFragment("kid-"), from: "tail", reason: "r"},
			wantErr: []string{"clause is missing"},
		},
		{
			name: "re-wrapped clause passes",
			c:    clauseContract{fragment: "wrap.md", clause: "fold each fix into the commit it belongs to, then kernel-level stop", reason: "r"},
		},
		{
			name:    "missing clause names fragment clause and reason",
			c:       clauseContract{fragment: "wrap.md", clause: "absent words", reason: "because of issue #1"},
			wantErr: []string{"wrap.md", "absent words", "because of issue #1", "cell cell-a", "missing"},
		},
		{
			name:    "never-rendered fragment fails",
			c:       clauseContract{fragment: "nope.md", clause: "x", reason: "r"},
			wantErr: []string{"nope.md", "renders in no checked prompt"},
		},
		{
			name: "clause inside scope passes",
			c:    clauseContract{fragment: "scope.md", clause: "beta", from: "alpha", to: "MARK", reason: "r"},
		},
		{
			name:    "clause outside scope fails",
			c:       clauseContract{fragment: "scope.md", clause: "gamma", from: "alpha", to: "MARK", reason: "r"},
			wantErr: []string{"clause is missing"},
		},
		{
			name:    "missing scope start fails",
			c:       clauseContract{fragment: "scope.md", clause: "beta", from: "nowhere", reason: "r"},
			wantErr: []string{"scope start", "nowhere"},
		},
		{
			name:    "missing scope end fails",
			c:       clauseContract{fragment: "scope.md", clause: "beta", to: "nowhere", reason: "r"},
			wantErr: []string{"scope end", "nowhere"},
		},
		{
			name:    "absent clause that is present fails",
			c:       clauseContract{fragment: "scope.md", clause: "gamma", absent: true, reason: "r"},
			wantErr: []string{"must be absent"},
		},
		{
			name: "verbatim clause at column one passes",
			c:    clauseContract{fragment: "delim-ok.md", clause: "\nX\n", verbatim: true, reason: "r"},
		},
		{
			name:    "verbatim clause indented fails",
			c:       clauseContract{fragment: "delim-indented.md", clause: "\nX\n", verbatim: true, reason: "r"},
			wantErr: []string{"clause is missing"},
		},
		{
			name: "absent pattern that is absent passes",
			c:    clauseContract{fragment: "heredoc-ok.md", pattern: eofPattern, absent: true, reason: "r"},
		},
		{
			name:    "absent pattern matching a dash-quoted delimiter fails",
			c:       clauseContract{fragment: "heredoc-dash.md", pattern: eofPattern, absent: true, reason: "r"},
			wantErr: []string{"heredoc-dash.md", "EOF", "must be absent"},
		},
		{
			name:    "absent pattern matching a spaced delimiter fails",
			c:       clauseContract{fragment: "heredoc-space.md", pattern: eofPattern, absent: true, reason: "r"},
			wantErr: []string{"must be absent"},
		},
		{
			name:    "required pattern that does not match fails",
			c:       clauseContract{fragment: "heredoc-ok.md", pattern: eofPattern, reason: "r"},
			wantErr: []string{"clause is missing"},
		},
		{
			name:    "both clause and pattern is rejected",
			c:       clauseContract{fragment: "heredoc-ok.md", clause: "body", pattern: eofPattern, reason: "r"},
			wantErr: []string{"heredoc-ok.md", "both a clause and a pattern"},
		},
		{
			name:    "neither clause nor pattern is rejected",
			c:       clauseContract{fragment: "heredoc-ok.md", reason: "r"},
			wantErr: []string{"heredoc-ok.md", "neither a clause nor a pattern"},
		},
		{
			name: "absent clause that is absent passes",
			c:    clauseContract{fragment: "scope.md", clause: "force-push", absent: true, reason: "r"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkClauseContract(tt.c, renders)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want error, got nil")
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q lacks %q", err, s)
				}
			}
		})
	}
}

func TestCheckTwinContract(t *testing.T) {
	renders := renderedFragments{
		"cell-a": {
			"twin-a.md":   {"intro A\n\nShared one two\nthree four. Tail."},
			"clause-a.md": {"It logs lines instead. Shared body."},
			"all-a.md":    {"go FILE ISSUES, then FILE ISSUES, stop."},
			"end-a.md":    {"pre START mid never the run."},
			"end-long.md": {"pre START mid never the run. extra"},
			"cut-a.md":    {"head\n\nShared body."},
		},
		"cell-b": {
			"twin-b.md":    {"intro B\n\nShared one two three four. Tail."},
			"twin-bad.md":  {"intro B\n\nShared one two three CHANGED. Tail."},
			"clause-b.md":  {"It sends sockets instead. Shared body."},
			"clause-2x.md": {"It sends sockets instead. It sends sockets instead. Shared body."},
			"all-b.md":     {"go then stop."},
			"cut-b.md":     {"head\n\nSend para here\nmore.\n\nShared body."},
			"end-b.md":     {"x START mid never the run. Different tail."},
		},
	}
	tests := []struct {
		name    string
		c       twinContract
		wantErr []string
	}{
		{
			name: "equal after re-wrap passes across cells",
			c:    twinContract{a: "twin-a.md", b: "twin-b.md", from: "Shared", reason: "r"},
		},
		{
			name:    "one-sided reword names both fragments, the reason and the divergence",
			c:       twinContract{a: "twin-a.md", b: "twin-bad.md", from: "Shared", reason: "because of issue #2"},
			wantErr: []string{"twin-a.md", "twin-bad.md", "because of issue #2", "diverge", "three four", "three CHANGED"},
		},
		{
			name: "text outside the scope is not compared",
			c:    twinContract{a: "twin-a.md", b: "twin-bad.md", from: "Shared", through: "three", reason: "r"},
		},
		{
			name:    "missing scope start fails",
			c:       twinContract{a: "twin-a.md", b: "twin-b.md", from: "nowhere", reason: "r"},
			wantErr: []string{"scope start", "nowhere"},
		},
		{
			name:    "missing scope end fails",
			c:       twinContract{a: "twin-a.md", b: "twin-b.md", from: "Shared", through: "nowhere", reason: "r"},
			wantErr: []string{"scope end", "nowhere"},
		},
		{
			name: "per-side substitution passes at the exact count",
			c: twinContract{a: "clause-a.md", b: "clause-b.md", reason: "r",
				editA: []twinEdit{{"It logs lines instead", "<m>", 1}},
				editB: []twinEdit{{"It sends sockets instead", "<m>", 1}}},
		},
		{
			name: "substitution count mismatch fails",
			c: twinContract{a: "clause-a.md", b: "clause-2x.md", reason: "r",
				editA: []twinEdit{{"It logs lines instead", "<m>", 1}},
				editB: []twinEdit{{"It sends sockets instead", "<m>", 1}}},
			wantErr: []string{"clause-2x.md", "It sends sockets instead", "exactly 1", "found 2"},
		},
		{
			name: "substitution that never matches fails",
			c: twinContract{a: "clause-a.md", b: "clause-b.md", reason: "r",
				editA: []twinEdit{{"absent words", "<m>", 1}}},
			wantErr: []string{"clause-a.md", "absent words", "found 0"},
		},
		{
			name: "replace-all substitution passes",
			c: twinContract{a: "all-a.md", b: "all-b.md", reason: "r",
				editA: []twinEdit{{"FILE ISSUES, ", "", 0}}},
		},
		{
			name: "replace-all that matches nothing fails",
			c: twinContract{a: "all-b.md", b: "all-b.md", reason: "r",
				editA: []twinEdit{{"FILE ISSUES, ", "", 0}}},
			wantErr: []string{"FILE ISSUES", "found 0"},
		},
		{
			name: "paragraph cut from b before comparing passes",
			c:    twinContract{a: "cut-a.md", b: "cut-b.md", cutB: "Send para", reason: "r"},
		},
		{
			name:    "cut marker missing fails",
			c:       twinContract{a: "cut-a.md", b: "cut-b.md", cutB: "no such para", reason: "r"},
			wantErr: []string{"cut-b.md", "no such para"},
		},
		{
			name: "nothing follows the end marker passes",
			c:    twinContract{a: "end-a.md", b: "end-b.md", from: "START", through: "never the run.", endsA: true, reason: "r"},
		},
		{
			name:    "text after the end marker in a fails",
			c:       twinContract{a: "end-long.md", b: "end-b.md", from: "START", through: "never the run.", endsA: true, reason: "r"},
			wantErr: []string{"end-long.md", "text follows", "never the run."},
		},
		{
			name:    "never-rendered twin fails naming it",
			c:       twinContract{a: "twin-a.md", b: "nope.md", reason: "r"},
			wantErr: []string{"nope.md", "renders in no checked prompt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkTwinContract(tt.c, renders)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want error, got nil")
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q lacks %q", err, s)
				}
			}
		})
	}
}

// normalizeEdit normalizes an edit string like the text it rewrites, but keeps
// an edge space as one space, since "FILE ISSUES, " must swallow the space
// that follows it.
func normalizeEdit(s string) string {
	n := normalizeWhitespace(s)
	if n == "" {
		return n
	}
	if strings.TrimLeft(s, " \t\r\n") != s {
		n = " " + n
	}
	if strings.TrimRight(s, " \t\r\n") != s {
		n += " "
	}
	return n
}
