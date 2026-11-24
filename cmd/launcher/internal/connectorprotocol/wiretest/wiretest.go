// Package wiretest holds a core struct to the wire shape the Connector protocol
// description gives it, for tests only: the SDK renders its twin of the same
// shape from the same description, so a struct that agrees here cannot drift
// from the SDK's.
package wiretest

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
)

// Agree fails t unless the JSON field names of v's struct type are exactly
// those the description lists under key, in connectorprotocol.WireFields. A
// field without a json tag counts under its Go name (driverkit.Event has no
// tags), and an embedded struct is flattened as encoding/json does. exempt
// names core-only fields the description leaves out; each must still exist, so
// an exemption cannot outlive its field.
func Agree(t *testing.T, key string, v any, exempt ...string) {
	t.Helper()
	want, ok := connectorprotocol.WireFields[key]
	if !ok {
		t.Fatalf("the protocol description has no wire shape %q", key)
	}
	got := fieldNames(reflect.TypeOf(v))
	for _, e := range exempt {
		if !slices.Contains(got, e) {
			t.Errorf("%T exempts %s, which it no longer has; drop the exemption", v, e)
		}
	}
	got = slices.DeleteFunc(got, func(n string) bool { return slices.Contains(exempt, n) })
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("%T drifted from protocol wire shape %q\n core:        %q\n description: %q", v, key, got, want)
	}
}

func fieldNames(rt reflect.Type) []string {
	var out []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			out = append(out, fieldNames(f.Type)...)
		case !f.IsExported():
		case name == "":
			out = append(out, f.Name)
		default:
			out = append(out, name)
		}
	}
	return out
}
