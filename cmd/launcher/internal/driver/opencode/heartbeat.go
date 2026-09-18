package opencode

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// Writer parses opencode's flat one-event-per-line NDJSON and emits a
// per-issue heartbeat line to out on each type:"text" event with non-empty
// prose. Every byte is forwarded to raw unchanged; the heartbeat is a side
// effect.
type Writer struct {
	raw   io.Writer
	issue string
	out   io.Writer

	mu    sync.Mutex
	frame driverkit.LineFramer
}

// New returns a Writer that forwards every byte to raw and writes heartbeat lines to out.
func New(raw io.Writer, issue string, out io.Writer) *Writer {
	return &Writer{raw: raw, issue: issue, out: out}
}

// Write forwards p to raw before parsing any line it completes.
func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.raw.Write(p)
	if err != nil {
		return n, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frame.Push(p[:n], w.parseLine)
	return n, nil
}

func (w *Writer) parseLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var ev textEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}
	if ev.Type != "text" {
		return
	}
	text := strings.TrimSpace(ev.Part.Text)
	if text == "" {
		return
	}
	firstLine := text
	if i := strings.IndexByte(firstLine, '\n'); i >= 0 {
		firstLine = firstLine[:i]
	}
	fmt.Fprintf(w.out, "#%s \xc2\xb7 %s\n", w.issue, firstLine)
}
