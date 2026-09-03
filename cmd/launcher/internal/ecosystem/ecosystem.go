// Package ecosystem is the single table of what the Harness knows about
// each dependency ecosystem: its lockfile names and its toolchain-nudge
// classification. It is the home ADR 0045's "One table: the ecosystem
// package" calls for -- knowledge every consumer reads from here, so no
// consumer has to import another for it. registryproxy still owns its own
// path-allowlist patterns; those become rows here in a later ticket.
package ecosystem

// Row is one ecosystem's entry in Table: its name, the lockfile filenames
// that identify a repo as using it, the presentation string the
// toolchain-nudge phase emits for it, and the path (repo-root-relative) of
// its in-tree registry-config file. Classification is not one-to-one with
// Name: npm, yarn and pnpm are separate rows because each is its own
// ecosystem with its own lockfile name and its own in-tree registry-config
// path, but the nudge collapses all three into one "npm/pnpm/yarn" family,
// as entrypoint.sh's old lockfile chain did. An empty InTreeConfigPath means
// the ecosystem has no in-tree registry config to rewrite (go, gradle) --
// consumers exclude such rows by filtering on that emptiness at read time,
// never via a second hand-maintained list.
type Row struct {
	Name             string
	LockfileNames    []string
	Classification   string
	InTreeConfigPath string
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
	},
	{
		Name:             "npm",
		LockfileNames:    []string{"package-lock.json"},
		Classification:   "npm/pnpm/yarn",
		InTreeConfigPath: ".npmrc",
	},
	{
		Name:             "yarn",
		LockfileNames:    []string{"yarn.lock"},
		Classification:   "npm/pnpm/yarn",
		InTreeConfigPath: ".yarnrc.yml",
	},
	{
		Name:             "pnpm",
		LockfileNames:    []string{"pnpm-lock.yaml"},
		Classification:   "npm/pnpm/yarn",
		InTreeConfigPath: "pnpm-workspace.yaml",
	},
	{
		Name:           "go",
		LockfileNames:  []string{"go.sum"},
		Classification: "go mod",
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
	},
}
