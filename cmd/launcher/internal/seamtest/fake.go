package seamtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakeConfigExit is the status a fake exits with when its config cannot be
// loaded; distinct from anything a configured tool status would normally be.
const fakeConfigExit = 97

// fakes maps a tool name to its fake. The test binary re-execs as a fake when
// invoked under one of these names.
var fakes = map[string]func(args []string) int{
	"gh":     ghMain,
	"podman": podmanMain,
	"docker": podmanMain,
	"bwrap":  bwrapMain,
	// claude and orchestrator stand in for the Driver CLI and the in-box
	// orchestrator when a seam test runs the box binary.
	"claude":       driverMain,
	"orchestrator": orchestratorMain,
}

// Main is the TestMain hook: invoked under a registered tool name the test
// binary runs that fake and exits with its status, before any test starts;
// otherwise it runs the tests.
//
//	func TestMain(m *testing.M) { seamtest.Main(m) }
func Main(m *testing.M) {
	os.Exit(dispatch(filepath.Base(os.Args[0]), os.Args[1:], m.Run))
}

func dispatch(name string, args []string, runTests func() int) int {
	if fake, ok := fakes[name]; ok {
		return fake(args)
	}
	return runTests()
}

// InstallFakes symlinks the running test binary under each tool name in a
// fresh temp dir and returns it, for Cmd.PathDirs.
func InstallFakes(tb testing.TB, names ...string) string {
	tb.Helper()
	exe, err := os.Executable()
	if err != nil {
		tb.Fatalf("seamtest: %v", err)
	}
	dir := tb.TempDir()
	for _, n := range names {
		if _, ok := fakes[n]; !ok {
			tb.Fatalf("seamtest: no fake registered for %q", n)
		}
		if err := os.Symlink(exe, filepath.Join(dir, n)); err != nil {
			tb.Fatalf("seamtest: %v", err)
		}
	}
	return dir
}

func configEnv(tool string) string {
	return "SEAMTEST_FAKE_" + strings.ToUpper(tool) + "_CONFIG"
}

// WriteFakeConfig writes cfg as JSON to a temp file and returns the env
// overlay (for Cmd.Env) that points tool's fake at it.
func WriteFakeConfig(tb testing.TB, tool string, cfg any) map[string]string {
	tb.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		tb.Fatalf("seamtest: %v", err)
	}
	path := filepath.Join(tb.TempDir(), tool+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		tb.Fatalf("seamtest: %v", err)
	}
	return map[string]string{configEnv(tool): path}
}

// WriteFakeConfigFile writes cfg where loadConfig's working-directory
// fallback finds it, for fakes the launcher runs with a scrubbed env.
func WriteFakeConfigFile(tb testing.TB, dir, tool string, cfg any) {
	tb.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		tb.Fatalf("seamtest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFile(tool)), b, 0o644); err != nil {
		tb.Fatalf("seamtest: %v", err)
	}
}

func configFile(tool string) string { return ".seamtest-" + tool + ".json" }

// loadConfig reads the JSON file named by tool's config env var into cfg.
// With the var unset it reads ./.seamtest-<tool>.json instead: the launcher
// runs bwrap with an allowlisted env that drops the var but keeps its cwd.
func loadConfig(tool string, cfg any) error {
	env := configEnv(tool)
	path := os.Getenv(env)
	if path == "" {
		path = configFile(tool)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// appendRecord appends argv to path as one JSON line and returns the records
// that were there before it. The read and the single O_APPEND write sit under
// one flock, so concurrent fakes (MAX_PARALLEL boxes) neither interleave
// lines nor see the same prior count.
func appendRecord(path string, argv []string) ([][]string, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	prior, err := parseRecords(b)
	if err != nil {
		return nil, err
	}
	line, err := json.Marshal(argv)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	return prior, nil
}

func parseRecords(b []byte) ([][]string, error) {
	var out [][]string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var argv []string
		if err := json.Unmarshal(sc.Bytes(), &argv); err != nil {
			return nil, fmt.Errorf("bad record %q: %w", sc.Text(), err)
		}
		out = append(out, argv)
	}
	return out, sc.Err()
}

// ReadRecord returns every argv a fake appended to path, in order. A missing
// file means no invocations.
func ReadRecord(tb testing.TB, path string) [][]string {
	tb.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		tb.Fatalf("seamtest: %v", err)
	}
	recs, err := parseRecords(b)
	if err != nil {
		tb.Fatalf("seamtest: %v", err)
	}
	return recs
}
