package dispatch

import (
	"io"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/runner"
)

// newKillFactory builds a Factory with an injected fake Clock and a discarded
// heartbeat sink, so a kill-latch test can drive both Factory.Kill and
// Factory.New against the one Factory the way Console terminate does.
func newKillFactory(t *testing.T, cfg Config, fr runner.Runner, drv fakeDriver, clock Clock) *Factory {
	t.Helper()
	f, err := NewFactory(cfg, tempLogDir(t), fr, drv, clock)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(f.Cleanup)
	f.SetHeartbeatOut(io.Discard)
	return f
}

// A Kill landing between the claim and the first runOnce matches no container
// at all, so without a latch the Box would launch anyway for an issue the
// abort already released back to the dispatchable pool (issue #3521).
func TestKill_BeforeFirstRun_NeverLaunches(t *testing.T) {
	fr := runner.NewFake()
	clock := Clock{
		Now:   time.Now,
		Sleep: func(time.Duration) { t.Error("Sleep called; want no retry after kill") },
	}
	f := newKillFactory(t, retryConfig(3, 5, 0), fr, fakeDriver{}, clock)
	d := f.New("1", "t")

	if err := f.Kill("1"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	done := make(chan Result, 1)
	go func() { done <- d.Run() }()
	var result Result
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a kill")
	}

	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls = %d, want 0 (killed before launch)", len(fr.RunCalls))
	}
	if result.Success {
		t.Error("want Success=false after a kill, got true")
	}
	if result.AlreadyInFlight {
		t.Error("want AlreadyInFlight=false after a kill, got true")
	}
}

// A Kill landing during a transient backoff must both stop the re-dispatch and
// cut the wait short: a rate-limit hold can run an hour (issue #3521).
func TestKill_DuringBackoff_InterruptsWaitAndStopsRedispatch(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Network}, nil
	}}
	var f *Factory
	clock := Clock{
		Now: time.Now,
		Sleep: func(time.Duration) {
			_ = f.Kill("1")
			time.Sleep(10 * time.Second)
		},
	}
	f = newKillFactory(t, retryConfig(3, 5, 0), fr, drv, clock)
	d := f.New("1", "t")

	done := make(chan Result, 1)
	start := time.Now()
	go func() { done <- d.Run() }()
	var result Result
	select {
	case result = <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Run waited out the backoff after a kill")
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %s; want a prompt return once the kill landed", elapsed)
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls = %d, want 1 (no re-dispatch after the kill)", len(fr.RunCalls))
	}
	if result.Success {
		t.Error("want Success=false after a kill, got true")
	}
}

// Kill is legitimately called twice for one issue (a Console Terminate, then a
// signalled abort), so closing the latch must be idempotent (issue #3521).
func TestKill_TwiceDoesNotPanic(t *testing.T) {
	fr := runner.NewFake()
	f := newKillFactory(t, Config{}, fr, fakeDriver{}, RealClock())
	f.New("1", "t")

	if err := f.Kill("1"); err != nil {
		t.Fatalf("first Kill: %v", err)
	}
	if err := f.Kill("1"); err != nil {
		t.Fatalf("second Kill: %v", err)
	}

	if len(fr.KillCalls) != 2 {
		t.Errorf("KillCalls = %v, want two reaps", fr.KillCalls)
	}
}

// The negative cases: a Dispatch whose own latch was never closed launches
// normally, whether the Factory killed a different issue or an earlier claim
// on this one — a re-pick after a Console Terminate (issue #649) must still
// dispatch.
func TestKill_LeavesUnkilledClaimsRunnable(t *testing.T) {
	tests := []struct {
		name     string
		killWhen func(f *Factory)
	}{
		{name: "other issue killed", killWhen: func(f *Factory) { _ = f.Kill("99") }},
		{name: "prior claim on this issue killed", killWhen: func(f *Factory) {
			f.New("1", "t")
			_ = f.Kill("1")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := runner.NewFake()
			var sleeps []time.Duration
			f := newKillFactory(t, retryConfig(3, 5, 0), fr, fakeDriver{}, fakeClock(time.Time{}, &sleeps))
			tt.killWhen(f)
			d := f.New("1", "t")
			fr.WriteToOutput = nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")

			result := d.Run()

			if len(fr.RunCalls) != 1 {
				t.Errorf("RunCalls = %d, want 1", len(fr.RunCalls))
			}
			if !result.Success {
				t.Error("want Success=true for an unkilled claim, got false")
			}
		})
	}
}
