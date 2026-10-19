package dispatchkind

import (
	"reflect"
	"testing"
)

// TestDescriptorFieldsMatchNixRows pins the hand-written Descriptor struct to
// the field table lib/renderers.nix renderDispatchKindsGo renders kinds_gen.go
// from, so a field added on only one side fails here instead of silently never
// being set.
func TestDescriptorFieldsMatchNixRows(t *testing.T) {
	structFields := map[string]bool{}
	typ := reflect.TypeOf(Descriptor{})
	for i := 0; i < typ.NumField(); i++ {
		structFields[typ.Field(i).Name] = true
	}
	nixFields := map[string]bool{}
	for _, name := range descriptorRowFields {
		nixFields[name] = true
	}
	for name := range structFields {
		if !nixFields[name] {
			t.Errorf("Descriptor.%s has no entry in rowFields (renderDispatchKindsGo in lib/renderers.nix); add the row attribute there and set it on the lib/dispatch-kinds/*.nix rows that need a non-default value, then run `nix run .#regen`", name)
		}
	}
	for name := range nixFields {
		if !structFields[name] {
			t.Errorf("rowFields (renderDispatchKindsGo in lib/renderers.nix) renders %s but Descriptor has no such field; add it to the struct in dispatchkind.go or drop the rowFields entry, then run `nix run .#regen`", name)
		}
	}
}

func TestDefaultIsWork(t *testing.T) {
	if Default != Work {
		t.Errorf("Default = %q, want work: lib/dispatch-kinds/work.nix is the one row with default = true", Default.Name)
	}
}
