package ecosystemkit

import (
	"net/url"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// TestRepointRegistryURL_PreservesQueryAndFragment covers what neither
// Strategy's own tests reach: a registry value carrying a query, a fragment or a
// percent-escaped path segment keeps all three verbatim across the
// re-point, since only the prefix goes on in front of the path.
func TestRepointRegistryURL_PreservesQueryAndFragment(t *testing.T) {
	rc := registryvocab.RewriteContext{
		MatchHost: "registry.example.com",
		Forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
		Prefix:    "r0",
	}

	cases := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "query preserved",
			value: "https://registry.example.com/pkg/-/pkg-1.0.0.tgz?token=abc",
			want:  "http://127.0.0.1:9999/r0/pkg/-/pkg-1.0.0.tgz?token=abc",
		},
		{
			name:  "fragment preserved",
			value: "https://registry.example.com/pkg/-/pkg-1.0.0.tgz#sha512",
			want:  "http://127.0.0.1:9999/r0/pkg/-/pkg-1.0.0.tgz#sha512",
		},
		{
			name:  "escaped path segment preserved",
			value: "https://registry.example.com/%40scope%2Fpkg/-/pkg-1.0.0.tgz",
			want:  "http://127.0.0.1:9999/r0/%40scope%2Fpkg/-/pkg-1.0.0.tgz",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edit, ok := RepointRegistryURL(tc.value, rc)
			if !ok {
				t.Fatalf("RepointRegistryURL(%q) reported the value unusable", tc.value)
			}
			if edit.To != tc.want {
				t.Errorf("To = %q, want %q", edit.To, tc.want)
			}
		})
	}
}

// TestDecodeOneJSONObject_NonObjectValues covers the top-level shapes no
// Strategy's own test body reaches: a well-formed JSON value that simply isn't
// an object is refused outright, as is an empty body.
func TestDecodeOneJSONObject_NonObjectValues(t *testing.T) {
	for _, body := range []string{`[1,2,3]`, `"a string"`, `42`, `true`, ``} {
		if obj, ok := DecodeOneJSONObject([]byte(body)); ok {
			t.Errorf("DecodeOneJSONObject(%q) = %v, true; want ok=false", body, obj)
		}
	}
}

// TestNormalizeHostPort pins the comparison RepointRegistryURL's open-relay
// check runs through: only the scheme's own default port is dropped, and the
// host is lowercased either way.
func TestNormalizeHostPort(t *testing.T) {
	cases := []struct {
		name, hostport, scheme, want string
	}{
		{"bare host lowercased", "Registry.Example.com", "https", "registry.example.com"},
		{"https default port stripped", "registry.example.com:443", "https", "registry.example.com"},
		{"http default port stripped", "registry.example.com:80", "http", "registry.example.com"},
		{"scheme case ignored", "Registry.Example.com:443", "HTTPS", "registry.example.com"},
		{"https port kept over http", "registry.example.com:443", "http", "registry.example.com:443"},
		{"http port kept over https", "registry.example.com:80", "https", "registry.example.com:80"},
		{"non-default port kept", "Registry.Example.com:8080", "https", "registry.example.com:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeHostPort(tc.hostport, tc.scheme); got != tc.want {
				t.Errorf("normalizeHostPort(%q, %q) = %q, want %q", tc.hostport, tc.scheme, got, tc.want)
			}
		})
	}
}
