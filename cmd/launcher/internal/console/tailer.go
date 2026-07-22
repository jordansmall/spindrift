package console

import (
	"bytes"
	"io"
	"os"

	"spindrift.dev/launcher/internal/driver"
)

// tailer holds the persistent per-log-path parser state appendHeartbeat and
// appendActivity both keep alive across refreshes: the byte offset already
// fed to writer, drv's own stateful heartbeat-parser Writer (shared by both
// callers — the heartbeat and activity feeds replay the same driver
// machinery, just extracting different shapes from its output), and the
// scratch buffer that pins each call's own emitted output. heartbeatCacheEntry
// and activityCacheEntry each embed a tailer and add only the accumulated
// result field their own return shape needs (a single line vs. an ordered
// []ActivityLine).
type tailer struct {
	path   string
	offset int64
	writer io.Writer
	out    *bytes.Buffer
}

// readAppended reads the bytes appended to t.path since t.offset, feeds only
// that tail to t.writer — creating writer and out on first use — and
// advances t.offset by what it read. writer is drv's own stateful heartbeat
// parser (role, turn counts, phase all persist on it across calls), so this
// replays exactly what a whole-file reparse would have replayed, just
// without re-walking bytes already consumed. out is reset before every feed
// so it only ever holds this call's own emitted lines, not the pass's whole
// history. The returned data is t.out's parsed output for this call, not the
// raw file bytes read. ok is false when the file can't be read or written
// through drv's parser; t.offset is left unmodified in that case.
func (t *tailer) readAppended(drv driver.Driver, number string) (data string, ok bool) {
	f, err := os.Open(t.path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return "", false
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return "", false
	}
	if t.writer == nil {
		t.out = &bytes.Buffer{}
		t.writer = drv.NewHeartbeatWriter(io.Discard, number, t.out)
	} else {
		t.out.Reset()
	}
	if _, err := t.writer.Write(raw); err != nil {
		return "", false
	}
	t.offset += int64(len(raw))
	return t.out.String(), true
}
