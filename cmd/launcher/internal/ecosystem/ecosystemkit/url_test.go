package ecosystemkit

import "testing"

func TestHTTPAbsoluteURL(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantHost string
		wantBase string
		wantOK   bool
	}{
		{"https", "https://registry.example.com/repo", "registry.example.com", "https://registry.example.com/repo", true},
		{"http with port", "http://registry.example.com:8080/repo", "registry.example.com:8080", "http://registry.example.com:8080/repo", true},
		{"trailing slash trimmed", "https://registry.example.com/repo/", "registry.example.com", "https://registry.example.com/repo", true},
		{"bare origin trailing slash trimmed", "https://registry.example.com/", "registry.example.com", "https://registry.example.com", true},
		{"userinfo rejected", "https://user:pass@registry.example.com/", "", "", false},
		{"user only rejected", "https://user@registry.example.com/", "", "", false},
		{"port-only host rejected", "http://:8080/", "", "", false},
		{"relative rejected", "/repo", "", "", false},
		{"schemeless host rejected", "registry.example.com/repo", "", "", false},
		{"non-http scheme rejected", "ftp://registry.example.com/", "", "", false},
		{"empty rejected", "", "", "", false},
		{"unparsable rejected", "http://[::1", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, base, ok := HTTPAbsoluteURL(tc.raw)
			if ok != tc.wantOK || host != tc.wantHost || base != tc.wantBase {
				t.Errorf("HTTPAbsoluteURL(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.raw, host, base, ok, tc.wantHost, tc.wantBase, tc.wantOK)
			}
		})
	}
}
