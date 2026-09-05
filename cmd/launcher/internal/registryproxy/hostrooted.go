package registryproxy

import (
	"path"
	"strings"
)

// pathSetAdmits reports whether requestPath falls inside any subtree named in
// enforcedPaths, by the same segment-boundary membership rule as
// registrypathset.HostPathSet.Admits (registrypathset/pathset.go): a subtree
// root matches only itself or a path with the root plus "/" as its prefix, so
// a declared "/index" admits "/index/config.json" but not "/indexfoo", and a
// "/" entry admits everything. This is a local copy rather than an import --
// registrypathset's package doc explains why registryproxy must never import
// it (it would pull discovery's config parsers in transitively). requestPath
// is cleaned first for the same reason Admits cleans it: a traversal such as
// "/index/../../api/token" must be judged as the path it resolves to, not the
// "/index" prefix it appears to start under.
func pathSetAdmits(enforcedPaths []string, requestPath string) bool {
	cleaned := path.Clean(requestPath)
	if !strings.HasPrefix(cleaned, "/") {
		return false
	}
	for _, sub := range enforcedPaths {
		if sub == "/" {
			return true
		}
		if cleaned == sub || strings.HasPrefix(cleaned, sub+"/") {
			return true
		}
	}
	return false
}
