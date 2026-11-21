package runner

import "sync"

// BoundedWriter retains only the last boundedOutputCap bytes written (#1130),
// so an uncapped verbose nix build cannot pin a multi-MB transcript in memory.
// It keeps the tail because failures print at the end. Safe for concurrent
// use: a Connector sandbox's stderr is written by the exec copier while a
// caller reads it.
type BoundedWriter struct {
	mu  sync.Mutex
	buf []byte
}

// boundedOutputCap bounds a single BoundedWriter capture. 64 KiB keeps enough
// trailing context to diagnose a failed command.
const boundedOutputCap = 64 * 1024

func (w *BoundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > boundedOutputCap {
		w.buf = w.buf[len(w.buf)-boundedOutputCap:]
	}
	return len(p), nil
}

func (w *BoundedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
