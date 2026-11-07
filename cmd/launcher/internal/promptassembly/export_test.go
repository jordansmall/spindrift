package promptassembly

// FragmentRenders exposes Result.fragmentRenders to the external test package.
func FragmentRenders(r Result) map[string][]string { return r.fragmentRenders }

// FragmentRenderGap exposes the delimiter that marks a splice in a fragment's
// render, so the contract tests build splice clauses from the same encoding.
const FragmentRenderGap = fragmentRenderGap
