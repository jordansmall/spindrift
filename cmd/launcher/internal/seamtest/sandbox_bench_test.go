//go:build integration

package seamtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/bwrap"
	"spindrift.dev/launcher/internal/runner/oci"
)

// The Connector sandbox benchmarks (issue #7018): the claude Connector's decode
// and load, a sandbox's start, and one http_request Host call, each in the
// Launcher (in-process, the interpreter a Connector sandbox's host also runs)
// and through the Sandbox the Launcher starts, over each runner that can start
// one. They live here because only the runner families and seam tests may import
// a runner family (ADR 0069), and they are not part of the gate:
//
//	SPINDRIFT_DRIVER_CONNECTOR_WASM=<claude.wasm> \
//	  go test -tags integration -run '^$' -bench ConnectorSandbox ./internal/seamtest/
//
// -count repeats each row as one block, in row order, so a host whose load
// drifts during the run charges the drift to the later rows (issue #7074). To
// compare rows, run one row per invocation (-bench .../sandbox-bwrap$ and so
// on), interleaved in a shuffled order, and compare each row against the
// process row from the same round.
//
// The Start and HostCall benchmarks also have a -x2 and -x3 row per sandbox row
// (sandbox-bwrap-x2): that many sandboxes over one runner, all live together,
// as a dispatch has the Drivers' shared sandbox and its backend rows' own. Read
// them against the plain row from the same round.
//
// A sandbox row's ns/line less in-process is the per-call overhead; the
// HostCall row's ns/op less in-process is the per-call and per-Host-call
// overhead together. hop-ns/call is the sandbox's own measure of a call, and
// includes the guest's run inside it, not only the transport. The rows skip
// until their input is named. A runtime row's env var is the operator's opt-in:
// once set, a missing runtime or connector-host, or a sandbox that fails to
// start, fails the benchmark rather than skipping it:
//
//	SPINDRIFT_DRIVER_CONNECTOR_WASM  the compiled claude Connector (Decode, Load, Start)
//	SPINDRIFT_CONNECTOR_FIXTURES_DIR the fixture Connectors (HostCall); else nix builds them
//	SPINDRIFT_CONNECTOR_HOST         connector-host, at <pkg>/bin/connector-host for the bwrap row; else nix builds it
//	SPINDRIFT_BENCH_SANDBOX_IMAGE    the connector-sandbox-image tag, podman load'ed (podman row)
//	SPINDRIFT_BENCH_SECCOMP_FILTER   the compiled BPF filter, lib/seccomp.nix (bwrap row)
//
// Benchmarks run with -run '^$', so the fakes other tests put on PATH
// (UsePodman, UseBwrap) are never installed here: t.Setenv scopes them to their
// test, and a row's LookPath finds only a real runtime.
const (
	benchImageEnv  = "SPINDRIFT_BENCH_SANDBOX_IMAGE"
	benchFilterEnv = "SPINDRIFT_BENCH_SECCOMP_FILTER"
	benchWasmEnv   = "SPINDRIFT_DRIVER_CONNECTOR_WASM"
)

// benchRow is one way to run a Connector.
type benchRow struct {
	name string
	// sandboxes is how many sandboxes the row holds live together.
	sandboxes int
	// newSandbox builds the row's runner once, skipping tb when the row is not
	// opted into, and returns a factory for fresh, unstarted Sandboxes over it;
	// nil for the in-process row.
	newSandbox func(tb testing.TB) func() *sandbox.Sandbox
}

func benchRows() []benchRow {
	return []benchRow{
		{name: "in-process", sandboxes: 1},
		{name: "sandbox-process", sandboxes: 1, newSandbox: newProcessSandbox},
		{name: "sandbox-podman", sandboxes: 1, newSandbox: newPodmanSandbox},
		{name: "sandbox-bwrap", sandboxes: 1, newSandbox: newBwrapSandbox},
	}
}

func sandboxRows() []benchRow {
	var rows []benchRow
	for _, r := range benchRows() {
		if r.newSandbox != nil {
			rows = append(rows, r)
		}
	}
	return rows
}

// multiSandboxRows is a -x2 and a -x3 row per sandbox row, each opted into by
// the same input as its single row.
func multiSandboxRows() []benchRow {
	var rows []benchRow
	for _, n := range []int{2, 3} {
		for _, r := range sandboxRows() {
			r.name = fmt.Sprintf("%s-x%d", r.name, n)
			r.sandboxes = n
			rows = append(rows, r)
		}
	}
	return rows
}

// benchLoader loads a Connector, in the Launcher or through one started
// Sandbox, whose Stats are nil in the Launcher.
type benchLoader struct {
	load        func(context.Context, []byte) (*connectorhost.Connector, error)
	loadNetwork func(context.Context, []byte, connectorhost.Network) (*connectorhost.Connector, error)
	stats       func() (connectorsandbox.Stats, bool)
}

// open returns the loader of a row that holds one sandbox. A sandbox is started
// by its first load, and closed at tb's cleanup.
func (r benchRow) open(tb testing.TB) benchLoader {
	tb.Helper()
	if r.sandboxes != 1 {
		tb.Fatalf("row %s holds %d sandboxes, open wants one", r.name, r.sandboxes)
	}
	return r.openAll(tb)[0]
}

// openAll returns one loader per sandbox the row holds, each closed at tb's
// cleanup.
func (r benchRow) openAll(tb testing.TB) []benchLoader {
	tb.Helper()
	if r.newSandbox == nil {
		return []benchLoader{{load: connectorhost.Load, loadNetwork: connectorhost.LoadNetwork}}
	}
	newSandbox := r.newSandbox(tb)
	var ls []benchLoader
	for range r.sandboxes {
		sb := newSandbox()
		tb.Cleanup(func() { _ = sb.Close(context.Background()) })
		ls = append(ls, benchLoader{load: sb.Load, loadNetwork: sb.LoadNetwork, stats: sb.Stats})
	}
	return ls
}

// sumStats is the loaders' Stats added together, so hop measures a row's
// sandboxes as one; nil in the Launcher. Every loader in a row is the same
// kind, all sandboxed or all in the Launcher, so the first one's stats decide.
func sumStats(ls []benchLoader) func() (connectorsandbox.Stats, bool) {
	if ls[0].stats == nil {
		return nil
	}
	return func() (connectorsandbox.Stats, bool) {
		var sum connectorsandbox.Stats
		for _, l := range ls {
			st, _ := l.stats()
			sum.Start += st.Start
			sum.HopStats = sum.HopStats.Add(st.HopStats)
		}
		return sum, true
	}
}

// hop runs f and reports the hop-ns and Host calls each call paid, from the
// Stats it moved; a no-op around f when stats is nil, the in-process row. The
// load's own exchanges are before f, so they are not a call's.
func hop(b *testing.B, stats func() (connectorsandbox.Stats, bool), f func()) {
	b.Helper()
	if stats == nil {
		f()
		return
	}
	before, _ := stats()
	f()
	after, _ := stats()
	if calls := after.Calls - before.Calls; calls > 0 {
		b.ReportMetric(float64((after.Hop-before.Hop).Nanoseconds())/float64(calls), "hop-ns/call")
		b.ReportMetric(float64(after.HostCalls-before.HostCalls)/float64(calls), "host-calls/call")
	}
}

func benchWasm(b *testing.B) []byte {
	b.Helper()
	path := os.Getenv(benchWasmEnv)
	if path == "" {
		b.Skipf("%s is unset; it names the compiled claude Connector", benchWasmEnv)
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	return wasm
}

// claudeCorpusLines is every non-empty line of the claude Driver's #6798 corpus
// pass logs, the lines BenchmarkConnectorDecode/corpus=6798 decodes.
func claudeCorpusLines(b *testing.B) []string {
	b.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "connectors", "claude", "testdata", "corpus", "*", "pass.jsonl"))
	if err != nil || len(paths) == 0 {
		b.Fatalf("claude corpus pass logs: %v (found %d)", err, len(paths))
	}
	var lines []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			b.Fatal(err)
		}
		for _, l := range bytes.Split(data, []byte("\n")) {
			if len(l) > 0 {
				lines = append(lines, string(l))
			}
		}
	}
	return lines
}

// BenchmarkConnectorSandboxDecode is the per-line decode cost: a fresh decoder
// over every corpus line, through the claude Connector under each row.
func BenchmarkConnectorSandboxDecode(b *testing.B) {
	wasm := benchWasm(b)
	lines := claudeCorpusLines(b)
	for _, r := range benchRows() {
		b.Run(r.name, func(b *testing.B) {
			l := r.open(b)
			c, err := l.load(context.Background(), wasm)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = c.Close(context.Background()) })
			// Cleanups run last-in first-out, so this one sees the Connector
			// before Close. A failed call kills it and every later line degrades
			// to unparsed, which is fast: a dead run would read as a speedup.
			b.Cleanup(func() {
				if err := c.Err(); err != nil {
					b.Errorf("Connector died during the run: %v", err)
				}
			})
			s, err := c.DriverStrategy()
			if err != nil {
				b.Fatal(err)
			}
			hop(b, l.stats, func() {
				for b.Loop() {
					decode := s.NewDecoder()
					for _, line := range lines {
						decode(line)
					}
				}
			})
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(lines)), "ns/line")
		})
	}
}

// BenchmarkConnectorSandboxLoad is the load through a sandbox already started:
// an untimed first load starts it, and BenchmarkConnectorSandboxStart times that.
func BenchmarkConnectorSandboxLoad(b *testing.B) {
	wasm := benchWasm(b)
	for _, r := range benchRows() {
		b.Run(r.name, func(b *testing.B) {
			l := r.open(b)
			once := func() {
				c, err := l.load(context.Background(), wasm)
				if err != nil {
					b.Fatal(err)
				}
				_ = c.Close(context.Background())
			}
			if l.stats != nil {
				once()
			}
			for b.Loop() {
				once()
			}
		})
	}
}

// BenchmarkConnectorSandboxStart is a sandbox's start: a fresh sandbox per
// iteration over one runner, started by its first load, then closed. start-ns is the start
// alone (runner start to the host's hello); ns/op adds the first load and the
// teardown. An -xN row starts N such sandboxes one after another and holds them
// all live before closing them, and its start-ns is the sum of their starts.
func BenchmarkConnectorSandboxStart(b *testing.B) {
	wasm := benchWasm(b)
	for _, r := range append(sandboxRows(), multiSandboxRows()...) {
		b.Run(r.name, func(b *testing.B) {
			newSandbox := r.newSandbox(b)
			var start time.Duration
			for b.Loop() {
				var sbs []*sandbox.Sandbox
				var cs []*connectorhost.Connector
				closeAll := func() {
					for _, c := range cs {
						_ = c.Close(context.Background())
					}
					for _, sb := range sbs {
						_ = sb.Close(context.Background())
					}
				}
				for range r.sandboxes {
					sb := newSandbox()
					sbs = append(sbs, sb)
					c, err := sb.Load(context.Background(), wasm)
					if err != nil {
						closeAll()
						b.Fatal(err)
					}
					cs = append(cs, c)
					if st, ok := sb.Stats(); ok {
						start += st.Start
					}
				}
				closeAll()
			}
			b.ReportMetric(float64(start.Nanoseconds())/float64(b.N), "start-ns")
		})
	}
}

// BenchmarkConnectorSandboxHostCall is one Connector call that makes one
// http_request Host call against a local backend. An -xN row loads the Connector
// into N sandboxes and rotates the calls across them, so ns/op stays one call
// and hop-ns/call and host-calls/call are over every sandbox's calls.
func BenchmarkConnectorSandboxHostCall(b *testing.B) {
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		b.Skipf("no fixture Connectors: %v", err)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "network.wasm"))
	if err != nil {
		b.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "scripted "+r.URL.Path)
	}))
	b.Cleanup(srv.Close)
	network := connectorhost.Network{
		Name:   "fixture-network",
		Routes: []connectorhost.Route{{Method: "GET", Host: srv.Listener.Addr().String(), Path: "/repos"}},
		Authorize: func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer s3cret")
			return nil
		},
		Client:        srv.Client(),
		PublicForTest: connectorhost.LoopbackForTest,
		Audit:         connectorhost.NewAuditLog(io.Discard),
	}
	params := map[string]string{"url": srv.URL + "/repos/x", "method": "GET"}
	for _, r := range append(benchRows(), multiSandboxRows()...) {
		b.Run(r.name, func(b *testing.B) {
			ls := r.openAll(b)
			var cs []*connectorhost.Connector
			for _, l := range ls {
				c, err := l.loadNetwork(context.Background(), wasm, network)
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = c.Close(context.Background()) })
				cs = append(cs, c)
			}
			var out struct {
				Status int `json:"status"`
			}
			hop(b, sumStats(ls), func() {
				for i := 0; b.Loop(); i++ {
					if err := cs[i%len(cs)].Call(context.Background(), "fetch", params, &out); err != nil {
						b.Fatal(err)
					}
				}
			})
			if out.Status != 200 {
				b.Fatalf("fetched status %d, want the backend's answer", out.Status)
			}
		})
	}
}

func noEnv(string) (string, bool) { return "", false }

func newPodmanSandbox(tb testing.TB) func() *sandbox.Sandbox {
	tb.Helper()
	tag := os.Getenv(benchImageEnv)
	if tag == "" {
		tb.Skipf("%s is unset; it names a loaded Connector sandbox image (the connector-sandbox-image flake package, podman load'ed)", benchImageEnv)
	}
	if _, err := exec.LookPath("podman"); err != nil {
		tb.Fatalf("%s is set but podman is not on PATH", benchImageEnv)
	}
	art := inputdoc.OCIArtifacts{
		ImageTag:                 inputdoc.ValueOf(tag),
		ConnectorSandboxImageTag: inputdoc.ValueOf(tag),
	}
	r, err := oci.New(runner.Config{Runtime: "podman"}, art, noEnv, "")
	if err != nil {
		tb.Fatal(err)
	}
	return func() *sandbox.Sandbox { return sandbox.New(r, "podman") }
}

func newBwrapSandbox(tb testing.TB) func() *sandbox.Sandbox {
	tb.Helper()
	filter := os.Getenv(benchFilterEnv)
	if filter == "" {
		tb.Skipf("%s is unset; it names the compiled BPF syscall filter (lib/seccomp.nix)", benchFilterEnv)
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		tb.Fatalf("%s is set but bwrap is not on PATH", benchFilterEnv)
	}
	bin, err := fixturebuild.ConnectorHost()
	if err != nil {
		tb.Fatalf("no connector-host: %v", err)
	}
	pkg, err := connectorHostPackage(bin)
	if err != nil {
		tb.Fatal(err)
	}
	art := inputdoc.BwrapArtifacts{
		ConnectorHost: inputdoc.ValueOf(pkg),
		SyscallFilter: inputdoc.ValueOf(filter),
	}
	r, err := bwrap.New(runner.Config{NetworkMode: runner.NetworkModeHost}, art, noEnv, tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	return func() *sandbox.Sandbox { return sandbox.New(r, "bwrap") }
}

// connectorHostPackage is the package directory the bwrap sandbox binds
// read-only: it execs <dir>/bin/connector-host, so a binary anywhere else would
// bind the wrong directory and fail at start.
func connectorHostPackage(bin string) (string, error) {
	binDir := filepath.Dir(bin)
	if filepath.Base(bin) != "connector-host" || filepath.Base(binDir) != "bin" {
		return "", fmt.Errorf("connector-host %q is not at <package>/bin/connector-host, the layout the bwrap sandbox binds", bin)
	}
	return filepath.Dir(binDir), nil
}

func TestConnectorHostPackage(t *testing.T) {
	for _, tc := range []struct {
		name, bin, want string
		wantErr         bool
	}{
		{name: "package layout", bin: "/nix/store/abc-connector-host/bin/connector-host", want: "/nix/store/abc-connector-host"},
		{name: "bare binary", bin: "/tmp/build/connector-host", wantErr: true},
		{name: "wrong basename", bin: "/nix/store/abc-connector-host/bin/other", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := connectorHostPackage(tc.bin)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("connectorHostPackage(%q) = %q, %v; want %q, error %v", tc.bin, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func newProcessSandbox(tb testing.TB) func() *sandbox.Sandbox {
	tb.Helper()
	if _, err := fixturebuild.ConnectorHost(); err != nil {
		tb.Fatalf("no connector-host: %v", err)
	}
	p := &processStarter{cmds: map[string]*exec.Cmd{}}
	return func() *sandbox.Sandbox { return sandbox.New(p, "process") }
}

// processStarter runs connector-host as a plain child over pipes: the cost of
// the framing and the pipe hop with no isolation, which bwrap's pass-through
// stdio matches. It is the baseline the isolating rows are read against.
type processStarter struct {
	mu   sync.Mutex
	cmds map[string]*exec.Cmd
}

func (p *processStarter) StartConnectorSandbox(name string) (runner.ConnectorSandbox, error) {
	var stderr lockedBuffer
	cmd, err := fixturebuild.NewConnectorHostCmd(&stderr)
	if err != nil {
		return runner.ConnectorSandbox{}, err
	}
	// os.Pipe, not StdinPipe/StdoutPipe: Wait must not close the reader a later
	// read of the final frames still holds.
	inR, inW, err := os.Pipe()
	if err != nil {
		return runner.ConnectorSandbox{}, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return runner.ConnectorSandbox{}, errors.Join(err, inR.Close(), inW.Close())
	}
	cmd.Stdin, cmd.Stdout = inR, outW
	err = cmd.Start()
	_ = inR.Close()
	_ = outW.Close()
	if err != nil {
		return runner.ConnectorSandbox{}, errors.Join(err, inW.Close(), outR.Close())
	}
	p.mu.Lock()
	p.cmds[name] = cmd
	p.mu.Unlock()
	return runner.ConnectorSandbox{
		Stdin:  inW,
		Stdout: outR,
		Wait:   sync.OnceValue(cmd.Wait),
		Stderr: stderr.String,
	}, nil
}

func (p *processStarter) Kill(name string) error {
	p.mu.Lock()
	cmd := p.cmds[name]
	delete(p.cmds, name)
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	return nil
}

// lockedBuffer is the child's stderr: written by exec's copier, read by the
// Sandbox naming a start failure.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
