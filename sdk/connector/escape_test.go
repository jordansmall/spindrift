//go:build !wasip1

package connector

import (
	"net/url"
	"testing"
)

func TestPathSegment(t *testing.T) {
	tests := []struct {
		name, in, want string
		refused        bool
	}{
		{"plain", "main", "main", false},
		{"empty", "", "", true},
		{"dot", ".", "", true},
		{"dot-dot", "..", "", true},
		{"slash", "/", "", true},
		{"inner slash", "a/b", "", true},
		{"semicolon", "a;b", "", true},
		{"dots inside", "a..b", "a..b", false},
		{"three dots", "...", "...", false},
		{"percent", "100%", "100%25", false},
		{"already escaped", "a%2Fb", "a%252Fb", false},
		{"accented", "é", "%C3%A9", false},
		{"cjk", "日本", "%E6%97%A5%E6%9C%AC", false},
		{"comma stays literal", "a,b", "a,b", false},
		{"space", "a b", "a%20b", false},
		{"question mark", "a?b", "a%3Fb", false},
		{"hash", "a#b", "a%23b", false},
		{"backslash", `a\b`, "a%5Cb", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PathSegment(tc.in)
			if tc.refused {
				if err == nil {
					t.Fatalf("PathSegment(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("PathSegment(%q) = %q, %v, want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

// Every accepted segment must reach the host canonical: url.Parse keeps a
// RawPath for any escaping it would not itself produce, and the host refuses
// that.
func TestPathSegmentIsCanonical(t *testing.T) {
	for _, in := range []string{
		"a", "a,b", "a b", "a?b", "a#b", "100%", "é", "日本", "a:b", "a@b", "a=b",
		"a&b", "a+b", "a$b", "a!b", "a*b", "a(b)", "a'b", `a"b`, "a<b>", "a[b]",
		"a{b}", "a|b", "a^b", "a`b", "a~b", "a\tb", "a\x00b", "a\x7fb", "\xff",
		"%2F", "%3b", "...", "a..",
	} {
		out, err := PathSegment(in)
		if err != nil {
			t.Errorf("PathSegment(%q): %v", in, err)
			continue
		}
		u, err := url.Parse("https://h/x/" + out)
		if err != nil {
			t.Errorf("PathSegment(%q) = %q: %v", in, out, err)
			continue
		}
		if u.RawPath != "" || u.Path != "/x/"+in {
			t.Errorf("PathSegment(%q) = %q parses to Path %q RawPath %q", in, out, u.Path, u.RawPath)
		}
	}
}
