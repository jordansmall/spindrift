package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Writer is a streaming NDJSON parser: it wraps a raw io.Writer (the log
// file) and emits per-issue heartbeat lines to out on each type:"text" event
// with non-empty prose. Every byte written to Writer is forwarded to raw
// unchanged; heartbeat emission is a side-effect — mirrors
// driver/claude/heartbeat.go's Writer contract, but for opencode's flat
// one-event-per-line NDJSON shape rather than claude's stream-json framing.
type Writer struct {
	raw   io.Writer
	issue string
	out   io.Writer

	mu  sync.Mutex
	buf []byte
}

// New returns a Writer that passes all bytes to raw unchanged and emits a
// heartbeat line to out on each type:"text" event with non-empty prose.
func New(raw io.Writer, issue string, out io.Writer) *Writer {
	return &Writer{raw: raw, issue: issue, out: out}
}

// Write implements io.Writer. All bytes are forwarded to raw unchanged, then
// complete lines are parsed for heartbeat events.
func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.raw.Write(p)
	if err != nil {
		return n, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p[:n]...)
	for {
		nl := bytes.IndexByte(w.buf, '\n')
		if nl < 0 {
			break
		}
		line := string(w.buf[:nl])
		w.buf = w.buf[nl+1:]
		w.parseLine(line)
	}
	return n, nil
}

// parseLine parses one complete NDJSON line and, for a type:"text" event
// with non-empty part.text, emits a single heartbeat line to out.
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
