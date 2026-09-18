package console

import (
	"bytes"
	"io"
	"os"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// tailer holds the per-log-path parser state that survives a refresh. writer
// is drv's stateful heartbeat parser, so keeping it alive alongside offset
// lets a caller feed it only the new bytes instead of reparsing the whole
// file. out is scratch, reused on every call.
type tailer struct {
	path   string
	offset int64
	writer io.Writer
	out    *bytes.Buffer
}

// readAppended feeds the bytes appended to t.path since t.offset through
// t.writer, whose parser state (role, turn counts, phase) persists across
// calls, so the tail replays what a whole-file reparse would. data is the
// parser's output for this call alone, never the raw bytes read. On any
// error t.offset stays put and the next call re-reads the same tail.
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
		t.writer = drv.NewHeartbeatWriter(io.Discard, number, t.out, driverkit.RenderOptions{})
	} else {
		t.out.Reset()
	}
	if _, err := t.writer.Write(raw); err != nil {
		return "", false
	}
	t.offset += int64(len(raw))
	return t.out.String(), true
}
