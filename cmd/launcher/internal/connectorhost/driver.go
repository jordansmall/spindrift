package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"

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

// The result shapes the Driver Strategy decodes from the guest.
type (
	describeResult struct {
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
	resultEventResult struct {
		Event string `json:"event"`
	}
	sessionFlagsResult struct {
		Flags string `json:"flags"`
	}
	resolveExitResult struct {
		ExitCode int `json:"exitCode"`
	}
	newDecoderResult struct {
		Decoder uint32 `json:"decoder"`
	}
	newStreamFilterResult struct {
		Filter uint32 `json:"filter"`
	}
	streamDataResult struct {
		Data []byte `json:"data"`
	}
	toolPhaseResult struct {
		Phase string `json:"phase"`
	}
	toolKindResult struct {
		Kind string `json:"kind"`
	}
	modelFamilyResult struct {
		Label string `json:"label"`
		Rank  int    `json:"rank"`
	}
)

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
// protocol are set; the stream filter is set only for a Connector that
// advertises "driver.streamFilter", else driverkit's identity passthrough stays.
//
// Strategy funcs carry no context or error return, so every guest call runs
// under context.Background() and the funcs fail closed, as documented on each.
func (c *Connector) DriverStrategy() (driverkit.Strategy, error) {
	if !slices.Contains(c.Capabilities, "driver") {
		return driverkit.Strategy{}, fmt.Errorf("%w: capabilities %v lack \"driver\"", ErrNotDriver, c.Capabilities)
	}
	ctx := context.Background()
	var d describeResult
	if err := c.Call(ctx, "driver.describe", struct{}{}, &d); err != nil {
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
			var r resultEventResult
			err := c.Call(ctx, "driver.resultEvent", map[string]any{"text": text}, &r)
			return []byte(r.Event), err
		},
		SessionFlags: func(mode, repoSlug, issue, home string) string {
			var r sessionFlagsResult
			err := c.Call(ctx, "driver.sessionFlags",
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
			var r resolveExitResult
			if err := c.Call(ctx, resolveExitMethod, resolveExitParams(log, exitCode), &r); err != nil {
				return exitCode, err
			}
			// driver.Driver's invariant: a failing exit is never resolved to success.
			if exitCode != 0 && r.ExitCode == 0 {
				return exitCode, fmt.Errorf("%w: driver.resolveExit resolved exit %d to 0", ErrCall, exitCode)
			}
			return r.ExitCode, nil
		}
	}
	if slices.Contains(c.Capabilities, "driver.streamFilter") {
		s.NewStreamFilter = c.newStreamFilter
	}
	// These funcs have no error return. A failed call leaves the heartbeat
	// uninformative rather than wrong: no phase, no kind, and the model id
	// as its own label at the last rank.
	if slices.Contains(c.Capabilities, "driver.toolPhase") {
		s.Heartbeat.Phase = func(tool string, input json.RawMessage) string {
			var r toolPhaseResult
			if c.Call(ctx, "driver.toolPhase", map[string]any{"tool": tool, "input": input}, &r) != nil {
				return ""
			}
			return r.Phase
		}
	}
	if slices.Contains(c.Capabilities, "driver.toolKind") {
		s.Heartbeat.Kind = func(tool string) string {
			var r toolKindResult
			if c.Call(ctx, "driver.toolKind", map[string]any{"tool": tool}, &r) != nil {
				return ""
			}
			return r.Kind
		}
	}
	if slices.Contains(c.Capabilities, "driver.modelFamily") {
		s.Heartbeat.Family = func(model string) (string, int) {
			var r modelFamilyResult
			if c.Call(ctx, "driver.modelFamily", map[string]any{"model": model}, &r) != nil {
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

// guestHandle is the heap object whose reachability tracks a guest decoder or
// filter.
type guestHandle struct{ id uint32 }

// bind sets the handle's guest id and frees it with freeMethod once the handle
// is unreachable. The Connector may be closed by then, so the cleanup ignores
// errors.
func (c *Connector) bind(h *guestHandle, id uint32, freeMethod, key string) {
	h.id = id
	runtime.AddCleanup(h, func(id uint32) {
		_ = c.Call(context.Background(), freeMethod, map[string]any{key: id}, nil)
	}, id)
}

func (c *Connector) newDecoder() driverkit.Decoder {
	ctx := context.Background()
	var r newDecoderResult
	h := &guestHandle{}
	// A Decoder has no error return, so a failed guest call yields a single
	// KindUnparsed event carrying the line: the classifier then still sees it
	// for terminal markers, and no guest-supplied event is trusted.
	unparsed := func(line string) []driverkit.Event {
		return []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: strings.TrimSpace(line)}}
	}
	err := c.Call(ctx, "driver.newDecoder", struct{}{}, &r)
	if err == nil {
		c.bind(h, r.Decoder, "driver.freeDecoder", "decoder")
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
				_ = c.Call(ctx, "driver.decode", map[string]any{"decoder": h.id, "line": line}, nil)
			}
			return []driverkit.Event{{Kind: driverkit.KindHostOp, HostOp: op.SpindriftOp}}
		}
		if err != nil {
			return unparsed(line)
		}
		var raw json.RawMessage
		if c.Call(ctx, "driver.decode", map[string]any{"decoder": h.id, "line": line}, &raw) != nil {
			return unparsed(line)
		}
		evs, ok := guestEvents(raw)
		if !ok {
			return unparsed(line)
		}
		return evs
	}
}

// streamPiece is the most raw bytes one streamWrite carries: base64 grows it by
// a third, so half of maxRequest keeps a piece's request under it, which the
// host would otherwise refuse. A var so a test can shrink it: the interpreter
// takes tens of seconds on a real piece.
var streamPiece = maxRequest / 2

// guestFilter is a driverkit.StreamFilter backed by a guest filter. Its first
// error, guest or downstream write, is sticky: the filter is the log tee's
// fail-closed channel, and a guest that failed mid-stream has lost state, so
// later bytes must not pass unfiltered. mu serialises Write and Flush because
// driver-exec flushes from its SIGTERM goroutine mid-Write, so Flush waits out
// an in-flight Write, up to a call budget per piece.
type guestFilter struct {
	c   *Connector
	w   io.Writer
	h   *guestHandle
	mu  sync.Mutex
	err error
}

func (c *Connector) newStreamFilter(w io.Writer) driverkit.StreamFilter {
	ctx := context.Background()
	f := &guestFilter{c: c, w: w, h: &guestHandle{}}
	var r newStreamFilterResult
	if f.err = c.Call(ctx, "driver.newStreamFilter", struct{}{}, &r); f.err != nil {
		return f
	}
	c.bind(f.h, r.Filter, "driver.freeStreamFilter", "filter")
	return f
}

// call runs one guest filter method and writes the bytes it returns to w. The
// caller holds mu.
func (f *guestFilter) call(method string, params map[string]any) error {
	var r streamDataResult
	if err := f.c.Call(context.Background(), method, params, &r); err != nil {
		f.err = err
		return err
	}
	if _, err := f.w.Write(r.Data); err != nil {
		// The guest has already consumed the bytes, so the tee has a gap.
		f.err = err
		return err
	}
	return nil
}

func (f *guestFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for f.err == nil && len(p) > 0 {
		piece := p[:min(len(p), streamPiece)]
		params := map[string]any{"filter": f.h.id, "data": piece}
		if err := f.call("driver.streamWrite", params); err != nil {
			return n, err
		}
		n += len(piece)
		p = p[len(piece):]
	}
	return n, f.err
}

func (f *guestFilter) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	return f.call("driver.streamFlush", map[string]any{"filter": f.h.id})
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
