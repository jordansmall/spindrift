package console_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/console"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/panicguard"
)

const panicHelperEnv = "SPINDRIFT_CONSOLE_PANIC_HELPER"

const (
	enterAlt = "\x1b[?1049h"
	exitAlt  = "\x1b[?1049l"
	showCur  = "\x1b[?25h"
)

// signalWriter forwards to w and closes seen the first time the stream
// contains marker, so the child panics only once bubbletea owns the screen.
type signalWriter struct {
	w      io.Writer
	marker string
	seen   chan struct{}
	once   sync.Once
}

func (s *signalWriter) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if strings.Contains(string(p), s.marker) {
		s.once.Do(func() { close(s.seen) })
	}
	return n, err
}

// TestConsolePanicHelper is not a real test: it is re-executed as a
// subprocess by the test below, since the re-panic kills the process.
func TestConsolePanicHelper(t *testing.T) {
	if os.Getenv(panicHelperEnv) == "" {
		return
	}
	in, _ := io.Pipe() // never yields input
	out := &signalWriter{w: os.Stdout, marker: enterAlt, seen: make(chan struct{})}
	go func() {
		select {
		case <-out.seen:
		case <-time.After(10 * time.Second):
		}
		panicguard.Go(func() { panic("console-boom") })
	}()
	_ = console.Run(forge.NewFake(), t.TempDir(), in, out, nil)
}

func TestRun_GoPanicReleasesTerminalBeforeCrash(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestConsolePanicHelper$")
	cmd.Env = append(os.Environ(), panicHelperEnv+"=1")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err == nil {
		t.Fatalf("child exited zero, want crash\n%q", buf.String())
	}
	got := buf.String()
	pan := strings.Index(got, "panic: console-boom")
	if pan < 0 {
		t.Fatalf("no panic in child output:\n%q", got)
	}
	enter := strings.Index(got, enterAlt)
	if enter < 0 || enter > pan {
		t.Fatalf("alt-screen never entered before panic:\n%q", got)
	}
	for _, seq := range []string{exitAlt, showCur} {
		i := strings.LastIndex(got[:pan], seq)
		if i < 0 || i < enter {
			t.Errorf("%q not emitted between alt-screen entry and panic:\n%q", seq, got)
		}
	}
}
