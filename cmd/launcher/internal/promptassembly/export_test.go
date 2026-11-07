package promptassembly

// FragmentRenders exposes Result.fragmentRenders to the external test package.
func FragmentRenders(r Result) map[string][]string { return r.fragmentRenders }
