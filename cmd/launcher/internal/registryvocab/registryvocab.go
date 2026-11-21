// Package registryvocab holds the vocabulary shared by every hop of the
// registry pipeline: routes, path-set derivation, the proxy, and the Box-facing
// manifest (ADR 0045), held once so the hops cannot drift (ADR 0048, issue
// #3398). It stays stdlib-only so registryproxy, which must never import
// registrypathset, can still import it.
package registryvocab

import (
	"errors"
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
// [registries.<name>] key, else "".
type Subtree struct {
	Ecosystem    string `json:"ecosystem"` // a registered Strategy's Name: "cargo" | "npm" | "yarn" | "pnpm" | "go" | "gradle"
	Path         string `json:"path"`      // leading "/", no trailing "/"; "/" is the whole host
	RegistryName string `json:"-"`
	// Scoped marks a path only an npm-family scoped declaration (`@acme:registry=`)
	// names; only the npm-family binding reads it.
	Scoped bool `json:"scoped,omitempty"`
}

// PathSet is a set of subtree roots, in the membership sense Admits defines.
type PathSet []string

// Admits reports whether requestPath falls inside any root in s: the root
// itself or a path under root+"/", so "/index" admits "/index/config.json"
// but not "/indexfoo"; "/" admits every other path. A path containing ";" is
// refused whatever s holds: servlet containers (Tomcat, older Jetty) strip ";"
// path parameters per segment before normalizing, so "/index/..;/x" would
// resolve outside the root after path.Clean admitted it (Artifactory
// ";prop=value" matrix params, which clients are not expected to send, are
// refused too). requestPath must be the decoded path (URL.Path), since
// path.Clean leaves "%2e%2e" intact.
func (s PathSet) Admits(requestPath string) bool {
	if strings.Contains(requestPath, ";") {
		return false
	}
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

// Reasons CheckPathPattern refuses a pattern; callers wrap them with their own
// context and test with errors.Is.
var (
	ErrPatternURLDelimiter = errors.New(`contains "?" or "#"`)
	ErrPatternNotCanonical = errors.New(`not an absolute path already in canonical form (leading "/", no trailing "/", no "." or ".." segment)`)
	ErrPatternBlanketRoot  = errors.New(`"/" would blanket-authorize the whole host`)
)

// HasURLDelimiter reports whether s contains "?" or "#" (issue #3321): they
// open a URL's query and fragment, so no request path carries them, and a
// declared path spliced into a GOPROXY or Gradle URL would be cut short at them.
func HasURLDelimiter(s string) bool {
	return strings.ContainsAny(s, "?#")
}

// CheckPathPattern rejects a path pattern not already in the canonical
// subtree-root form PathSet.Admits matches (ADR 0047, issue #3258). It checks
// with path.Clean rather than normalizing, so a mistyped pattern fails at parse
// time instead of silently mismatching a request path. "/" is rejected too:
// Admits reads it as "admit every path", the off switch ADR 0047 bans. "?" and
// "#" are checked first (HasURLDelimiter): path.Clean keeps them.
func CheckPathPattern(p string) error {
	if HasURLDelimiter(p) {
		return ErrPatternURLDelimiter
	}
	if p == "" || path.Clean(p) != p || !strings.HasPrefix(p, "/") {
		return ErrPatternNotCanonical
	}
	if p == "/" {
		return ErrPatternBlanketRoot
	}
	return nil
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
