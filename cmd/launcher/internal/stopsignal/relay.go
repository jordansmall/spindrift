// Package stopsignal is the Stop/Abort latch's signal source, shared by the
// launcher and daemon binaries so both relay SIGTERM/SIGINT onto the same
// two-stage stop-then-abort channels.
package stopsignal

import (
	"os"
	"os/signal"
	"syscall"
)

// Notify installs a SIGTERM and SIGINT handler and relays them onto the
// returned channels, the wind-down/escalation seam each binary's own config
// expects: the launcher's waves.Config.Stop/Abort, and the daemon's
// daemon.Config.Stop/Abort, which the pool re-exposes on every child
// request (#3626). The kind of signal never matters, only first
// versus second (#3521): the first of either kind closes stop (drain), the
// second closes abort (escalate). sig is buffered at 2, not 1, so a second
// signal delivered back-to-back with the first -- before the relay
// goroutine has had a chance to run -- is queued rather than dropped; a
// third or later signal, once the buffer and the relay's own count are both
// spent, is simply dropped, which is exactly the "third and later are
// no-ops" requirement (nothing ever drains sig again once the relay has
// closed abort and returned). cleanup stops the relay goroutine but
// deliberately never calls signal.Stop -- reverting the disposition
// mid-teardown would let a third signal reach the OS default (kill) while
// the launcher's cleanup and each Box's deferred teardown are still running
// (#3520).
func Notify() (stop, abort <-chan struct{}, cleanup func()) {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	return Relay(sig)
}

// Relay is factored out of Notify so a test can drive it from a fake chan
// os.Signal instead of sending a real OS signal to the test binary (#3520).
// It closes stopCh/abortCh, rather than merely writing to them, since every
// reader reads them independently and concurrently -- RunContinuous's
// refill guard, terminal check, and stop/abort-watcher goroutines in the
// launcher, the pool's Stop watcher and one forwarder goroutine per running
// child in the daemon -- and a close is the only send all of them observe. The goroutine
// returns immediately after closing abortCh: escalation is edge-triggered
// and happens exactly once, so a third value on sig (mashing Ctrl-C) finds
// nothing left reading the channel, and can neither re-enter the abort path
// nor race the teardown it triggered (#3521).
func Relay(sig <-chan os.Signal) (stop, abort <-chan struct{}, cleanup func()) {
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	quit := make(chan struct{})
	go func() {
		for n := 0; ; {
			select {
			case <-sig:
				n++
				switch n {
				case 1:
					close(stopCh)
				case 2:
					close(abortCh)
					return
				}
			case <-quit:
				return
			}
		}
	}()
	return stopCh, abortCh, func() { close(quit) }
}
