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
