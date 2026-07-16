package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
)

// TestFake_ImplementsSettler asserts that *Fake satisfies Settler.
func TestFake_ImplementsSettler(t *testing.T) {
	var _ Settler = NewFake()
}

// TestFake_RecordsCalls verifies the Fake records Settle/SettleAdopted
// invocations for callers that only need to assert wiring.
func TestFake_RecordsCalls(t *testing.T) {
	f := NewFake()
	d := dispatch.NewFake()
	result := dispatch.Result{Success: true}

	f.Settle(d, "1", 7, result)
	f.SettleAdopted(d, "2", 0, testPR)

	if len(f.SettleCalls) != 1 || f.SettleCalls[0].Num != "1" || f.SettleCalls[0].Gen != 7 {
		t.Errorf("SettleCalls = %+v, want one call for num=1, gen=7", f.SettleCalls)
	}
	if len(f.SettleAdoptedCalls) != 1 || f.SettleAdoptedCalls[0].Num != "2" || f.SettleAdoptedCalls[0].PRURL != testPR {
		t.Errorf("SettleAdoptedCalls = %+v, want one call for num=2, pr=%s", f.SettleAdoptedCalls, testPR)
	}
}
