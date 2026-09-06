package registryvocab

import (
	"encoding/json"
	"testing"
)

func TestHostKey(t *testing.T) {
	cases := []struct {
		name     string
		hostport string
		want     string
	}{
		{"lowercases", "Example.COM", "example.com"},
		{"strips port", "Example.COM:443", "example.com"},
		{"bracketed IPv6 with port", "[::1]:443", "::1"},
		{"bracketed IPv6 without port", "[::1]", "::1"},
		{"already lowercase passthrough", "example.com", "example.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HostKey(c.hostport); got != c.want {
				t.Errorf("HostKey(%q) = %q, want %q", c.hostport, got, c.want)
			}
		})
	}
}

func TestPathSet_Admits(t *testing.T) {
	cases := []struct {
		name        string
		set         PathSet
		requestPath string
		want        bool
	}{
		{"exact match", PathSet{"/index"}, "/index", true},
		{"segment boundary child", PathSet{"/index"}, "/index/config.json", true},
		{"segment boundary rejects suffix collision", PathSet{"/index"}, "/indexfoo", false},
		{"nested child", PathSet{"/npm/registry"}, "/npm/registry/pkg/tarball", true},
		{"trailing slash cleans to the root", PathSet{"/index"}, "/index/", true},
		{"dot segment cleans away", PathSet{"/index"}, "/index/./config.json", true},
		{"parent of the root is not admitted", PathSet{"/artifactory/index"}, "/artifactory", false},
		{"traversal resolves before admission", PathSet{"/api/token"}, "/index/../../api/token", true},
		{"traversal escaping the subtree is refused", PathSet{"/index"}, "/index/../../security/token", false},
		{"traversal above root refused", PathSet{"/artifactory/index"}, "/../etc/passwd", false},
		{"relative path refused", PathSet{"/"}, "relative/path", false},
		{"root subtree admits everything", PathSet{"/"}, "/anything/at/all", true},
		{"root subtree admits the root itself", PathSet{"/"}, "/", true},
		{"empty set admits nothing", PathSet{}, "/index", false},
		{"empty set refuses the root itself", PathSet{}, "/", false},
		{"empty set refuses a deep path", PathSet{}, "/api/v1/token", false},
		{"root subtree admits a scoped npm tarball path", PathSet{"/"}, "/@myorg/pkg/-/pkg-1.0.0.tgz", true},
		{"empty path refused", PathSet{"/index"}, "", false},
		{"second root in a multi-root set matches", PathSet{"/npm", "/index"}, "/index/config.json", true},
		{"unrelated path outside a multi-root set is refused", PathSet{"/npm", "/index"}, "/api/token", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.set.Admits(c.requestPath); got != c.want {
				t.Errorf("PathSet(%v).Admits(%q) = %v, want %v", c.set, c.requestPath, got, c.want)
			}
		})
	}
}

func TestIsValidHeaderFieldName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"X-Custom-Header", true},
		{"", false},
		{"has space", false},
		{"has:colon", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsValidHeaderFieldName(c.name); got != c.want {
				t.Errorf("IsValidHeaderFieldName(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}

	// RFC 7230 tchar special characters, each valid alone as a field name.
	for _, c := range "!#$%&'*+-.^_`|~" {
		name := string(c)
		if !IsValidHeaderFieldName(name) {
			t.Errorf("IsValidHeaderFieldName(%q) = false, want true", name)
		}
	}
}

func TestSubtree_JSONRoundTrip(t *testing.T) {
	s := Subtree{Ecosystem: "npm", Path: "/npm", RegistryName: "internal"}

	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"ecosystem":"npm","path":"/npm"}`
	if string(encoded) != want {
		t.Errorf("json.Marshal(%+v) = %s, want %s", s, encoded, want)
	}

	var decoded Subtree
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Ecosystem != s.Ecosystem || decoded.Path != s.Path {
		t.Errorf("round trip = %+v, want Ecosystem/Path of %+v", decoded, s)
	}
	if decoded.RegistryName != "" {
		t.Errorf("round trip RegistryName = %q, want empty (json:\"-\")", decoded.RegistryName)
	}
}
