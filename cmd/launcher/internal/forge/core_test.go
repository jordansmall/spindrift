package forge

import (
	"reflect"
	"sync"
	"testing"
)

// TestCoreFieldShape asserts that core holds exactly the five members
// admitted under the "writers in two different capabilities" rule (issue
// #2358): mu, prStates, LandingCallLog, ProbeErr, and ProbeRepo — no more,
// no fewer, with these exact names and types.
func TestCoreFieldShape(t *testing.T) {
	want := map[string]reflect.Type{
		"mu":             reflect.TypeOf(sync.Mutex{}),
		"prStates":       reflect.TypeOf(map[string]PRState{}),
		"LandingCallLog": reflect.TypeOf([]string{}),
		"ProbeErr":       reflect.TypeOf((*error)(nil)).Elem(),
		"ProbeRepo":      reflect.TypeOf(""),
	}

	typ := reflect.TypeOf(core{})
	if typ.NumField() != len(want) {
		t.Fatalf("core has %d fields, want %d", typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		wantType, ok := want[f.Name]
		if !ok {
			t.Errorf("unexpected field %q on core", f.Name)
			continue
		}
		if f.Type != wantType {
			t.Errorf("field %q has type %s, want %s", f.Name, f.Type, wantType)
		}
	}
	for name := range want {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("core missing expected field %q", name)
		}
	}
}
