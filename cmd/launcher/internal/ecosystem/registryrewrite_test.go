package ecosystem

import (
	"net/url"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// TestRepointRegistryURL_PreservesQueryAndFragment covers what neither
// row's own tests reach: a registry value carrying a query, a fragment or a
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
			edit, ok := repointRegistryURL(tc.value, rc)
			if !ok {
				t.Fatalf("repointRegistryURL(%q) reported the value unusable", tc.value)
			}
			if edit.To != tc.want {
				t.Errorf("To = %q, want %q", edit.To, tc.want)
			}
		})
	}
}

// TestDecodeOneJSONObject_NonObjectValues covers the top-level shapes no
// row's own test body reaches: a well-formed JSON value that simply isn't
// an object is refused outright, as is an empty body.
func TestDecodeOneJSONObject_NonObjectValues(t *testing.T) {
	for _, body := range []string{`[1,2,3]`, `"a string"`, `42`, `true`, ``} {
		if obj, ok := decodeOneJSONObject([]byte(body)); ok {
			t.Errorf("decodeOneJSONObject(%q) = %v, true; want ok=false", body, obj)
		}
	}
}
