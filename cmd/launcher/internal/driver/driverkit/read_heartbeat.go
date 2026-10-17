package driverkit

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// HeartbeatWriter decodes the Pass log stream it is written and emits
// per-issue heartbeat lines to out: one for each KindText event with non-empty
// prose, sanitized and bounded by TrimNarration, and one for each KindHostOp
// event carrying a payload, rendered by FormatSpindriftOp. With Track set it
// also groups tool calls into per-role phases and count lines, and prints a
// turn line at each KindResult. Every byte is forwarded to raw unchanged; the
// heartbeat is a side effect.
type HeartbeatWriter struct {
	raw          io.Writer
	issue        string
	out          io.Writer
	decode       Decoder
	topLevelRole string

	onModel func(model, role string)
	track   HeartbeatTracking

	mu    sync.Mutex
	frame LineFramer
	last  modelChange
	// pendingModels holds changes found during one Write, delivered after mu
	// is released. Callback order matches last only because Writes are serial
	// (os/exec runs one copy goroutine when Stdout and Stderr share a writer).
	pendingModels []modelChange

	turns           int
	currentRole     string                    // role of the message being decoded
	currentModel    string                    // family label of the current message
	lastHeader      string                    // role of the last emitted switch header
	lastHeaderModel string                    // model label of the last emitted switch header
	roleCounts      map[string]map[string]int // tool counts per role
	rolePhase       map[string]string         // current phase per role
}

type modelChange struct{ model, role string }

// HeartbeatTracking opts a HeartbeatWriter into phase and count lines. Every
// hook is optional: no Kind yields no counts, no Phase an empty phase, and no
// Family labels a model by its exact id.
type HeartbeatTracking struct {
	// Phase maps a tool call to the work phase it puts its role in.
	Phase func(tool string, input json.RawMessage) string
	// Kind maps a tool call to the bucket its count line tallies it under.
	Kind func(tool string) string
	// KindOrder lists the kinds a count line names first, in this order; the
	// rest follow sorted.
	KindOrder []string
	// KindPlurals maps a kind to its irregular plural; others append "s".
	KindPlurals map[string]string
	// Family maps a model id to the label a switch header shows. The live
	// heartbeat groups by this coarse label, not the exact id OnModel reports.
	Family func(model string) string
}

// NewHeartbeatWriter returns a HeartbeatWriter that forwards every byte to raw
// and writes heartbeat lines to out. It decodes with one decoder from
// newDecoder for its whole life. topLevelRole is the role reported for a model
// event with no role of its own.
func NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, newDecoder func() Decoder, topLevelRole string) *HeartbeatWriter {
	return &HeartbeatWriter{
		raw: raw, issue: issue, out: out, decode: newDecoder(), topLevelRole: topLevelRole,
		roleCounts: make(map[string]map[string]int),
		rolePhase:  make(map[string]string),
	}
}

// OnModel registers fn to be called with each new (model id, role) pair a
// KindModel event names. It returns w for chaining; nil means no one listens.
// A message carrying no model id, or flagged Synthetic, is skipped.
func (w *HeartbeatWriter) OnModel(fn func(model, role string)) *HeartbeatWriter {
	w.onModel = fn
	return w
}

// Track turns on phase and count lines using t and returns w. Call it before
// the first Write.
func (w *HeartbeatWriter) Track(t HeartbeatTracking) *HeartbeatWriter {
	w.track = t
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

// parseLine handles one decoded line. A message's narration and first tool
// call act on the role and model its KindModel set, whatever order its blocks
// stream in, and narration comes before the tool call, so both wait for the
// loop to finish.
func (w *HeartbeatWriter) parseLine(line string) {
	var text, tool *Event
	for _, ev := range w.decode(line) {
		switch ev.Kind {
		case KindModel:
			w.onModelEvent(ev)
		case KindText:
			// Only a message's first text block narrates, even when it trims
			// to nothing.
			if text == nil {
				text = &ev
			}
		case KindToolCall:
			if tool == nil {
				tool = &ev
			}
		case KindResult:
			// A result with an undecodable turn count is dropped whole.
			if ev.NoTurns {
				continue
			}
			if ev.NumTurns > 0 {
				w.turns = ev.NumTurns
			}
			w.emit()
		case KindHostOp:
			if ev.HostOp != nil {
				fmt.Fprintln(w.out, FormatSpindriftOp(w.issue, *ev.HostOp))
			}
		}
	}
	if text != nil && !text.InSubagent {
		w.narrate(text.Text)
	}
	if tool != nil && w.track.Kind != nil {
		w.countTool(*tool)
	}
}

func (w *HeartbeatWriter) onModelEvent(ev Event) {
	role := ev.Role
	if role == "" {
		role = w.topLevelRole
	}
	if cur := (modelChange{ev.Model, role}); w.onModel != nil && cur.model != "" && !ev.Synthetic && cur != w.last {
		w.last = cur
		w.pendingModels = append(w.pendingModels, cur)
	}
	model := ev.Model
	if w.track.Family != nil {
		model = w.track.Family(model)
	}
	if role != w.currentRole || model != w.currentModel {
		w.flushRole(w.rolePhase[w.currentRole])
		w.currentRole = role
		w.currentModel = model
	}
}

func (w *HeartbeatWriter) narrate(text string) {
	narration := TrimNarration(text)
	if narration == "" {
		return
	}
	phase := w.rolePhase[w.currentRole]
	var narLine string
	if phase != "" {
		narLine = "#" + w.issue + " [" + phase + "] " + narration
	} else {
		narLine = "#" + w.issue + " \xc2\xb7 " + narration
	}
	w.header()
	fmt.Fprintln(w.out, narLine)
	w.flushRole(phase)
}

// countTool tallies a tool call under its role, first flushing the role's
// counts if the call moves it to another phase. Subagent calls count too.
func (w *HeartbeatWriter) countTool(ev Event) {
	var phase string
	if w.track.Phase != nil {
		phase = w.track.Phase(ev.ToolName, ev.ToolInput)
	}
	if currPhase := w.rolePhase[w.currentRole]; phase != currPhase {
		w.flushRole(currPhase)
		w.rolePhase[w.currentRole] = phase
	}
	if w.roleCounts[w.currentRole] == nil {
		w.roleCounts[w.currentRole] = make(map[string]int)
	}
	w.roleCounts[w.currentRole][w.track.Kind(ev.ToolName)]++
}

func (w *HeartbeatWriter) emit() {
	w.flushRole(w.rolePhase[w.currentRole])
	if w.turns > 0 {
		fmt.Fprintln(w.out, FormatHeartbeat(w.issue, w.turns, "", w.currentRole, w.rolePhase[w.currentRole]))
	}
}

// header prints the current role's switch header unless it is already the
// one on screen for the current model.
func (w *HeartbeatWriter) header() {
	role := w.currentRole
	if role == "" || (role == w.lastHeader && w.currentModel == w.lastHeaderModel) {
		return
	}
	fmt.Fprintln(w.out, FormatRoleHeader(w.issue, role, w.currentModel))
	w.lastHeader = role
	w.lastHeaderModel = w.currentModel
}

// flushRole prints and clears the current role's counts under phase, behind a
// switch header when needed. A role with no counts prints nothing.
func (w *HeartbeatWriter) flushRole(phase string) {
	role := w.currentRole
	counts := w.roleCounts[role]
	if role == "" || !hasCounts(counts) {
		return
	}
	w.header()
	fmt.Fprintln(w.out, w.track.FormatCountLine(w.issue, role, phase, counts))
	clearCounts(counts)
}

func hasCounts(counts map[string]int) bool {
	for _, n := range counts {
		if n > 0 {
			return true
		}
	}
	return false
}

func clearCounts(counts map[string]int) {
	for k := range counts {
		delete(counts, k)
	}
}
