package runner

import (
	"reflect"
	"testing"
)

// TestDescriptorFieldsMatchNixRows pins the hand-written Descriptor struct to
// the field table lib/renderers.nix renderRunnerFamiliesGo renders
// families_gen.go from, so a field added on only one side fails here instead
// of silently never being set.
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
			t.Errorf("Descriptor.%s has no entry in rowFields (renderRunnerFamiliesGo in lib/renderers.nix); add the row attribute there and set it on the lib/runners/*.nix rows, then run `nix run .#regen`", name)
		}
	}
	for name := range nixFields {
		if !structFields[name] {
			t.Errorf("rowFields (renderRunnerFamiliesGo in lib/renderers.nix) renders %s but Descriptor has no such field; add it to the struct in family.go or drop the rowFields entry, then run `nix run .#regen`", name)
		}
	}
}

// Order is load-bearing: the quickstart prompt and the flake `runtime` enum
// list the values in this order.
func TestValidValuesFollowRowOrder(t *testing.T) {
	want := []string{"podman", "docker", "rancher", "bwrap"}
	if !reflect.DeepEqual(ValidValues, want) {
		t.Errorf("ValidValues = %v, want %v", ValidValues, want)
	}
}

func TestFamilyNamesAreTheKindConstants(t *testing.T) {
	if OCI.Name != KindOCI || Bwrap.Name != KindBwrap {
		t.Errorf("family Names = %q, %q; want %q, %q", OCI.Name, Bwrap.Name, KindOCI, KindBwrap)
	}
	if got := len(Families); got != 2 || Families[0].Name != KindOCI || Families[1].Name != KindBwrap {
		t.Errorf("Families = %v, want oci then bwrap", Families)
	}
}
