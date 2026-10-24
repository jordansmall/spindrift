// Package ecosystem is the core of the Ecosystem Strategy registry: the
// Strategy type and its hook types, Register and the accessors over the
// registered Strategies, and the route keys registryroutes reads without
// importing a Strategy. What the Harness knows about each dependency
// ecosystem (lockfile names, config readers, the env-export and
// home-level-config renderers) lives in that ecosystem's own package under
// internal/ecosystem, which installs its Strategy through Register (ADR
// 0048's 2026-10-22 amendment). No ecosystem's file-format code lives here.
package ecosystem

import (
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// EnvExportRenderer renders an ecosystem's env-var exports and any warnings
// about values they override. getenv lets a renderer preserve prior values
// (go reads GOTOOLCHAIN and friends). The endpoint arrives as (port, prefix)
// so each renderer owns its trailing-slash shape (go's GOPROXY has none,
// gradle's URL does); the npm-family renderers ignore prefix and bind their
// matched route's own. routes (issue #3259) carries the tagged EnforcedPaths a
// renderer keys off.
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

// RepoAwareHomeConfigRenderer re-renders a Strategy's whole HomeConfig file once
// the Target repo is on disk (issue #3201), keying off repoConfig, the Strategy's
// in-tree config as the clone carries it ("" if absent). It returns the
// placeholder EnvExports the rewrite needs bound, since a source-replacement
// binding still needs one. A nil field means no such notion at all.
type RepoAwareHomeConfigRenderer func(port int, prefix string, routes []registrymanifest.Route, repoConfig string) (content string, exports []EnvExport, warnings []string)

// ConfigParser returns every registry declaration named in the content of a
// Strategy's InTreeConfigPath. It is pure: registrydiscover's walker reads the file
// and stamps Declaration.Ecosystem and ConfigPath after the call returns.
// namedAny separates "named nothing" from "named only unusable things". Nil
// for a Strategy with no committed config (go, gradle); a walk skips such a Strategy.
type ConfigParser func(content string) (decls []Declaration, namedAny bool, err error)

// RouteDeclarationValidator validates one key of a route's
// [routes.ecosystems.<name>] block other than "path" (issue #3403). value
// arrives exactly as go-toml decoded it, so a TOML array is []any, never
// []string. The error is a bare noun phrase; the caller prefixes "<route>:
// <key>: ". Nil means the caller must reject every key beyond "path".
type RouteDeclarationValidator func(key string, value any) error

// Strategy is one ecosystem's entry in Table. A nil hook field contributes nothing
// to a walk over Table.
type Strategy struct {
	Name          string
	LockfileNames []string

	// Classification is not one-to-one with Name: npm, yarn and pnpm are
	// separate Strategies with their own lockfiles and in-tree config paths, but the
	// nudge collapses all three into one "npm/pnpm/yarn" family, as
	// entrypoint.sh's old lockfile chain did.
	Classification string

	// InTreeConfigPath is empty for an ecosystem with no in-tree registry
	// config to rewrite (go, gradle); consumers exclude such Strategies by
	// filtering on that emptiness, never via a second hand-maintained list.
	InTreeConfigPath string

	EnvExports EnvExportRenderer

	HomeConfig *HomeConfig

	// RepoAwareHomeConfig is nil for every Strategy but cargo's (issue #3201). Such
	// a Strategy keeps its InTreeConfigPath, which the renderer reads, but is
	// excluded from the in-tree rewrite, since the two do not compose
	// (bindregistry.InTreeBindings). It needs a non-nil HomeConfig: the only
	// caller filters HomeConfigRows(), so a Strategy setting one alone is skipped.
	RepoAwareHomeConfig RepoAwareHomeConfigRenderer

	// BindingEnvVar names the one env var the Strategy's binding lands in (npm,
	// pnpm, yarn, go), the same name its EnvExports renders. A file-bound Strategy
	// (cargo, gradle) leaves it empty and names its HomeConfig path. Naming a var
	// is no promise the run rendered it (issues #3259, #3260): confirm it against
	// the exports (see ExportValue).
	BindingEnvVar string

	// AnnounceBindingValue makes BindHomes print a dedicated "==> <Name> bound
	// to it via <BindingEnvVar>=<value>" line after the Forwarder line when the
	// export exists. It requires BindingEnvVar.
	AnnounceBindingValue bool

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

	// CredentialStores names, by credresolver source key, the stores this Strategy's
	// declarations are looked up in. It is a set, not an order: search order
	// comes from credresolver's StoreKinds. A store missing here is still named
	// in an unmatched host's StoresSearched but never queried for this Strategy.
	CredentialStores []string
}

// EnvExportRows returns the Strategies carrying an EnvExports renderer, in Table
// order, which is the rendered export file's line order.
// The slice is fresh, but its Strategy copies share their LockfileNames backing
// array with Table, which callers must not write through.
func EnvExportRows() []Strategy {
	return EnvExportRowsOf(Table)
}

// EnvExportRowsOf is EnvExportRows over the given Strategies rather than Table. The
// slice is fresh, but its Strategy copies share their LockfileNames backing array
// with in, which callers must not write through.
func EnvExportRowsOf(in []Strategy) []Strategy {
	rows := make([]Strategy, 0, len(in))
	for _, row := range in {
		if row.EnvExports == nil {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

// HomeConfigRows returns the Strategies carrying a non-nil HomeConfig, in Table
// order. The slice is fresh, but its Strategy copies share their LockfileNames
// backing array with Table, which callers must not write through.
func HomeConfigRows() []Strategy {
	return HomeConfigRowsOf(Table)
}

// HomeConfigRowsOf is HomeConfigRows over the given Strategies rather than Table. The
// slice is fresh, but its Strategy copies share their LockfileNames backing array
// with in, which callers must not write through.
func HomeConfigRowsOf(in []Strategy) []Strategy {
	rows := make([]Strategy, 0, len(in))
	for _, row := range in {
		if row.HomeConfig == nil {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

// ResponseRewriteRows returns every rewrite row declared across Table, in
// Table order, so registryproxy never has to know which Strategies declare them.
func ResponseRewriteRows() []registryvocab.RewriteRow {
	var rows []registryvocab.RewriteRow
	for _, row := range Table {
		rows = append(rows, row.RewriteRows...)
	}
	return rows
}

// CredentialStores returns each Strategy's CredentialStores keyed by Strategy Name, so
// registrydiscover never has to know which Strategy reads which store. The slices
// are copies; callers may write through them.
func CredentialStores(rows []Strategy) map[string][]string {
	stores := make(map[string][]string, len(rows))
	for _, row := range rows {
		stores[row.Name] = append([]string(nil), row.CredentialStores...)
	}
	return stores
}

// registered holds the Strategies Register installed, in precedence order.
var registered []Strategy

// Register installs the ecosystems in precedence order; the Bundle's
// ecosystems.go makes the one call. That order is load-bearing twice over: it
// encodes the first-hit lockfile precedence entrypoint.sh's old if/elif chain
// had (issue #2930), and it sets the env-export file's line order
// (EnvExportRows), so do not reorder Strategies without checking every caller
// that stops at the first match. It panics on a second call, a duplicate Name,
// or an empty list, so a half-registered or silently shadowed table cannot run.
func Register(strategies ...Strategy) {
	register(&registered, strategies)
	Table = registered
}

func register(into *[]Strategy, strategies []Strategy) {
	if len(*into) > 0 {
		panic("ecosystem: Register called twice")
	}
	if len(strategies) == 0 {
		panic("ecosystem: Register called with no Strategies")
	}
	seen := make(map[string]bool, len(strategies))
	for _, s := range strategies {
		if seen[s.Name] {
			panic("ecosystem: duplicate Strategy name " + s.Name)
		}
		seen[s.Name] = true
	}
	*into = append([]Strategy(nil), strategies...)
}

// Registered returns the Strategies Register installed, in precedence order.
// It is empty until the Bundle (or a test's TestMain) has registered. Treat it
// as read-only; a consumer handing Strategies further out copies them.
func Registered() []Strategy { return registered }

// Table is the stage-1 alias of the registered list that the wrappers below
// and their callers still read; stage 2 of issue #5940 deletes it. Order comes
// from the Register call in internal/driver/bundle/ecosystems.go.
var Table []Strategy

// RowByRetiredRouteKey returns the Strategy whose RetiredRouteKey equals key, so a
// caller translating a retired routes-file key never hand-lists which
// ecosystem it stood for. ok is false when nothing matches, including key ==
// "": npm, yarn and pnpm all carry an empty RetiredRouteKey, so without the
// guard an empty key would resolve to whichever of them Table lists first.
func RowByRetiredRouteKey(key string) (Strategy, bool) {
	if key == "" {
		return Strategy{}, false
	}
	for _, row := range Table {
		if row.RetiredRouteKey == key {
			return row, true
		}
	}
	return Strategy{}, false
}
