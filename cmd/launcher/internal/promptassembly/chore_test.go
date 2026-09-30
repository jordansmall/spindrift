package promptassembly

import "testing"

func TestValidChoreName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"triage", true},
		{"stale-docs", true},
		{"a_b1", true},
		{"", false},
		{"../x", false},
		{"a/b", false},
		{"a b", false},
		{"..", false},
		{"-legacy", false},
		{"-", false},
		{"legacy-", true},
		{"a-b", true},
	}
	for _, c := range cases {
		if got := ValidChoreName(c.in); got != c.want {
			t.Errorf("ValidChoreName(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestValidChoreNameMatchesRule pins choreNameRe to the set ChoreNameRule
// names, so widening one without the other fails here.
func TestValidChoreNameMatchesRule(t *testing.T) {
	for b := 0; b < 128; b++ {
		c := byte(b)
		// A lone '-' is a leading dash, so the rule forbids it as a whole name.
		want := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_'
		if got := ValidChoreName(string(c)); got != want {
			t.Errorf("ValidChoreName(%q) = %v, but ChoreNameRule %q implies %v", c, got, ChoreNameRule, want)
		}
	}
}
