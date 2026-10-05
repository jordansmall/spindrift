package dispatch

import (
	"errors"
	"testing"
)

// routeArm records which Route arm ran and the payload it was handed.
func routeArm(d Disposition) (arm string, calls int, got Result) {
	arm = Route(d,
		func() string { calls++; return "skip" },
		func(r Result) string { calls++; got = r; return "failure" },
		func(r Result) string { calls++; got = r; return "success" },
	)
	return arm, calls, got
}

func TestRoute_EachConstructorHitsOnlyItsOwnArm(t *testing.T) {
	payload := Result{Comment: "c", Err: errors.New("boom"), KilledBySignal: true}
	tests := []struct {
		name        string
		d           Disposition
		wantArm     string
		wantPayload Result
	}{
		{"Skipped", Skipped(), "skip", Result{}},
		{"Failed", Failed(payload), "failure", payload},
		{"Succeeded", Succeeded(payload), "success", payload},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			arm, calls, got := routeArm(tc.d)
			if arm != tc.wantArm {
				t.Errorf("arm = %q, want %q", arm, tc.wantArm)
			}
			if calls != 1 {
				t.Errorf("arms called = %d, want exactly 1", calls)
			}
			if got.Comment != tc.wantPayload.Comment || got.Err != tc.wantPayload.Err ||
				got.KilledBySignal != tc.wantPayload.KilledBySignal {
				t.Errorf("payload = %+v, want %+v", got, tc.wantPayload)
			}
		})
	}
}

// A forgotten initialization must fail closed (see dispositionFailed).
func TestRoute_ZeroDispositionIsFailure(t *testing.T) {
	arm, calls, got := routeArm(Disposition{})
	if arm != "failure" || calls != 1 {
		t.Errorf("zero Disposition routed to %q (%d call(s)), want one failure call", arm, calls)
	}
	if got.Comment != "" || got.Err != nil {
		t.Errorf("zero Disposition payload = %+v, want empty", got)
	}
}
