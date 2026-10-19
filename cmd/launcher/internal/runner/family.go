package runner

// Descriptor is one runner family, rendered into families_gen.go from the
// lib/runners rows. TestDescriptorFieldsMatchNixRows fails if the struct and
// the row table disagree.
type Descriptor struct {
	// Name is the family string the launcher receives as RUNNER_KIND.
	Name string
	// Runtimes are the operator-facing runtime values the family serves.
	Runtimes []string
	// Artifact is the flake package the family exposes under imageAttrName.
	Artifact string
}

// ValidValues are the operator-facing runtime values, the same list
// lib/flakeModule.nix's `runtime` option enum uses, in lib/runners row order;
// the quickstart wizard prompts in this order.
var ValidValues = func() []string {
	var vs []string
	for _, f := range Families {
		vs = append(vs, f.Runtimes...)
	}
	return vs
}()
