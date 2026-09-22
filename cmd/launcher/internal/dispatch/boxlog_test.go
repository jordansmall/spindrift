package dispatch

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// errWriter fails every write after the first n bytes have been accepted.
type errWriter struct {
	err error
}

func (w *errWriter) Write(p []byte) (int, error) { return 3, w.err }

func TestBoxLogMirrorAtLineBoundaryWritesThrough(t *testing.T) {
	var buf bytes.Buffer
	bl := newBoxLog(&buf)

	if _, err := bl.stream().Write([]byte("{\"type\":\"a\"}\n")); err != nil {
		t.Fatalf("stream write: %v", err)
	}
	n, err := bl.mirror().Write([]byte("spindrift_op one\n"))
	if err != nil || n != len("spindrift_op one\n") {
		t.Fatalf("mirror write = (%d, %v)", n, err)
	}
	if _, err := bl.stream().Write([]byte("{\"type\":\"b\"}\n")); err != nil {
		t.Fatalf("stream write: %v", err)
	}

	want := "{\"type\":\"a\"}\nspindrift_op one\n{\"type\":\"b\"}\n"
	if got := buf.String(); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestBoxLogMirrorHeldUntilStreamLineCompletes(t *testing.T) {
	var buf bytes.Buffer
	bl := newBoxLog(&buf)

	// The splice hazard: the driver's stream arrives in pipe-sized chunks
	// that can end mid-line, so a mirror line written between two chunks
	// would land inside the stream-json line carrying SPINDRIFT_OUTCOME.
	if _, err := bl.stream().Write([]byte("{\"outcome\":\"SPINDRIFT_")); err != nil {
		t.Fatalf("stream write: %v", err)
	}
	n, err := bl.mirror().Write([]byte("spindrift_op held\n"))
	if err != nil || n != len("spindrift_op held\n") {
		t.Fatalf("mirror write = (%d, %v)", n, err)
	}
	if got := buf.String(); strings.Contains(got, "spindrift_op") {
		t.Fatalf("mirror spliced into a mid-line stream chunk: %q", got)
	}
	if _, err := bl.stream().Write([]byte("OUTCOME=success\"}\n")); err != nil {
		t.Fatalf("stream write: %v", err)
	}

	want := "{\"outcome\":\"SPINDRIFT_OUTCOME=success\"}\nspindrift_op held\n"
	if got := buf.String(); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestBoxLogMultiplePendingMirrorsFlushInOrder(t *testing.T) {
	var buf bytes.Buffer
	bl := newBoxLog(&buf)

	if _, err := bl.stream().Write([]byte("{\"a\":")); err != nil {
		t.Fatalf("stream write: %v", err)
	}
	for _, line := range []string{"op one\n", "op two\n", "op three\n"} {
		if _, err := bl.mirror().Write([]byte(line)); err != nil {
			t.Fatalf("mirror write: %v", err)
		}
	}
	if _, err := bl.stream().Write([]byte("1}\n")); err != nil {
		t.Fatalf("stream write: %v", err)
	}

	want := "{\"a\":1}\nop one\nop two\nop three\n"
	if got := buf.String(); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestBoxLogFlushEmitsPendingWhenStreamNeverCompletes(t *testing.T) {
	var buf bytes.Buffer
	bl := newBoxLog(&buf)

	if _, err := bl.stream().Write([]byte("{\"partial\":")); err != nil {
		t.Fatalf("stream write: %v", err)
	}
	if _, err := bl.mirror().Write([]byte("op last\n")); err != nil {
		t.Fatalf("mirror write: %v", err)
	}
	if err := bl.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	want := "{\"partial\":\nop last\n"
	if got := buf.String(); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
	if err := bl.flush(); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if got := buf.String(); got != want {
		t.Fatalf("second flush wrote again: %q", got)
	}
}

func TestBoxLogConcurrentViewsNeverSplice(t *testing.T) {
	var buf bytes.Buffer
	bl := newBoxLog(&buf)

	const writers = 8
	const perWriter = 50
	want := make(map[string]int)
	for i := 0; i < writers; i++ {
		want[fmt.Sprintf("stream-%d", i)] = perWriter
		want[fmt.Sprintf("mirror-%d", i)] = perWriter
	}

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			line := []byte(fmt.Sprintf("stream-%d\n", i))
			for j := 0; j < perWriter; j++ {
				if _, err := bl.stream().Write(line); err != nil {
					t.Errorf("stream write: %v", err)
					return
				}
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			line := []byte(fmt.Sprintf("mirror-%d\n", i))
			for j := 0; j < perWriter; j++ {
				if _, err := bl.mirror().Write(line); err != nil {
					t.Errorf("mirror write: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if err := bl.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got := make(map[string]int)
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	for _, line := range lines {
		if _, ok := want[line]; !ok {
			t.Fatalf("spliced or unexpected line %q", line)
		}
		got[line]++
	}
	for line, n := range want {
		if got[line] != n {
			t.Fatalf("line %q written %d times, want %d", line, got[line], n)
		}
	}
}

func TestBoxLogStreamPropagatesUnderlyingResult(t *testing.T) {
	sentinel := errors.New("disk full")
	bl := newBoxLog(&errWriter{err: sentinel})

	n, err := bl.stream().Write([]byte("abcdef\n"))
	if n != 3 {
		t.Fatalf("stream n = %d, want 3", n)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("stream err = %v, want %v", err, sentinel)
	}
}
