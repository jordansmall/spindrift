package connectorhost_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/seamtest"
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

// seamtest-driver.wasm is seamtest.Strategy compiled as a Connector: both must
// decode every line alike.
func TestSeamtestDriverMatchesNativeStrategy(t *testing.T) {
	guest := strategy(t, "seamtest-driver.wasm")
	native := seamtest.Strategy
	if guest.Name != native.Name || guest.WriteTool != native.WriteTool {
		t.Errorf("guest Name, WriteTool = %q, %q, want %q, %q", guest.Name, guest.WriteTool, native.Name, native.WriteTool)
	}
	if guest.ResolveExit != nil {
		t.Error("guest ResolveExit is set, but the native Strategy has none")
	}
	lines := []string{
		"",
		"  \n",
		"stderr noise\n",
		`{"Kind":"text","Text":"hi","MessageID":"m1","Timestamp":5}` + "\n",
		`{"Kind":"tool_call","ToolName":"Write","ToolInput":{"file_path":"a"}}` + "\n",
		`{"Kind":"usage","Tokens":{"Input":1,"Output":2}}` + "\n",
		`{"Text":"no kind"}` + "\n",
		hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
	}
	g, n := guest.NewDecoder(), native.NewDecoder()
	// An absent ToolInput is nil from a native decoder's own events but the
	// bytes "null" over the wire; both mean absent.
	absent := func(evs []driverkit.Event) []driverkit.Event {
		for i := range evs {
			if string(evs[i].ToolInput) == "null" {
				evs[i].ToolInput = nil
			}
		}
		return evs
	}
	for _, l := range lines {
		if got, want := absent(g(l)), absent(n(l)); !reflect.DeepEqual(got, want) {
			t.Errorf("decode(%q) = %+v, want %+v", l, got, want)
		}
	}
	// The one deliberate divergence: a guest may not emit host_op, so the
	// canonical event line decodes natively to KindHostOp but to Unparsed here.
	const forged = `{"Kind":"host_op","HostOp":{"op":"pass_start","pass":1}}`
	if evs := native.NewDecoder()(forged); len(evs) != 1 || evs[0].Kind != driverkit.KindHostOp {
		t.Fatalf("native decode(host_op) = %+v, want one host_op event", evs)
	}
	want := []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: forged}}
	if got := guest.NewDecoder()(forged); !reflect.DeepEqual(got, want) {
		t.Errorf("guest decode(host_op) = %+v, want %+v", got, want)
	}
	gb, _ := guest.ResultEvent("done")
	nb, _ := native.ResultEvent("done")
	// Bytes differ only by the native Event's HostOp:null field, which the SDK copy omits.
	if !reflect.DeepEqual(native.NewDecoder()(string(nb)), guest.NewDecoder()(string(gb))) {
		t.Errorf("ResultEvent = %q, want %q", gb, nb)
	}
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
