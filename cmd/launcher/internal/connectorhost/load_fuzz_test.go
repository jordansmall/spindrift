package connectorhost_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
)

// loadSlack is the wall time FuzzLoad grants on top of the init and handshake
// budgets: compile and instantiate have no budget of their own, and the
// multi-MB fixture Connectors compile for seconds on a loaded CI runner.
const loadSlack = 2 * time.Minute

// lyingSeeds are modules that declare more tables, locals, frame slots or
// vector entries than they hold; the pre-scan must refuse each before compile.
func lyingSeeds(tb testing.TB) map[string][]byte {
	tb.Helper()
	const frame, total = connectorhost.MaxFrameSlots, connectorhost.MaxModuleLocals
	const localsPerFn = frame - 2 // within the per-frame cap, so only the module total is over
	many := make([]uint32, total/localsPerFn+1)
	for i := range many {
		many[i] = localsPerFn
	}
	stack := append(append(consts(frame+1), drops(frame+1)...), store(goodHandshake)...)
	stack = append(stack, handshakeAt(0, int64(len(goodHandshake)))...)
	return map[string][]byte{
		"table minimum": withTable(tb, handshakeModule(goodHandshake), connectorhost.MaxTableEntries+1),
		"frame slots":   wasmModule(nil, []byte{i64}, stack, true),
		"frame locals":  withFunctions(tb, handshakeModule(goodHandshake), 1<<32-1),
		"vector count":  append([]byte(wasmHeader), section(10, uleb(declaredEntries))...),
		"module locals": withFunctions(tb, handshakeModule(goodHandshake), many...),
	}
}

func TestLyingSeedsAreRefusedByThePreScan(t *testing.T) {
	for name, w := range lyingSeeds(t) {
		t.Run(name, func(t *testing.T) {
			c, err := connectorhost.Load(context.Background(), w)
			if c != nil {
				_ = c.Close(context.Background())
			}
			if !errors.Is(err, connectorhost.ErrDeclaredSize) {
				t.Fatalf("err = %v, want ErrDeclaredSize", err)
			}
		})
	}
}

// FuzzLoad runs arbitrary module bytes through Load: the declared-size
// pre-scan, compile, instantiate, _initialize and the handshake, on the
// interpreter as production uses it. Every input must load or fail with a
// named error. The seeds are the fixture Connectors in
// SPINDRIFT_CONNECTOR_FIXTURES_DIR (when set) plus hand-built modules that lie
// about their sizes, built at run time rather than committed under
// testdata/fuzz because the fixtures are multi-MB Nix outputs. See
// FuzzHandshake for the -fuzzminimizetime=0 advice on long sessions.
func FuzzLoad(f *testing.F) {
	f.Add(handshakeModule(goodHandshake))
	for _, w := range lyingSeeds(f) {
		f.Add(w)
	}
	if dir := os.Getenv(fixturesEnv); dir != "" {
		paths, err := filepath.Glob(filepath.Join(dir, "*.wasm"))
		if err != nil {
			f.Fatal(err)
		}
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				f.Fatal(err)
			}
			f.Add(b)
		}
	} else {
		f.Logf("%s is unset; fuzzing without the fixture Connector seeds", fixturesEnv)
	}
	f.Fuzz(func(t *testing.T, wasm []byte) {
		done := make(chan error, 1)
		go func() {
			c, err := connectorhost.Load(context.Background(), wasm)
			if c != nil {
				_ = c.Close(context.Background())
			}
			done <- err
		}()
		select {
		case err := <-done:
			// Load wraps every decode, compile and link failure in ErrHandshake,
			// so this mostly holds the host to that contract; the panic and
			// hang checks carry the weight.
			if err != nil && !errors.Is(err, connectorhost.ErrHandshake) && !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
				t.Fatalf("unnamed error: %v", err)
			}
		case <-time.After(connectorhost.InitBudget + connectorhost.CallBudget + loadSlack):
			t.Fatal("Load did not return within its budgets")
		}
	})
}
