package runner

import (
	"reflect"
	"testing"
)

// OffArgvKeys is exactly the non-Driver literal plus the generated Driver
// credential env names, so a new Driver credential is off-argv without a hand
// edit here (issue #5507).
func TestOffArgvKeys_CoversDriverCredentialEnv(t *testing.T) {
	want := map[string]bool{}
	for k := range nonDriverOffArgvKeys {
		want[k] = true
	}
	for _, k := range driverCredentialEnv {
		want[k] = true
	}
	if !reflect.DeepEqual(OffArgvKeys, want) {
		t.Errorf("OffArgvKeys = %v, want the non-Driver keys plus the Driver credential env %v", OffArgvKeys, want)
	}
}
