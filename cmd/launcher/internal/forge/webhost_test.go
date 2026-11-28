package forge

import "testing"

func TestSameHTTPSHost(t *testing.T) {
	for name, tc := range map[string]struct {
		a, b string
		want bool
	}{
		"identical":                {"h.example", "h.example", true},
		"case-insensitive":         {"H.Example", "h.example", true},
		"default port implied":     {"h.example", "h.example:443", true},
		"default port both":        {"h.example:443", "H.example:443", true},
		"zero-padded default port": {"h.example:0443", "h.example", true},
		"empty port":               {"h.example:", "h.example", true},
		"ipv6 default port":        {"[::1]:443", "[::1]", true},
		"ipv6 other port":          {"[::1]:8443", "[::1]", false},
		"non-default vs implied":   {"h.example:8443", "h.example", false},
		"non-default vs default":   {"h.example:8443", "h.example:443", false},
		"non-default both":         {"h.example:8443", "h.example:8443", true},
		"different host":           {"h.example:443", "other.example", false},
		"signed port is not 443":   {"h.example:+443", "h.example", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := SameHTTPSHost(tc.a, tc.b); got != tc.want {
				t.Errorf("SameHTTPSHost(%q, %q) = %t, want %t", tc.a, tc.b, got, tc.want)
			}
			if got := SameHTTPSHost(tc.b, tc.a); got != tc.want {
				t.Errorf("SameHTTPSHost(%q, %q) = %t, want %t", tc.b, tc.a, got, tc.want)
			}
		})
	}
}
