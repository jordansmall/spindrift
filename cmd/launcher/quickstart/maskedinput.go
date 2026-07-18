package main

import (
	"bufio"
	"io"
	"os"
	"os/signal"

	"github.com/charmbracelet/x/term"
)

// readMasked reads one line from stdin without echoing it to the terminal,
// but only when stdin is a real TTY: term.IsTerminal reports false for a
// pipe or a redirected file even though both are *os.File, so gating on
// IsTerminal (not just the type assertion) is what keeps non-interactive
// input working. Every other shape — including the strings.NewReader the
// wizard's test suite drives — falls back to scanner, the same echoing read
// every other prompt in the wizard uses.
func readMasked(stdin io.Reader, scanner *bufio.Scanner) (value string, masked bool) {
	f, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		scanner.Scan()
		return scanner.Text(), false
	}

	state, err := term.GetState(f.Fd())
	if err != nil {
		// Echo is still on here — GetState never touched terminal state,
		// so there is nothing to restore. This is the one path where a
		// masked prompt can still echo, and only on a GetState failure,
		// which is rare enough to accept rather than add a second
		// fallback layer for.
		scanner.Scan()
		return scanner.Text(), false
	}

	// term.ReadPassword restores the terminal via its own deferred
	// IoctlSetTermios, but only on a normal return — a Ctrl-C mid-paste
	// otherwise skips that defer, since it isn't caught anywhere, and
	// leaves the terminal echoing nothing. Catch the interrupt here and
	// restore the pre-prompt state ourselves before the process exits.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			term.Restore(f.Fd(), state)
			os.Exit(130)
		case <-done:
		}
	}()

	// Reading directly off the fd here, instead of through scanner, is
	// safe only because a canonical-mode TTY hands read() one line at a
	// time — scanner can never have buffered ahead into this line.
	secret, err := term.ReadPassword(f.Fd())
	signal.Stop(sig)
	close(done)
	if err != nil {
		scanner.Scan()
		return scanner.Text(), false
	}
	return string(secret), true
}
