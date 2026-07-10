// Package heartbeat provides a streaming stream-json parser that emits
// per-issue status lines to the launcher terminal at natural event boundaries
// (narration, phase change, result) while forwarding all bytes to the raw log
// writer unchanged.
package heartbeat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/claudetranscript"
)

// Writer wraps a raw io.Writer (the log file) and emits heartbeat lines to
// out (the launcher terminal) at natural event boundaries. Every byte written
// to Writer is forwarded to raw unchanged; heartbeat emission is a side-effect.
type Writer struct {
	raw   io.Writer
	issue string
	out   io.Writer

	mu              sync.Mutex
	buf             []byte
	turns           int
	taskRole        map[string]string         // Task tool-use id → subagent role
	currentRole     string                    // role of the message being parsed
	currentModel    string                    // shortened model family of the current message
	lastHeader      string                    // role of last emitted switch header
	lastHeaderModel string                    // model of last emitted switch header
	roleCounts      map[string]map[string]int // tool counts per role
	rolePhase       map[string]string         // current phase per role
}

// New returns a Writer that passes all bytes to raw unchanged and emits
// heartbeat lines to out at natural boundaries (narration, phase change, result).
func New(raw io.Writer, issue string, out io.Writer) *Writer {
	return &Writer{
		raw:        raw,
		issue:      issue,
		out:        out,
		taskRole:   make(map[string]string),
		roleCounts: make(map[string]map[string]int),
		rolePhase:  make(map[string]string),
	}
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

func (w *Writer) parseLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var ev claudetranscript.Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}
	switch ev.Type {
	case "assistant":
		if ev.Message != nil {
			// Collect Task tool-use IDs → subagent role from implementor messages
			// (online, single-pass; mirrors usage.BreakdownByRole pass 1).
			claudetranscript.CollectTaskRoles(ev, w.taskRole)

			// Resolve acting role from parent_tool_use_id.
			role := claudetranscript.ResolveRole(ev, w.taskRole)
			model := ModelFamily(ev.Message.Model)

			// On (role, model) change, flush the departing role's pending counts.
			if role != w.currentRole || model != w.currentModel {
				w.flushCounts(w.currentRole)
				w.currentRole = role
				w.currentModel = model
			}

			// Subagent narration (parent_tool_use_id != "") is dropped; only
			// implementor text is emitted.
			if ev.ParentToolUseID == "" {
				for _, block := range ev.Message.Content {
					if block.Type == "text" {
						if narration := trimNarration(block.Text); narration != "" {
							phase := w.rolePhase[w.currentRole]
							var narLine string
							if phase != "" {
								narLine = "#" + w.issue + " [" + phase + "] " + narration
							} else {
								narLine = "#" + w.issue + " \xc2\xb7 " + narration
							}
							w.ensureHeader()
							fmt.Fprintln(w.out, narLine)
							if w.hasCurrCounts() {
								fmt.Fprintln(w.out, FormatCountLine(w.issue, phase, w.currCounts()))
								clearCounts(w.currCounts())
							}
						}
						break
					}
				}
			}

			// Accumulate tool counts per role; emit count line on phase transition.
			for _, block := range ev.Message.Content {
				if block.Type == "tool_use" {
					phase := toolToPhase(block.Name, block.Input)
					currPhase := w.rolePhase[w.currentRole]
					if phase != currPhase {
						if w.hasCurrCounts() {
							w.ensureHeader()
							fmt.Fprintln(w.out, FormatCountLine(w.issue, currPhase, w.currCounts()))
							clearCounts(w.currCounts())
						}
						w.rolePhase[w.currentRole] = phase
					}
					if w.roleCounts[w.currentRole] == nil {
						w.roleCounts[w.currentRole] = make(map[string]int)
					}
					w.roleCounts[w.currentRole][toolKind(block.Name)]++
					break
				}
			}
		}
	case "result":
		if ev.NumTurns > 0 {
			w.turns = ev.NumTurns
		}
		w.emit()
		return
	}
}

func (w *Writer) emit() {
	if w.hasCurrCounts() {
		w.ensureHeader()
		fmt.Fprintln(w.out, FormatCountLine(w.issue, w.rolePhase[w.currentRole], w.currCounts()))
		clearCounts(w.currCounts())
	}
	if w.turns > 0 {
		fmt.Fprintln(w.out, FormatHeartbeat(w.issue, w.turns, "", w.rolePhase["implementor"]))
	}
}

// ensureHeader emits a switch header for currentRole if the last emitted header
// is for a different role. It is a no-op when the acting role header was already
// emitted and no intervening header was needed.
func (w *Writer) ensureHeader() {
	if w.currentRole != "" && (w.currentRole != w.lastHeader || w.currentModel != w.lastHeaderModel) {
		fmt.Fprintln(w.out, FormatRoleHeader(w.issue, w.currentRole, w.currentModel))
		w.lastHeader = w.currentRole
		w.lastHeaderModel = w.currentModel
	}
}

// flushCounts emits the pending count line for role, preceded by a switch
// header if needed. It is a no-op when role is empty or has no accumulated counts.
func (w *Writer) flushCounts(role string) {
	if role == "" {
		return
	}
	counts := w.roleCounts[role]
	if !hasCounts(counts) {
		return
	}
	if w.lastHeader != role || w.lastHeaderModel != w.currentModel {
		fmt.Fprintln(w.out, FormatRoleHeader(w.issue, role, w.currentModel))
		w.lastHeader = role
		w.lastHeaderModel = w.currentModel
	}
	fmt.Fprintln(w.out, FormatCountLine(w.issue, w.rolePhase[role], counts))
	clearCounts(counts)
}

func (w *Writer) hasCurrCounts() bool {
	return hasCounts(w.roleCounts[w.currentRole])
}

func (w *Writer) currCounts() map[string]int {
	return w.roleCounts[w.currentRole]
}

// hasCounts reports whether any tool kind has a non-zero count.
func hasCounts(counts map[string]int) bool {
	for _, n := range counts {
		if n > 0 {
			return true
		}
	}
	return false
}

// clearCounts resets all counts to zero by deleting every key.
func clearCounts(counts map[string]int) {
	for k := range counts {
		delete(counts, k)
	}
}
