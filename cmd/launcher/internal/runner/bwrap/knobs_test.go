package bwrap

import (
	"slices"
	"testing"
)

// BWRAP_UNSHARE_NET is a presence knob: any non-empty value is on, so the
// reader never errors and os.LookupEnv stays a valid Lookup.
func TestReadKnobs_UnshareNetIsPresence(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", true},
		{"false", true},
		{"no", true},
		{"1", true},
		{"true", true},
	} {
		k, err := ReadKnobs(func(env string) (string, bool) {
			return tc.value, env == "BWRAP_UNSHARE_NET"
		})
		if err != nil {
			t.Fatalf("ReadKnobs(%q): %v", tc.value, err)
		}
		if k.BwrapUnshareNet != tc.want {
			t.Errorf("BWRAP_UNSHARE_NET=%q read as %v, want %v", tc.value, k.BwrapUnshareNet, tc.want)
		}
	}
}

func TestRawNetworkKnobs(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"unset", nil, nil},
		{"empty", map[string]string{"BWRAP_UNSHARE_NET": ""}, nil},
		{"set", map[string]string{"BWRAP_UNSHARE_NET": "1"}, []string{"BWRAP_UNSHARE_NET"}},
	} {
		got, err := RawNetworkKnobs(func(env string) (string, bool) {
			v, ok := tc.env[env]
			return v, ok
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: RawNetworkKnobs = %v, want %v", tc.name, got, tc.want)
		}
	}
}
