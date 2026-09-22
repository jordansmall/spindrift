package dispatch

import (
	"io"
	"sync"
)

// boxLog is the line-atomic shared sink for the Box's raw output log. Two
// producers write that one file concurrently: the driver's stream-json copy
// (claude.Writer forwards every pipe chunk to raw verbatim) and the Signal
// socket handler's spindrift_op mirror. A bare mutex is not enough -- a pipe
// chunk can end mid-line, so a mirror line serialised between two chunks
// still splices itself into the middle of a stream-json line. If that line is
// the one carrying SPINDRIFT_OUTCOME, outcome.Resolve no longer finds a
// token-leading fielded outcome and a clean Box exit settles as a no-outcome
// run. So the mirror waits for a line boundary instead (ADR 0052, issue
// #3725).
type boxLog struct {
	mu sync.Mutex
	w  io.Writer
	// atLine reports whether the last stream byte written was '\n'.
	atLine bool
	// pending holds mirror bytes written while the stream was mid-line.
	pending []byte
}

// newBoxLog returns a boxLog writing to w, which must not be written directly
// by anyone else for as long as the views are in use.
func newBoxLog(w io.Writer) *boxLog {
	return &boxLog{w: w, atLine: true}
}

// stream returns the view for the driver's verbatim stream-json copy, to be
// passed as raw to claude.NewHeartbeatWriter.
func (l *boxLog) stream() io.Writer { return boxLogStream{l: l} }

// mirror returns the view for the Signal socket handler's log writer. Each
// Write must be one whole line, newline included.
func (l *boxLog) mirror() io.Writer { return boxLogMirror{l: l} }

// flush writes any mirror bytes still held because the stream never finished
// its line -- the Box died mid-line. Callers defer it ahead of closing the
// log file.
func (l *boxLog) flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flushLocked()
}

func (l *boxLog) flushLocked() error {
	if len(l.pending) == 0 {
		return nil
	}
	if !l.atLine {
		// Terminate the stream's partial line so the held mirror line
		// still lands on a line of its own.
		if _, err := l.w.Write([]byte("\n")); err != nil {
			l.pending = l.pending[:0]
			return err
		}
		l.atLine = true
	}
	_, err := l.w.Write(l.pending)
	l.pending = l.pending[:0]
	return err
}

type boxLogStream struct{ l *boxLog }

func (s boxLogStream) Write(p []byte) (int, error) {
	l := s.l
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.w.Write(p)
	if n > 0 {
		l.atLine = p[n-1] == '\n'
	}
	if err != nil {
		return n, err
	}
	if l.atLine {
		// A failed mirror flush is dropped rather than returned: the
		// stream copy is the Box's output and must not abort because a
		// log line could not be mirrored.
		_ = l.flushLocked()
	}
	return n, nil
}

type boxLogMirror struct{ l *boxLog }

func (m boxLogMirror) Write(p []byte) (int, error) {
	l := m.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.atLine || len(l.pending) > 0 {
		l.pending = append(l.pending, p...)
		return len(p), nil
	}
	return l.w.Write(p)
}
