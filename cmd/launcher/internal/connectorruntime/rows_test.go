package connectorruntime_test

import (
	"encoding/json"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorruntime"
)

func TestRuntimeRowsAndBaseErrorReadItsOwnSet(t *testing.T) {
	t.Parallel()
	get := connectorhost.Route{Method: "GET", Host: "{base}", Path: "/rest"}
	raw, err := json.Marshal([]backendrows.Row{
		{Name: "zeta", Module: "/x/z.wasm", Routes: []connectorhost.Route{{Method: "GET", Host: "h.test", Path: "/a"}}, CredentialEnv: "T"},
		{Name: "site", Module: "/x/s.wasm", Routes: []connectorhost.Route{get}, CredentialEnv: "T", BaseEnv: "SITE_URL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := backendrows.Decode(raw, func(string) string { return "http://plain.lan" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := connectorruntime.New(set, nil, nil)
	if got := rt.Rows(); len(got) != 1 || got[0].Name != "zeta" {
		t.Errorf("Rows() = %+v, want only zeta", got)
	}
	if _, ok := rt.Row("zeta"); !ok {
		t.Error("Row(zeta) = false")
	}
	if _, ok := rt.Row("site"); ok {
		t.Error("Row(site) = true for a dropped row")
	}
	if err := rt.BaseError("site"); err == nil {
		t.Error("BaseError(site) = nil, want the unusable base")
	}
	if err := rt.BaseError("zeta"); err != nil {
		t.Errorf("BaseError(zeta) = %v", err)
	}
}

func TestNilRuntimeHasNoRows(t *testing.T) {
	t.Parallel()
	var rt *connectorruntime.Runtime
	if _, ok := rt.Row("x"); ok {
		t.Error("nil runtime has a row")
	}
	if rt.Rows() != nil || rt.BaseError("x") != nil {
		t.Error("nil runtime has rows or a base error")
	}
}

func TestInProcessRuntimeHasNoRows(t *testing.T) {
	t.Parallel()
	rt := connectorruntime.New(backendrows.Set{}, nil, nil)
	if len(rt.Rows()) != 0 || rt.BaseError("x") != nil {
		t.Error("zero-Set runtime is not empty")
	}
}
