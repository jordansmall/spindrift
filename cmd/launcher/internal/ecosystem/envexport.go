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

// ExportValue returns the value bound to name in exports, and whether name
// was present at all. Callers need the presence bit rather than an empty
// string because a row's exports are conditional -- a host-rooted route can
// leave GOPROXY or the npm family's vars unrendered entirely -- so "absent"
// and "bound to the empty string" are different answers.
func ExportValue(exports []EnvExport, name string) (string, bool) {
	for _, e := range exports {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}
