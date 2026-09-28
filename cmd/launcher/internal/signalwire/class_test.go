package signalwire

import (
	"fmt"
	"strings"
	"testing"
)

// TestValidClass pins the one Chore class slug grammar (issue #3986):
// lowercase letter or digit first, then lowercase letters, digits, and
// hyphens, at most 40 characters.
func TestValidClass(t *testing.T) {
	cases := []struct {
		name  string
		class string
		want  bool
	}{
		{"plain slug", "error-handling", true},
		{"another plain slug", "dead-code", true},
		{"single char", "a", true},
		{"letter then digit", "a1", true},
		{"digit first", "4xx", true},
		{"trailing hyphen accepted", "error-", true},
		{"double hyphen accepted", "a--b", true},
		{"exactly max length", strings.Repeat("a", MaxClassLen), true},
		{"empty rejected", "", false},
		{"uppercase rejected", "Error_Handling", false},
		{"underscore rejected", "error_handling", false},
		{"leading hyphen rejected", "-lead", false},
		{"over max length rejected", strings.Repeat("a", MaxClassLen+1), false},
		{"embedded space rejected", "a b", false},
		{"mixed case with hyphen rejected", "Error-Handling", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidClass(tc.class); got != tc.want {
				t.Errorf("ValidClass(%q) = %v, want %v", tc.class, got, tc.want)
			}
		})
	}
}

// TestClassRuleMatchesMaxClassLen pins the "at most N characters" prose in
// ClassRule to MaxClassLen, so widening one without the other fails here.
func TestClassRuleMatchesMaxClassLen(t *testing.T) {
	want := fmt.Sprintf("at most %d characters", MaxClassLen)
	if !strings.Contains(ClassRule, want) {
		t.Errorf("ClassRule = %q, want it to contain %q", ClassRule, want)
	}
}
