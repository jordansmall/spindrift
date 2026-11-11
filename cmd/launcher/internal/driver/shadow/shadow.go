// Package shadow replays a Pass log through a Connector's Strategy next to the
// native one and reports where they disagree, so a Connector port can be
// trusted before it replaces the native Driver. The native Strategy stays
// authoritative: nothing here returns a result a caller could use in its place,
// and no failure of the Connector escapes Run.
package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/logscan"
)

const (
	// maxRecorded caps the divergence ops one run emits, so a wholly divergent
	// Connector cannot balloon the Pass log; the summary still counts them all.
	maxRecorded = 100
	// maxPayload caps each side of a divergence and its line, for the same
	// reason: a single log line may be megabytes.
	maxPayload = 8 << 10
)

// Run replays the Pass log at logPath through native and through the Connector
// compiled in wasm, handing each divergence and then one closing summary to
// emit. exitCode is the CLI's exit code, for the resolveExit comparison. ctx
// bounds the whole replay, Connector load included: when it ends, the Connector
// is aborted mid-call and the run stops with a summary Error. Run never fails
// and never panics: a Connector that does not load, or fails closed mid-run, is
// reported in the summary.
func Run(ctx context.Context, native driverkit.Strategy, wasm []byte, logPath string, exitCode int, emit func(hostop.SpindriftOp)) {
	r := &run{native: native, emit: emit}
	r.sum.Driver = native.Name
	defer r.finish()

	// The interpreter, not LoadCompiled: a compiled module is cached on disk,
	// and a shadow must not depend on, or write to, the host's filesystem.
	c, err := connectorhost.Load(ctx, wasm)
	if err != nil {
		r.sum.Error = err.Error()
		return
	}
	defer func() { _ = c.Close(context.WithoutCancel(ctx)) }()
	// A guest call holds the Connector's lock for up to its own budget, which
	// ctx alone cannot cut short.
	defer context.AfterFunc(ctx, c.Abort)()
	shadow, err := c.DriverStrategy()
	if err != nil {
		r.sum.Error = err.Error()
		return
	}
	r.sum.Loaded = true
	r.compare(ctx, shadow, c.Err, logPath, exitCode)
}

type run struct {
	native     driverkit.Strategy
	emit       func(hostop.SpindriftOp)
	sum        hostop.ShadowSummary
	nativeTime time.Duration
	shadowTime time.Duration
}

// finish emits the summary, the last op of every run. It also turns a panic
// anywhere in the shadow into the summary's Error.
func (r *run) finish() {
	if p := recover(); p != nil {
		r.sum.Error = fmt.Sprintf("panic: %v", p)
	}
	if r.sum.Compared > 0 {
		r.sum.NativeNsPerLine = r.nativeTime.Nanoseconds() / int64(r.sum.Compared)
		r.sum.ShadowNsPerLine = r.shadowTime.Nanoseconds() / int64(r.sum.Compared)
	}
	sum := r.sum
	r.emit(hostop.SpindriftOp{Op: hostop.OpShadowSummary, ShadowSummary: &sum})
}

// compare runs the three checks. failed reports the Connector's fatal failure;
// that and ctx ending both stop the run, since a dead Connector's answers say
// nothing about its code.
func (r *run) compare(ctx context.Context, shadow driverkit.Strategy, failed func() error, logPath string, exitCode int) {
	// stop records why the run cannot go on, if it cannot. ctx comes first: an
	// aborted Connector fails with a trap that would hide the deadline.
	stop := func() bool {
		if err := ctx.Err(); err != nil {
			r.sum.Error = fmt.Sprintf("shadow deadline exceeded after %d lines: %v", r.sum.Compared, err)
			return true
		}
		if err := failed(); err != nil {
			r.sum.Error = err.Error()
			return true
		}
		return false
	}
	nativeDec, shadowDec := r.native.NewDecoder(), shadow.NewDecoder()
	lineNo := 0
	stopped := false
	// The same scan the Pass-log readers use, so both see the lines they would.
	err := driverkit.ScanLog(logPath, logscan.SkipOversized, func(line string) {
		lineNo++
		if stopped || stop() {
			stopped = true
			return
		}
		t0 := time.Now()
		nativeEvs := nativeDec(line)
		t1 := time.Now()
		shadowEvs := shadowDec(line)
		t2 := time.Now()
		if stop() {
			stopped = true
			return
		}
		r.nativeTime += t1.Sub(t0)
		r.shadowTime += t2.Sub(t1)
		r.sum.Compared++
		r.check("events", lineNo, line, eventsJSON(nativeEvs), eventsJSON(shadowEvs))
	})
	if stopped {
		return
	}
	if err != nil {
		r.sum.Error = fmt.Sprintf("scan %s: %v", logPath, err)
		return
	}
	nativeExit, shadowExit := resolveExit(r.native, logPath, exitCode), resolveExit(shadow, logPath, exitCode)
	if stop() {
		return
	}
	r.check("resolveExit", 0, "", nativeExit, shadowExit)

	// The result text is the native reader's, so both sides encode the same input.
	text, err := r.native.ReadResult(logPath)
	if err != nil {
		r.sum.Error = fmt.Sprintf("read result: %v", err)
		return
	}
	nativeRes, shadowRes := resultEvent(r.native, text), resultEvent(shadow, text)
	if stop() {
		return
	}
	r.check("resultEvent", 0, text, nativeRes, shadowRes)
}

// check compares two JSON encodings and records a divergence when they differ.
func (r *run) check(check string, lineNo int, line string, native, shadow json.RawMessage) {
	if string(native) == string(shadow) {
		return
	}
	r.sum.Diverged++
	if r.sum.Recorded >= maxRecorded {
		return
	}
	r.sum.Recorded++
	r.emit(hostop.SpindriftOp{Op: hostop.OpShadowDivergence, Divergence: &hostop.ShadowDivergence{
		Driver: r.native.Name, Check: check, LineNo: lineNo,
		Line: clip(line), Native: clipJSON(native), Shadow: clipJSON(shadow),
	}})
}

// eventsJSON encodes evs for comparison. ResetAt is reduced to its presence:
// a Connector's clock is a fixed epoch, so a "resets <time>" line resolves to a
// different instant by design (issue #6751).
func eventsJSON(evs []driverkit.Event) json.RawMessage {
	norm := make([]driverkit.Event, len(evs))
	copy(norm, evs)
	for i := range norm {
		if norm[i].ResetAt != nil {
			norm[i].ResetAt = &time.Time{}
		}
	}
	return encodeJSON(norm)
}

// resolveExit encodes a Strategy's resolved exit code. A nil ResolveExit keeps
// the code, as driverkit.Driver does.
func resolveExit(s driverkit.Strategy, logPath string, exitCode int) json.RawMessage {
	got, err := exitCode, error(nil)
	if s.ResolveExit != nil {
		got, err = s.ResolveExit(logPath, exitCode)
	}
	return encodeJSON(map[string]any{"exit_code": got, "error": errString(err)})
}

func resultEvent(s driverkit.Strategy, text string) json.RawMessage {
	var ev []byte
	var err error
	if s.ResultEvent != nil {
		ev, err = s.ResultEvent(text)
	}
	return encodeJSON(map[string]any{"event": string(ev), "error": errString(err)})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func encodeJSON(v any) json.RawMessage {
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
	return encodeJSON(clip(string(raw)))
}
