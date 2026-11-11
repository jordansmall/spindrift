package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/logscan"
)

// ErrNotDriver marks a Connector that does not advertise the "driver"
// capability.
var ErrNotDriver = errors.New("connector is not a driver")

type wirePattern struct {
	Substr string `json:"substr"`
	Reason string `json:"reason"`
}

// guestKinds are the kinds a guest decoder may emit. KindHostOp is core's own
// grammar and never crosses the boundary.
var guestKinds = []driverkit.Kind{
	driverkit.KindStart, driverkit.KindText, driverkit.KindReasoning,
	driverkit.KindToolCall, driverkit.KindToolResult, driverkit.KindUsage,
	driverkit.KindModel, driverkit.KindError, driverkit.KindUnparsed,
	driverkit.KindResult,
}

// DriverStrategy builds a driverkit.Strategy from a Driver Connector, so
// everything above the Strategy is unchanged. Only the fields that cross the
// protocol are set: the stream filter stays at its zero value.
//
// Strategy funcs carry no context or error return, so every guest call runs
// under context.Background() and the funcs fail closed, as documented on each.
func (c *Connector) DriverStrategy() (driverkit.Strategy, error) {
	if !slices.Contains(c.Capabilities, "driver") {
		return driverkit.Strategy{}, fmt.Errorf("%w: capabilities %v lack \"driver\"", ErrNotDriver, c.Capabilities)
	}
	ctx := context.Background()
	var d struct {
		Name              string        `json:"name"`
		TransientPatterns []wirePattern `json:"transientPatterns"`
		TerminalPatterns  []wirePattern `json:"terminalPatterns"`
		WriteTool         string        `json:"writeTool"`
		Classify          struct {
			ChunkOversized bool `json:"chunkOversized"`
			FirstMatch     bool `json:"firstMatch"`
		} `json:"classify"`
		Totals              string            `json:"totals"`
		DefaultTopLevelRole string            `json:"defaultTopLevelRole"`
		Result              string            `json:"result"`
		KindOrder           []string          `json:"kindOrder"`
		KindPlurals         map[string]string `json:"kindPlurals"`
	}
	if err := c.call(ctx, "driver.describe", struct{}{}, &d); err != nil {
		return driverkit.Strategy{}, err
	}
	transient, err := patterns(d.TransientPatterns)
	if err != nil {
		return driverkit.Strategy{}, err
	}
	terminal, err := patterns(d.TerminalPatterns)
	if err != nil {
		return driverkit.Strategy{}, err
	}
	// An absent name is the zero value, so a guest built before it existed stays valid.
	totals, ok := map[string]driverkit.TotalsSource{"": driverkit.PerCallTotals, "perCall": driverkit.PerCallTotals, "session": driverkit.SessionTotals}[d.Totals]
	if !ok {
		return driverkit.Strategy{}, fmt.Errorf("%w: unknown totals %q", ErrCall, d.Totals)
	}
	result, ok := map[string]driverkit.ResultReader{"": driverkit.ResultEvents, "events": driverkit.ResultEvents, "lastMessage": driverkit.LastMessage}[d.Result]
	if !ok {
		return driverkit.Strategy{}, fmt.Errorf("%w: unknown result %q", ErrCall, d.Result)
	}
	oversized := logscan.SkipOversized
	if d.Classify.ChunkOversized {
		oversized = logscan.ChunkOversized
	}
	s := driverkit.Strategy{
		Name:                d.Name,
		NewDecoder:          c.newDecoder,
		TransientPatterns:   transient,
		TerminalPatterns:    terminal,
		WriteTool:           d.WriteTool,
		Classify:            driverkit.ClassifyOptions{Oversized: oversized, FirstMatch: d.Classify.FirstMatch},
		Totals:              totals,
		Result:              result,
		DefaultTopLevelRole: d.DefaultTopLevelRole,
		Heartbeat:           driverkit.HeartbeatTracking{KindOrder: d.KindOrder, KindPlurals: d.KindPlurals},
		ResultEvent: func(text string) ([]byte, error) {
			var r struct {
				Event string `json:"event"`
			}
			err := c.call(ctx, "driver.resultEvent", map[string]any{"text": text}, &r)
			return []byte(r.Event), err
		},
		SessionFlags: func(mode, repoSlug, issue, home string) string {
			var r struct {
				Flags string `json:"flags"`
			}
			err := c.call(ctx, "driver.sessionFlags",
				map[string]any{"mode": mode, "repoSlug": repoSlug, "issue": issue, "home": home}, &r)
			if err != nil {
				panic(err) // no error return: a wrong flag string would start a mis-configured session
			}
			return r.Flags
		},
	}
	if slices.Contains(c.Capabilities, "driver.resolveExit") {
		s.ResolveExit = func(logPath string, exitCode int) (int, error) {
			// A Connector has no filesystem, so the host reads the log. Refusing
			// one whose request passes maxRequest would silently turn resolveExit
			// off, and a resolver judges the end of the log, so send its tail.
			raw, err := os.ReadFile(logPath)
			if err != nil {
				return exitCode, err
			}
			log, err := logWindow(raw, exitCode)
			if err != nil {
				return exitCode, err
			}
			var r struct {
				ExitCode int `json:"exitCode"`
			}
			if err := c.call(ctx, resolveExitMethod, resolveExitParams(log, exitCode), &r); err != nil {
				return exitCode, err
			}
			// driver.Driver's invariant: a failing exit is never resolved to success.
			if exitCode != 0 && r.ExitCode == 0 {
				return exitCode, fmt.Errorf("%w: driver.resolveExit resolved exit %d to 0", ErrCall, exitCode)
			}
			return r.ExitCode, nil
		}
	}
	// These funcs have no error return. A failed call leaves the heartbeat
	// uninformative rather than wrong: no phase, no kind, and the model id
	// as its own label at the last rank.
	if slices.Contains(c.Capabilities, "driver.toolPhase") {
		s.Heartbeat.Phase = func(tool string, input json.RawMessage) string {
			var r struct {
				Phase string `json:"phase"`
			}
			if c.call(ctx, "driver.toolPhase", map[string]any{"tool": tool, "input": input}, &r) != nil {
				return ""
			}
			return r.Phase
		}
	}
	if slices.Contains(c.Capabilities, "driver.toolKind") {
		s.Heartbeat.Kind = func(tool string) string {
			var r struct {
				Kind string `json:"kind"`
			}
			if c.call(ctx, "driver.toolKind", map[string]any{"tool": tool}, &r) != nil {
				return ""
			}
			return r.Kind
		}
	}
	if slices.Contains(c.Capabilities, "driver.modelFamily") {
		s.Heartbeat.Family = func(model string) (string, int) {
			var r struct {
				Label string `json:"label"`
				Rank  int    `json:"rank"`
			}
			if c.call(ctx, "driver.modelFamily", map[string]any{"model": model}, &r) != nil {
				return model, math.MaxInt
			}
			return r.Label, r.Rank
		}
	}
	return s, nil
}

func patterns(ws []wirePattern) ([]driverkit.Pattern, error) {
	var out []driverkit.Pattern
	for _, w := range ws {
		r := driverkit.Reason(w.Reason)
		if !slices.Contains(driverkit.AllReasons, r) {
			return nil, fmt.Errorf("%w: pattern %q has unknown reason %q", ErrCall, w.Substr, w.Reason)
		}
		out = append(out, driverkit.Pattern{Substr: w.Substr, Reason: r})
	}
	return out, nil
}

// decoderHandle is the heap object whose reachability tracks a guest decoder.
type decoderHandle struct{ id uint32 }

func (c *Connector) newDecoder() driverkit.Decoder {
	ctx := context.Background()
	var r struct {
		Decoder uint32 `json:"decoder"`
	}
	h := &decoderHandle{}
	// A Decoder has no error return, so a failed guest call yields a single
	// KindUnparsed event carrying the line: the classifier then still sees it
	// for terminal markers, and no guest-supplied event is trusted.
	unparsed := func(line string) []driverkit.Event {
		return []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: strings.TrimSpace(line)}}
	}
	err := c.call(ctx, "driver.newDecoder", struct{}{}, &r)
	if err == nil {
		h.id = r.Decoder
		// The Connector may be closed by then, so the cleanup ignores errors.
		runtime.AddCleanup(h, func(id uint32) {
			_ = c.call(ctx, "driver.freeDecoder", map[string]any{"decoder": id}, nil)
		}, r.Decoder)
	}
	return func(line string) []driverkit.Event {
		// spindrift_op lines are core's own grammar, host-written into every
		// Pass log. The guest sees them so its state can follow passes, but
		// core stays their only decoder: whatever the guest answers, or fails
		// with, is discarded.
		var op hostop.Event
		if t := strings.TrimSpace(line); t != "" &&
			json.Unmarshal([]byte(t), &op) == nil && op.Type == hostop.TypeSpindriftOp && op.SpindriftOp != nil {
			if err == nil {
				_ = c.call(ctx, "driver.decode", map[string]any{"decoder": h.id, "line": line}, nil)
			}
			return []driverkit.Event{{Kind: driverkit.KindHostOp, HostOp: op.SpindriftOp}}
		}
		if err != nil {
			return unparsed(line)
		}
		var raw json.RawMessage
		if c.call(ctx, "driver.decode", map[string]any{"decoder": h.id, "line": line}, &raw) != nil {
			return unparsed(line)
		}
		evs, ok := guestEvents(raw)
		if !ok {
			return unparsed(line)
		}
		return evs
	}
}

// guestEvents strictly decodes the events a guest returned and reports false
// if any is malformed or not one a guest may emit.
func guestEvents(raw json.RawMessage) ([]driverkit.Event, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var evs []driverkit.Event
	if dec.Decode(&evs) != nil {
		return nil, false
	}
	for _, ev := range evs {
		if !slices.Contains(guestKinds, ev.Kind) || ev.HostOp != nil {
			return nil, false
		}
	}
	if len(evs) == 0 {
		return nil, true // a native decoder returns nil for a line with no events
	}
	return evs, true
}

const resolveExitMethod = "driver.resolveExit"

// resolveExitParams is the one shape of a resolveExit request, so logWindow
// measures exactly the bytes the call sends.
func resolveExitParams(log string, exitCode int) map[string]any {
	return map[string]any{"log": log, "exitCode": exitCode}
}

// logWindow returns the newest lines of log whose resolveExit request fits under
// maxRequest. A line too large to fit on its own is skipped and the walk
// continues behind it, as the native resolvers skip an oversized line, so an
// unrecovered error before it still reaches the resolver; the walk stops at the
// first line that fits alone but not on top of those kept. Splitting at '\n' is
// safe for sizing: it is ASCII, so each line escapes on its own and the sizes
// add up.
func logWindow(log []byte, exitCode int) (string, error) {
	if len(log) <= maxRequest { // raw length is a lower bound on the encoded size
		req, err := encodeRequest(resolveExitMethod, resolveExitParams(string(log), exitCode))
		if err != nil || len(req) <= maxRequest {
			return string(log), err
		}
	}
	empty, err := encodeRequest(resolveExitMethod, resolveExitParams("", exitCode))
	if err != nil {
		return "", err
	}
	lineMax := maxRequest - len(empty)
	room := lineMax
	var kept []string // newest first
	start := len(log)
	for start > 0 {
		from := bytes.LastIndexByte(log[:start-1], '\n') + 1
		seg := log[from:start]
		start = from
		if len(seg) > lineMax {
			continue
		}
		enc, err := encodeRequest(resolveExitMethod, resolveExitParams(string(seg), exitCode))
		if err != nil {
			return "", err
		}
		n := len(enc) - len(empty)
		if n > lineMax {
			continue
		}
		if n > room {
			break
		}
		room -= n
		kept = append(kept, string(seg))
	}
	slices.Reverse(kept)
	return strings.Join(kept, ""), nil
}
