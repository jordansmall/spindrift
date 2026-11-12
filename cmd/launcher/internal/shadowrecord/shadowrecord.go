// Package shadowrecord is the recording half of a shadow: it compares the
// native and Connector answers to one check, emits a clipped shadow_divergence
// op for each mismatch, and closes with one shadow_summary. The Pass-log replay
// (driver/shadow) and the backend read shadow share it, so both report in one
// shape. A shadow must never fail or delay its run, so nothing here returns an
// error: callers put failures in Sum.Error.
package shadowrecord

import (
	"encoding/json"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/hostop"
)

const (
	// MaxRecorded caps the divergence ops one run emits, so a wholly divergent
	// Connector cannot balloon the Pass log; the summary still counts them all.
	MaxRecorded = 100
	// maxPayload caps each side of a divergence and its line, for the same
	// reason: a single log line may be megabytes.
	maxPayload = 8 << 10
)

// Recorder accumulates a run's summary and emits its divergences. Sum is the
// caller's to fill in (Loaded, Compared, Error, timings); Check maintains
// Diverged and Recorded.
type Recorder struct {
	Sum  hostop.ShadowSummary
	emit func(hostop.SpindriftOp)
}

// New returns a Recorder for driver that hands every op to emit.
func New(driver string, emit func(hostop.SpindriftOp)) *Recorder {
	return &Recorder{Sum: hostop.ShadowSummary{Driver: driver}, emit: emit}
}

// Check compares two JSON encodings and records a divergence when they differ.
func (r *Recorder) Check(check string, lineNo int, line string, native, shadow json.RawMessage) {
	if string(native) == string(shadow) {
		return
	}
	r.Sum.Diverged++
	if r.Sum.Recorded >= MaxRecorded {
		return
	}
	r.Sum.Recorded++
	r.emit(hostop.SpindriftOp{Op: hostop.OpShadowDivergence, Divergence: &hostop.ShadowDivergence{
		Driver: r.Sum.Driver, Check: check, LineNo: lineNo,
		Line: clip(line), Native: clipJSON(native), Shadow: clipJSON(shadow),
	}})
}

// Finish emits the summary, the last op of every run.
func (r *Recorder) Finish() {
	sum := r.Sum
	r.emit(hostop.SpindriftOp{Op: hostop.OpShadowSummary, ShadowSummary: &sum})
}

// EncodeJSON encodes v for Check; an unencodable value becomes a JSON string
// saying so, which differs from any real encoding.
func EncodeJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(fmt.Sprintf("%q", "unencodable: "+err.Error()))
	}
	return b
}

func clip(s string) string {
	if len(s) <= maxPayload {
		return s
	}
	return strings.ToValidUTF8(s[:maxPayload], "") + fmt.Sprintf("...[%d bytes]", len(s))
}

// clipJSON swaps an oversized encoding for a string holding its head, so the
// op stays valid JSON.
func clipJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) <= maxPayload {
		return raw
	}
	return EncodeJSON(clip(string(raw)))
}
