// Package ecosystem is the single table of what the Harness knows about
// each dependency ecosystem: its lockfile names, its toolchain-nudge
// classification, path-allowlist patterns, and (where applicable) its
// env-export and home-level-config render functions. It is the home ADR
// 0045's "One table: the ecosystem package" calls for -- knowledge every
// consumer reads from here, so no consumer has to import another for it.
// registryproxy reads Patterns only; it does not own an ecosystem table of
// its own.
package ecosystem

import (
	"regexp"
	"sort"

	"spindrift.dev/launcher/internal/registrymanifest"
)

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

// EnvExportRenderer renders the env-var exports (and any warnings about
// values they override) an ecosystem's row binds a bindings-mode Forwarder
// route to. getenv is the environment-snapshot accessor -- satisfied by
// os.Getenv at the call site -- so a renderer that needs to read prior env
// values (go's GOTOOLCHAIN/GONOPROXY/GOPRIVATE/GOSUMDB/GONOSUMDB) can,
// without the caller knowing which renderers read anything and which (like
// npm's) ignore it entirely. The local endpoint arrives as (port, prefix)
// rather than one pre-composed URL because the rows disagree on its shape --
// npm's three registry vars need a trailing slash, go's GOPROXY must not
// have one -- so each row composes it from the parts.
type EnvExportRenderer func(port int, prefix string, getenv func(string) string) (exports []EnvExport, warnings []string)

// HomeConfigRenderer renders a home-level registry-config file's contents
// for the route-aware local endpoint at (port, prefix). Unlike
// EnvExportRenderer it takes no getenv: a home-config file rewrites
// resolution wholesale, so it never needs to read (and preserve or warn
// about) a prior environment value the way go's env-export renderer does.
type HomeConfigRenderer func(port int, prefix string) string

// HomeConfig is one ecosystem's home-level (as opposed to in-tree) registry
// config: an env var whose value names the ecosystem's home directory, the
// path segment under $HOME the verb falls back to when that var is unset,
// the path of the config file to write under whichever home was resolved,
// and the function that renders its contents. HomeRelativeDefault and
// ConfigPath are separate fields (rather than one path assembled here)
// because the verb resolves the home first -- via the env var or the
// fallback -- and only then joins ConfigPath onto it; folding them together
// would force the verb to string-split them back apart.
type HomeConfig struct {
	HomeEnvVar          string
	HomeRelativeDefault string
	ConfigPath          string
	Render              HomeConfigRenderer
}

// RepoAwareHomeConfigRenderer re-renders a row's whole HomeConfig file
// content once the Target repo is on disk (issue #3201) -- unlike
// HomeConfigRenderer, which bindings mode runs pre-clone from (port, prefix)
// alone, this runs once, post-clone, and can read repoConfig (the row's own
// tracked in-tree config file as it exists in the cloned repo, or "" if the
// file doesn't exist) to key its rewrite off content only the cloned repo
// carries. routes is the manifest's full route list, so the renderer can key
// its own per-route decisions (e.g. cargo's repo-declared registries) off
// each route's UpstreamHost/Prefix itself. It returns the placeholder
// EnvExports the rewrite needs bound (and any warnings) alongside the
// re-rendered content, since a binding scheme wired through source
// replacement rather than an in-tree rewrite still needs a caller-visible
// placeholder to export. A nil field means the row has no such notion at all
// -- not merely "nothing to derive this run", which the empty-
// exports/unchanged-content return already covers.
type RepoAwareHomeConfigRenderer func(port int, prefix string, routes []registrymanifest.Route, repoConfig string) (content string, exports []EnvExport, warnings []string)

// Row is one ecosystem's entry in Table: its name, the lockfile filenames
// that identify a repo as using it, the presentation string the
// toolchain-nudge phase emits for it, the path (repo-root-relative) of its
// in-tree registry-config file, the path shapes its registry protocol
// defines, and -- for ecosystems bindings mode binds env vars for -- its
// EnvExports render function. Classification is not one-to-one with Name:
// npm, yarn and pnpm are separate rows because each is its own ecosystem
// with its own lockfile name and its own in-tree registry-config path, but
// the nudge collapses all three into one "npm/pnpm/yarn" family, as
// entrypoint.sh's old lockfile chain did. An empty InTreeConfigPath means
// the ecosystem has no in-tree registry config to rewrite (go, gradle) --
// consumers exclude such rows by filtering on that emptiness at read time,
// never via a second hand-maintained list. Patterns is nil for a row with no
// derivable path shape (gradle); every other row's Patterns is non-empty.
// EnvExports is nil for rows with no env-export bindings; a nil renderer
// contributes nothing to a walk over Table, no placeholder needed. HomeConfig
// is nil for rows with no home-level (as opposed to in-tree) registry config
// to write -- the same "nil contributes nothing" shape as EnvExports.
// RepoAwareHomeConfig is nil for every row but cargo's (issue #3201): only
// cargo binds via source replacement re-keyed off the cloned repo's own
// un-rewritten registry declarations, so a nil renderer again means "no such
// notion", not "nothing to do this run". A row carrying it still keeps its
// InTreeConfigPath -- that path is exactly what the renderer reads, and what
// registrydiscover scans host-side -- but is thereby excluded from the
// in-tree rewrite, since the two mechanisms don't compose; see
// bindregistry.InTreeBindings' own doc for why that exclusion is the
// invariant, not a coincidence. A non-nil RepoAwareHomeConfig requires a
// non-nil HomeConfig: the renderer re-renders that HomeConfig's own file,
// and its only caller reaches repo-aware rows by filtering HomeConfigRows(),
// so a row setting one without the other would be silently skipped.
//
// EnvExportOrder pins where the row's exports land in the rendered export
// file. A row carries both orders because they are answers to different
// questions: Table's own order encodes lockfile-classification precedence,
// while the export file's line order (go's exports, then the npm family's)
// predates Table entirely -- it is the order of the hand-written by-name
// calls the table walk replaced, and issue #3181 requires the rendered file
// stay byte-identical to it. Zero is the default and is fine for a new row:
// nothing reads the file positionally (agent/entrypoint.sh sources it), so
// the pins exist only to preserve that one historical order.
//
// BindingEnvVar names the single env var a caller can point to as where the
// row's binding landed, for a row bound via one stable var (npm, pnpm,
// yarn, go). pnpm and yarn set it despite carrying no EnvExports of their
// own -- npm's renderer (NpmFamilyBindings) renders all three vars in one
// call, so the var exists at bindings-mode's Forwarder even though it isn't
// behind these two rows' own EnvExports field. go's rendered export set is
// env-dependent (GOTOOLCHAIN/GONOPROXY/GOSUMDB are conditional, see
// gobinding.go), so GOPROXY -- the one export it always renders -- is named
// here rather than left for a caller to derive from a walk that could omit
// it. A row whose binding is a file instead (cargo, gradle) leaves this
// empty; such a row names its HomeConfig's own resolved path instead.
type Row struct {
	Name                string
	LockfileNames       []string
	Classification      string
	InTreeConfigPath    string
	Patterns            []*regexp.Regexp
	EnvExports          EnvExportRenderer
	EnvExportOrder      int
	HomeConfig          *HomeConfig
	RepoAwareHomeConfig RepoAwareHomeConfigRenderer
	BindingEnvVar       string
}

// The rendered export file's line order, one constant per row that has
// exports. Consecutive by construction: a row that wants to land between two
// of them renumbers the block rather than guessing at spare values.
const (
	envExportOrderGo = iota + 1
	envExportOrderNpmFamily
)

// EnvExportRows returns the rows carrying an EnvExports renderer, in
// ascending EnvExportOrder (ties keep Table order). Bindings mode walks this
// rather than Table so the rendered file's line order is a property of the
// rows themselves, not of the classification precedence Table's order
// encodes -- neither order has to bend to the other. The returned slice is
// fresh, so callers may not add or remove Table rows through it; the Row
// copies in it still share their LockfileNames backing array with Table,
// which callers must not write through.
func EnvExportRows() []Row {
	rows := make([]Row, 0, len(Table))
	for _, row := range Table {
		if row.EnvExports == nil {
			continue
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].EnvExportOrder < rows[j].EnvExportOrder })
	return rows
}

// HomeConfigRows returns the rows carrying a non-nil HomeConfig, in Table
// order. Unlike EnvExportRows there is no separate order field to sort by:
// the rendered export file predates Table and needed its own historical line
// order preserved, but the two home-config writes have no such legacy file
// to match -- Table's own cargo-before-gradle order already matches today's
// verb, so nothing needs a second pin. The returned slice is fresh, so
// callers may not add or remove Table rows through it; the Row copies in it
// still share their LockfileNames backing array with Table, which callers
// must not write through.
func HomeConfigRows() []Row {
	rows := make([]Row, 0, len(Table))
	for _, row := range Table {
		if row.HomeConfig == nil {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

// Table lists every known ecosystem in cargo, npm, yarn, pnpm, go, gradle
// order. That order is load-bearing: it encodes the first-hit precedence
// agent/entrypoint.sh's old cargo -> npm-family -> go -> gradle if/elif
// chain had (issue #2930) -- a caller walking Table in order and stopping
// at the first lockfile match reproduces that same precedence. Do not
// reorder rows without checking every such caller.
//
// Read-only to consumers: Go cannot express that on a package-level slice,
// so a consumer that needs to hand rows further out copies them itself.
var Table = []Row{
	{
		Name:             "cargo",
		LockfileNames:    []string{"Cargo.lock"},
		Classification:   "cargo",
		InTreeConfigPath: ".cargo/config.toml",
		Patterns:         cargoSparseIndexPatterns,
		HomeConfig: &HomeConfig{
			HomeEnvVar:          "CARGO_HOME",
			HomeRelativeDefault: ".cargo",
			ConfigPath:          "config.toml",
			Render:              CargoConfigTOML,
		},
		RepoAwareHomeConfig: CargoRepoAwareConfig,
	},
	{
		Name:             "npm",
		LockfileNames:    []string{"package-lock.json"},
		Classification:   "npm/pnpm/yarn",
		InTreeConfigPath: ".npmrc",
		Patterns:         npmPackageRegistryPatterns,
		EnvExports: func(port int, prefix string, _ func(string) string) ([]EnvExport, []string) {
			return NpmFamilyBindings(port, prefix), nil
		},
		EnvExportOrder: envExportOrderNpmFamily,
		BindingEnvVar:  "npm_config_registry",
	},
	{
		Name:             "yarn",
		LockfileNames:    []string{"yarn.lock"},
		Classification:   "npm/pnpm/yarn",
		InTreeConfigPath: ".yarnrc.yml",
		Patterns:         npmPackageRegistryPatterns,
		BindingEnvVar:    "YARN_NPM_REGISTRY_SERVER",
	},
	{
		Name:             "pnpm",
		LockfileNames:    []string{"pnpm-lock.yaml"},
		Classification:   "npm/pnpm/yarn",
		InTreeConfigPath: "pnpm-workspace.yaml",
		Patterns:         npmPackageRegistryPatterns,
		BindingEnvVar:    "pnpm_config_registry",
	},
	{
		Name:           "go",
		LockfileNames:  []string{"go.sum"},
		Classification: "go mod",
		Patterns:       goModulePatterns,
		EnvExports: func(port int, prefix string, getenv func(string) string) ([]EnvExport, []string) {
			result := ComputeGoBindings(port, prefix, GoBindingInput{
				GOTOOLCHAIN: getenv("GOTOOLCHAIN"),
				GONOPROXY:   getenv("GONOPROXY"),
				GOPRIVATE:   getenv("GOPRIVATE"),
				GOSUMDB:     getenv("GOSUMDB"),
				GONOSUMDB:   getenv("GONOSUMDB"),
			})
			return result.Exports, result.Warnings
		},
		EnvExportOrder: envExportOrderGo,
		BindingEnvVar:  "GOPROXY",
	},
	{
		Name: "gradle",
		LockfileNames: []string{
			"build.gradle",
			"build.gradle.kts",
			"settings.gradle",
			"settings.gradle.kts",
			"gradle.lockfile",
		},
		Classification: "gradle",
		// Patterns is nil: gradle's Binding (agent/entrypoint.sh) is a
		// home-level init script pointing resolution at the Forwarder,
		// rather than a set of allowlisted path shapes -- like cargo's own
		// excluded download endpoint above, a Maven/Gradle repository's
		// artifact base path is registry-specific (the repository
		// ID/layout configured on whatever Nexus/Artifactory/etc. serves
		// it) and can't be statically derived here. The row still belongs
		// in the table as the record that gradle is a known ecosystem.
		Patterns: nil,
		HomeConfig: &HomeConfig{
			HomeEnvVar:          "GRADLE_USER_HOME",
			HomeRelativeDefault: ".gradle",
			ConfigPath:          "init.d/spindrift-registry-proxy.init.gradle",
			Render:              GradleInitScript,
		},
	},
}
