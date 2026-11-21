// Package stopsignal is the Stop/Abort latch's signal source, shared by the
// launcher and daemon binaries so both relay SIGTERM/SIGINT onto the same
// two-stage stop-then-abort channels.
package stopsignal

import (
	"os"
	"os/signal"
	"syscall"

	"spindrift.dev/launcher/internal/panicguard"
)

// Notify installs a SIGTERM and SIGINT handler and relays them onto the
// returned channels: the launcher's waves.Config.Stop/Abort and the daemon's
// daemon.Config.Stop/Abort, which the pool re-exposes on every child request
// (#3626). Only first versus second matters (#3521): the first closes stop
// (drain), the second closes abort (escalate). sig is buffered at 2 so a
// second signal arriving before the relay runs is queued, not dropped; a
// third or later is dropped, a no-op. cleanup never calls signal.Stop: reverting
// the disposition mid-teardown would let a third signal reach the OS default
// (kill) while Box teardown is still running (#3520).
func Notify() (stop, abort <-chan struct{}, cleanup func()) {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	return Relay(sig)
}

// Relay is factored out of Notify so a test can drive it from a fake chan
// os.Signal (#3520). It closes stopCh/abortCh rather than sending, since many
// goroutines in both binaries read them concurrently and a close is the only
// send all observe. It returns right after closing abortCh, so a third value
// on sig finds no reader and can neither re-enter the abort path nor race the
// teardown it triggered (#3521).
func Relay(sig <-chan os.Signal) (stop, abort <-chan struct{}, cleanup func()) {
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	quit := make(chan struct{})
	panicguard.Go(func() {
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
	})
	return stopCh, abortCh, func() { close(quit) }
}
