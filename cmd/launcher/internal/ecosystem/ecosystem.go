// Package ecosystem is the single table of what the Harness knows about
// each dependency ecosystem: its lockfile names, its toolchain-nudge
// classification, and (where applicable) its env-export and
// home-level-config render functions. It is the home ADR
// 0045's "One table: the ecosystem package" calls for -- knowledge every
// consumer reads from here, so no consumer has to import another for it.
//
// Each ecosystem's complete row lives in its own file named for it (cargo.go,
// npm.go, yarn.go, pnpm.go, go.go, gradle.go); this root file holds only the
// row and hook types, the ordered Table, and the table's accessors -- so
// adding an ecosystem is one new file plus one line in Table. ADR 0048 pins
// that shape against a premature split: "Promote a single ecosystem to its
// own package only on a trigger: a dependency scoped to it (e.g. a YAML
// library only yarn and pnpm need) or a file that has outgrown itself. The
// row-value shape makes that promotion mechanical."
package ecosystem

import (
	"sort"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// EnvExportRenderer renders the env-var exports (and any warnings about
// values they override) an ecosystem's row binds a bindings-mode Forwarder
// route to. getenv is the environment-snapshot accessor -- satisfied by
// os.Getenv at the call site -- so a renderer that needs to read prior env
// values (go's GOTOOLCHAIN/GONOPROXY/GOPRIVATE/GOSUMDB/GONOSUMDB) can,
// without the caller knowing which renderers read anything and which (like
// npm's) ignore it entirely. The local endpoint arrives as (port, prefix)
// rather than one pre-composed URL because the rows disagree on its shape --
// npm's three registry vars need a trailing slash, go's GOPROXY must not
// have one -- so each row composes it from the parts. routes is the
// manifest's full route list (issue #3259), mirroring
// RepoAwareHomeConfigRenderer's own routes param, so a renderer that runs
// pre-clone (npm's) can still key its decision off the first route's
// ecosystem-tagged EnforcedPaths -- a row with no such need (go's) simply
// ignores it.
type EnvExportRenderer func(port int, prefix string, getenv func(string) string, routes []registrymanifest.Route) (exports []EnvExport, warnings []string)

// HomeConfigRenderer renders a home-level registry-config file's contents
// for the route-aware local endpoint at (port, prefix). Unlike
// EnvExportRenderer it takes no getenv: a home-config file rewrites
// resolution wholesale, so it never needs to read (and preserve or warn
// about) a prior environment value the way go's env-export renderer does.
// routes is the manifest's full route list (issue #3259), mirroring
// EnvExportRenderer's own routes param, so a renderer that runs pre-clone
// (gradle's) can still key its decision off the first route's
// ecosystem-tagged EnforcedPaths -- what a route declares changes what a
// home-config renderer may legitimately emit, since gradle's HomeConfig has
// no committed in-tree config to key an override off the way cargo's
// RepoAwareHomeConfig does; a
// row with no such need (cargo's own pre-clone base template) simply ignores
// it.
type HomeConfigRenderer func(port int, prefix string, routes []registrymanifest.Route) string

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

// ConfigParser reads a row's committed in-tree config file -- content is
// InTreeConfigPath's file as the caller (registrydiscover's walker) read it
// -- and returns every registry declaration it names. It is pure: it never
// reads a file itself and never stamps Declaration.Ecosystem or
// Declaration.ConfigPath -- the walker does both after the call returns, so
// a parser cannot mis-stamp either field. namedAny reports whether the file
// named one or more registries even if none was usable (e.g. a non-http(s)
// or userinfo URL), which is what lets the walker tell "named nothing" from
// "named only unusable things" when it builds a Note. A nil ConfigParser
// means the row has no committed config to parse (go, gradle) -- a walk
// skips such a row rather than treating the absence as an error.
type ConfigParser func(content string) (decls []Declaration, namedAny bool, err error)

// Row is one ecosystem's entry in Table: its name, the lockfile filenames
// that identify a repo as using it, the presentation string the
// toolchain-nudge phase emits for it, the path (repo-root-relative) of its
// in-tree registry-config file, and -- for ecosystems bindings mode binds env vars for -- its
// EnvExports render function. Classification is not one-to-one with Name:
// npm, yarn and pnpm are separate rows because each is its own ecosystem
// with its own lockfile name and its own in-tree registry-config path, but
// the nudge collapses all three into one "npm/pnpm/yarn" family, as
// entrypoint.sh's old lockfile chain did. An empty InTreeConfigPath means
// the ecosystem has no in-tree registry config to rewrite (go, gradle) --
// consumers exclude such rows by filtering on that emptiness at read time,
// never via a second hand-maintained list. ConfigParser reads that same
// in-tree file (registrydiscover's walker owns the actual read) and is nil
// exactly when InTreeConfigPath is empty -- a row can't have one without
// the other.
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
// behind these two rows' own EnvExports field.
//
// The var named here is the one a binding would land in, not a promise that
// this run rendered it: whether any of these four vars is exported depends
// on the route. A route exports a var only for an ecosystem it
// declares a tagged path for: no tagged path renders no export at all (go,
// issue #3260, see go.go; npm/pnpm/yarn, issue #3259, see
// npm.go, which renders none for an ambiguous tagging too). A
// caller reporting where a binding landed must therefore confirm the var
// against the run's rendered exports (see ExportValue) before naming it. A row whose binding is a file instead
// (cargo, gradle) leaves this empty; such a row names its HomeConfig's own
// resolved path instead, and that file is always written.
//
// RewriteRows is empty for every row declaring no response rewrite. Where
// non-empty, it holds the registryvocab.RewriteRow values registryproxy
// matches a response against for this ecosystem's tagged subtrees --
// cargo's sparse-index config.json "dl" row (ADR 0045) is the only one
// today.
type Row struct {
	Name                string
	LockfileNames       []string
	Classification      string
	InTreeConfigPath    string
	EnvExports          EnvExportRenderer
	EnvExportOrder      int
	HomeConfig          *HomeConfig
	RepoAwareHomeConfig RepoAwareHomeConfigRenderer
	BindingEnvVar       string
	RewriteRows         []registryvocab.RewriteRow
	ConfigParser        ConfigParser
}

// The rendered export file's line order, one constant per row that has
// exports. Consecutive by construction: a row that wants to land between two
// of them renumbers the block rather than guessing at spare values. It
// stays in this root file rather than moving to any one row's own file
// because, like Table, it orders rows against each other rather than
// describing a single ecosystem.
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

// ResponseRewriteRows returns every rewrite row declared across Table, in
// Table order -- registryproxy walks this rather than Table itself so it
// never has to know which rows declare rewrite rows at all. Named
// ResponseRewriteRows rather than RewriteRows so it doesn't read as the Row
// field of the same name.
func ResponseRewriteRows() []registryvocab.RewriteRow {
	var rows []registryvocab.RewriteRow
	for _, row := range Table {
		rows = append(rows, row.RewriteRows...)
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
	cargoRow,
	npmRow,
	yarnRow,
	pnpmRow,
	goRow,
	gradleRow,
}
