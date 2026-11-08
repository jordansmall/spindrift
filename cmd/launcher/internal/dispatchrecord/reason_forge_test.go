package dispatchrecord_test

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

// Pins the State* consts to forge's state names.
func TestStateConstsMatchForge(t *testing.T) {
	for _, c := range []struct {
		got  string
		want forge.DispatchState
	}{
		{dispatchrecord.StateComplete, forge.Complete},
		{dispatchrecord.StateFailed, forge.Failed},
		{dispatchrecord.StateAmbiguous, forge.Ambiguous},
		{dispatchrecord.StateRecoverable, forge.Recoverable},
		{dispatchrecord.StateInProgress, forge.InProgress},
	} {
		if c.got != c.want.String() {
			t.Errorf("state const %q != forge %s", c.got, c.want)
		}
	}
}
