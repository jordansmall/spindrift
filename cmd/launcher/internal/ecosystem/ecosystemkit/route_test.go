package ecosystemkit

import (
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

func TestDeclaredPath(t *testing.T) {
	route := func(path string) registrymanifest.Route {
		return registrymanifest.Route{Ecosystems: registryvocab.RouteEcosystems{
			"synthetic": {registryvocab.RouteDeclarationPathKey: path},
		}}
	}

	cases := []struct {
		name      string
		routes    []registrymanifest.Route
		ecosystem string
		want      string
	}{
		{"no routes", nil, "synthetic", ""},
		{"first route's declared path", []registrymanifest.Route{route("/first"), route("/second")}, "synthetic", "/first"},
		{"undeclared ecosystem", []registrymanifest.Route{route("/first")}, "other", ""},
		{"route without a block", []registrymanifest.Route{{}}, "synthetic", ""},
		{"only the first route counts", []registrymanifest.Route{{}, route("/second")}, "synthetic", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeclaredPath(tc.routes, tc.ecosystem); got != tc.want {
				t.Errorf("DeclaredPath = %q, want %q", got, tc.want)
			}
		})
	}
}
