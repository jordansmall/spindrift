package connectorhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/logscan"
)

func strategy(t *testing.T, name string) driverkit.Strategy {
	t.Helper()
	c, err := load(t, name)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDriverStrategyRefusesNonDriver(t *testing.T) {
	c, err := load(t, "current.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DriverStrategy(); !errors.Is(err, connectorhost.ErrNotDriver) {
		t.Fatalf("err = %v, want ErrNotDriver", err)
	}
}

func TestDriverStrategyDescribe(t *testing.T) {
	s := strategy(t, "driver.wasm")
	if s.Name != "fixture" || s.WriteTool != "Put" {
		t.Errorf("Name, WriteTool = %q, %q", s.Name, s.WriteTool)
	}
	if want := []driverkit.Pattern{{Substr: "slow down", Reason: driverkit.RateLimit}}; !reflect.DeepEqual(s.TransientPatterns, want) {
		t.Errorf("TransientPatterns = %v, want %v", s.TransientPatterns, want)
	}
	if want := []driverkit.Pattern{{Substr: "bad flag", Reason: driverkit.UnsupportedFlag}}; !reflect.DeepEqual(s.TerminalPatterns, want) {
		t.Errorf("TerminalPatterns = %v, want %v", s.TerminalPatterns, want)
	}
}

func TestDriverDecoderKeepsStatePerInstance(t *testing.T) {
	s := strategy(t, "driver.wasm")
	a, b := s.NewDecoder(), s.NewDecoder()
	text := func(d driverkit.Decoder, line string) string {
		evs := d(line)
		if len(evs) != 1 || evs[0].Kind != driverkit.KindText {
			t.Fatalf("decode(%q) = %v, want one text event", line, evs)
		}
		return evs[0].Text
	}
	got := []string{text(a, "x"), text(a, "y"), text(b, "z")}
	if want := []string{"1:x", "2:y", "1:z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("decoded %v, want %v", got, want)
	}
}

func TestDriverDecoderHandlesHostOpLinesItself(t *testing.T) {
	s := strategy(t, "driver.wasm")
	d := s.NewDecoder()
	line := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: "pass_start", Pass: 2})
	evs := d(line)
	if len(evs) != 1 || evs[0].Kind != driverkit.KindHostOp || evs[0].HostOp == nil || evs[0].HostOp.Pass != 2 {
		t.Fatalf("decode(spindrift_op) = %+v, want one host_op event", evs)
	}
	// The guest never saw the line, so its counter is still at zero.
	if got := d("x")[0].Text; got != "1:x" {
		t.Errorf("next line decoded as %q, want 1:x", got)
	}
}

func TestDriverDecoderRejectsInvalidGuestEvent(t *testing.T) {
	s := strategy(t, "driver.wasm")
	evs := s.NewDecoder()("forge\n")
	want := []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: "forge"}}
	if !reflect.DeepEqual(evs, want) {
		t.Errorf("decode = %+v, want %+v", evs, want)
	}
}

func TestDriverDecoderFailsClosedWhenConnectorClosed(t *testing.T) {
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	d := s.NewDecoder()
	_ = c.Close(t.Context())
	want := []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: "x"}}
	if got := d(" x "); !reflect.DeepEqual(got, want) {
		t.Errorf("decode = %+v, want %+v", got, want)
	}
}

func TestDriverResultEventAndSessionFlags(t *testing.T) {
	s := strategy(t, "driver.wasm")
	b, err := s.ResultEvent("done")
	if err != nil || string(b) != "result done\n" {
		t.Errorf("ResultEvent = %q, %v", b, err)
	}
	if got := s.SessionFlags("implement", "o/r", "7", "/home/x"); got != "implement|o/r|7|/home/x" {
		t.Errorf("SessionFlags = %q", got)
	}
}

func TestDriverResolveExitReadsLog(t *testing.T) {
	s := strategy(t, "driver.wasm")
	if s.ResolveExit == nil {
		t.Fatal("ResolveExit is nil, want it wired from the capability")
	}
	log := filepath.Join(t.TempDir(), "pass.log")
	if err := os.WriteFile(log, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ResolveExit(log, 1); err != nil || got != 4 {
		t.Errorf("ResolveExit = %d, %v, want 4", got, err)
	}
	if _, err := s.ResolveExit(filepath.Join(t.TempDir(), "missing"), 1); err == nil {
		t.Error("ResolveExit on a missing log = nil error")
	}
	empty := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveExit(empty, 1); !errors.Is(err, connectorhost.ErrCall) {
		t.Errorf("guest error = %v, want ErrCall", err)
	}
}

// A log whose request would pass the cap still resolves: the host sends the
// newest whole lines that fit, skipping any single line too large to fit alone.
func TestDriverResolveExitSendsTheTailOfAnOversizedLog(t *testing.T) {
	c, err := connectorhost.LoadCompiled(context.Background(), fixture(t, "driver.wasm"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	// The fixture answers exitCode plus the newlines it received. A line is 98
	// ASCII bytes and a newline that encodes as 2, so each costs 100 request bytes.
	line := strings.Repeat("x", 98) + "\n"
	fit := (connectorhost.MaxRequest - envelope) / 100
	path := filepath.Join(t.TempDir(), "pass.log")
	if err := os.WriteFile(path, []byte(strings.Repeat(line, fit+fit/8)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolveExit(path, 1)
	if err != nil || got != 1+fit {
		t.Errorf("ResolveExit = %d, %v, want %d: the %d newest lines that fit", got, err, 1+fit, fit)
	}
}

func TestDriverWithoutResolveExit(t *testing.T) {
	s := strategy(t, "seamtest-driver.wasm")
	if s.ResolveExit != nil {
		t.Error("ResolveExit is set, but the Connector does not advertise driver.resolveExit")
	}
}

func TestDriverConcurrentDecoders(t *testing.T) {
	s := strategy(t, "driver.wasm")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := s.NewDecoder()
			for n := 1; n <= 20; n++ {
				want := fmt.Sprintf("%d:%d", n, i)
				if evs := d(fmt.Sprint(i)); len(evs) != 1 || evs[0].Text != want {
					t.Errorf("decoder %d line %d = %+v, want %q", i, n, evs, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestDriverStrategyRefusesUnknownPatternReason(t *testing.T) {
	for _, field := range []string{"transientPatterns", "terminalPatterns"} {
		t.Run(field, func(t *testing.T) {
			c := loadWasm(t, answeringGuest([]string{"driver"},
				`{"result":{"name":"x","`+field+`":[{"substr":"s","reason":"bogus"}]}}`))
			if _, err := c.DriverStrategy(); !errors.Is(err, connectorhost.ErrCall) {
				t.Fatalf("err = %v, want ErrCall", err)
			}
		})
	}
}

func TestDriverSessionFlagsPanicsWhenCallFails(t *testing.T) {
	c := loadWasm(t, answeringGuest([]string{"driver"}, `{"result":{"name":"x"}}`))
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close(t.Context())
	defer func() {
		if err, _ := recover().(error); !errors.Is(err, connectorhost.ErrCall) {
			t.Errorf("recovered %v, want an ErrCall panic", err)
		}
	}()
	s.SessionFlags("implement", "o/r", "7", "/home/x")
	t.Error("SessionFlags returned after a failed guest call")
}

func TestGuestEventsFailClosed(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field":   `[{"Kind":"text","Bogus":1}]`,
		"malformed JSON":  `[{"Kind":`,
		"not an array":    `{"Kind":"text"}`,
		"host op payload": `[{"Kind":"text","HostOp":{"op":"pass_start"}}]`,
		"host op kind":    `[{"Kind":"host_op"}]`,
		"unknown kind":    `[{"Kind":"bogus"}]`,
		"empty kind":      `[{"Text":"x"}]`,
		"one bad of two":  `[{"Kind":"text"},{"Kind":"host_op"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if evs, ok := connectorhost.GuestEvents(json.RawMessage(raw)); ok {
				t.Errorf("GuestEvents(%s) = %+v, true, want false", raw, evs)
			}
		})
	}
	if evs, ok := connectorhost.GuestEvents(json.RawMessage(`[{"Kind":"text","Text":"a"}]`)); !ok || len(evs) != 1 {
		t.Errorf("valid event = %+v, %v", evs, ok)
	}
	if evs, ok := connectorhost.GuestEvents(json.RawMessage(`[]`)); !ok || evs != nil {
		t.Errorf("empty array = %+v, %v, want nil, true", evs, ok)
	}
}

// driver.Driver never resolves a non-zero exit to 0, so the host refuses a
// Connector that does.
func TestDriverResolveExitRefusesZeroForFailure(t *testing.T) {
	c := loadWasm(t, answeringGuest([]string{"driver", "driver.resolveExit"}, `{"result":{"exitCode":0}}`))
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "pass.log")
	if err := os.WriteFile(log, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ResolveExit(log, 3); got != 3 || !errors.Is(err, connectorhost.ErrCall) {
		t.Errorf("ResolveExit(3) = %d, %v, want 3 and ErrCall", got, err)
	}
	if got, err := s.ResolveExit(log, 0); got != 0 || err != nil {
		t.Errorf("ResolveExit(0) = %d, %v, want 0 and no error", got, err)
	}
}

func TestLoadCompiledDecodesLikeLoad(t *testing.T) {
	interp := strategy(t, "driver.wasm")
	cacheDir := t.TempDir()
	c, err := connectorhost.LoadCompiled(context.Background(), fixture(t, "driver.wasm"), cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	compiled, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	// Off amd64 and arm64 wazero has no compiler: it interprets and caches nothing.
	hasCompiler := runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	if ents, err := os.ReadDir(cacheDir); hasCompiler && (err != nil || len(ents) == 0) {
		t.Errorf("compilation cache dir holds %d entries (err %v), want the compiled module", len(ents), err)
	}
	a, b := interp.NewDecoder(), compiled.NewDecoder()
	for _, line := range []string{"x", "", "not json {", "y", hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: "pass_start", Pass: 1})} {
		if got, want := b(line), a(line); !reflect.DeepEqual(got, want) {
			t.Errorf("compiled decode(%q) = %+v, interpreted %+v", line, got, want)
		}
	}
}

// envelope is the size of a resolveExit request carrying an empty log.
const envelope = len(`{"method":"driver.resolveExit","params":{"exitCode":1,"log":""}}`)

func TestLogWindow(t *testing.T) {
	room := connectorhost.MaxRequest - envelope
	window := func(log string) string {
		t.Helper()
		got, err := connectorhost.LogWindow([]byte(log), 1)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := window("a\nb\n"); got != "a\nb\n" {
		t.Errorf("a log that fits = %q, want it unchanged", got)
	}
	// A final line with no newline counts as a line.
	big := strings.Repeat("x", room-4)
	if got := window("old\n" + big + "\nend"); got != "end" {
		t.Errorf("window = %d bytes, want the final line alone", len(got))
	}
	// A quote doubles in the encoding, so the two lines below fill the cap and "ab\n"
	// before them does not fit, though their raw length would.
	quotes := strings.Repeat(`"`, room/4-1) + "\n"
	if got := window("ab\n" + quotes + quotes); got != quotes+quotes {
		t.Errorf("window = %d bytes, want the two escaped lines", len(got))
	}
	// A line too big to fit alone is skipped, not the end of the walk: older
	// lines behind it (an unrecovered error) still reach the resolver.
	if got := window("a\n" + strings.Repeat("x", room) + "\n"); got != "a\n" {
		t.Errorf("window over a final line too big alone = %q, want the line before it", got)
	}
	// Control bytes escape to 6 bytes, so this line passes the cap though its raw
	// length does not.
	huge := strings.Repeat("\x01", connectorhost.MaxRequest/6+1) + "\n"
	if got := window("error\n" + huge + "ok\n"); got != "error\nok\n" {
		t.Errorf("window = %d bytes, want the error and ok lines around the skipped one", len(got))
	}
	// A skipped line does not extend the window: a line that fits alone but not
	// cumulatively still ends the walk.
	if got := window("ab\n" + quotes + huge + quotes); got != quotes+quotes {
		t.Errorf("window = %d bytes, want the two escaped lines", len(got))
	}
}

func TestDriverStrategyCarriesTuning(t *testing.T) {
	s := strategy(t, "driver.wasm")
	if want := (driverkit.ClassifyOptions{Oversized: logscan.ChunkOversized, FirstMatch: true}); s.Classify != want {
		t.Errorf("Classify = %+v, want %+v", s.Classify, want)
	}
	if s.Totals != driverkit.SessionTotals {
		t.Errorf("Totals = %v, want SessionTotals", s.Totals)
	}
	if s.Result != driverkit.LastMessage {
		t.Errorf("Result = %v, want LastMessage", s.Result)
	}
	if s.DefaultTopLevelRole != "lead" {
		t.Errorf("DefaultTopLevelRole = %q, want lead", s.DefaultTopLevelRole)
	}
	h := s.Heartbeat
	if want := []string{"read", "edit"}; !reflect.DeepEqual(h.KindOrder, want) {
		t.Errorf("KindOrder = %v, want %v", h.KindOrder, want)
	}
	if want := map[string]string{"search": "searches"}; !reflect.DeepEqual(h.KindPlurals, want) {
		t.Errorf("KindPlurals = %v, want %v", h.KindPlurals, want)
	}
	if h.Phase == nil || h.Kind == nil || h.Family == nil {
		t.Fatalf("heartbeat funcs = %v %v %v, want all wired from their capabilities", h.Phase == nil, h.Kind == nil, h.Family == nil)
	}
	if got := h.Phase("Task", json.RawMessage(`{"a":1}`)); got != `Task@{"a":1}` {
		t.Errorf("Phase = %q", got)
	}
	if got := h.Kind("Read"); got != "read" {
		t.Errorf("Kind = %q, want read", got)
	}
	if l, r := h.Family("claude-opus-4"); l != "opus" || r != 0 {
		t.Errorf("Family(opus) = %q, %d", l, r)
	}
	if l, r := h.Family("gpt"); l != "gpt" || r != 3 {
		t.Errorf("Family(gpt) = %q, %d", l, r)
	}
}

func TestDriverWithoutTuningStaysAtZeroValues(t *testing.T) {
	s := strategy(t, "seamtest-driver.wasm")
	if s.Classify != (driverkit.ClassifyOptions{}) || s.Totals != driverkit.PerCallTotals ||
		s.Result != driverkit.ResultEvents || s.DefaultTopLevelRole != "" {
		t.Errorf("tuning = %+v %v %v %q, want zero values", s.Classify, s.Totals, s.Result, s.DefaultTopLevelRole)
	}
	h := s.Heartbeat
	if h.Phase != nil || h.Kind != nil || h.Family != nil || h.KindOrder != nil || h.KindPlurals != nil {
		t.Errorf("Heartbeat = %+v, want zero", h)
	}
}

func TestDriverStrategyRefusesUnknownEnum(t *testing.T) {
	for _, field := range []string{"totals", "result"} {
		t.Run(field, func(t *testing.T) {
			c := loadWasm(t, answeringGuest([]string{"driver"},
				`{"result":{"name":"x","`+field+`":"bogus"}}`))
			if _, err := c.DriverStrategy(); !errors.Is(err, connectorhost.ErrCall) {
				t.Fatalf("err = %v, want ErrCall", err)
			}
		})
	}
}

func TestDriverHeartbeatFuncsFailClosedWhenCallFails(t *testing.T) {
	c := loadWasm(t, answeringGuest([]string{"driver", "driver.toolPhase", "driver.toolKind", "driver.modelFamily"},
		`{"result":{"name":"x"}}`))
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close(t.Context())
	if got := s.Heartbeat.Phase("Task", nil); got != "" {
		t.Errorf("Phase = %q, want empty", got)
	}
	if got := s.Heartbeat.Kind("Task"); got != "" {
		t.Errorf("Kind = %q, want empty", got)
	}
	if l, r := s.Heartbeat.Family("m"); l != "m" || r != math.MaxInt {
		t.Errorf("Family = %q, %d, want the id at the last rank", l, r)
	}
}
