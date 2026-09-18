package main

import (
	"bufio"
	"io"
	"os"
	"os/signal"

	"github.com/charmbracelet/x/term"
)

// readMasked reads one line from stdin without echoing it, but only when stdin
// is a real TTY. A pipe or a redirected file is also an *os.File, so the
// IsTerminal check, not the type assertion, keeps non-interactive input working.
// Every other shape falls back to the echoing scanner read.
func readMasked(stdin io.Reader, scanner *bufio.Scanner) (value string, masked bool) {
	f, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		scanner.Scan()
		return scanner.Text(), false
	}

	state, err := term.GetState(f.Fd())
	if err != nil {
		// GetState never changed terminal state, so echo is still on and there
		// is nothing to restore. This is the one path where a masked prompt
		// still echoes.
		scanner.Scan()
		return scanner.Text(), false
	}

	// term.ReadPassword restores the terminal in a defer that a Ctrl-C mid-paste
	// skips, leaving the terminal echoing nothing. Catch the interrupt and
	// restore the pre-prompt state before the process exits.
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

	// Reading off the fd instead of through scanner is safe only because a
	// canonical-mode TTY hands read() one line at a time, so scanner cannot
	// have buffered ahead into this line.
	secret, err := term.ReadPassword(f.Fd())
	signal.Stop(sig)
	close(done)
	if err != nil {
		scanner.Scan()
		return scanner.Text(), false
	}
	return string(secret), true
}
