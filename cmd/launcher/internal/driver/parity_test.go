package driver

import (
	"reflect"
	"testing"
)

// descriptors is generated from lib/drivers/*.nix by nix/regen.nix; see
// descriptor_gen.go. Edit the Driver rows and run `nix run .#regen`.

func TestParityWithNixDriverRegistry(t *testing.T) {
	goNames := Names()

	nixSet := make(map[string]bool, len(descriptors))
	for _, d := range descriptors {
		nixSet[d.Name] = true
	}
	goSet := make(map[string]bool, len(goNames))
	for _, n := range goNames {
		goSet[n] = true
	}

	for _, d := range descriptors {
		if !goSet[d.Name] {
			t.Errorf("nix Driver %q has no matching Go strategy", d.Name)
		}
	}
	for _, n := range goNames {
		if !nixSet[n] {
			t.Errorf("Go Driver strategy %q has no matching nix Driver", n)
		}
	}
}

// TestDescriptorFieldsMatchNixRows pins the hand-written Descriptor struct to
// the field table lib/renderers.nix renders descriptor_gen.go from, so a field
// added on only one side fails here instead of silently never being set.
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
			t.Errorf("Descriptor.%s has no entry in rowFields (lib/renderers.nix); add the row attribute there and set it on the lib/drivers/*.nix rows that need a non-default value, then run `nix run .#regen`", name)
		}
	}
	for name := range nixFields {
		if !structFields[name] {
			t.Errorf("rowFields (lib/renderers.nix) renders %s but Descriptor has no such field; add it to the struct in descriptor.go or drop the rowFields entry, then run `nix run .#regen`", name)
		}
	}
}
