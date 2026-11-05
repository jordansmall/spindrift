package pi

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// NewStreamFilter drops the streaming deltas of every assistant message that
// ends, since its message_end repeats the whole message: a Pass log otherwise
// carries each token twice over (issue #6066). It also drops each subagent
// tool_execution_update that the call's next update or its tool_execution_end
// supersedes: updates are cumulative and the end carries every child in full
// (issue #6383). A process killed without Flush loses the held update, where
// the unfiltered log kept every one. Every other line passes through
// byte-identical.
//
// The deltas of a message still open when the stream stops are the only record
// of what it said, so Flush writes them back coalesced, one event per content
// block, in the shape of the originals. It likewise writes the latest update
// of each subagent call still running, in the order the calls were first seen;
// an end that carries no results writes its call's update ahead of itself.
func NewStreamFilter(w io.Writer) driverkit.StreamFilter {
	return &streamFilter{w: w}
}

type streamFilter struct {
	mu     sync.Mutex
	w      io.Writer
	framer driverkit.LineFramer
	held   []heldDelta
	// updates holds the latest update line of each running subagent call.
	updates heldUpdates
}

// heldDelta is one held delta line; its fields are read again only when the
// held deltas are written back, on Flush or at the next assistant message_start.
type heldDelta struct {
	line  string
	index int
	delta string
}

// heldUpdates maps a toolCallId to its latest update line, remembering the
// order in which the ids were first seen.
type heldUpdates struct {
	lines map[string]string
	ids   []string
}

func (h *heldUpdates) put(id, line string) {
	if h.lines == nil {
		h.lines = map[string]string{}
	}
	if _, ok := h.lines[id]; !ok {
		h.ids = append(h.ids, id)
	}
	h.lines[id] = line
}

// take removes the update held for id and returns it.
func (h *heldUpdates) take(id string) (string, bool) {
	line, ok := h.lines[id]
	if ok {
		delete(h.lines, id)
		h.ids = slices.DeleteFunc(h.ids, func(s string) bool { return s == id })
	}
	return line, ok
}

// drain returns every held line in first-seen order and empties the hold.
func (h *heldUpdates) drain() []string {
	out := make([]string, 0, len(h.ids))
	for _, id := range h.ids {
		out = append(out, h.lines[id])
	}
	*h = heldUpdates{}
	return out
}

// filterLine is the part of a pi event line the filter decides on.
type filterLine struct {
	Type       string `json:"type"`
	ToolName   string `json:"toolName"`
	ToolCallID string `json:"toolCallId"`
	Result     struct {
		Details struct {
			Results []json.RawMessage `json:"results"`
		} `json:"details"`
	} `json:"result"`
	Message struct {
		Role string `json:"role"`
	} `json:"message"`
	Event struct {
		Type         string `json:"type"`
		ContentIndex int    `json:"contentIndex"`
		Delta        string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

func (f *streamFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var err error
	f.framer.Push(p, func(line string) {
		if err == nil {
			err = f.line(line)
		}
	})
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (f *streamFilter) line(line string) error {
	var l filterLine
	if json.Unmarshal([]byte(line), &l) == nil {
		switch {
		case isDelta(l.Type, l.Event.Type):
			f.held = append(f.held, heldDelta{line, l.Event.ContentIndex, l.Event.Delta})
			return nil
		case l.Type == "tool_execution_update" && l.ToolName == subagentTool:
			f.updates.put(l.ToolCallID, line)
			return nil
		case l.Type == "tool_execution_end" && l.ToolName == subagentTool:
			// A subagent that threw ends in an error result with no children, so
			// the held update is then the only record of them.
			if held, ok := f.updates.take(l.ToolCallID); ok && len(l.Result.Details.Results) == 0 {
				if _, err := io.WriteString(f.w, held+"\n"); err != nil {
					return err
				}
			}
		case l.Type == "message_end" && l.Message.Role == "assistant":
			f.held = nil
		case l.Type == "message_start" && l.Message.Role == "assistant":
			if err := f.flushHeld(); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(f.w, line+"\n")
	return err
}

// Flush writes the held deltas, coalesced, then the held subagent updates, then
// any trailing partial line. The text lands after the open message's non-delta
// lines (a text_end that already passed, say); accepted, since the decoder
// ignores message_update.
func (f *streamFilter) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.flushHeld(); err != nil {
		return err
	}
	for _, held := range f.updates.drain() {
		if _, err := io.WriteString(f.w, held+"\n"); err != nil {
			return err
		}
	}
	rest := f.framer.Drain()
	if rest == "" {
		return nil
	}
	_, err := io.WriteString(f.w, rest)
	return err
}

// isDelta reports whether an event line of type typ, carrying the
// assistantMessageEvent type ameType, is a streaming delta.
func isDelta(typ, ameType string) bool {
	return typ == "message_update" && strings.HasSuffix(ameType, "_delta")
}

// heldBlock is the deltas of one content block, in arrival order.
type heldBlock struct {
	text  strings.Builder
	lines []string
}

func (f *streamFilter) flushHeld() error {
	held := f.held
	f.held = nil
	var order []int
	blocks := map[int]*heldBlock{}
	for _, h := range held {
		b, ok := blocks[h.index]
		if !ok {
			b = &heldBlock{}
			blocks[h.index] = b
			order = append(order, h.index)
		}
		b.text.WriteString(h.delta)
		b.lines = append(b.lines, h.line)
	}
	for _, idx := range order {
		b := blocks[idx]
		out, err := coalesced(b.lines[len(b.lines)-1], b.text.String())
		if err != nil {
			// The partial text is why the deltas were held; never drop it.
			out = strings.Join(b.lines, "\n") + "\n"
		}
		if _, err := io.WriteString(f.w, out); err != nil {
			return err
		}
	}
	return nil
}

// coalesced rewrites a delta line with delta as its text, keeping the rest of
// the envelope, and ends it in a newline. Key order is not preserved.
func coalesced(line, delta string) (string, error) {
	var env, ev map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return "", err
	}
	if err := json.Unmarshal(env["assistantMessageEvent"], &ev); err != nil {
		return "", err
	}
	var err error
	if ev["delta"], err = marshal(delta); err != nil {
		return "", err
	}
	if env["assistantMessageEvent"], err = marshal(ev); err != nil {
		return "", err
	}
	out, err := marshal(env)
	return string(out), err
}

// marshal encodes v without HTML escaping, so prose keeps its < > and &. The
// trailing newline Encode adds is the line terminator on the outermost value;
// nested, json compacts it away.
func marshal(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
