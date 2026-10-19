package runner

import (
	"fmt"
	"strings"
)

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
	// NetworkModes are the NETWORK_MODE values the family can render; the
	// mkHarness assert and the launch gate both reject any other.
	NetworkModes []string
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

// DescriptorFor returns the family row for kind, resolving an empty kind to
// the default family the way Lookup does.
func DescriptorFor(kind string) (Descriptor, error) {
	kind = orDefault(kind)
	for _, f := range Families {
		if f.Name == kind {
			return f, nil
		}
	}
	return Descriptor{}, fmt.Errorf("unknown RUNNER_KIND %q; registered runner families: %v", kind, Names())
}

// CheckNetworkMode returns nil when mode is empty or listed in d.NetworkModes.
// lib/runners/index.nix's unsupportedNetworkModeMessage builds the same text
// for mkHarness's assert, and both are pinned to one literal (see
// nix/checks/network-mode.nix), so change them together.
func (d Descriptor) CheckNetworkMode(mode NetworkMode) error {
	if mode == "" {
		return nil
	}
	for _, m := range d.NetworkModes {
		if m == string(mode) {
			return nil
		}
	}
	return fmt.Errorf("NETWORK_MODE=%s is unsupported on runner family %s, which supports: %s", mode, d.Name, strings.Join(d.NetworkModes, ", "))
}
