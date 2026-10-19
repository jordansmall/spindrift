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
			r:    RouteEcosystems{"synthetic": RouteDeclaration{RouteDeclarationPathKey: "/synthetic-remote"}},
			eco:  "synthetic",
			want: "/synthetic-remote",
		},
		"absent ecosystem": {
			r:    RouteEcosystems{"synthetic": RouteDeclaration{RouteDeclarationPathKey: "/synthetic-remote"}},
			eco:  "other",
			want: "",
		},
		"absent key": {
			r:    RouteEcosystems{"synthetic": RouteDeclaration{}},
			eco:  "synthetic",
			want: "",
		},
		"wrong type": {
			r:    RouteEcosystems{"synthetic": RouteDeclaration{RouteDeclarationPathKey: 42}},
			eco:  "synthetic",
			want: "",
		},
		"empty string": {
			r:    RouteEcosystems{"synthetic": RouteDeclaration{RouteDeclarationPathKey: ""}},
			eco:  "synthetic",
			want: "",
		},
		"nil receiver": {
			r:    nil,
			eco:  "synthetic",
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
			r:    RouteEcosystems{"synthetic": RouteDeclaration{"widgets": []any{"alpha", "beta"}}},
			eco:  "synthetic",
			key:  "widgets",
			want: []string{"alpha", "beta"},
		},
		"absent key": {
			r:    RouteEcosystems{"synthetic": RouteDeclaration{}},
			eco:  "synthetic",
			key:  "widgets",
			want: nil,
		},
		"non-array value": {
			r:    RouteEcosystems{"synthetic": RouteDeclaration{"widgets": "alpha"}},
			eco:  "synthetic",
			key:  "widgets",
			want: nil,
		},
		"array with non-string element": {
			r:    RouteEcosystems{"synthetic": RouteDeclaration{"widgets": []any{"alpha", 7}}},
			eco:  "synthetic",
			key:  "widgets",
			want: nil,
		},
		"nil receiver": {
			r:    nil,
			eco:  "synthetic",
			key:  "widgets",
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
	values := []string{"alpha", "beta"}
	r := RouteEcosystems{"synthetic": RouteDeclaration{"widgets": StringsValue(values)}}

	got := r.Strings("synthetic", "widgets")
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
		"synthetic": RouteDeclaration{RouteDeclarationPathKey: "/synthetic-remote"},
		"other":     RouteDeclaration{"widgets": StringsValue([]string{"alpha", "beta"})},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded RouteEcosystems
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got, want := decoded.Path("synthetic"), "/synthetic-remote"; got != want {
		t.Errorf("decoded.Path(synthetic) = %q, want %q", got, want)
	}

	gotWidgets := decoded.Strings("other", "widgets")
	wantWidgets := []string{"alpha", "beta"}
	if len(gotWidgets) != len(wantWidgets) {
		t.Fatalf("decoded.Strings(other, widgets) = %#v, want %#v", gotWidgets, wantWidgets)
	}
	for i := range gotWidgets {
		if gotWidgets[i] != wantWidgets[i] {
			t.Fatalf("decoded.Strings(other, widgets) = %#v, want %#v", gotWidgets, wantWidgets)
		}
	}
}

func TestRouteDeclarationKeyLabel(t *testing.T) {
	if got, want := RouteDeclarationKeyLabel("synthetic", RouteDeclarationPathKey), "ecosystems.synthetic.path"; got != want {
		t.Errorf("RouteDeclarationKeyLabel(synthetic, path) = %q, want %q", got, want)
	}
	if got, want := RouteDeclarationKeyLabel("other", "widgets"), "ecosystems.other.widgets"; got != want {
		t.Errorf("RouteDeclarationKeyLabel(other, widgets) = %q, want %q", got, want)
	}
}
