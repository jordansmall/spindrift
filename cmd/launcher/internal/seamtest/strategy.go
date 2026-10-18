package seamtest

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
)

// fakeDriverName is the name the fake CLI is invoked under, so the Strategy
// must be registered under it for the launcher to find a Driver for it.
const fakeDriverName = "claude"

// Strategy declares a Driver whose log grammar is the canonical Event itself:
// one JSON-encoded driverkit.Event per line, plus the host-written spindrift_op
// lines every Pass log carries. It proves the shared readers work over the
// canonical grammar only, not the real claude decoder, and is read-only by
// convention like every Strategy.
var Strategy = driverkit.Strategy{
	Name:         fakeDriverName,
	NewDecoder:   newDecoder,
	Totals:       driverkit.PerCallTotals,
	WriteTool:    "Write",
	Result:       driverkit.ResultEvents,
	ResultEvent:  resultEvent,
	SessionFlags: func(mode, repoSlug, issue, home string) string { return "" },
}

func newDecoder() driverkit.Decoder {
	return func(line string) []driverkit.Event {
		line = strings.TrimSpace(line)
		if line == "" {
			return nil
		}
		var op hostop.Event
		if json.Unmarshal([]byte(line), &op) == nil && op.Type == hostop.TypeSpindriftOp && op.SpindriftOp != nil {
			return []driverkit.Event{{Kind: driverkit.KindHostOp, HostOp: op.SpindriftOp}}
		}
		var ev driverkit.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Kind == "" {
			return []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: line}}
		}
		return []driverkit.Event{ev}
	}
}

// Line is ev as the Strategy's log line: one JSON-encoded Event and a newline.
func Line(ev driverkit.Event) string {
	b, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// Millis is the RFC3339 ts as epoch milliseconds, the canonical Event's
// Timestamp unit. It panics on a malformed ts.
func Millis(rfc3339 string) int64 {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func resultEvent(text string) ([]byte, error) {
	b, err := json.Marshal(driverkit.Event{Kind: driverkit.KindResult, Text: text})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

var registerMu sync.Mutex

// RegisterDriver installs Strategy as the "claude" Driver when none is
// registered, and does nothing when one is (a test binary that links the
// bundle keeps the real Driver). The registry panics on a duplicate name and
// is not synchronized: the lock only orders concurrent RegisterDriver calls,
// so call it before any parallel test reads the registry.
func RegisterDriver(tb testing.TB) {
	tb.Helper()
	RegisterStrategy(Strategy)
}

// RegisterStrategy installs s under its own Name when no Driver of that name
// is registered, and does nothing when one is. It takes no testing.TB so a
// TestMain can call it; the same ordering caveat as RegisterDriver applies.
func RegisterStrategy(s driverkit.Strategy) {
	registerMu.Lock()
	defer registerMu.Unlock()
	if _, err := driver.New(s.Name); err == nil {
		return
	}
	driver.Register(s)
}
