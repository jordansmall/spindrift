package connectortest_test

import (
	"net/http"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortest"
)

func TestRoutePerMethodDeclaresTheRepoSubtreeOnHost(t *testing.T) {
	t.Parallel()
	got := connectortest.RoutePerMethod("forge.example", http.MethodGet, http.MethodDelete)
	want := []connectorhost.Route{
		{Method: http.MethodGet, Host: "forge.example", Path: "/api/v1/repos/{repo}"},
		{Method: http.MethodDelete, Host: "forge.example", Path: "/api/v1/repos/{repo}"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RoutePerMethod = %+v, want %+v", got, want)
	}
}

func TestBoundRoutesSubstitutesRepoAndRebindsHost(t *testing.T) {
	t.Parallel()
	c := connectortest.Connector{Routes: connectortest.RoutePerMethod("forge.example", http.MethodGet)}
	got := c.BoundRoutes("127.0.0.1:1")
	if len(got) != 1 || got[0].Host != "127.0.0.1:1" || got[0].Path != "/api/v1/repos/"+connectortest.Repo {
		t.Fatalf("BoundRoutes = %+v", got)
	}
	if c.Routes[0].Host != "forge.example" {
		t.Errorf("BoundRoutes mutated the declared table: %+v", c.Routes)
	}
}
