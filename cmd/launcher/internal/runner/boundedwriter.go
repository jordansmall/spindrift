package runner

// BoundedWriter retains only the last rebuildOutputCap bytes written (#1130).
// RunNixBuild's capture backs Launcher.rebuildOutput and stays in memory until
// the next rebuild, so an uncapped verbose build pins a multi-MB transcript for
// a whole Console session. It keeps the tail because failures print at the end.
type BoundedWriter struct {
	buf []byte
}

// rebuildOutputCap bounds a single RunNixBuild capture. 64 KiB keeps enough
// trailing context to diagnose a nix build failure.
const rebuildOutputCap = 64 * 1024

func (w *BoundedWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > rebuildOutputCap {
		w.buf = w.buf[len(w.buf)-rebuildOutputCap:]
	}
	return len(p), nil
}

func (w *BoundedWriter) String() string {
	return string(w.buf)
}
