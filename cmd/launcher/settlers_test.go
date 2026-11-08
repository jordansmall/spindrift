package main

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// Go has no exhaustive-switch check, so a Settle value added without an arm in
// newSettlers would reach bootstrap as a launch context with no settler. This
// feeds every value the axis declares and demands exactly one settler back.
// Settle is an iota block and kindAxes keeps source order, so the i-th name is
// Settle(i).
func TestNewSettlers_EveryStrategyYieldsExactlyOneSettler(t *testing.T) {
	consts := kindAxes()["Settle"]
	if len(consts) == 0 {
		t.Fatal("kindAxes()[Settle] is empty; the guard's source derivation broke")
	}
	for i, name := range consts {
		t.Run(name, func(t *testing.T) {
			fc := forge.NewFake()
			merge, verdict, terminated, err := newSettlers(dispatchkind.Settle(i), minimalValidConfig(), fc, testWired(fc), fc, forge.Capabilities{})
			if err != nil {
				t.Fatalf("newSettlers(%s) error = %v, want an arm for every Settle value", name, err)
			}
			if terminated == nil {
				t.Errorf("newSettlers(%s): terminated registry is nil; Console, waves and the shutdown gate need one", name)
			}
			if (merge == nil) == (verdict == nil) {
				t.Errorf("newSettlers(%s): merge nil = %t, verdict nil = %t; want exactly one set", name, merge == nil, verdict == nil)
			}
		})
	}
	fc := forge.NewFake()
	if _, _, _, err := newSettlers(dispatchkind.Settle(len(consts)), minimalValidConfig(), fc, testWired(fc), fc, forge.Capabilities{}); err == nil {
		t.Error("newSettlers(undeclared strategy) error = nil, want one")
	}
}

// The registry newSettlers hands back is the one the merge settler's checkpoints
// read: a mark written to any other registry never reaches an in-flight settle,
// and the reclaimed issue settles on to agent-complete (#3522).
func TestNewSettlers_MergeSettlerSharesTheSessionRegistry(t *testing.T) {
	for _, strategy := range []dispatchkind.Settle{dispatchkind.SettleMerge, dispatchkind.SettleLedger} {
		fc := forge.NewFake()
		merge, _, terminated, err := newSettlers(strategy, minimalValidConfig(), fc, testWired(fc), fc, forge.Capabilities{})
		if err != nil {
			t.Fatalf("newSettlers(%d): %v", strategy, err)
		}
		if terminated != merge.Registry() {
			t.Errorf("newSettlers(%d): returned registry is not the merge settler's own", strategy)
		}
	}
}
