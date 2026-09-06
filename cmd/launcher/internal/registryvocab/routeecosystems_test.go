package registryvocab

import (
	"encoding/json"
	"testing"
)

func TestRouteEcosystems_Path(t *testing.T) {
	tests := map[string]struct {
		r    RouteEcosystems
		eco  string
		want string
	}{
		"present": {
			r:    RouteEcosystems{"go": RouteDeclaration{RouteDeclarationPathKey: "/go-remote"}},
			eco:  "go",
			want: "/go-remote",
		},
		"absent ecosystem": {
			r:    RouteEcosystems{"go": RouteDeclaration{RouteDeclarationPathKey: "/go-remote"}},
			eco:  "gradle",
			want: "",
		},
		"absent key": {
			r:    RouteEcosystems{"go": RouteDeclaration{}},
			eco:  "go",
			want: "",
		},
		"wrong type": {
			r:    RouteEcosystems{"go": RouteDeclaration{RouteDeclarationPathKey: 42}},
			eco:  "go",
			want: "",
		},
		"empty string": {
			r:    RouteEcosystems{"go": RouteDeclaration{RouteDeclarationPathKey: ""}},
			eco:  "go",
			want: "",
		},
		"nil receiver": {
			r:    nil,
			eco:  "go",
			want: "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.r.Path(tc.eco); got != tc.want {
				t.Errorf("Path(%q) = %q, want %q", tc.eco, got, tc.want)
			}
		})
	}
}

func TestRouteEcosystems_Strings(t *testing.T) {
	tests := map[string]struct {
		r    RouteEcosystems
		eco  string
		key  string
		want []string
	}{
		"happy path": {
			r:    RouteEcosystems{"cargo": RouteDeclaration{"registries": []any{"internal", "crates-remote"}}},
			eco:  "cargo",
			key:  "registries",
			want: []string{"internal", "crates-remote"},
		},
		"absent key": {
			r:    RouteEcosystems{"cargo": RouteDeclaration{}},
			eco:  "cargo",
			key:  "registries",
			want: nil,
		},
		"non-array value": {
			r:    RouteEcosystems{"cargo": RouteDeclaration{"registries": "internal"}},
			eco:  "cargo",
			key:  "registries",
			want: nil,
		},
		"array with non-string element": {
			r:    RouteEcosystems{"cargo": RouteDeclaration{"registries": []any{"internal", 7}}},
			eco:  "cargo",
			key:  "registries",
			want: nil,
		},
		"nil receiver": {
			r:    nil,
			eco:  "cargo",
			key:  "registries",
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := tc.r.Strings(tc.eco, tc.key)
			if len(got) != len(tc.want) {
				t.Fatalf("Strings(%q, %q) = %#v, want %#v", tc.eco, tc.key, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Strings(%q, %q) = %#v, want %#v", tc.eco, tc.key, got, tc.want)
				}
			}
		})
	}
}

func TestStringsValue_RoundTripsThroughStrings(t *testing.T) {
	values := []string{"internal", "crates-remote"}
	r := RouteEcosystems{"cargo": RouteDeclaration{"registries": StringsValue(values)}}

	got := r.Strings("cargo", "registries")
	if len(got) != len(values) {
		t.Fatalf("Strings after StringsValue = %#v, want %#v", got, values)
	}
	for i := range got {
		if got[i] != values[i] {
			t.Fatalf("Strings after StringsValue = %#v, want %#v", got, values)
		}
	}
}

func TestStringsValue_EmptyInputIsNil(t *testing.T) {
	if got := StringsValue(nil); got != nil {
		t.Errorf("StringsValue(nil) = %#v, want nil", got)
	}
	if got := StringsValue([]string{}); got != nil {
		t.Errorf("StringsValue([]string{}) = %#v, want nil", got)
	}
}

func TestRouteEcosystems_JSONRoundTrip(t *testing.T) {
	original := RouteEcosystems{
		"go":    RouteDeclaration{RouteDeclarationPathKey: "/go-remote"},
		"cargo": RouteDeclaration{"registries": StringsValue([]string{"internal", "crates-remote"})},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded RouteEcosystems
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got, want := decoded.Path("go"), "/go-remote"; got != want {
		t.Errorf("decoded.Path(go) = %q, want %q", got, want)
	}

	gotRegistries := decoded.Strings("cargo", "registries")
	wantRegistries := []string{"internal", "crates-remote"}
	if len(gotRegistries) != len(wantRegistries) {
		t.Fatalf("decoded.Strings(cargo, registries) = %#v, want %#v", gotRegistries, wantRegistries)
	}
	for i := range gotRegistries {
		if gotRegistries[i] != wantRegistries[i] {
			t.Fatalf("decoded.Strings(cargo, registries) = %#v, want %#v", gotRegistries, wantRegistries)
		}
	}
}

func TestRouteDeclarationKeyLabel(t *testing.T) {
	if got, want := RouteDeclarationKeyLabel("go", RouteDeclarationPathKey), "ecosystems.go.path"; got != want {
		t.Errorf("RouteDeclarationKeyLabel(go, path) = %q, want %q", got, want)
	}
	if got, want := RouteDeclarationKeyLabel("cargo", "registries"), "ecosystems.cargo.registries"; got != want {
		t.Errorf("RouteDeclarationKeyLabel(cargo, registries) = %q, want %q", got, want)
	}
}
