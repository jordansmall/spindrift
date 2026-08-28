package registryproxy

import "regexp"

// binding is one ecosystem's entry in the path-allowlist table (ADR 0044):
// "each ecosystem is a small table entry, not a parser." v1 ships cargo and
// go; a future ecosystem is added as another entry in bindings, not a
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

// goModulePathPrefix is the shared "one-or-more slash-separated module-path
// segments" prefix of all five goproxy-protocol path shapes below, factored
// into one constant so a fix to the segment class only needs to change in
// one place.
//
// The segment class (letters/digits/./-/_ plus "!" and "~") covers both the
// spec's case-encoding rule, which escapes an originally-uppercase letter as
// "!" followed by its lowercase form (e.g. "!google-cloud" for
// "Google-Cloud") so the module path stays safe on case-insensitive
// filesystems, and "~", which module.CheckPath accepts as a legal import-path
// character but the case-encoding rule doesn't otherwise cover. RE2
// (regexp's engine) has no lookahead, so a dots-only segment (e.g. "..") is
// excluded by requiring at least one non-dot character rather than by
// negating "..": every segment must contain a letter, digit, "!", "~", or
// "-" somewhere, which a pure run of dots never does.
const goModulePathPrefix = `^/(?:(?:[A-Za-z0-9._!~-]*[A-Za-z0-9!~-][A-Za-z0-9._!~-]*)/)+`

// goModuleVersion is the version segment class used by the @v/<version>.ext
// shapes. It includes "!" because goproxy case-encodes uppercase letters in
// versions the same way it does module paths (e.g. "v1.0.0-!r!c1" for
// "v1.0.0-RC1").
const goModuleVersion = `[A-Za-z0-9.+!-]+`

// goModulePatterns are the path shapes the Go module proxy protocol defines:
// https://go.dev/ref/mod#goproxy-protocol
//
// Deliberately absent: the checksum database endpoints (/sumdb/...). Those
// belong to GOSUMDB, a separate protocol namespace from GOPROXY's
// module-fetch shape modeled here.
var goModulePatterns = []*regexp.Regexp{
	regexp.MustCompile(goModulePathPrefix + `@v/list$`),
	regexp.MustCompile(goModulePathPrefix + `@latest$`),
	regexp.MustCompile(goModulePathPrefix + `@v/` + goModuleVersion + `\.info$`),
	regexp.MustCompile(goModulePathPrefix + `@v/` + goModuleVersion + `\.mod$`),
	regexp.MustCompile(goModulePathPrefix + `@v/` + goModuleVersion + `\.zip$`),
}

var bindings = []binding{
	{
		ecosystem: "cargo",
		patterns:  cargoSparseIndexPatterns,
	},
	{
		ecosystem: "go",
		patterns:  goModulePatterns,
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
