package runner

// boundedWriter is an io.Writer that retains only the most recently written
// rebuildOutputCap bytes, discarding older bytes as new ones arrive — issue
// #1130. RunNixBuild's captured output backs Launcher.rebuildOutput, which
// is held in memory until the next rebuild attempt; without a cap, a
// verbose cold-store `nix run .# -- build` can pin a multi-MB transcript in
// memory for a whole Console session. The tail survives, not the head,
// since the failure or final status an operator needs is at the end of the
// log.
type boundedWriter struct {
	buf []byte
}

// rebuildOutputCap bounds a single RunNixBuild capture. 64 KiB comfortably
// holds the trailing context of a nix build failure while staying far below
// "multi-MB" territory.
const rebuildOutputCap = 64 * 1024

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > rebuildOutputCap {
		w.buf = w.buf[len(w.buf)-rebuildOutputCap:]
	}
	return len(p), nil
}

func (w *boundedWriter) String() string {
	return string(w.buf)
}
