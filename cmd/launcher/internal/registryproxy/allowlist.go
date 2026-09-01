package registryproxy

import "regexp"

// binding is one ecosystem's entry in the shared table (ADR 0044) backing two
// unrelated consumers: the path-allowlist patterns below, and bindregistry's
// lockfile-name lookup. Each ecosystem is a small table entry, not a parser.
type binding struct {
	ecosystem string
	// lockfileNames are the ecosystem's dependency-lockfile filenames,
	// relative to a repo's working directory root -- what Ecosystems' callers
	// need for nudge classification, independent of the patterns below.
	lockfileNames []string
	patterns      []*regexp.Regexp
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
// segments" prefix of all five goproxy-protocol path shapes below.
//
// The segment class covers the spec's case-encoding rule, which escapes an
// originally-uppercase letter as "!" plus its lowercase form (e.g.
// "!google-cloud"), and "~", which module.CheckPath accepts but the
// case-encoding rule doesn't cover. RE2 has no lookahead, so a dots-only
// segment (e.g. "..") is excluded by requiring at least one non-dot character
// rather than by negating "..".
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

// npmNameSegment is the package/scope name segment class shared by every
// npmPackageRegistryPatterns shape below. The registry protocol doesn't
// enforce npm's lowercase-by-convention naming at the URL level, but every
// segment must start with an alphanumeric character -- real npm names can
// never start with "." or "_"
// (https://docs.npmjs.com/cli/v10/configuring-npm/package-json#name), and
// requiring a leading alnum keeps a dot-leading, traversal-shaped segment
// (e.g. "..") from matching the bare-name/name-version shapes below.
const npmNameSegment = `[A-Za-z0-9][A-Za-z0-9._-]*`

// npmTarballFilenameSegment is npmNameSegment widened with "+": a tarball
// filename embeds a semver version, and semver's build-metadata component
// (e.g. "1.0.0+build") legally contains a "+" that a package/scope name
// never does.
const npmTarballFilenameSegment = `[A-Za-z0-9][A-Za-z0-9._+-]*`

// npmPackageRegistryPatterns are the registry path shapes npm's package
// registry protocol defines (unofficially documented at
// https://github.com/npm/registry/blob/main/docs/REGISTRY-API.md): package
// metadata, version-specific metadata, tarball fetches, and the search
// endpoint, each in scoped and unscoped form where applicable. The tarball
// shape is rarely reached through the proxy: npm's client (pacote) fetches
// the packument's embedded "tarball" URL verbatim rather than deriving this
// path from the configured registry, so that request goes straight to
// upstream, unauthenticated -- the accepted gap ADR 0044 documents for a
// redirect-based download.
var npmPackageRegistryPatterns = []*regexp.Regexp{
	// These two bare-name/two-segment shapes also match plenty of paths that
	// aren't real packages (e.g. "/admin", "/login", "/v1/tokens") -- npm's
	// flat namespace gives a real package name and an arbitrary one/two-
	// segment path the same shape, so this is a known, unfixable residual
	// false-positive, and the allowlist-miss log is near-vacuous for paths of
	// this shape as a result.
	regexp.MustCompile(`^/` + npmNameSegment + `$`),
	regexp.MustCompile(`^/` + npmNameSegment + `/` + npmNameSegment + `$`),
	regexp.MustCompile(`^/` + npmNameSegment + `/-/` + npmTarballFilenameSegment + `\.tgz$`),
	regexp.MustCompile(`^/@` + npmNameSegment + `/` + npmNameSegment + `$`),
	regexp.MustCompile(`^/@` + npmNameSegment + `/` + npmNameSegment + `/` + npmNameSegment + `$`),
	regexp.MustCompile(`^/@` + npmNameSegment + `/` + npmNameSegment + `/-/` + npmTarballFilenameSegment + `\.tgz$`),
	regexp.MustCompile(`^/-/v1/search$`),
}

// bindings is the path-allowlist table (ADR 0044) and the shared ecosystem
// table. gradle's row has nil patterns: a Maven/Gradle repository's artifact
// base path is registry-specific (the repository ID/layout configured on
// whatever Nexus/Artifactory serves it) rather than a derivable shape, so
// gradle needs a row for its lockfile names but no allowlist.
//
// Row order matters: Ecosystems' caller walks rows in this order for
// nudge-classification precedence, so it must not change without checking
// that caller. isAllowedPath itself is order-independent.
var bindings = []binding{
	{
		ecosystem:     "cargo",
		lockfileNames: []string{"Cargo.lock"},
		patterns:      cargoSparseIndexPatterns,
	},
	{
		ecosystem:     "npm",
		lockfileNames: []string{"package-lock.json"},
		patterns:      npmPackageRegistryPatterns,
	},
	{
		ecosystem:     "yarn",
		lockfileNames: []string{"yarn.lock"},
		patterns:      npmPackageRegistryPatterns,
	},
	{
		ecosystem:     "pnpm",
		lockfileNames: []string{"pnpm-lock.yaml"},
		patterns:      npmPackageRegistryPatterns,
	},
	{
		ecosystem:     "go",
		lockfileNames: []string{"go.sum"},
		patterns:      goModulePatterns,
	},
	{
		ecosystem: "gradle",
		lockfileNames: []string{
			"build.gradle",
			"build.gradle.kts",
			"settings.gradle",
			"settings.gradle.kts",
			"gradle.lockfile",
		},
		patterns: nil,
	},
}

// EcosystemBinding is the caller-visible projection of one table row, for a
// caller that needs the ecosystem/lockfile shape without reaching into the
// allowlist patterns, which stay this package's own concern.
type EcosystemBinding struct {
	Ecosystem     string
	LockfileNames []string
}

// Ecosystems returns the ecosystem table's rows in table order. LockfileNames
// is copied, not aliased to the table's backing array, so a caller mutating
// its copy can't corrupt what every other caller reads.
func Ecosystems() []EcosystemBinding {
	out := make([]EcosystemBinding, len(bindings))
	for i, b := range bindings {
		out[i] = EcosystemBinding{
			Ecosystem:     b.ecosystem,
			LockfileNames: append([]string(nil), b.lockfileNames...),
		}
	}
	return out
}

// isAllowedPath reports whether path matches any ecosystem's patterns.
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
