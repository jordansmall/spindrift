package registryproxy

import "regexp"

// binding is one ecosystem's entry in the path-allowlist table (ADR 0044):
// "each ecosystem is a small table entry, not a parser." v1 ships cargo
// alone; a future ecosystem is added as another entry in bindings, not a
// rewrite of this file.
type binding struct {
	ecosystem string
	patterns  []*regexp.Regexp
}

// cargoSparseIndexPatterns are the sparse-index path shapes cargo's
// registry protocol defines (crate names are [A-Za-z0-9_-]+):
// https://doc.rust-lang.org/cargo/reference/registries.html#index-format
//
// Deliberately absent: the download/artifact endpoint. Its path is
// registry-specific, named by config.json's own "dl" field rather than a
// fixed shape, so it can't be statically derived here.
var cargoSparseIndexPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^/config\.json$`),
	regexp.MustCompile(`^/1/[A-Za-z0-9_-]$`),
	regexp.MustCompile(`^/2/[A-Za-z0-9_-]{2}$`),
	regexp.MustCompile(`^/3/[A-Za-z0-9_-]/[A-Za-z0-9_-]{3}$`),
	regexp.MustCompile(`^/[A-Za-z0-9_-]{2}/[A-Za-z0-9_-]{2}/[A-Za-z0-9_-]{4,}$`),
}

var bindings = []binding{
	{
		ecosystem: "cargo",
		patterns:  cargoSparseIndexPatterns,
	},
}

// isAllowedPath reports whether path matches any ecosystem's path patterns
// in the binding table.
func isAllowedPath(path string) bool {
	for _, b := range bindings {
		for _, p := range b.patterns {
			if p.MatchString(path) {
				return true
			}
		}
	}
	return false
}
