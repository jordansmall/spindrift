// Package shadow replays a Pass log through a Connector's Strategy next to the
// native one and reports where they disagree, so a Connector port can be
// trusted before it replaces the native Driver. The native Strategy stays
// authoritative: nothing here returns a result a caller could use in its
// place, and no Connector failure escapes Run. The log is decoded in batches,
// one Connector call each, but compared line by line.
package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/shadowrecord"
)

// Loader loads the Connector compiled in a wasm module. The caller picks it: a
// Box main loads in-process (connectorhost.Load), a Launcher main through the
// sandbox (connectorsandbox.Load), and the engine-reach guard holds each to its
// side, so this package names neither.
type Loader func(ctx context.Context, wasm []byte) (*connectorhost.Connector, error)

// Budget bounds one Pass replay, Connector load included. The orchestrator
// waits on a live replay before it sees the Pass's exit code. A replay that
// runs out is a summary error, never a clean run.
const Budget = 2 * time.Minute

// Run replays the Pass log at logPath through native and through the Connector
// that load builds from wasm, handing each divergence and then one closing
// summary to emit. exitCode feeds the resolveExit comparison. When ctx ends,
// the Connector is aborted mid-call and the run stops with a summary Error.
// Run never fails or panics: Connector failures land in the summary.
func Run(ctx context.Context, native driverkit.Strategy, load Loader, wasm []byte, logPath string, exitCode int, emit func(hostop.SpindriftOp)) {
	r := &run{native: native, Recorder: shadowrecord.New(native.Name, emit)}
	defer r.finish()

	c, err := load(ctx, wasm)
	if err != nil {
		r.Sum.Error = err.Error()
		return
	}
	defer func() { _ = c.Close(context.WithoutCancel(ctx)) }()
	// A guest call holds the Connector's lock for up to its own budget, which
	// ctx alone cannot cut short.
	defer context.AfterFunc(ctx, c.Abort)()
	shadow, err := c.DriverStrategy()
	if err != nil {
		r.Sum.Error = err.Error()
		return
	}
	r.Sum.Loaded = true
	r.compare(ctx, shadow, c.Err, logPath, exitCode)
}

type run struct {
	native driverkit.Strategy
	*shadowrecord.Recorder
	nativeTime time.Duration
	shadowTime time.Duration
}

// finish emits the summary, the last op of every run. It also turns a panic
// anywhere in the shadow into the summary's Error.
func (r *run) finish() {
	if p := recover(); p != nil {
		r.Sum.Error = fmt.Sprintf("panic: %v", p)
	}
	if r.Sum.Compared > 0 {
		r.Sum.NativeNsPerLine = r.nativeTime.Nanoseconds() / int64(r.Sum.Compared)
		r.Sum.ShadowNsPerLine = r.shadowTime.Nanoseconds() / int64(r.Sum.Compared)
	}
	r.Recorder.Finish()
}

// flushBytes bounds the buffered line bytes per decoded batch: enough to
// amortise the per-call cost, small enough to bound memory and the time
// between stop() checks. A stop can leave about one batch uncompared, and the
// "deadline exceeded after N lines" count undercounts by as much. A var for
// tests.
var flushBytes = 1 << 20

// compare runs the three checks. failed reports the Connector's fatal failure;
// that and ctx ending both stop the run, since a dead Connector's answers say
// nothing about its code.
func (r *run) compare(ctx context.Context, shadow driverkit.Strategy, failed func() error, logPath string, exitCode int) {
	// stop records why the run cannot go on, if it cannot. ctx comes first: an
	// aborted Connector fails with a trap that would hide the deadline.
	stop := func() bool {
		if err := ctx.Err(); err != nil {
			r.Sum.Error = fmt.Sprintf("shadow deadline exceeded after %d lines: %v", r.Sum.Compared, err)
			return true
		}
		if err := failed(); err != nil {
			r.Sum.Error = err.Error()
			return true
		}
		return false
	}
	nativeDec, shadowDec := r.native.NewDecoder(), shadow.BatchDecoder()
	type numbered struct {
		no   int
		line string
	}
	var batch []numbered
	batchBytes := 0
	stopped := false
	flush := func() {
		if len(batch) == 0 {
			return
		}
		defer func() { batch, batchBytes = batch[:0], 0 }()
		if stopped || stop() {
			stopped = true
			return
		}
		lines := make([]string, len(batch))
		nativeEvs := make([][]driverkit.Event, len(batch))
		var nativeSpent time.Duration
		for i, l := range batch {
			lines[i] = l.line
			t := time.Now()
			nativeEvs[i] = nativeDec(l.line)
			nativeSpent += time.Since(t)
		}
		t := time.Now()
		shadowEvs := shadowDec(lines)
		shadowSpent := time.Since(t)
		if stop() {
			stopped = true
			return
		}
		r.nativeTime += nativeSpent
		r.shadowTime += shadowSpent
		for i, l := range batch {
			r.Sum.Compared++
			r.Check("events", l.no, l.line, shadowrecord.EncodeJSON(nativeEvs[i]), shadowrecord.EncodeJSON(shadowEvs[i]))
		}
	}
	lineNo := 0
	// The same scan the Pass-log readers use, so both see the lines they would.
	err := driverkit.ScanLog(logPath, logscan.SkipOversized, func(line string) {
		lineNo++
		if stopped {
			return
		}
		batch = append(batch, numbered{lineNo, line})
		if batchBytes += len(line); batchBytes >= flushBytes {
			flush()
		}
	})
	flush()
	if stopped {
		return
	}
	if err != nil {
		r.Sum.Error = fmt.Sprintf("scan %s: %v", logPath, err)
		return
	}
	nativeExit, shadowExit := resolveExit(r.native, logPath, exitCode), resolveExit(shadow, logPath, exitCode)
	if stop() {
		return
	}
	r.Check("resolveExit", 0, "", nativeExit, shadowExit)

	// The result text is the native reader's, so both sides encode the same input.
	text, err := r.native.ReadResult(logPath)
	if err != nil {
		r.Sum.Error = fmt.Sprintf("read result: %v", err)
		return
	}
	nativeRes, shadowRes := resultEvent(r.native, text), resultEvent(shadow, text)
	if stop() {
		return
	}
	r.Check("resultEvent", 0, text, nativeRes, shadowRes)
}

// resolveExit encodes a Strategy's resolved exit code. A nil ResolveExit keeps
// the code, as driverkit.Driver does.
func resolveExit(s driverkit.Strategy, logPath string, exitCode int) json.RawMessage {
	got, err := exitCode, error(nil)
	if s.ResolveExit != nil {
		got, err = s.ResolveExit(logPath, exitCode)
	}
	return shadowrecord.EncodeJSON(map[string]any{"exit_code": got, "error": errString(err)})
}

func resultEvent(s driverkit.Strategy, text string) json.RawMessage {
	var ev []byte
	var err error
	if s.ResultEvent != nil {
		ev, err = s.ResultEvent(text)
	}
	return shadowrecord.EncodeJSON(map[string]any{"event": string(ev), "error": errString(err)})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
