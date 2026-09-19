package ecosystem

// EnvExport is one name/value pair to export into the child process's environment.
// Exports render as a sourceable "export NAME=\"value\"" file, so a slice keeps
// line order stable across runs; Go randomizes map iteration order.
type EnvExport struct {
	Name  string
	Value string
}

// ExportValue returns the value bound to name in exports, and whether name was
// present. A row's exports are conditional (a host-rooted route can leave GOPROXY
// or the npm vars unrendered), so absent and empty are different answers.
func ExportValue(exports []EnvExport, name string) (string, bool) {
	for _, e := range exports {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}
