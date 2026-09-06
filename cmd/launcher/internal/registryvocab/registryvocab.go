// Package registryvocab is the one vocabulary every hop of the registry
// pipeline -- routes, path-set derivation, the proxy, and the Box-facing
// manifest (ADR 0045) -- shares: a host key, a tagged subtree, a path-set
// admission rule, a header-field-name check, and a response-rewrite row.
// Before this package (ADR 0048, issue #3398) each hop kept its own copy of
// these, so a fix or a format change to one had to be repeated by hand at
// every copy or the hops would silently drift apart. It stays
// dependency-free (stdlib only) so any registry package -- including
// registryproxy, which must never import registrypathset -- can import it
// without pulling in anything else.
package registryvocab

import (
	"net"
	"path"
	"strings"
)

// HostKey lowercases hostport and strips any ":port" suffix, so a route's
// match-host and a derived path-set's host compare equal regardless of case
// or an explicit default port. IPv6 brackets are stripped only on the
// no-port path, since net.SplitHostPort already strips them when a port is
// present -- otherwise "[::1]" and "[::1]:443" would key differently even
// though they name the same host.
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

// Subtree is one ecosystem-tagged URL subtree a host serves. The JSON tags
// are the manifest's own wire shape (ADR 0045) and must not change; adding a
// field here without json:"-" would change every manifest byte for byte.
// CargoRegistryName carries the [registries.<name>] key the subtree came
// from (cargo only, else ""), which tells two cargo registries on one host
// apart when their index paths are otherwise interchangeable -- it is
// excluded from JSON because the manifest never needed it.
type Subtree struct {
	Ecosystem         string `json:"ecosystem"` // an ecosystem.Table row's Name: "cargo" | "npm" | "yarn" | "pnpm" | "go" | "gradle"
	Path              string `json:"path"`      // leading "/", no trailing "/"; "/" is the whole host
	CargoRegistryName string `json:"-"`
}

// PathSet is a set of subtree roots, in the membership sense Admits defines.
type PathSet []string

// Admits reports whether requestPath falls inside any root in s, by a
// segment-boundary rule: a root matches only itself or a path with the root
// plus "/" as its prefix, so "/index" admits "/index/config.json" but not
// "/indexfoo", and a "/" entry admits everything. requestPath is cleaned
// first so a traversal such as "/index/../../api/token" is judged as the
// "/api/token" it resolves to, not the "/index" prefix it appears to start
// under; anything that isn't an absolute path after cleaning is refused, and
// an empty set admits nothing (fails closed). requestPath must be the
// decoded path (an http.Request's URL.Path, not its EscapedPath()), since
// path.Clean does not percent-decode and an escaped "%2e%2e" traversal
// would survive cleaning intact and be wrongly admitted.
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

// IsValidHeaderFieldName reports whether name is a valid RFC 7230 "token":
// one or more of the allowed token characters, no separators, no CR/LF --
// hand-rolled so a crafted Name can't smuggle a header injection (CRLF) past
// validation and into a 502 at request time when Go's http layer rejects it.
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
