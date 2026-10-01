package stopsignal

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// Relay is Notify's internal relay factored out so it can be driven from a
// fake chan os.Signal (#3520): the real Notify itself is a one-line
// signal.Notify call plus this relay, and the signal.Notify half is trusted
// stdlib wiring not worth exercising with a real SIGTERM/SIGINT to the test
// binary. A lone signal, of either kind, closes stop and leaves abort open
// -- it drains, it does not abort (#3521).
func TestRelaySignals_SingleSignalClosesStopOnly(t *testing.T) {
	for _, sigVal := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sigVal.String(), func(t *testing.T) {
			sig := make(chan os.Signal, 2)
			stop, abort, cleanup := Relay(sig)
			defer cleanup()

			sig <- sigVal // a plain channel send, never a real OS signal

			select {
			case <-stop:
			case <-time.After(time.Second):
				t.Fatal("stop never closed after a value arrived on sig")
			}

			select {
			case <-abort:
				t.Fatal("abort closed after only one signal")
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

// The kind of signal never matters, only first versus second: every
// combination of TERM/INT closes stop on the first and abort on the second
// (#3521).
func TestRelaySignals_SecondSignalOfEitherKindClosesAbort(t *testing.T) {
	combos := []struct {
		name          string
		first, second os.Signal
	}{
		{"TERM,TERM", syscall.SIGTERM, syscall.SIGTERM},
		{"TERM,INT", syscall.SIGTERM, syscall.SIGINT},
		{"INT,INT", syscall.SIGINT, syscall.SIGINT},
		{"INT,TERM", syscall.SIGINT, syscall.SIGTERM},
	}
	for _, combo := range combos {
		t.Run(combo.name, func(t *testing.T) {
			sig := make(chan os.Signal, 2)
			stop, abort, cleanup := Relay(sig)
			defer cleanup()

			sig <- combo.first

			select {
			case <-stop:
			case <-time.After(time.Second):
				t.Fatal("stop never closed after the first signal")
			}
			select {
			case <-abort:
				t.Fatal("abort closed after only the first signal")
			case <-time.After(50 * time.Millisecond):
			}

			sig <- combo.second

			select {
			case <-abort:
			case <-time.After(time.Second):
				t.Fatal("abort never closed after the second signal")
			}
		})
	}
}

// Third and later signals are no-ops: Relay's goroutine returns right after
// closing abort, so nothing is left reading sig and a second close on
// either channel (which would panic) can never happen (#3521).
func TestRelaySignals_ThirdAndLaterSignalsAreNoOps(t *testing.T) {
	sig := make(chan os.Signal, 4)
	stop, abort, cleanup := Relay(sig)
	defer cleanup()

	sig <- syscall.SIGTERM
	sig <- syscall.SIGINT
	sig <- syscall.SIGTERM
	sig <- syscall.SIGINT

	select {
	case <-abort:
	case <-time.After(time.Second):
		t.Fatal("abort never closed after the second signal")
	}
	// Give the (already-returned) relay goroutine a moment in case a bug
	// left it still draining sig, then confirm neither channel panicked and
	// both ended up in their expected closed state.
	select {
	case <-stop:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("stop was not closed")
	}
}

// cleanup must actually silence the relay: a value sent on sig after cleanup
// is never consumed, so neither channel closes (#3640).
func TestRelaySignals_CleanupSilencesRelayWithoutClosingEitherChannel(t *testing.T) {
	base := runtime.NumGoroutine()
	sig := make(chan os.Signal, 2)
	stop, abort, cleanup := Relay(sig)
	cleanup()

	// cleanup closes quit but does not wait for the goroutine. Sending first
	// would race the relay's select (both quit and sig ready picks at random),
	// so wait for the goroutine to exit before sending.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > base {
		t.Fatalf("relay goroutine still running after cleanup: %d goroutines, baseline %d", got, base)
	}

	// Two values: a live relay would close stop on the first and abort on
	// the second, so both assertions below bind.
	sig <- syscall.SIGTERM
	sig <- syscall.SIGINT

	select {
	case <-stop:
		t.Fatal("stop closed by a signal sent after cleanup")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-abort:
		t.Fatal("abort closed by a signal sent after cleanup")
	case <-time.After(50 * time.Millisecond):
	}
}
