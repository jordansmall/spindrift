package local

import "testing"

func TestResolveParent_UsesFrontmatterParentWhenSet(t *testing.T) {
	if got, want := ResolveParent("42", "Calc Engine").String(), "calc-engine"; got != want {
		t.Errorf("ResolveParent(42, Calc Engine) = %q, want %q", got, want)
	}
}

func TestResolveParent_FallsBackToOwnSlugWhenUnset(t *testing.T) {
	if got, want := ResolveParent("01-calc-add", "").String(), "01-calc-add"; got != want {
		t.Errorf("ResolveParent(01-calc-add, \"\") = %q, want %q", got, want)
	}
}

// TestResolveParent_FallsBackToOwnSlugWhenParentSanitizesToEmpty verifies a
// parent: value made entirely of non-[a-z0-9] characters (e.g. "///") — which
// SanitizeParent collapses to "" — falls back to the issue's own sanitized
// slug instead of silently producing "integration/" (no parent segment at
// all).
func TestResolveParent_FallsBackToOwnSlugWhenParentSanitizesToEmpty(t *testing.T) {
	if got, want := ResolveParent("42", "///").String(), "42"; got != want {
		t.Errorf("ResolveParent(42, \"///\") = %q, want %q", got, want)
	}
}

func TestSanitizeParent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"jordansmall/spindrift#1694", "jordansmall-spindrift-1694"},
		{"Calc Engine", "calc-engine"},
		{"01-calc-add", "01-calc-add"},
	}
	for _, c := range cases {
		if got := SanitizeParent(c.in); got != c.want {
			t.Errorf("SanitizeParent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
