package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
)

func TestForReasonStateFromTable(t *testing.T) {
	for _, row := range dispatchrecord.Reasons() {
		if row.State == dispatchrecord.AnyState {
			continue
		}
		v := ForReason(row.Name)
		if v.Reason() != row.Name {
			t.Errorf("ForReason(%q).Reason() = %q", row.Name, v.Reason())
		}
		if v.State().String() != row.State {
			t.Errorf("ForReason(%q).State() = %s, want %s", row.Name, v.State(), row.State)
		}
	}
}

func TestSettledPanicsOnZeroVerdict(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Settled with a zero Verdict did not panic")
		}
	}()
	Settled(dispatchkey.Issue("7"), "", Verdict{}, hostop.DispatchSettled{})
}

func TestForReasonPanicsWithoutFixedState(t *testing.T) {
	for _, reason := range []string{dispatchrecord.ReasonTransitionRefused, "failed", "no-such-reason"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("ForReason(%q) did not panic", reason)
				}
			}()
			ForReason(reason)
		}()
	}
}

func TestRefusedKeepsDisplacedState(t *testing.T) {
	v := refused(ForReason(dispatchrecord.ReasonMerged))
	if v.State() != forge.Complete || v.Reason() != dispatchrecord.ReasonTransitionRefused {
		t.Errorf("refused = (%s, %q), want (complete, transition-refused)", v.State(), v.Reason())
	}
}

func TestSettledPanicsOnInProgressVerdict(t *testing.T) {
	for _, reason := range []string{dispatchrecord.ReasonOffScript, dispatchrecord.ReasonHandoffBlocked} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Settled with the in-progress %q Verdict did not panic", reason)
				}
			}()
			Settled(dispatchkey.Issue("7"), "", ForReason(reason), hostop.DispatchSettled{})
		}()
	}
}
