package connectorhost_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
)

const fixturesEnv = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	dir := os.Getenv(fixturesEnv)
	if dir == "" {
		t.Skipf("%s is unset; it names the dir holding the fixture Connector .wasm files", fixturesEnv)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func load(t *testing.T, name string) (*connectorhost.Connector, error) {
	t.Helper()
	c, err := connectorhost.Load(context.Background(), fixture(t, name))
	if c != nil {
		t.Cleanup(func() { _ = c.Close(context.Background()) })
	}
	return c, err
}

func TestLoadCurrentYieldsHandshake(t *testing.T) {
	c, err := load(t, "current.wasm")
	if err != nil {
		t.Fatal(err)
	}
	// current.wasm carries the SDK's default protocolVersion, so this is the
	// parity guard between the host's version and the SDK's.
	if c.ProtocolVersion != connectorhost.ProtocolVersion {
		t.Errorf("ProtocolVersion = %q, want %q", c.ProtocolVersion, connectorhost.ProtocolVersion)
	}
	if want := []string{"fixture.alpha", "fixture.beta"}; !reflect.DeepEqual(c.Capabilities, want) {
		t.Errorf("Capabilities = %v, want %v", c.Capabilities, want)
	}
}

func TestLoadAcceptsPreviousMinor(t *testing.T) {
	c, err := load(t, "previous.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if c.ProtocolVersion != "0.0.3" {
		t.Errorf("ProtocolVersion = %q, want 0.0.3", c.ProtocolVersion)
	}
}

func TestLoadRefusesFutureProtocol(t *testing.T) {
	_, err := load(t, "future.wasm")
	if !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Fatalf("err = %v, want ErrProtocolUnsupported", err)
	}
	var pe *connectorhost.ProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a *ProtocolError", err)
	}
	if pe.Connector != "0.2.0" {
		t.Errorf("ProtocolError.Connector = %q, want 0.2.0", pe.Connector)
	}
	for _, want := range []string{"0.2.0", "0.0.x", "0.1.x", connectorhost.ProtocolVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestLoadSandboxReachesNothing(t *testing.T) {
	a, err := load(t, "probe.wasm")
	if err != nil {
		t.Fatal(err)
	}
	b, err := load(t, "probe.wasm")
	if err != nil {
		t.Fatal(err)
	}
	// "rand:<hex>" is the probe's crypto/rand draw: identical across loads only
	// if the random source is deterministic. Any fs, env, args, net or clock
	// capability means the sandbox leaked.
	if !reflect.DeepEqual(a.Capabilities, b.Capabilities) {
		t.Errorf("Capabilities differ across loads: %v vs %v: the random source is not deterministic", a.Capabilities, b.Capabilities)
	}
	if got := a.Capabilities; len(got) != 2 || got[0] != "probe" || !strings.HasPrefix(got[1], "rand:") {
		t.Errorf("Capabilities = %v, want [probe rand:<hex>]: the Connector reached fs, env, args, net or a real clock", got)
	}
}

func TestLoadRefusesUngrantedImport(t *testing.T) {
	for _, imp := range [][2]string{{"env", "connect"}, {"wasi_snapshot_preview1", "sock_open"}} {
		t.Run(imp[1], func(t *testing.T) {
			wasm := wasmModule(nil, []byte{i64}, i64Const(0), true, imp)
			c, err := connectorhost.Load(context.Background(), wasm)
			if c != nil {
				_ = c.Close(context.Background())
			}
			if !errors.Is(err, connectorhost.ErrHandshake) {
				t.Fatalf("err = %v, want ErrHandshake", err)
			}
			if !strings.Contains(err.Error(), imp[1]) {
				t.Errorf("error %q does not name the import %q", err, imp[1])
			}
		})
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	_, err := connectorhost.Load(context.Background(), []byte("not a wasm module"))
	if !errors.Is(err, connectorhost.ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake", err)
	}
}

func TestCheckProtocolWindow(t *testing.T) {
	for _, v := range []string{"0.1.0", "0.1.9", "0.0.0", "0.0.3"} {
		if err := connectorhost.CheckProtocol(v); err != nil {
			t.Errorf("CheckProtocol(%q) = %v, want nil", v, err)
		}
	}
	for _, v := range []string{"0.2.0", "1.0.0", "1.1.0", "0.1", "v0.1.0", "0.1.0-rc1", "0.1.x", "", "0.01.0", "0.1.00", "00.1.0", "0.+1.0", "0..0", "0.1.0.0"} {
		if err := connectorhost.CheckProtocol(v); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
			t.Errorf("CheckProtocol(%q) = %v, want ErrProtocolUnsupported", v, err)
		}
	}
}

const (
	i32 = 0x7f
	i64 = 0x7e
)

// wasmModule assembles a one-function wasm module whose function, of type
// params -> results, is exported as connector_handshake and runs body; withMem
// adds one page of memory exported as "memory". The Go toolchain cannot emit a
// malformed Connector and no wat2wasm is on hand, so the tests build their own.
// Each imports entry {module, name} adds a function import; imports come first
// in the function index space, so the export's index follows them.
func wasmModule(params, results, body []byte, withMem bool, imports ...[2]string) []byte {
	pages := -1
	if withMem {
		pages = 1
	}
	return wasmModuleMem(params, results, body, pages, imports...)
}

// wasmModuleMem is wasmModule with the memory's minimum page count explicit;
// memPages < 0 means no memory.
func wasmModuleMem(params, results, body []byte, memPages int, imports ...[2]string) []byte {
	withMem := memPages >= 0
	exports := append([]byte{1}, exportEntry("connector_handshake", 0, byte(len(imports)))...)
	if withMem {
		exports[0] = 2
		exports = append(exports, exportEntry("memory", 2, 0)...)
	}
	code := append(append([]byte{0}, body...), 0x0b)
	m := []byte("\x00asm\x01\x00\x00\x00")
	m = append(m, section(1, append([]byte{1, 0x60}, append(vec(params), vec(results)...)...))...)
	if len(imports) > 0 {
		imps := []byte{byte(len(imports))}
		for _, imp := range imports {
			imps = append(append(imps, vec([]byte(imp[0]))...), vec([]byte(imp[1]))...)
			imps = append(imps, 0, 0) // function import of type 0
		}
		m = append(m, section(2, imps)...)
	}
	m = append(m, section(3, []byte{1, 0})...)
	if withMem {
		m = append(m, section(5, append([]byte{1, 0}, uleb(memPages)...))...)
	}
	m = append(m, section(7, exports)...)
	return append(m, section(10, append([]byte{1}, vec(code)...))...)
}

// exportEntry is one export-section entry: name, kind, index.
func exportEntry(name string, kind, idx byte) []byte {
	return append(append([]byte{byte(len(name))}, name...), kind, idx)
}

// vec is a length-prefixed byte vector.
func vec(b []byte) []byte { return append(uleb(len(b)), b...) }

// section is a wasm section: id, size, content.
func section(id byte, content []byte) []byte {
	return append(append([]byte{id}, uleb(len(content))...), content...)
}

// store writes s at address 0 of module memory, byte by byte (the helper
// emits no data section).
func store(s string) []byte {
	var out []byte
	for i := range len(s) {
		out = append(out, i32Const(int64(i))...)
		out = append(out, i32Const(int64(s[i]))...)
		out = append(out, 0x3a, 0, 0) // i32.store8 align=0 offset=0
	}
	return out
}

// i64Const encodes i64.const v, whose operand is signed LEB128.
func i64Const(v int64) []byte {
	out := []byte{0x42}
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func TestLoadRefusesBrokenHandshake(t *testing.T) {
	handshake := handshakeAt
	tests := []struct {
		name string
		wasm []byte
	}{
		{"missing export", []byte("\x00asm\x01\x00\x00\x00")},
		{"result is i32", wasmModule(nil, []byte{i32}, []byte{0x41, 0}, true)},
		{"two results", wasmModule(nil, []byte{i64, i64}, append(handshake(0, 1), handshake(0, 1)...), true)},
		{"no result", wasmModule(nil, nil, nil, true)},
		{"takes a parameter", wasmModule([]byte{i32}, []byte{i64}, handshake(0, 1), true)},
		{"no memory export", wasmModule(nil, []byte{i64}, handshake(0, 0), false)},
		{"length past memory", wasmModule(nil, []byte{i64}, handshake(0, 2*page), true)},
		{"pointer past memory", wasmModule(nil, []byte{i64}, handshake(page, 1), true)},
		{"malformed JSON", wasmModule(nil, []byte{i64}, handshake(0, 1), true)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := connectorhost.Load(context.Background(), tc.wasm)
			if c != nil {
				_ = c.Close(context.Background())
			}
			if !errors.Is(err, connectorhost.ErrHandshake) {
				t.Fatalf("err = %v, want ErrHandshake", err)
			}
		})
	}
}

func TestLoadHonoursDeadline(t *testing.T) {
	wasm := wasmModule(nil, []byte{i64}, append(loop, i64Const(0)...), true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := connectorhost.Load(ctx, wasm)
	if c != nil {
		_ = c.Close(context.Background())
	}
	if err == nil {
		t.Fatal("Load returned nil error for a handshake that never returns")
	}
	if d := time.Since(start); d > slack {
		t.Fatalf("Load took %v past a 100ms deadline", d)
	}
}

// A Driver Connector is granted no Host calls (ADR 0073 rule 2): a module
// advertising "driver" and importing one fails to link.
func TestLoadRefusesHostCallImport(t *testing.T) {
	hs, fns := connectorFns([]string{"driver"}, allocOff, 0)
	wasm := guestModule([][2]string{{"spindrift", "http_request"}}, []dataSeg{{0, hs}}, fns...)
	c, err := connectorhost.Load(context.Background(), wasm)
	if c != nil {
		_ = c.Close(context.Background())
	}
	if !errors.Is(err, connectorhost.ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake", err)
	}
	if !strings.Contains(err.Error(), "spindrift") {
		t.Errorf("error %q does not name the import module", err)
	}
}

const (
	unreachable = 0x00
	capPages    = connectorhost.MemoryCapBytes / page
	// slack is what a call may take beyond its budget before a test calls it hung.
	slack = 2 * time.Second
	// promptly bounds a Load that must refuse a module up front, without running it.
	promptly = 2 * time.Second
)

// loop spins forever: loop; br 0; end.
var loop = []byte{0x03, 0x40, 0x0c, 0x00, 0x0b}

// sentinels are the named call failures; each case matches exactly one.
var sentinels = []error{
	connectorhost.ErrBudget, connectorhost.ErrTrap, connectorhost.ErrMemoryCap, connectorhost.ErrDeclaredSize,
	connectorhost.ErrOutputTooLarge, connectorhost.ErrMalformedOutput, connectorhost.ErrExport,
	connectorhost.ErrRequestTooLarge,
}

// assertOnlySentinel fails t unless err matches want and no other sentinel.
func assertOnlySentinel(t *testing.T, err, want error) {
	t.Helper()
	for _, s := range sentinels {
		if got := errors.Is(err, s); got != (s == want) {
			t.Errorf("errors.Is(err, %v) = %v, err = %v", s, got, err)
		}
	}
}

func handshakeAt(ptr, n int64) []byte { return i64Const(ptr<<32 | n) }

// grow returns code that grows memory by pages and traps if the grow failed.
func grow(pages int32) []byte {
	code := append(i32Const(int64(pages)), 0x40, 0x00) // memory.grow
	code = append(code, i32Const(-1)...)
	return append(code, 0x46, 0x04, 0x40, unreachable, 0x0b) // i32.eq; if; unreachable; end
}

const goodHandshake = `{"protocolVersion":"0.1.0","capabilities":["a"]}`

func TestHostileConnectorFailsWithItsOwnNamedError(t *testing.T) {
	tests := []struct {
		name string
		wasm []byte
		want error
	}{
		{"loop", wasmModule(nil, []byte{i64}, append(loop, i64Const(0)...), true), connectorhost.ErrBudget},
		{"unreachable", wasmModule(nil, []byte{i64}, []byte{unreachable}, true), connectorhost.ErrTrap},
		{"memory grow past cap", wasmModule(nil, []byte{i64}, append(grow(capPages+1), handshakeAt(0, 1)...), true), connectorhost.ErrMemoryCap},
		{"minimum memory past cap", wasmModuleMem(nil, []byte{i64}, handshakeAt(0, 1), capPages+1), connectorhost.ErrMemoryCap},
		{"oversize", wasmModuleMem(nil, []byte{i64}, handshakeAt(0, connectorhost.MaxOutput+1), 32), connectorhost.ErrOutputTooLarge},
		{"oversize and past memory", wasmModule(nil, []byte{i64}, handshakeAt(0, 1<<31), true), connectorhost.ErrOutputTooLarge},
		{"length past memory", wasmModule(nil, []byte{i64}, handshakeAt(0, 2*page), true), connectorhost.ErrMalformedOutput},
		{"pointer past memory", wasmModule(nil, []byte{i64}, handshakeAt(page, 1), true), connectorhost.ErrMalformedOutput},
		{"malformed JSON", wasmModule(nil, []byte{i64}, handshakeAt(0, 1), true), connectorhost.ErrMalformedOutput},
		{"invalid UTF-8 in a string", handshakeModule("{\"protocolVersion\":\"0.1.0\",\"capabilities\":[\"\xff\"]}"), connectorhost.ErrMalformedOutput},
		{"handshake null", handshakeModule("null"), connectorhost.ErrMalformedOutput},
		{"handshake empty object", handshakeModule("{}"), connectorhost.ErrMalformedOutput},
		{"handshake without capabilities", handshakeModule(`{"protocolVersion":"0.1.0"}`), connectorhost.ErrMalformedOutput},
		{"handshake with null capabilities", handshakeModule(`{"protocolVersion":"0.1.0","capabilities":null}`), connectorhost.ErrMalformedOutput},
		{"JSON of the wrong shape", handshakeModule("[]"), connectorhost.ErrMalformedOutput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			c, err := connectorhost.Load(context.Background(), tc.wasm)
			if c != nil {
				_ = c.Close(context.Background())
			}
			if d := time.Since(start); d > connectorhost.InitBudget+connectorhost.CallBudget+slack {
				t.Fatalf("Load took %v with no caller deadline", d)
			}
			if !errors.Is(err, connectorhost.ErrHandshake) {
				t.Errorf("err = %v, want ErrHandshake", err)
			}
			assertOnlySentinel(t, err, tc.want)
		})
	}
}

func TestCallerDeadlineIsNotABudgetOverrun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := connectorhost.Load(ctx, wasmModule(nil, []byte{i64}, append(loop, i64Const(0)...), true))
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, connectorhost.ErrBudget) {
		t.Fatalf("err = %v, want the caller's DeadlineExceeded and not ErrBudget", err)
	}
}

func TestInitializeWithParameterIsErrExportNotTrap(t *testing.T) {
	wasm := withInitialize([]byte{i32}, handshakeAt(0, 1))
	c, err := connectorhost.Load(context.Background(), wasm)
	if c != nil {
		_ = c.Close(context.Background())
	}
	if !errors.Is(err, connectorhost.ErrHandshake) || !errors.Is(err, connectorhost.ErrExport) || errors.Is(err, connectorhost.ErrTrap) {
		t.Fatalf("err = %v, want ErrHandshake and ErrExport, not ErrTrap", err)
	}
}

// A module that does not export its memory is refused at load. Its minimum is
// over the cap so that were the export check dropped, the module would reach the
// allocator, which panics inside wazero; the over-cap minimum check itself is
// covered by "minimum memory past cap" above.
func TestUnexportedMemoryIsRefusedWithoutPanic(t *testing.T) {
	w := wasmModuleMem(nil, []byte{i64}, handshakeAt(0, 1), capPages+1)
	entry := []byte("\x06memory\x02\x00") // name length, name, kind memory, index 0
	i := bytes.Index(w, entry)
	p := bytes.Index(w, []byte("\x02\x13connector_handshake")) // export count follows the section length
	if i < 0 || p < 0 {
		t.Fatal("export section not found")
	}
	w[p-1] -= byte(len(entry))
	w[p] = 1
	w = append(w[:i:i], w[i+len(entry):]...)
	c, err := connectorhost.Load(context.Background(), w)
	if c != nil {
		_ = c.Close(context.Background())
	}
	if !errors.Is(err, connectorhost.ErrHandshake) || !errors.Is(err, connectorhost.ErrExport) {
		t.Fatalf("err = %v, want ErrHandshake and ErrExport", err)
	}
}

// withTable inserts a table section (one funcref table with the given minimum)
// after the function section of a wasmModule built without imports.
func withTable(t *testing.T, w []byte, minimum uint32) []byte {
	t.Helper()
	funcSection := []byte{3, 2, 1, 0}
	i := bytes.Index(w, funcSection)
	if i < 0 {
		t.Fatal("function section not found")
	}
	tbl := section(4, append([]byte{1, 0x70, 0x00}, uleb(int(minimum))...))
	i += len(funcSection)
	return append(w[:i:i], append(tbl, w[i:]...)...)
}

// A start section runs inside instantiation, outside any call budget, so the
// scan refuses one before it can loop or trap; init belongs in _initialize.
func TestStartSectionIsRefusedAtLoad(t *testing.T) {
	body := []byte{0x03, 0x40, 0x0c, 0x00, 0x0b} // loop; br 0; end
	w := wasmModule(nil, nil, body, true)
	code := section(10, append([]byte{1}, vec(append(append([]byte{0}, body...), 0x0b))...))
	cut := len(w) - len(code)
	w = append(w[:cut:cut], append(section(8, []byte{0}), code...)...)
	done := make(chan error, 1)
	go func() {
		c, err := connectorhost.Load(context.Background(), w)
		if c != nil {
			_ = c.Close(context.Background())
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, connectorhost.ErrHandshake) || !errors.Is(err, connectorhost.ErrExport) {
			t.Fatalf("err = %v, want ErrHandshake and ErrExport", err)
		}
	case <-time.After(connectorhost.CallBudget):
		t.Fatal("Load did not return: the start function ran unbudgeted")
	}
}

// table.grow is never capped and runs as one uninterruptible host operation, so
// reference types are off: a module using it must not compile.
func TestTableGrowIsRefusedAtLoad(t *testing.T) {
	body := []byte{0xd0, 0x70}                  // ref.null func
	body = append(body, i32Const(-2)...)        // a delta far past any real table
	body = append(body, 0xfc, 0x0f, 0x00, 0x1a) // table.grow 0; drop
	body = append(body, handshakeAt(0, 1)...)
	w := withTable(t, wasmModule(nil, []byte{i64}, body, true), 0)
	start := time.Now()
	c, err := connectorhost.Load(context.Background(), w)
	if c != nil {
		_ = c.Close(context.Background())
	}
	if !errors.Is(err, connectorhost.ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake", err)
	}
	if d := time.Since(start); d > promptly {
		t.Fatalf("Load took %v to refuse table.grow", d)
	}
}

func TestDeclaredTableMinimumIsCapped(t *testing.T) {
	const maxTableEntries = connectorhost.MaxTableEntries
	for _, tc := range []struct {
		name string
		min  uint32
		want error
	}{
		{"over the cap", 1 << 27, connectorhost.ErrDeclaredSize},
		{"just over the cap", maxTableEntries + 1, connectorhost.ErrDeclaredSize},
		{"at the cap", maxTableEntries, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := withTable(t, handshakeModule(goodHandshake), tc.min)
			start := time.Now()
			c, err := connectorhost.Load(context.Background(), w)
			if c != nil {
				_ = c.Close(context.Background())
			}
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) || !errors.Is(err, connectorhost.ErrHandshake) {
				t.Fatalf("err = %v, want ErrHandshake wrapping %v", err, tc.want)
			}
			if d := time.Since(start); d > promptly {
				t.Fatalf("Load took %v to refuse an over-cap table", d)
			}
		})
	}
}

// withInitialize returns a module like wasmModule(nil, i64, body, true) that
// also exports _initialize with the given parameter types and no results.
func withInitialize(initParams []byte, body []byte) []byte {
	types := []byte{2, 0x60, 0, 1, i64, 0x60}
	types = append(append(types, vec(initParams)...), 0)
	exports := []byte{3}
	exports = append(exports, exportEntry("connector_handshake", 0, 0)...)
	exports = append(exports, exportEntry("memory", 2, 0)...)
	exports = append(exports, exportEntry("_initialize", 0, 1)...)
	handshakeBody := vec(append(append([]byte{0}, body...), 0x0b))
	initBody := vec([]byte{0, 0x0b})
	m := []byte("\x00asm\x01\x00\x00\x00")
	m = append(m, section(1, types)...)
	m = append(m, section(3, []byte{2, 0, 1})...)
	m = append(m, section(5, []byte{1, 0, 1})...)
	m = append(m, section(7, exports)...)
	return append(m, section(10, append([]byte{2}, append(handshakeBody, initBody...)...))...)
}

func handshakeModule(json string) []byte {
	return wasmModule(nil, []byte{i64}, append(store(json), handshakeAt(0, int64(len(json)))...), true)
}

// A memory-cap refusal the guest survives in one call must not be blamed for a
// later, unrelated trap: the handshake is refused a grow and returns normally,
// then connector_call traps.
func TestMemoryCapRefusalDoesNotColourALaterTrap(t *testing.T) {
	hs, fns := connectorFns(nil, allocOff, 0)
	fns[fnHandshake].body = append(append(i32Const(capPages+1), 0x40, 0x00, 0x1a), fns[fnHandshake].body...) // memory.grow; drop
	fns[fnCall].body = []byte{unreachable}
	c := loadWasm(t, guestModule(nil, []dataSeg{{0, hs}}, fns...))
	err := c.Call(context.Background(), "m", nil, nil)
	if !errors.Is(err, connectorhost.ErrTrap) || errors.Is(err, connectorhost.ErrMemoryCap) {
		t.Fatalf("err = %v, want ErrTrap and not ErrMemoryCap", err)
	}
}

// withFunctions rewrites the function and code sections of a module built by
// handshakeModule to hold len(locals) functions, function k declaring locals[k]
// i32 locals. Function 0 keeps the handshake body; the rest return 0.
func withFunctions(t *testing.T, w []byte, locals ...uint32) []byte {
	t.Helper()
	decl := func(n uint32) []byte {
		if n == 0 {
			return []byte{0}
		}
		return append(append([]byte{1}, uleb(int(n))...), i32)
	}
	out := w[:8:8]
	for i := 8; i < len(w); {
		size, n := binary.Uvarint(w[i+1:])
		content := w[i+1+n : i+1+n+int(size)]
		switch w[i] {
		case 3:
			content = append(uleb(len(locals)), make([]byte, len(locals))...)
		case 10:
			_, n := binary.Uvarint(content[1:]) // past the count and the body size
			handshake := content[1+n+1:]        // past the empty local declarations
			bodies := uleb(len(locals))
			for k, l := range locals {
				body := append(i64Const(0), 0x0b)
				if k == 0 {
					body = handshake
				}
				bodies = append(bodies, vec(append(decl(l), body...))...)
			}
			content = bodies
		}
		out = append(out, section(w[i], content)...)
		i += 1 + n + int(size)
	}
	return out
}

func TestDeclaredLocalsAreCapped(t *testing.T) {
	const frame, total = connectorhost.MaxFrameSlots, connectorhost.MaxModuleLocals
	// The handshake body peaks at two operands and the others at one, so locals
	// of frame-2 keep every function within the per-frame cap.
	const fn = frame - 2
	many := make([]uint32, total/fn+1) // each within the per-frame cap, together over the total
	for i := range many {
		many[i] = fn
	}
	for _, tc := range []struct {
		name   string
		locals []uint32
		want   error
	}{
		{"2^27 locals in one function", []uint32{1 << 27}, connectorhost.ErrDeclaredSize},
		{"2^32-1 locals in one function", []uint32{1<<32 - 1}, connectorhost.ErrDeclaredSize},
		{"just over the per-frame cap", []uint32{fn + 1}, connectorhost.ErrDeclaredSize},
		{"many functions over the total cap", many, connectorhost.ErrDeclaredSize},
		{"at the per-frame cap", []uint32{fn}, nil},
		{"many functions at the total cap", many[:total/fn], nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := withFunctions(t, handshakeModule(goodHandshake), tc.locals...)
			start := time.Now()
			c, err := connectorhost.Load(context.Background(), w)
			if c != nil {
				_ = c.Close(context.Background())
			}
			if d := time.Since(start); d > promptly {
				t.Errorf("Load took %v", d)
			}
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) || !errors.Is(err, connectorhost.ErrHandshake) {
				t.Fatalf("err = %v, want ErrHandshake wrapping %v", err, tc.want)
			}
		})
	}
}

// consts is n copies of i32.const 0; drops is n drops.
func consts(n int) []byte { return bytes.Repeat(i32Const(0), n) }
func drops(n int) []byte  { return bytes.Repeat([]byte{0x1a}, n) }

// stackBodies each build a handshake prefix whose operand stack peaks at
// exactly n values and ends empty, so the cap on a frame's slots is probed
// through Load.
var stackBodies = []struct {
	name string
	body func(n int) []byte
}{
	{"pushes", func(n int) []byte { return append(consts(n), drops(n)...) }},
	{"a block's result", func(n int) []byte { // n-1 below, then a block leaving one
		b := append(consts(n-1), 0x02, i32)
		b = append(b, i32Const(0)...)
		return append(append(b, 0x0b), drops(n)...)
	}},
	{"a branch resetting the block's stack", func(n int) []byte {
		b := append(consts(n-1), 0x02, i32)
		b = append(b, i32Const(0)...)
		b = append(b, 0x0c, 0x00, 0x0b) // br 0; end
		return append(b, drops(n)...)
	}},
	{"a call's result", func(n int) []byte { // a never-taken if calls the handshake itself
		b := append(consts(n-1), i32Const(0)...)
		b = append(b, 0x04, 0x40, 0x10, 0x00, 0x1a, 0x0b) // if; call 0; drop; end
		return append(b, drops(n-1)...)
	}},
}

func loadHandshake(prefix []byte) (*connectorhost.Connector, time.Duration, error) {
	body := append(prefix, store(goodHandshake)...)
	body = append(body, handshakeAt(0, int64(len(goodHandshake)))...)
	start := time.Now()
	c, err := connectorhost.Load(context.Background(), wasmModule(nil, []byte{i64}, body, true))
	return c, time.Since(start), err
}

// A frame's slots are its parameters, its locals and the height its operand
// stack reaches; the interpreter allocates all of them per frame, so the cap
// counts them together.
func TestOperandStackHeightIsCapped(t *testing.T) {
	const frame = connectorhost.MaxFrameSlots
	for _, sb := range stackBodies {
		for _, tc := range []struct {
			n    int
			want error
		}{{frame, nil}, {frame + 1, connectorhost.ErrDeclaredSize}} {
			t.Run(fmt.Sprintf("%s peaking at %d", sb.name, tc.n), func(t *testing.T) {
				c, d, err := loadHandshake(sb.body(tc.n))
				if c != nil {
					_ = c.Close(context.Background())
				}
				if d > promptly {
					t.Errorf("Load took %v", d)
				}
				if tc.want == nil {
					if err != nil {
						t.Fatalf("err = %v, want nil", err)
					}
					return
				}
				if !errors.Is(err, tc.want) || !errors.Is(err, connectorhost.ErrHandshake) {
					t.Fatalf("err = %v, want ErrHandshake wrapping %v", err, tc.want)
				}
			})
		}
	}
}

// The attack the locals cap alone missed: a handshake that stacks far more
// operands than the cap and recurses, so each of the interpreter's 2000 frames
// would hold them.
func TestRecursingOperandStackIsRefusedBeforeItRuns(t *testing.T) {
	prefix := append(consts(60000), 0x10, 0x00, 0x0f) // call 0; return
	var err error
	alloc := allocatedBy(func() {
		var c *connectorhost.Connector
		var d time.Duration
		c, d, err = loadHandshake(prefix)
		if c != nil {
			_ = c.Close(context.Background())
		}
		if d > promptly {
			t.Errorf("Load took %v", d)
		}
	})
	if !errors.Is(err, connectorhost.ErrDeclaredSize) || !errors.Is(err, connectorhost.ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake wrapping ErrDeclaredSize", err)
	}
	if alloc > connectorhost.MemoryCapBytes {
		t.Fatalf("Load allocated %d bytes refusing a %d byte module", alloc, len(prefix))
	}
}

// A frame just under the cap, recursed until the interpreter's call stack
// overflows, is the most stack the host lets a guest hold; it must fail as a
// trap with allocation bounded, not in the GiBs. 2000 frames of maxFrameSlots
// 8-byte slots is about memoryCapBytes; growing and copying the stack slice
// leaves total allocation within a small multiple of that, hence the 8x.
func TestRecursingFrameAtTheCapStaysBounded(t *testing.T) {
	prefix := append(consts(connectorhost.MaxFrameSlots-1), 0x10, 0x00, 0x0f) // call 0; return
	var err error
	alloc := allocatedBy(func() {
		var c *connectorhost.Connector
		c, _, err = loadHandshake(prefix)
		if c != nil {
			_ = c.Close(context.Background())
		}
	})
	if !errors.Is(err, connectorhost.ErrTrap) || !errors.Is(err, connectorhost.ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake wrapping ErrTrap", err)
	}
	t.Logf("recursion allocated %d bytes", alloc)
	if alloc > 8*connectorhost.MemoryCapBytes {
		t.Fatalf("recursion allocated %d bytes, over 8x the %d byte memory cap", alloc, connectorhost.MemoryCapBytes)
	}
}

// The size scan runs on a binary wazero has not validated, so a truncated one
// must reach CompileModule's decode error, not panic.
func TestTruncatedBinaryFailsWithoutPanic(t *testing.T) {
	w := withTable(t, handshakeModule(goodHandshake), 1)
	for n := range len(w) {
		c, err := connectorhost.Load(context.Background(), w[:n])
		if c != nil {
			_ = c.Close(context.Background())
		}
		if !errors.Is(err, connectorhost.ErrHandshake) {
			t.Fatalf("%d of %d bytes: err = %v, want ErrHandshake", n, len(w), err)
		}
	}
}

const wasmHeader = "\x00asm\x01\x00\x00\x00"

// allocatedBy returns the bytes the heap allocated while f ran.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// wazero's decoder reads one reader over the whole module and sums a body's
// local counts before it checks them against the body's declared size, so locals
// that run past a tiny body must be refused by the scan, not by the decoder
// after it has sized and filled the slice.
func TestLocalsPastTheBodyAreRefused(t *testing.T) {
	const locals = 1 << 26
	content := append([]byte{1, 2, 1}, uleb(locals)...) // one body of 2 bytes: a decl count of 1 and a count byte
	content = append(content, i32, 0x0b)
	w := append([]byte(wasmHeader), section(10, content)...)
	var err error
	alloc := allocatedBy(func() {
		var c *connectorhost.Connector
		if c, err = connectorhost.Load(context.Background(), w); c != nil {
			_ = c.Close(context.Background())
		}
	})
	if !errors.Is(err, connectorhost.ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake", err)
	}
	if alloc > locals/4 {
		t.Fatalf("Load allocated %d bytes sizing %d declared locals", alloc, locals)
	}
}

// Each vector in the binary format takes at least a byte per entry, so a count
// over the section's length is a lie wazero would size a slice from.
func TestVectorCountPastTheSectionIsRefused(t *testing.T) {
	for _, id := range []byte{1, 2, 3, 4, 5, 6, 7, 9, 10, 11} {
		t.Run(fmt.Sprintf("section %d", id), func(t *testing.T) {
			w := append([]byte(wasmHeader), section(id, uleb(declaredEntries))...)
			var err error
			alloc := allocatedBy(func() {
				var c *connectorhost.Connector
				if c, err = connectorhost.Load(context.Background(), w); c != nil {
					_ = c.Close(context.Background())
				}
			})
			if !errors.Is(err, connectorhost.ErrDeclaredSize) || !errors.Is(err, connectorhost.ErrHandshake) {
				t.Fatalf("err = %v, want ErrHandshake wrapping ErrDeclaredSize", err)
			}
			if alloc > 1<<20 {
				t.Fatalf("Load allocated %d bytes for a %d byte module", alloc, len(w))
			}
		})
	}
}

const declaredEntries = 1<<32 - 1

// A count or length nested in an entry is sized from by wazero too: a 16 GiB
// slice for an element segment's vector, 96 GiB for name-section function
// names, either fatal on a host with less memory to overcommit.
func TestNestedLengthsPastTheirBytesAreRefused(t *testing.T) {
	big := uleb(declaredEntries)
	cat := func(bs ...[]byte) []byte { return bytes.Join(bs, nil) }
	nameSection := func(sub byte, content ...[]byte) []byte {
		return section(0, cat([]byte{4}, []byte("name"), []byte{sub}, vec(cat(content...))))
	}
	for _, tc := range []struct {
		name string
		sec  []byte
	}{
		{"parameter types", section(1, cat([]byte{1, 0x60}, big))},
		{"result types", section(1, cat([]byte{1, 0x60, 0}, big))},
		{"import module name", section(2, cat([]byte{1}, big))},
		{"export name", section(7, cat([]byte{1}, big))},
		{"element segment", section(9, cat([]byte{1, 0, 0x41, 0, 0x0b}, big))},
		{"passive data segment", section(11, cat([]byte{1, 1}, big))},
		{"active data segment", section(11, cat([]byte{1, 0, 0x41, 0, 0x0b}, big))},
		{"custom section name", section(0, big)},
		{"function names", nameSection(1, big)},
		{"local names", nameSection(2, []byte{1, 0}, big)},
		{"name too long for its subsection", nameSection(1, []byte{1, 0, 0}, []byte{0})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := append([]byte(wasmHeader), tc.sec...)
			var err error
			alloc := allocatedBy(func() {
				var c *connectorhost.Connector
				if c, err = connectorhost.Load(context.Background(), w); c != nil {
					_ = c.Close(context.Background())
				}
			})
			if !errors.Is(err, connectorhost.ErrHandshake) {
				t.Fatalf("err = %v, want ErrHandshake", err)
			}
			if alloc > 1<<20 {
				t.Fatalf("Load allocated %d bytes for a %d byte module", alloc, len(w))
			}
		})
	}
}
