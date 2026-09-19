// Package ecosystem is the single table of what the Harness knows about each
// dependency ecosystem: lockfile names, toolchain-nudge classification, and
// the env-export and home-level-config renderers (ADR 0045). Each row lives in
// its own file; this file holds the types, the ordered Table, and its
// accessors. ADR 0048 pins that shape: a row gets its own package on a trigger.
package ecosystem

import (
	"sort"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// EnvExportRenderer renders an ecosystem's env-var exports and any warnings
// about values they override. getenv lets a renderer preserve prior values
// (go reads GOTOOLCHAIN and friends). The endpoint arrives as (port, prefix)
// because npm's vars need a trailing slash and go's GOPROXY must not. routes
// (issue #3259) carries the tagged EnforcedPaths a renderer keys off.
type EnvExportRenderer func(port int, prefix string, getenv func(string) string, routes []registrymanifest.Route) (exports []EnvExport, warnings []string)

// HomeConfigRenderer renders a home-level registry-config file for the local
// endpoint at (port, prefix). It takes no getenv because such a file rewrites
// resolution wholesale, so it never preserves a prior environment value.
// routes is the manifest's full route list (issue #3259), which a pre-clone
// renderer (gradle's) keys off through the first route's tagged EnforcedPaths.
type HomeConfigRenderer func(port int, prefix string, routes []registrymanifest.Route) string

// HomeConfig is one ecosystem's home-level (not in-tree) registry config.
// HomeRelativeDefault and ConfigPath stay separate fields because the verb
// resolves the home first, through HomeEnvVar or the fallback, and only then
// joins ConfigPath onto it.
type HomeConfig struct {
	HomeEnvVar          string
	HomeRelativeDefault string
	ConfigPath          string
	Render              HomeConfigRenderer
}

// RepoAwareHomeConfigRenderer re-renders a row's whole HomeConfig file once
// the Target repo is on disk (issue #3201), keying off repoConfig, the row's
// in-tree config as the clone carries it ("" if absent). It returns the
// placeholder EnvExports the rewrite needs bound, since a source-replacement
// binding still needs one. A nil field means no such notion at all.
type RepoAwareHomeConfigRenderer func(port int, prefix string, routes []registrymanifest.Route, repoConfig string) (content string, exports []EnvExport, warnings []string)

// ConfigParser returns every registry declaration named in the content of a
// row's InTreeConfigPath. It is pure: registrydiscover's walker reads the file
// and stamps Declaration.Ecosystem and ConfigPath after the call returns.
// namedAny separates "named nothing" from "named only unusable things". Nil
// for a row with no committed config (go, gradle); a walk skips such a row.
type ConfigParser func(content string) (decls []Declaration, namedAny bool, err error)

// RouteDeclarationValidator validates one key of a route's
// [routes.ecosystems.<name>] block other than "path" (issue #3403). value
// arrives exactly as go-toml decoded it, so a TOML array is []any, never
// []string. The error is a bare noun phrase; the caller prefixes "<route>:
// <key>: ". Nil means the caller must reject every key beyond "path".
type RouteDeclarationValidator func(key string, value any) error

// Row is one ecosystem's entry in Table. A nil hook field contributes nothing
// to a walk over Table.
type Row struct {
	Name          string
	LockfileNames []string

	// Classification is not one-to-one with Name: npm, yarn and pnpm are
	// separate rows with their own lockfiles and in-tree config paths, but the
	// nudge collapses all three into one "npm/pnpm/yarn" family, as
	// entrypoint.sh's old lockfile chain did.
	Classification string

	// InTreeConfigPath is empty for an ecosystem with no in-tree registry
	// config to rewrite (go, gradle); consumers exclude such rows by filtering
	// on that emptiness, never via a second hand-maintained list.
	InTreeConfigPath string

	EnvExports EnvExportRenderer

	// EnvExportOrder pins where the row's exports land in the rendered export
	// file, whose line order predates Table and must stay byte-identical to the
	// hand-written calls the table walk replaced (issue #3181). Zero is fine for
	// a new row: nothing reads the file positionally.
	EnvExportOrder int

	HomeConfig *HomeConfig

	// RepoAwareHomeConfig is nil for every row but cargo's (issue #3201). Such
	// a row keeps its InTreeConfigPath, which the renderer reads, but is
	// excluded from the in-tree rewrite, since the two do not compose
	// (bindregistry.InTreeBindings). It needs a non-nil HomeConfig: the only
	// caller filters HomeConfigRows(), so a row setting one alone is skipped.
	RepoAwareHomeConfig RepoAwareHomeConfigRenderer

	// BindingEnvVar names the one env var the row's binding lands in (npm,
	// pnpm, yarn, go); yarn and pnpm set it though npm's NpmFamilyBindings
	// renders all three. A file-bound row (cargo, gradle) leaves it empty and
	// names its HomeConfig path. Naming a var is no promise the run rendered it
	// (issues #3259, #3260): confirm it against the exports (see ExportValue).
	BindingEnvVar string

	// RewriteRows is empty for a row declaring no response rewrite. The two
	// today are cargo's sparse-index config.json "dl" row (ADR 0045) and npm's
	// packument dist.tarball row (issue #3401).
	RewriteRows []registryvocab.RewriteRow

	// ConfigParser reads InTreeConfigPath and is nil exactly when that is empty.
	ConfigParser ConfigParser

	RouteDeclaration RouteDeclarationValidator

	// RetiredRouteKey names the top-level routes-file key an operator could once
	// declare this ecosystem's route through, before [routes.ecosystems.<name>]
	// existed (ADR 0047, issues #3261 and #3403), e.g. cargo's
	// "cargo-registries". Empty for npm, yarn and pnpm, which never had one.
	RetiredRouteKey string
}

// The rendered export file's line order. Consecutive by construction: a row
// that wants to land between two of these renumbers the block rather than
// guessing at spare values.
const (
	envExportOrderGo = iota + 1
	envExportOrderNpmFamily
)

// EnvExportRows returns the rows carrying an EnvExports renderer, in ascending
// EnvExportOrder (ties keep Table order), so the rendered file's line order
// stays independent of the classification precedence Table's order encodes.
// The slice is fresh, but its Row copies share their LockfileNames backing
// array with Table, which callers must not write through.
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
// order: the two home-config writes have no legacy file order to match, so
// they need no order field of their own. The slice is fresh, but its Row
// copies share their LockfileNames backing array with Table, which callers
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
// Table order, so registryproxy never has to know which rows declare them.
func ResponseRewriteRows() []registryvocab.RewriteRow {
	var rows []registryvocab.RewriteRow
	for _, row := range Table {
		rows = append(rows, row.RewriteRows...)
	}
	return rows
}

// Table lists every known ecosystem in cargo, npm, yarn, pnpm, go, gradle
// order. That order is load-bearing: it encodes the first-hit lockfile
// precedence entrypoint.sh's old if/elif chain had (issue #2930), so do not
// reorder rows without checking every caller that stops at the first match.
// Treat it as read-only; a consumer handing rows further out copies them.
var Table = []Row{
	cargoRow,
	npmRow,
	yarnRow,
	pnpmRow,
	goRow,
	gradleRow,
}

// RowByRetiredRouteKey returns the row whose RetiredRouteKey equals key, so a
// caller translating a retired routes-file key never hand-lists which
// ecosystem it stood for. ok is false when nothing matches, including key ==
// "": npm, yarn and pnpm all carry an empty RetiredRouteKey, so without the
// guard an empty key would resolve to whichever of them Table lists first.
func RowByRetiredRouteKey(key string) (Row, bool) {
	if key == "" {
		return Row{}, false
	}
	for _, row := range Table {
		if row.RetiredRouteKey == key {
			return row, true
		}
	}
	return Row{}, false
}
