// Package registrypathset derives the enforced path-set -- the set of URL
// subtrees a registry proxy may forward to each upstream host -- from a
// dispatch snapshot's own committed config files, and nothing else.
//
// The derivation reads only what registrydiscover.Extract already found in the
// repo tree, so the rule "absence of declaration is absence of binding" holds
// by construction: a snapshot that names no npm registry derives no npm path,
// and there is no ambient default, environment variable, or network probe that
// could add one behind the operator's back.
//
// It lives outside registrydiscover because that package's output is
// host-to-credential (one route per unique host) while this one is
// host-to-subtrees, and outside registryproxy so the proxy never has to import
// discovery's config parsers to learn what it may forward.
package registrypathset

import (
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/registryvocab"
)

// HostPathSet is every subtree one upstream host serves. Host is
// registryvocab.HostKey-normalized so it compares equal to a registry
// route's match-host; Origin keeps the scheme and the port, since it names
// the upstream to reach rather than the key to match on.
type HostPathSet struct {
	Host     string
	Origin   string
	Subtrees []registryvocab.Subtree
}

// dedupeKey identifies a subtree within a host for Derive's exact-repeat drop.
// It omits CargoRegistryName because registryvocab.PathSet.Admits keys on
// Path alone, so two cargo registry names sharing one index URL are
// path-set-equivalent -- the surviving name only labels which declaration
// the subtree came from, as Derive's doc comment records.
type dedupeKey struct {
	Host      string
	Ecosystem string
	Path      string
}

// Derive scans repoDir's committed config files and returns one HostPathSet
// per declared host. The snapshot directory is the entire input.
//
// Unlike registrydiscover.Discover, which keeps only the first declaration per
// host because a host has one credential, this keeps one subtree per
// declaration: an Artifactory-shaped repo declaring an internal and a remote
// cargo registry on one host serves both index subtrees, and dropping the
// second would make the derived set refuse crates the repo legitimately
// resolves. Only an exact (ecosystem, path) repeat within a host is dropped,
// since that adds no subtree. The dedupe key omits CargoRegistryName, so two
// cargo registry names sharing one index URL also collapse to one Subtree,
// keeping the first name: registryvocab.PathSet.Admits keys on Path alone,
// so the surviving name labels which declaration the subtree came from and
// nothing more.
//
// A host's Origin is the first declaration's; a later declaration disagreeing
// on scheme or port still contributes its subtrees to that same host entry
// rather than splitting it, because the path-set is enforced against the
// registryvocab.HostKey-normalized host a route matched on, and two entries
// for one host would leave the second unreachable.
//
// Order is deterministic given the tree: hosts in first-declaration order,
// subtrees in declaration order within a host, over Extract's own deterministic
// order. Nothing here iterates a map.
//
// Ecosystems with no committed config file to read derive nothing: go, whose
// path lives on the route rather than in the repo, and gradle, which has no
// ecosystem.Table in-tree config path and so declares nothing this scan can
// see.
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
			// Extract only emits declarations whose URL already parsed as an
			// absolute http(s) URL, so this is unreachable; skipping rather
			// than erroring keeps one malformed declaration from voiding the
			// whole path-set if that ever stops holding.
			continue
		}
		host := registryvocab.HostKey(d.Host)
		subtree := registryvocab.Subtree{
			Ecosystem:         d.Ecosystem,
			Path:              normalizePath(u.Path),
			CargoRegistryName: d.CargoRegistryName,
		}

		key := dedupeKey{Host: host, Ecosystem: subtree.Ecosystem, Path: subtree.Path}
		if seen[key] {
			continue
		}
		seen[key] = true

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
// "/", no trailing "/", and the empty path (a bare-host declaration such as
// "https://registry.npmjs.org") as "/", the root subtree -- for a registry
// declared with no path at all, the whole host is the registry.
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
