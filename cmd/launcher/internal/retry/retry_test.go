package retry

import (
	"testing"
	"time"
)

// recordingClock is a fake Clock that records durations passed to Sleep
// instead of actually sleeping.
type recordingClock struct {
	recorded []time.Duration
}

func newRecordingClock() *recordingClock {
	return &recordingClock{}
}

func (r *recordingClock) Clock() Clock {
	return Clock{
		Now: time.Now,
		Sleep: func(d time.Duration) {
			r.recorded = append(r.recorded, d)
		},
	}
}

func TestLinearBackoff_Do_NoJitter(t *testing.T) {
	rc := newRecordingClock()
	b := LinearBackoff{Unit: 5 * time.Second, Clock: rc.Clock()}

	b.Do(1)
	b.Do(2)
	b.Do(3)

	want := []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second}
	if len(rc.recorded) != len(want) {
		t.Fatalf("recorded %v, want %v", rc.recorded, want)
	}
	for i, d := range want {
		if rc.recorded[i] != d {
			t.Errorf("recorded[%d] = %v, want %v", i, rc.recorded[i], d)
		}
	}
}

func TestLinearBackoff_Do_WithJitter(t *testing.T) {
	rc := newRecordingClock()
	b := LinearBackoff{Unit: 2 * time.Second, Jitter: 1 * time.Second, Clock: rc.Clock()}

	b.Do(1)
	b.Do(2)

	want := []time.Duration{3 * time.Second, 5 * time.Second}
	if len(rc.recorded) != len(want) {
		t.Fatalf("recorded %v, want %v", rc.recorded, want)
	}
	for i, d := range want {
		if rc.recorded[i] != d {
			t.Errorf("recorded[%d] = %v, want %v", i, rc.recorded[i], d)
		}
	}
}

func TestLinearBackoff_Do_Cap(t *testing.T) {
	rc := newRecordingClock()
	b := LinearBackoff{Unit: 10 * time.Second, Cap: 25 * time.Second, Clock: rc.Clock()}

	b.Do(1)
	b.Do(2)
	b.Do(3)
	b.Do(4)

	want := []time.Duration{10 * time.Second, 20 * time.Second, 25 * time.Second, 25 * time.Second}
	if len(rc.recorded) != len(want) {
		t.Fatalf("recorded %v, want %v", rc.recorded, want)
	}
	for i, d := range want {
		if rc.recorded[i] != d {
			t.Errorf("recorded[%d] = %v, want %v", i, rc.recorded[i], d)
		}
	}
}

func TestLinearBackoff_Duration_NegativeUnitClampsToZero(t *testing.T) {
	b := LinearBackoff{Unit: -5 * time.Second, Jitter: 2 * time.Second}

	got := b.Duration(3)
	want := 2 * time.Second
	if got != want {
		t.Errorf("Duration(3) = %v, want %v", got, want)
	}
}

func TestLinearBackoff_Duration_NegativeJitterClampsToZero(t *testing.T) {
	b := LinearBackoff{Unit: 5 * time.Second, Jitter: -3 * time.Second}

	got := b.Duration(1)
	want := 5 * time.Second
	if got != want {
		t.Errorf("Duration(1) = %v, want %v", got, want)
	}
}

func TestLinearBackoff_Do_BothNegativeYieldsZero(t *testing.T) {
	rc := newRecordingClock()
	b := LinearBackoff{Unit: -5 * time.Second, Jitter: -3 * time.Second, Clock: rc.Clock()}

	b.Do(1)

	want := []time.Duration{0}
	if len(rc.recorded) != len(want) || rc.recorded[0] != want[0] {
		t.Errorf("recorded = %v, want %v", rc.recorded, want)
	}
}

func TestRealClock_NonNilFields(t *testing.T) {
	c := RealClock()

	if c.Now == nil {
		t.Error("RealClock().Now is nil")
	}
	if c.Sleep == nil {
		t.Error("RealClock().Sleep is nil")
	}
}

func TestPolicy_Backoff_WiresUnitAndClockOnly(t *testing.T) {
	rc := newRecordingClock()
	p := Policy{Unit: 5 * time.Second, Jitter: 1 * time.Second}

	b := p.Backoff(rc.Clock())

	if b.Jitter != 0 {
		t.Errorf("Jitter = %v, want 0", b.Jitter)
	}
	if b.Cap != 0 {
		t.Errorf("Cap = %v, want 0", b.Cap)
	}

	b.Do(2)

	want := []time.Duration{10 * time.Second}
	if len(rc.recorded) != len(want) || rc.recorded[0] != want[0] {
		t.Errorf("recorded = %v, want %v", rc.recorded, want)
	}
}

func TestLinearBackoff_DoUnless_SleepsFullDurationInSlices(t *testing.T) {
	rc := newRecordingClock()
	b := LinearBackoff{Unit: 2500 * time.Millisecond, Clock: rc.Clock()}

	if b.DoUnless(1, func() bool { return false }) {
		t.Fatal("DoUnless reported stopped though stop never fired")
	}

	var total time.Duration
	for _, d := range rc.recorded {
		if d <= 0 || d > stopSlice {
			t.Errorf("slice %v outside (0, %v]", d, stopSlice)
		}
		total += d
	}
	if total != b.Duration(1) {
		t.Errorf("slept %v in total, want %v", total, b.Duration(1))
	}
	if len(rc.recorded) < 2 {
		t.Errorf("recorded %v, want the wait split into several slices", rc.recorded)
	}
}

func TestLinearBackoff_DoUnless_StopMidwayEndsEarly(t *testing.T) {
	rc := newRecordingClock()
	b := LinearBackoff{Unit: 30 * time.Second, Clock: rc.Clock()}

	if !b.DoUnless(1, func() bool { return len(rc.recorded) >= 2 }) {
		t.Fatal("DoUnless did not report the stop")
	}
	if len(rc.recorded) != 2 {
		t.Errorf("recorded %d sleeps, want 2 (no sleep after stop): %v", len(rc.recorded), rc.recorded)
	}
}

func TestLinearBackoff_DoUnless_StopAlreadyTrueSleepsNothing(t *testing.T) {
	rc := newRecordingClock()
	b := LinearBackoff{Unit: 30 * time.Second, Clock: rc.Clock()}

	if !b.DoUnless(1, func() bool { return true }) {
		t.Fatal("DoUnless did not report the stop")
	}
	if len(rc.recorded) != 0 {
		t.Errorf("recorded %v, want no sleeps", rc.recorded)
	}
}
