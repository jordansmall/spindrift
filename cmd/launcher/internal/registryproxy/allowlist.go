package registryproxy

import (
	"regexp"
	"strings"
)

// binding is one ecosystem's entry in the path-allowlist table (ADR 0044).
// "Each ecosystem is a small table entry, not a parser." A future ecosystem
// is added as another entry in bindings, not a rewrite of this file.
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

// npmNameSegment is the package/scope name segment class shared by every
// npmPackageRegistryPatterns shape below, factored into one constant so a
// fix to the charset only needs to change in one place (the same reasoning
// as goModulePathPrefix above). The registry protocol doesn't enforce npm's
// lowercase-by-convention naming at the URL level, but every segment must
// start with an alphanumeric character -- real npm package/scope names can
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
// endpoint, each in scoped (@scope/name) and unscoped (name) form where
// applicable. The tarball shape is included for completeness, but like
// cargo's own download endpoint it's rarely reached through the proxy in
// practice: npm's client (pacote) fetches the packument's embedded
// "tarball" URL verbatim rather than deriving this path from the
// configured registry, so that request goes straight to upstream --
// unauthenticated, the same accepted gap ADR 0044 already documents for a
// redirect-based download (docs/adr/0044-private-registry-credentials-live-in-a-launcher-side-proxy.md).
var npmPackageRegistryPatterns = []*regexp.Regexp{
	// These two bare-name/two-segment shapes also match plenty of paths that
	// aren't real packages (e.g. "/admin", "/login", "/v1/tokens") -- npm's
	// flat namespace gives a real package name and an arbitrary one/two-
	// segment path the same shape, so this is a known, unfixable residual
	// false-positive: issue #2852's allowlist-miss log is near-vacuous for
	// paths of this shape as a result.
	regexp.MustCompile(`^/` + npmNameSegment + `$`),
	regexp.MustCompile(`^/` + npmNameSegment + `/` + npmNameSegment + `$`),
	regexp.MustCompile(`^/` + npmNameSegment + `/-/` + npmTarballFilenameSegment + `\.tgz$`),
	regexp.MustCompile(`^/@` + npmNameSegment + `/` + npmNameSegment + `$`),
	regexp.MustCompile(`^/@` + npmNameSegment + `/` + npmNameSegment + `/` + npmNameSegment + `$`),
	regexp.MustCompile(`^/@` + npmNameSegment + `/` + npmNameSegment + `/-/` + npmTarballFilenameSegment + `\.tgz$`),
	regexp.MustCompile(`^/-/v1/search$`),
}

// bindings is the path-allowlist table (ADR 0044). gradle's row has a nil
// patterns field because its Binding (agent/entrypoint.sh) is a home-level
// init script pointing resolution at the Forwarder, rather than a set of
// allowlisted path shapes -- like cargo's own excluded download endpoint
// above, a Maven/Gradle repository's artifact base path is
// registry-specific (the repository ID/layout configured on whatever
// Nexus/Artifactory/etc. serves it) and can't be statically derived here.
// The row still belongs in the table as the record that gradle is a known
// ecosystem.
var bindings = []binding{
	{
		ecosystem: "cargo",
		patterns:  cargoSparseIndexPatterns,
	},
	{
		ecosystem: "npm",
		patterns:  npmPackageRegistryPatterns,
	},
	{
		ecosystem: "yarn",
		patterns:  npmPackageRegistryPatterns,
	},
	{
		ecosystem: "pnpm",
		patterns:  npmPackageRegistryPatterns,
	},
	{
		ecosystem: "go",
		patterns:  goModulePatterns,
	},
	{
		ecosystem: "gradle",
		patterns:  nil,
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

// allowlistedEcosystemNames is the comma-joined ecosystem names isAllowedPath
// checks a path against, in table order, skipping a row with no patterns of
// its own (e.g. gradle, whose entry exists only for its lockfile names). An
// enforcing route's 403 body names this set, so a refusal reads as "not in
// this route's read-only registry-protocol policy" rather than a bare,
// unexplained "403 Forbidden". bindings is static, so this is computed once
// at init rather than per refusal; it's a joined string rather than a
// package-level slice to avoid the shared-mutable-state hazard Ecosystems
// above deliberately guards against.
var allowlistedEcosystemNames = func() string {
	names := make([]string, 0, len(bindings))
	for _, b := range bindings {
		if len(b.patterns) > 0 {
			names = append(names, b.ecosystem)
		}
	}
	return strings.Join(names, ", ")
}()
