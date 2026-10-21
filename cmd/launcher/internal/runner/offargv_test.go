package runner

import (
	"reflect"
	"sort"
	"testing"
)

// OffArgvKeys is exactly the non-Driver literal plus the generated Driver
// credential env names, so a new Driver credential is off-argv without a hand
// edit here (issue #5507).
func TestOffArgvKeys_CoversDriverCredentialEnv(t *testing.T) {
	seen := map[string]bool{}
	for k := range nonDriverOffArgvKeys {
		seen[k] = true
	}
	for _, k := range driverCredentialEnv {
		seen[k] = true
	}
	want := make([]string, 0, len(seen))
	for k := range seen {
		want = append(want, k)
	}
	sort.Strings(want)
	if got := OffArgvKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("OffArgvKeys() = %v, want the non-Driver keys plus the Driver credential env %v", got, want)
	}
}

// The accessors hand out copies: a caller scribbling on OffArgvKeys()'s result
// must not change the set any later caller (or IsOffArgvKey) sees.
func TestOffArgvKeys_CallerMutationDoesNotLeak(t *testing.T) {
	want := OffArgvKeys()
	got := OffArgvKeys()
	got[0] = "MUTATED"

	if again := OffArgvKeys(); !reflect.DeepEqual(again, want) {
		t.Errorf("OffArgvKeys() after caller mutation = %v, want %v", again, want)
	}
	for _, k := range want {
		if !IsOffArgvKey(k) {
			t.Errorf("IsOffArgvKey(%q) = false after caller mutation", k)
		}
	}
	if IsOffArgvKey("MUTATED") {
		t.Error(`IsOffArgvKey("MUTATED") = true; caller mutation leaked into the set`)
	}
}
