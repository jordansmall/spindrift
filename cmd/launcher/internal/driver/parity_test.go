package driver

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

// TestDescriptorFieldsMatchNixRows pins the hand-written Descriptor struct to
// the field table lib/renderers.nix renders descriptor_gen.go from, so a field
// added on only one side fails here instead of silently never being set.
func TestDescriptorFieldsMatchNixRows(t *testing.T) {
	testutil.AssertStructFieldsMatchRows(t, reflect.TypeOf(Descriptor{}), descriptorRowFields,
		"lib/renderers.nix",
		"the lib/drivers/*.nix rows that need a non-default value",
		"descriptor.go")
}
