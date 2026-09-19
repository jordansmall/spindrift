// Package registrypathset derives the set of URL subtrees a registry proxy may
// forward to each upstream host from a dispatch snapshot's committed config
// files, and nothing else: no ambient default, environment variable, or network
// probe can add a path, so absence of declaration is absence of binding. It
// stays separate from registrydiscover, whose output is host-to-credential.
package registrypathset

import (
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/registryvocab"
)

// HostPathSet is every subtree one upstream host serves. Host is
// registryvocab.HostKey-normalized so it compares equal to a registry route's
// match-host; Origin keeps the scheme and the port, since it names the upstream
// to reach rather than the key to match on.
type HostPathSet struct {
	Host     string
	Origin   string
	Subtrees []registryvocab.Subtree
}

// dedupeKey omits RegistryName because registryvocab.PathSet.Admits keys on
// Path alone, so two cargo registry names sharing one index URL are
// path-set-equivalent and the surviving name only labels the declaration.
type dedupeKey struct {
	Host      string
	Ecosystem string
	Path      string
}

// Derive scans repoDir's committed config files and returns one HostPathSet per
// declared host, keeping one subtree per declaration: an Artifactory-shaped repo
// declaring an internal and a remote cargo registry on one host serves both
// index subtrees. Hosts come out in first-declaration order, subtrees in
// declaration order. go and gradle declare no in-tree path, so derive nothing.
func Derive(repoDir string) ([]HostPathSet, error) {
	declared, _, err := registrydiscover.Extract(repoDir)
	if err != nil {
		return nil, err
	}

	var out []HostPathSet
	index := make(map[string]int, len(declared))
	seen := make(map[dedupeKey]bool, len(declared))
	for _, d := range declared {
		u, err := url.Parse(d.UpstreamBaseURL)
		if err != nil {
			// Extract only emits absolute http(s) URLs, so this is unreachable;
			// skipping rather than erroring keeps one malformed declaration from
			// voiding the whole path-set if that ever stops holding.
			continue
		}
		host := registryvocab.HostKey(d.Host)
		subtree := registryvocab.Subtree{
			Ecosystem:    d.Ecosystem,
			Path:         normalizePath(u.Path),
			RegistryName: d.RegistryName,
		}

		key := dedupeKey{Host: host, Ecosystem: subtree.Ecosystem, Path: subtree.Path}
		if seen[key] {
			continue
		}
		seen[key] = true

		// A later declaration disagreeing on scheme or port joins this same host
		// entry rather than splitting it: the path-set is enforced against the
		// HostKey-normalized host a route matched on, so a second entry for one
		// host would be unreachable.
		i, ok := index[host]
		if !ok {
			out = append(out, HostPathSet{Host: host, Origin: u.Scheme + "://" + u.Host})
			i = len(out) - 1
			index[host] = i
		}
		out[i].Subtrees = append(out[i].Subtrees, subtree)
	}

	return out, nil
}

// normalizePath renders a declaration's URL path as a subtree root: a leading
// "/", no trailing "/", and an empty path (a bare-host declaration) as "/", so
// a registry declared with no path at all covers the whole host.
func normalizePath(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}
