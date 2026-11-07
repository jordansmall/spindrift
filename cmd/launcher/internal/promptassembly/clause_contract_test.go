package promptassembly_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// clauseContract is a named obligation on one prompt fragment: clause is
// prose the fragment's rendered text must still say (or, with absent, must
// not say), and reason is why. from and to optionally scope the match to the
// text after from and before to within the fragment's span.
type clauseContract struct {
	fragment string
	clause   string
	reason   string
	from, to string
	absent   bool
	verbatim bool // for clauses whose whitespace is the obligation
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

// checkClauseContract checks c against every span of c.fragment in every
// cell. A fragment that renders in no cell fails, so a contract cannot go
// vacuous when a fragment is gated out of every checked prompt.
func checkClauseContract(c clauseContract, renders renderedFragments) error {
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
				return fmt.Errorf("fragment %s, clause %q (%s): cell %s: %w", c.fragment, c.clause, c.reason, cell, err)
			}
		}
	}
	if !rendered {
		return fmt.Errorf("fragment %s, clause %q (%s): fragment renders in no checked prompt", c.fragment, c.clause, c.reason)
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
	switch {
	case c.absent && has:
		return fmt.Errorf("clause is present but must be absent")
	case !c.absent && !has:
		return fmt.Errorf("clause is missing")
	}
	return nil
}

func TestCheckClauseContract(t *testing.T) {
	const rewrapped = "fold each fix into the\n  commit it belongs to, then\nkernel-\n  level stop"
	renders := renderedFragments{
		"cell-a": {"wrap.md": {rewrapped}, "scope.md": {"alpha beta MARK gamma"}},
		"cell-b": {"wrap.md": {rewrapped + " again"}, "scope.md": {"alpha  beta\nMARK gamma"}},
		"cell-c": {"delim-ok.md": {"a\nX\nb"}, "delim-indented.md": {"a\n  X\nb"}},
	}
	tests := []struct {
		name    string
		c       clauseContract
		wantErr []string
	}{
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
