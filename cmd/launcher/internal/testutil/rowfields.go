package testutil

import (
	"reflect"
	"testing"
)

// AssertStructFieldsMatchRows fails t for every field of typ missing from
// rowFields and every rowFields entry typ lacks, so a Descriptor field added
// on only one side of a Nix-rendered registry fails instead of silently never
// being set. renderer names where the field table lives (it fills "rowFields
// (<renderer>)"), rowsHint finishes "set it on <rowsHint>", and structFile is
// the Go file declaring typ; all three only shape the failure text.
func AssertStructFieldsMatchRows(t *testing.T, typ reflect.Type, rowFields []string, renderer, rowsHint, structFile string) {
	t.Helper()
	structFields := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		structFields[typ.Field(i).Name] = true
	}
	nixFields := map[string]bool{}
	for _, name := range rowFields {
		nixFields[name] = true
	}
	for name := range structFields {
		if !nixFields[name] {
			t.Errorf("%s.%s has no entry in rowFields (%s); add the row attribute there and set it on %s, then run `nix run .#regen`", typ.Name(), name, renderer, rowsHint)
		}
	}
	for name := range nixFields {
		if !structFields[name] {
			t.Errorf("rowFields (%s) renders %s but %s has no such field; add it to the struct in %s or drop the rowFields entry, then run `nix run .#regen`", renderer, name, typ.Name(), structFile)
		}
	}
}
