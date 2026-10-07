package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// Writer parses stream-json output, forwarding every byte to raw unchanged and
// emitting per-issue heartbeat lines to out as a side effect.
type Writer struct {
	raw   io.Writer
	issue string
	out   io.Writer

	mu sync.Mutex
	// activeTopLevelRole is the role ResolveRole uses for messages with an empty
	// parent_tool_use_id. A pass_start spindrift_op carrying a Role updates it
	// mid-stream (issue #2382), so an orchestrator review pass attributes its
	// top-level turns to reviewer rather than the construction-time default.
	activeTopLevelRole string
	frame              driverkit.LineFramer
	turns              int
	taskRole           map[string]string         // maps a Task tool-use id to the subagent role
	currentRole        string                    // role of the message being parsed
	currentModel       string                    // shortened model family of the current message
	lastHeader         string                    // role of last emitted switch header
	lastHeaderModel    string                    // model of last emitted switch header
	roleCounts         map[string]map[string]int // tool counts per role
	rolePhase          map[string]string         // current phase per role

	// onModel, when set, hears each change of the (role, exact model id) pair.
	// Tracked apart from currentModel, which is the coarse family the heartbeat
	// groups by.
	onModel func(model, role string)
	last    modelChange
	// pendingModels holds changes parseLine found during one Write, delivered
	// after mu is released so the Writer never calls out while holding its lock.
	// Callback order matches last only because Writes are serial (os/exec runs
	// one copy goroutine when Stdout and Stderr share a writer).
	pendingModels []modelChange
}

type modelChange struct{ model, role string }

// New returns a Writer that emits heartbeat lines for issue to out.
func New(raw io.Writer, issue string, out io.Writer) *Writer {
	return NewWithTopLevelRole(raw, issue, out, "")
}

// NewWithTopLevelRole is like New, but attributes top-level messages to
// topLevelRole, for a pass the orchestrator owns as something other than
// implementation (issue #2092). An empty topLevelRole keeps the
// ImplementorRole default.
func NewWithTopLevelRole(raw io.Writer, issue string, out io.Writer, topLevelRole string) *Writer {
	return &Writer{
		raw:                raw,
		issue:              issue,
		out:                out,
		activeTopLevelRole: topLevelRole,
		taskRole:           make(map[string]string),
		roleCounts:         make(map[string]map[string]int),
		rolePhase:          make(map[string]string),
	}
}

// OnModel sets fn to be called with the exact model id and role whenever the
// pair behind the streamed assistant messages changes, and returns w. A
// message carrying no model id, or the CLI's "<synthetic>" sentinel, is skipped.
func (w *Writer) OnModel(fn func(model, role string)) *Writer {
	w.onModel = fn
	return w
}

// Write forwards all bytes to raw unchanged, then parses complete lines for
// heartbeat events.
func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.raw.Write(p)
	if err != nil {
		return n, err
	}
	for _, c := range w.parse(p[:n]) {
		w.onModel(c.model, c.role)
	}
	return n, nil
}

// parse feeds b to the line framer under mu and returns the model changes it
// found. The deferred unlock keeps mu released if parsing panics.
func (w *Writer) parse(b []byte) []modelChange {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frame.Push(b, w.parseLine)
	pending := w.pendingModels
	w.pendingModels = nil
	return pending
}

func (w *Writer) parseLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var ev Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}
	switch ev.Type {
	case "assistant":
		if ev.Message != nil {
			// Single-pass resolution relies on a spawn block streaming before
			// its child's messages, which holds in practice.
			CollectTaskRoles(ev, w.taskRole)

			role := ResolveRole(ev, w.taskRole, w.activeTopLevelRole)
			// The live heartbeat groups by family, not exact model id: it is a
			// coarse in-flight signal, so one row per family stays readable.
			// The final per-model token table (usage.go) diverges on purpose
			// and keys on the exact id (issue #2110).
			model := ModelFamily(ev.Message.Model)

			if cur := (modelChange{ev.Message.Model, role}); w.onModel != nil && cur.model != "" && cur.model != syntheticModelSentinel && cur != w.last {
				w.last = cur
				w.pendingModels = append(w.pendingModels, cur)
			}

			if role != w.currentRole || model != w.currentModel {
				w.flushCounts(w.currentRole)
				w.currentRole = role
				w.currentModel = model
			}

			// Subagent narration is dropped; only top-level text is emitted.
			if ev.ParentToolUseID == "" {
				for _, block := range ev.Message.Content {
					if block.Type == "text" {
						if narration := driverkit.TrimNarration(block.Text); narration != "" {
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
								fmt.Fprintln(w.out, FormatCountLine(w.issue, w.currentRole, phase, w.currCounts()))
								clearCounts(w.currCounts())
							}
						}
						break
					}
				}
			}

			for _, block := range ev.Message.Content {
				if block.Type == "tool_use" {
					phase := toolToPhase(block.Name, block.Input)
					currPhase := w.rolePhase[w.currentRole]
					if phase != currPhase {
						if w.hasCurrCounts() {
							w.ensureHeader()
							fmt.Fprintln(w.out, FormatCountLine(w.issue, w.currentRole, currPhase, w.currCounts()))
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
	case "spindrift_op":
		if ev.SpindriftOp != nil {
			fmt.Fprintln(w.out, FormatSpindriftOp(w.issue, *ev.SpindriftOp))
			w.activeTopLevelRole = nextActiveTopLevelRole(w.activeTopLevelRole, ev.SpindriftOp)
		}
		return
	}
}

func (w *Writer) emit() {
	if w.hasCurrCounts() {
		w.ensureHeader()
		fmt.Fprintln(w.out, FormatCountLine(w.issue, w.currentRole, w.rolePhase[w.currentRole], w.currCounts()))
		clearCounts(w.currCounts())
	}
	if w.turns > 0 {
		fmt.Fprintln(w.out, FormatHeartbeat(w.issue, w.turns, "", w.currentRole, w.rolePhase[w.currentRole]))
	}
}

func (w *Writer) ensureHeader() {
	if w.currentRole != "" && (w.currentRole != w.lastHeader || w.currentModel != w.lastHeaderModel) {
		fmt.Fprintln(w.out, FormatRoleHeader(w.issue, w.currentRole, w.currentModel))
		w.lastHeader = w.currentRole
		w.lastHeaderModel = w.currentModel
	}
}

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
	fmt.Fprintln(w.out, FormatCountLine(w.issue, role, w.rolePhase[role], counts))
	clearCounts(counts)
}

func (w *Writer) hasCurrCounts() bool {
	return hasCounts(w.roleCounts[w.currentRole])
}

func (w *Writer) currCounts() map[string]int {
	return w.roleCounts[w.currentRole]
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
