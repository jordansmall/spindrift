package connectorhost_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

func TestLimitsForScalesWithTheRequest(t *testing.T) {
	budget, memory, output := connectorhost.LimitsFor(0)
	if budget != connectorhost.CallBudget || memory != connectorhost.MemoryCapBytes || output != connectorhost.MaxOutput {
		t.Fatalf("LimitsFor(0) = %v, %d, %d, want the bases", budget, memory, output)
	}
	const mib = 1 << 20
	budget, memory, output = connectorhost.LimitsFor(mib)
	if want := connectorhost.CallBudget + connectorhost.BudgetPerMiB; budget != want {
		t.Errorf("budget at 1 MiB = %v, want %v", budget, want)
	}
	if want := uint64(connectorhost.MemoryCapBytes + mib*connectorhost.MemoryPerRequestByte); memory != want {
		t.Errorf("memory at 1 MiB = %d, want %d", memory, want)
	}
	if want := uint64(connectorhost.MaxOutput + mib*connectorhost.OutputPerRequestByte); output != want {
		t.Errorf("output at 1 MiB = %d, want %d", output, want)
	}
}

// A correct Connector's decode time grows with the line, so a line the launcher
// legally reads (logscan takes up to 4 MiB) must not trip the base call budget
// and close the Connector under every later line.
func TestDriverDecodesALargeLineWithoutWedging(t *testing.T) {
	s := strategy(t, "driver.wasm")
	d := s.NewDecoder()
	line := strings.Repeat("a", 2<<20)
	start := time.Now()
	evs := d(line)
	t.Logf("decoded 2 MiB in %v", time.Since(start))
	if len(evs) != 1 || evs[0].Kind != driverkit.KindText || evs[0].Text != "1:"+line {
		t.Fatalf("decode of a 2 MiB line = %d events, want one text event", len(evs))
	}
	if evs := d("y"); len(evs) != 1 || evs[0].Kind != driverkit.KindText || evs[0].Text != "2:y" {
		t.Fatalf("decode after the large line = %+v, want text 2:y", evs)
	}
}

// The guest's encoder writes each of <, > and & as a 6-byte \u00XX escape, so a
// correct decode of such a line answers with about 6 times the line, past the
// base cap. The host sends the line raw, so the request stays near 1.5 MiB.
func TestDriverDecodeOutputMayExceedTheBaseCap(t *testing.T) {
	c, err := connectorhost.LoadCompiled(context.Background(), fixture(t, "driver.wasm"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("<>&", 512<<10) // 1.5 MiB; its response is ~9 MB
	evs := s.NewDecoder()(line)
	if len(evs) != 1 || evs[0].Kind != driverkit.KindText || evs[0].Text != "1:"+line {
		t.Fatalf("decode = %d events, want one text event", len(evs))
	}
}

// Memory the guest grew for one large call stays grown, so a later small call's
// ceiling must not fall below it: the guest's allocator would then be refused
// even one page and the Connector marked dead.
func TestSmallCallAfterALargeOneKeepsItsGrowth(t *testing.T) {
	const resp = `{"result":5}`
	hs, fns := connectorFns(nil, allocOff, respOff<<32|int64(len(resp)))
	// connector_alloc(n) grows memory by (n>>12)+1 pages, about the 16 bytes per
	// request byte limitsFor allows, traps if the grow failed, and returns the
	// first new page. The parameter is reused as the local holding the grow result.
	fns[fnAlloc].body = []byte{
		0x20, 0x00, 0x41, 12, 0x76, 0x41, 1, 0x6a, // local.get 0; i32.const 12; i32.shr_u; i32.const 1; i32.add
		0x40, 0x00, 0x21, 0x00, // memory.grow; local.set 0
		0x20, 0x00, 0x41, 1, 0x6a, 0x45, 0x04, 0x40, unreachable, 0x0b, // trap if the grow returned -1
		0x20, 0x00, 0x41, 16, 0x74, // local.get 0; i32.const 16; i32.shl
	}
	c := loadWasm(t, guestModule(nil, []dataSeg{{0, hs}, {respOff, []byte(resp)}}, fns...))
	var out int
	if err := c.Call(context.Background(), "m", strings.Repeat("a", 4<<20+512<<10), &out); err != nil {
		t.Fatalf("large call: %v", err)
	}
	if err := c.Call(context.Background(), "m", nil, &out); err != nil || out != 5 {
		t.Fatalf("small call after the large one = %d, %v, want 5", out, err)
	}
}

// The host refuses an oversized request before the guest sees it, so the
// Connector is still usable and its scaled limits stay bounded.
func TestRequestOverMaxRequestIsRefusedAndTheConnectorSurvives(t *testing.T) {
	c := loadWasm(t, answeringGuest(nil, `{"result":5}`))
	start := time.Now()
	err := c.Call(context.Background(), "m", strings.Repeat("a", connectorhost.MaxRequest), nil)
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrRequestTooLarge) {
		t.Fatalf("oversized call = %v, want ErrCall and ErrRequestTooLarge", err)
	}
	assertOnlySentinel(t, err, connectorhost.ErrRequestTooLarge)
	if d := time.Since(start); d > connectorhost.CallBudget {
		t.Errorf("refusal took %v, want it before the guest runs", d)
	}
	var out int
	if err := c.Call(context.Background(), "m", nil, &out); err != nil || out != 5 {
		t.Fatalf("call after the refusal = %d, %v, want 5", out, err)
	}
}
