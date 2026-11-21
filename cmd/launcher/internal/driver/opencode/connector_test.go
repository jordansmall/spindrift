package opencode_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/golden"
)

const (
	connectorWasmEnv = "SPINDRIFT_DRIVER_CONNECTOR_WASM"
	connectorNameEnv = "SPINDRIFT_DRIVER_CONNECTOR_NAME"
)

// Sizes are set against connectorhost's maxRequest (8 MiB), the most one
// resolveExit call carries: the host sends the Connector only the newest lines
// that fit.
const (
	// overRequest exceeds maxRequest, so the window drops the oldest filler.
	overRequest = 9 << 20
	// midLine is between native logscan's 4 MiB line buffer and the host's own
	// oversize skip (~8 MiB): the host forwards such a line, so the Connector must
	// skip it as native does.
	midLine = 4<<20 + 1<<10
	// fillerLine is a filler line small enough for the window to keep or drop whole.
	fillerLine = 1 << 20
)

// Exit resolution through the compiled Connector port (connectors/opencode) must
// agree with the native Strategy on every corpus log, including logs larger than
// the host's per-request limit and a line native skips but the host forwards.
func TestConnectorResolveExitMatchesNative(t *testing.T) {
	path := os.Getenv(connectorWasmEnv)
	if path == "" {
		t.Skipf("%s is unset; it names the compiled opencode Connector", connectorWasmEnv)
	}
	if name := os.Getenv(connectorNameEnv); name != "" && name != "opencode" {
		t.Skipf("%s=%s: the wasm is not the opencode Connector", connectorNameEnv, name)
	}
	if golden.Update() {
		t.Fatalf("refusing to run against the Connector with UPDATE_GOLDENS set; unset it or %s", connectorWasmEnv)
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := connectorhost.LoadCompiled(context.Background(), wasm, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	conn, native := driverkit.New(s), driverkit.New(opencode.Strategy)

	logs, err := filepath.Glob(filepath.Join("testdata", "corpus", "*", "pass.jsonl"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no corpus log: %v", err)
	}
	// Not JSON, so neutral to every resolver: neither an error nor progress.
	line := func(n int) []byte { return append(bytes.Repeat([]byte("x"), n-1), '\n') }
	var filler []byte
	for len(filler) < overRequest {
		filler = append(filler, line(fillerLine)...)
	}
	var midError []byte
	midError = append(midError, `{"type":"error","error":"`...)
	midError = append(midError, bytes.Repeat([]byte("x"), midLine)...)
	midError = append(midError, "\"}\n"...)

	// resolve writes log to a file and resolves it with exit code 0.
	resolve := func(t *testing.T, d driverkit.Driver, log []byte) int {
		t.Helper()
		p := filepath.Join(t.TempDir(), "pass.log")
		if err := os.WriteFile(p, log, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := d.ResolveExit(p, 0)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// The wasm costs seconds per MiB of request, so only the cheap variants run on
	// every log; the ones that send ~8 MiB run on one log of each outcome.
	var quiet, loud []byte
	for _, p := range logs {
		corpus, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(filepath.Dir(p)), func(t *testing.T) {
			agree := func(variant string, log []byte) (want int) {
				t.Helper()
				want = resolve(t, native, log)
				if got := resolve(t, conn, log); got != want {
					t.Errorf("%s: ResolveExit(0) = %d through the Connector, native %d", variant, got, want)
				}
				return want
			}
			natural := agree("natural", corpus)
			// One line over maxRequest: the host skips it whole and sends the corpus log.
			agree("oversized-prefix", append(line(overRequest), corpus...))
			switch want := natural; {
			case want == 0 && quiet == nil:
				quiet = corpus
			case want != 0 && loud == nil:
				loud = corpus
			}
		})
	}
	if quiet == nil || loud == nil {
		t.Fatalf("corpus lacks a log native resolves to 0 (%v) or non-zero (%v)", quiet != nil, loud != nil)
	}
	// The window drops the oldest filler; a lost corpus log would resolve differently.
	// Agreement is bounded: an unrecovered error older than the host's ~8 MiB
	// window followed by >8 MiB of non-progress output resolves 0 here but 1
	// natively, a limit of the host sending only the tail (connectorhost logWindow).
	t.Run("windowed", func(t *testing.T) {
		log := append(append([]byte{}, filler...), loud...)
		if want, got := resolve(t, native, log), resolve(t, conn, log); got != want {
			t.Errorf("ResolveExit(0) = %d through the Connector, native %d", got, want)
		}
	})
	// The error line is newest, so counting it would flip the quiet log to 1.
	t.Run("windowed-mid-size-error", func(t *testing.T) {
		log := append(append(append([]byte{}, filler[:5*fillerLine]...), quiet...), midError...)
		if want, got := resolve(t, native, log), resolve(t, conn, log); got != want {
			t.Errorf("ResolveExit(0) = %d through the Connector, native %d", got, want)
		}
	})
}
