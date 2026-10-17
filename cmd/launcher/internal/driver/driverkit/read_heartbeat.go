package driverkit

import (
	"fmt"
	"io"
	"sync"
)

// HeartbeatWriter decodes the Pass log stream it is written and emits a
// per-issue heartbeat line to out for each KindText event with non-empty
// prose, sanitized and bounded by TrimNarration. Every byte is forwarded to
// raw unchanged; the heartbeat is a side effect.
type HeartbeatWriter struct {
	raw          io.Writer
	issue        string
	out          io.Writer
	decode       Decoder
	topLevelRole string

	onModel func(model, role string)

	mu        sync.Mutex
	frame     LineFramer
	lastModel string
	// pendingModels holds changes found during one Write, delivered after mu
	// is released. Callback order matches lastModel only because Writes are
	// serial (os/exec runs one copy goroutine when Stdout and Stderr share a
	// writer).
	pendingModels []modelChange
}

type modelChange struct{ model, role string }

// NewHeartbeatWriter returns a HeartbeatWriter that forwards every byte to raw
// and writes heartbeat lines to out. It decodes with one decoder from
// newDecoder for its whole life. topLevelRole is the role reported for a model
// event with no role of its own.
func NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, newDecoder func() Decoder, topLevelRole string) *HeartbeatWriter {
	return &HeartbeatWriter{raw: raw, issue: issue, out: out, decode: newDecoder(), topLevelRole: topLevelRole}
}

// OnModel registers fn to be called with each new model id a KindModel event
// names and the role it is attributed to. It returns w for chaining; nil means
// no one listens.
func (w *HeartbeatWriter) OnModel(fn func(model, role string)) *HeartbeatWriter {
	w.onModel = fn
	return w
}

// Write forwards p to raw before decoding any line it completes. onModel runs
// after mu is released because it emits to the reporter.
func (w *HeartbeatWriter) Write(p []byte) (int, error) {
	n, err := w.raw.Write(p)
	if err != nil {
		return n, err
	}
	for _, c := range w.parse(p[:n]) {
		w.onModel(c.model, c.role)
	}
	return n, nil
}

// parse feeds p to the line framer under mu and returns the model changes it
// found. The deferred unlock keeps mu released if decoding panics.
func (w *HeartbeatWriter) parse(p []byte) []modelChange {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frame.Push(p, w.parseLine)
	pending := w.pendingModels
	w.pendingModels = nil
	return pending
}

func (w *HeartbeatWriter) parseLine(line string) {
	for _, ev := range w.decode(line) {
		switch ev.Kind {
		case KindModel:
			if m := ev.Model; w.onModel != nil && m != "" && m != w.lastModel {
				w.lastModel = m
				role := ev.Role
				if role == "" {
					role = w.topLevelRole
				}
				w.pendingModels = append(w.pendingModels, modelChange{m, role})
			}
		case KindText:
			if text := TrimNarration(ev.Text); text != "" {
				fmt.Fprintf(w.out, "#%s \xc2\xb7 %s\n", w.issue, text)
			}
		}
	}
}
