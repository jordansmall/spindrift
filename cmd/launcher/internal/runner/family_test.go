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

func TestCheckNetworkMode_ListedModesAndEmptyPass(t *testing.T) {
	for _, f := range Families {
		for _, mode := range append([]string{""}, f.NetworkModes...) {
			if err := f.CheckNetworkMode(NetworkMode(mode)); err != nil {
				t.Errorf("%s.CheckNetworkMode(%q) = %v, want nil", f.Name, mode, err)
			}
		}
	}
}

func TestCheckNetworkMode_UnlistedModeRejected(t *testing.T) {
	cases := []struct {
		family Descriptor
		mode   NetworkMode
		want   string
	}{
		// nix/checks/network-mode.nix pins the same literal for mkHarness's assert.
		{Bwrap, NetworkModeNoHostLoopback, "NETWORK_MODE=no-host-loopback is unsupported on runner family bwrap, which supports: open, none, host"},
		{OCI, NetworkMode("bogus"), "NETWORK_MODE=bogus is unsupported on runner family oci, which supports: open, no-host-loopback, none, host"},
	}
	for _, tc := range cases {
		err := tc.family.CheckNetworkMode(tc.mode)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s.CheckNetworkMode(%q) = %v, want %q", tc.family.Name, tc.mode, err, tc.want)
		}
	}
}

func TestDescriptorFor(t *testing.T) {
	cases := []struct {
		kind   string
		want   Descriptor
		wantOK bool
	}{
		{"", OCI, true},
		{KindOCI, OCI, true},
		{KindBwrap, Bwrap, true},
		{"nope", Descriptor{}, false},
	}
	for _, tc := range cases {
		got, err := DescriptorFor(tc.kind)
		if ok := err == nil; ok != tc.wantOK || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("DescriptorFor(%q) = (%v, %v), want ok=%v, %v", tc.kind, got, err, tc.wantOK, tc.want)
		}
	}
}
