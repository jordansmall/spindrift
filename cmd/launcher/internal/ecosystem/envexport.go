package ecosystem

// EnvExport is one name/value pair to export into the child process's
// environment. A slice (not a map) keeps rendering order deterministic --
// Go map iteration order is random, and a later slice turns Exports into a
// sourceable "export NAME=\"value\"" file where line order should be stable
// across runs.
type EnvExport struct {
	Name  string
	Value string
}
