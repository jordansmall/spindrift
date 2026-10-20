package dispatchkind

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

// TestDescriptorFieldsMatchNixRows pins the hand-written Descriptor struct to
// the field table lib/renderers.nix renderDispatchKindsGo renders kinds_gen.go
// from, so a field added on only one side fails here instead of silently never
// being set.
func TestDescriptorFieldsMatchNixRows(t *testing.T) {
	testutil.AssertStructFieldsMatchRows(t, reflect.TypeOf(Descriptor{}), descriptorRowFields,
		"renderDispatchKindsGo in lib/renderers.nix",
		"the lib/dispatch-kinds/*.nix rows that need a non-default value",
		"dispatchkind.go")
}

func TestDefaultIsWork(t *testing.T) {
	if Default != Work {
		t.Errorf("Default = %q, want work: lib/dispatch-kinds/work.nix is the one row with default = true", Default.Name)
	}
}
