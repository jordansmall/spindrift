package connectorhost_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	vec := func(b []byte) []byte { return append([]byte{byte(len(b))}, b...) }
	section := func(id byte, content []byte) []byte {
		return append([]byte{id, byte(len(content))}, content...)
	}
	export := func(name string, kind, idx byte) []byte {
		return append(append([]byte{byte(len(name))}, name...), kind, idx)
	}
	exports := append([]byte{1}, export("connector_handshake", 0, byte(len(imports)))...)
	if withMem {
		exports[0] = 2
		exports = append(exports, export("memory", 2, 0)...)
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
		m = append(m, section(5, []byte{1, 0, 1})...)
	}
	m = append(m, section(7, exports)...)
	return append(m, section(10, append([]byte{1}, vec(code)...))...)
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
	handshake := func(ptr, n int64) []byte { return i64Const(ptr<<32 | n) }
	const page = 0x10000
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
	loop := []byte{0x03, 0x40, 0x0c, 0x00, 0x0b} // loop; br 0; end
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
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Load took %v past a 100ms deadline", d)
	}
}
