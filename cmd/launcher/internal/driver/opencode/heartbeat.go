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
// prose, sanitized and bounded by driverkit.TrimNarration: like claude's
// narration it shows only the first sentence, not the whole first line. Every
// byte is forwarded to raw unchanged; the heartbeat is a side effect.
type Writer struct {
	raw   io.Writer
	issue string
	out   io.Writer

	onModel func(model, role string)

	mu        sync.Mutex
	frame     driverkit.LineFramer
	lastModel string
	// pendingModels holds changes parseLine found during one Write, delivered
	// after mu is released. Callback order matches lastModel only because
	// Writes are serial (os/exec runs one copy goroutine when Stdout and
	// Stderr share a writer).
	pendingModels []string
}

// New returns a Writer that forwards every byte to raw and writes heartbeat lines to out.
func New(raw io.Writer, issue string, out io.Writer) *Writer {
	return &Writer{raw: raw, issue: issue, out: out}
}

// OnModel registers fn to be called with each new model id a step_finish event
// names, and an empty role (opencode's transcript carries no role
// attribution). It returns w for chaining; nil means no one listens.
func (w *Writer) OnModel(fn func(model, role string)) *Writer {
	w.onModel = fn
	return w
}

// Write forwards p to raw before parsing any line it completes. onModel runs
// after mu is released because it emits to the reporter.
func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.raw.Write(p)
	if err != nil {
		return n, err
	}
	for _, model := range w.parse(p[:n]) {
		w.onModel(model, "")
	}
	return n, nil
}

// parse feeds p to the line framer under mu and returns the model changes it
// found. The deferred unlock keeps mu released if parsing panics.
func (w *Writer) parse(p []byte) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frame.Push(p, w.parseLine)
	pending := w.pendingModels
	w.pendingModels = nil
	return pending
}

func (w *Writer) parseLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var ev stepEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}
	switch ev.Type {
	case "step_finish":
		if m := ev.Part.ModelID; w.onModel != nil && m != "" && m != w.lastModel {
			w.lastModel = m
			w.pendingModels = append(w.pendingModels, m)
		}
	case "text":
		text := driverkit.TrimNarration(ev.Part.Text)
		if text == "" {
			return
		}
		fmt.Fprintf(w.out, "#%s \xc2\xb7 %s\n", w.issue, text)
	}
}
