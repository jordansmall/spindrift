package oci

import (
	"slices"
	"testing"
)

func TestRawNetworkKnobs(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"unset", nil, nil},
		{"empty", map[string]string{"PODMAN_NETWORK": ""}, nil},
		{"set", map[string]string{"PODMAN_NETWORK": "host"}, []string{"PODMAN_NETWORK"}},
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
