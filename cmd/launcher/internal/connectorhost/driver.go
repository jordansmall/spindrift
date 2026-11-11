package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
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
// protocol are set: classification, totals, heartbeat and stream-filter
// tuning stay at their zero values.
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
	s := driverkit.Strategy{
		Name:              d.Name,
		NewDecoder:        c.newDecoder,
		TransientPatterns: transient,
		TerminalPatterns:  terminal,
		WriteTool:         d.WriteTool,
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
			// A Connector has no filesystem, so the host reads the log.
			log, err := os.ReadFile(logPath)
			if err != nil {
				return exitCode, err
			}
			var r struct {
				ExitCode int `json:"exitCode"`
			}
			if err := c.call(ctx, "driver.resolveExit", map[string]any{"log": string(log), "exitCode": exitCode}, &r); err != nil {
				return exitCode, err
			}
			// driver.Driver's invariant: a failing exit is never resolved to success.
			if exitCode != 0 && r.ExitCode == 0 {
				return exitCode, fmt.Errorf("%w: driver.resolveExit resolved exit %d to 0", ErrCall, exitCode)
			}
			return r.ExitCode, nil
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
		// Pass log; the guest never sees them.
		var op hostop.Event
		if t := strings.TrimSpace(line); t != "" &&
			json.Unmarshal([]byte(t), &op) == nil && op.Type == hostop.TypeSpindriftOp && op.SpindriftOp != nil {
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
