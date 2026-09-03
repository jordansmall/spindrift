package ecosystem

import (
	"testing"
)

func TestCargoRegistryEnvVarName(t *testing.T) {
	cases := []struct {
		name         string
		registryName string
		want         string
	}{
		{
			name:         "plain name",
			registryName: "othercorp",
			want:         "CARGO_REGISTRIES_OTHERCORP_TOKEN",
		},
		{
			name:         "dashed name maps dashes to underscores",
			registryName: "my-registry",
			want:         "CARGO_REGISTRIES_MY_REGISTRY_TOKEN",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CargoRegistryEnvVarName(tc.registryName)
			if got != tc.want {
				t.Errorf("CargoRegistryEnvVarName(%q) = %q, want %q", tc.registryName, got, tc.want)
			}
		})
	}
}
