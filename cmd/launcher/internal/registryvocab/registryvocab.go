// Package registryvocab holds the vocabulary shared by every hop of the
// registry pipeline: routes, path-set derivation, the proxy, and the Box-facing
// manifest (ADR 0045). Each hop kept its own copy until ADR 0048 (issue #3398)
// and they drifted. It stays stdlib-only so registryproxy, which must never
// import registrypathset, can still import it.
package registryvocab

import (
	"net"
	"path"
	"strings"
)

// HostKey lowercases hostport and strips any ":port" suffix so a route's
// match-host and a path-set's host compare equal. Brackets are stripped only on
// the no-port path, since net.SplitHostPort strips them when a port is present,
// and otherwise "[::1]" and "[::1]:443" would key differently.
func HostKey(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
	}
	return strings.ToLower(host)
}

// Subtree is one ecosystem-tagged URL subtree a host serves. The JSON tags are
// the manifest's wire shape (ADR 0045); a new field without json:"-" changes
// every manifest byte for byte. RegistryName tells two sub-registries on one
// host apart when their index paths are interchangeable: cargo's
// [registries.<name>] key, an npm scope, a uv index name, else "".
type Subtree struct {
	Ecosystem    string `json:"ecosystem"` // an ecosystem.Table row's Name: "cargo" | "npm" | "yarn" | "pnpm" | "go" | "gradle"
	Path         string `json:"path"`      // leading "/", no trailing "/"; "/" is the whole host
	RegistryName string `json:"-"`
}

// PathSet is a set of subtree roots, in the membership sense Admits defines.
type PathSet []string

// Admits reports whether requestPath falls inside any root in s. A root matches
// itself or a path prefixed by the root plus "/", so "/index" admits
// "/index/config.json" but not "/indexfoo"; "/" admits everything and an empty
// set admits nothing. requestPath is cleaned first, and must be the decoded path
// (URL.Path, not EscapedPath), since path.Clean leaves an escaped "%2e%2e" intact.
func (s PathSet) Admits(requestPath string) bool {
	cleaned := path.Clean(requestPath)
	if !strings.HasPrefix(cleaned, "/") {
		return false
	}
	for _, sub := range s {
		if sub == "/" {
			return true
		}
		if cleaned == sub || strings.HasPrefix(cleaned, sub+"/") {
			return true
		}
	}
	return false
}

// IsValidHeaderFieldName reports whether name is a valid RFC 7230 token.
// Hand-rolled so a crafted Name cannot smuggle a CRLF header injection past
// validation into a 502 that Go's http layer raises at request time.
func IsValidHeaderFieldName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range []byte(name) {
		if !isTokenChar(c) {
			return false
		}
	}
	return true
}

func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		return true
	default:
		return false
	}
}
