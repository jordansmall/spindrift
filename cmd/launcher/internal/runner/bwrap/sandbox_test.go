package bwrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
	"spindrift.dev/launcher/internal/runner/runnertest/contract"
)

const sandboxConnectorHost = "/nix/store/aaa-connector-host"

func sandboxAdapter(t *testing.T) *bwrapAdapter {
	t.Helper()
	art := inputdoc.BwrapArtifacts{
		ImageTag:         inputdoc.ValueOf("spindrift:test"),
		ConnectorHost:    inputdoc.ValueOf(sandboxConnectorHost),
		ConnectorHostDrv: inputdoc.ValueOf("/nix/store/aaa-connector-host.drv"),
	}
	r, err := New(runner.Config{}, art, runnertest.Lookup(nil), t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r.(*bwrapAdapter)
}

func TestSandboxRenderingContract(t *testing.T) {
	contract.BwrapSandboxRender(t, sandboxAdapter(t).buildSandboxArgs(), sandboxConnectorHost)
}

// hostModeArg makes the test binary serve the Connector host protocol instead
// of running tests (see TestMain), standing in for connector-host inside bwrap.
// An argument, not an env var, because the sandbox's bwrap gets no environment.
const hostModeArg = "-spindrift-test-connector-host"

func serveHostMode() {
	if err := connectorhost.ServeSessions(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "connector-host:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// startableSandbox is an adapter that can start a sandbox: a connector-host
// dir and a syscall filter on disk, with execCommand and the cgroup tree
// stubbed so child stands in for bwrap. It returns the argv execCommand saw.
func startableSandbox(t *testing.T, child func(args []string) *exec.Cmd) (*bwrapAdapter, *[][]string) {
	t.Helper()
	var calls [][]string
	stubCgroupSeams(t, func() (string, error) { return "", nil }, func(_ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		return child(args)
	})
	a := sandboxAdapter(t)
	a.connectorHost = t.TempDir()
	a.syscallFilterPath = filepath.Join(t.TempDir(), "filter.bpf")
	if err := os.WriteFile(a.syscallFilterPath, []byte("filter"), 0o644); err != nil {
		t.Fatal(err)
	}
	return a, &calls
}

type bwrapSandboxHarness struct{}

func (bwrapSandboxHarness) Sandbox(t *testing.T, name string) runner.Runner {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := startableSandbox(t, func([]string) *exec.Cmd { return exec.Command(self, hostModeArg) })
	t.Cleanup(func() { _ = a.Kill(name) })
	return a
}

// Fresh is a second adapter over the same cgroup tree: it tracks no process.
func (bwrapSandboxHarness) Fresh(t *testing.T) runner.Runner { return sandboxAdapter(t) }

func TestBwrap_ConnectorSandboxOrphan(t *testing.T) {
	contract.SandboxOrphan(t, bwrapSandboxHarness{})
}

func TestBwrap_ConnectorSandboxLifecycle(t *testing.T) {
	contract.SandboxLifecycle(t, bwrapSandboxHarness{})
}

func TestStartConnectorSandbox_FailsClosedWithoutIsolation(t *testing.T) {
	name := runner.ConnectorSandboxPrefix + "closed"
	for label, mutate := range map[string]func(a *bwrapAdapter){
		"no connector-host in the document": func(a *bwrapAdapter) { a.connectorHost = "" },
		"host absent and no drv": func(a *bwrapAdapter) {
			a.connectorHost = filepath.Join(a.connectorHost, "absent")
			a.connectorHostDrv = ""
		},
		"no syscall filter":         func(a *bwrapAdapter) { a.syscallFilterPath = "" },
		"unopenable syscall filter": func(a *bwrapAdapter) { a.syscallFilterPath += ".missing" },
	} {
		t.Run(label, func(t *testing.T) {
			a, calls := startableSandbox(t, func([]string) *exec.Cmd { return exec.Command("true") })
			mutate(a)
			if _, err := a.StartConnectorSandbox(name); err == nil {
				t.Fatal("StartConnectorSandbox = nil error, want it to fail closed")
			}
			if len(*calls) != 0 {
				t.Errorf("launched %v although it cannot isolate", *calls)
			}
			if a.tracked(name) {
				t.Error("failed start left the name tracked")
			}
		})
	}
}

func TestStartConnectorSandbox_ArgvAndFilterFD(t *testing.T) {
	var cmd *exec.Cmd
	a, calls := startableSandbox(t, func([]string) *exec.Cmd {
		cmd = exec.Command("cat")
		return cmd
	})
	// A Box's pasta egress must never reach the sandbox.
	a.network = runner.BwrapNetwork{Isolate: true, Pasta: true}
	sb, err := a.StartConnectorSandbox(runner.ConnectorSandboxPrefix + "argv")
	if err != nil {
		t.Fatalf("StartConnectorSandbox: %v", err)
	}
	_ = sb.Stdin.Close()
	if err := sb.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// A tmp dir is not cgroupfs, so CLONE_INTO_CGROUP is refused and Start retries.
	if len(*calls) == 0 || slices.ContainsFunc(*calls, func(c []string) bool { return !slices.Equal(c, a.buildSandboxArgs()) }) {
		t.Fatalf("execCommand calls = %v, want only bwrap calls with buildSandboxArgs", *calls)
	}
	if v := runner.BwrapSandboxViolations((*calls)[0], a.connectorHost); len(v) != 0 {
		t.Errorf("argv violates the sandbox policy: %v", v)
	}
	if len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0].Name() != a.syscallFilterPath {
		t.Errorf("ExtraFiles = %v, want the syscall filter on fd 3", cmd.ExtraFiles)
	}
	if len(cmd.Env) != 0 || cmd.Env == nil {
		t.Errorf("Env = %v, want an empty, non-nil environment", cmd.Env)
	}
}

// A denied user namespace makes bwrap exit at once: the caller must see EOF on
// stdout and bwrap's message, never a hang.
func TestStartConnectorSandbox_ImmediateExitSurfacesStderr(t *testing.T) {
	const msg = "bwrap: No permissions to creating new namespace"
	a, _ := startableSandbox(t, func([]string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo '"+msg+"' >&2; exit 1")
	})
	sb, err := a.StartConnectorSandbox(runner.ConnectorSandboxPrefix + "denied")
	if err != nil {
		t.Fatalf("StartConnectorSandbox: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(sb.Stdout); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("reading stdout: %v, want a clean EOF", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stdout did not reach EOF after bwrap exited")
	}
	if err := sb.Wait(); err == nil {
		t.Error("Wait = nil, want bwrap's exit status")
	}
	if got := sb.Stderr(); !strings.Contains(got, msg) {
		t.Errorf("Stderr = %q, want it to carry %q", got, msg)
	}
	if a.tracked(runner.ConnectorSandboxPrefix + "denied") {
		t.Error("exited sandbox still tracked")
	}
}

// An absent connector-host is realized from its drv first, and a failed build
// starts nothing.
func TestStartConnectorSandbox_RealizesAbsentHost(t *testing.T) {
	a, calls := startableSandbox(t, func([]string) *exec.Cmd { return exec.Command("false") })
	a.connectorHost = filepath.Join(a.connectorHost, "absent")
	a.connectorHostDrv = "/nix/store/aaa-connector-host.drv"
	if _, err := a.StartConnectorSandbox(runner.ConnectorSandboxPrefix + "realize"); err == nil {
		t.Fatal("StartConnectorSandbox = nil error, want the failed build")
	}
	want := []string{"build", a.connectorHostDrv + "^*", "--no-link"}
	if len(*calls) == 0 || slices.ContainsFunc(*calls, func(c []string) bool { return !slices.Equal(c, want) }) {
		t.Errorf("execCommand calls = %v, want only %v", *calls, want)
	}
}

// The sandbox's cgroup carries the fixed Connector-sandbox caps, never the
// Box's PIDS_LIMIT / MEMORY_LIMIT.
func TestStartConnectorSandbox_CgroupUsesFixedCaps(t *testing.T) {
	a, _ := startableSandbox(t, func([]string) *exec.Cmd { return exec.Command("cat") })
	a.pidsLimit, a.memoryLimit = "9999", "64g"
	name := runner.ConnectorSandboxPrefix + "caps"
	sb, err := a.StartConnectorSandbox(name)
	if err != nil {
		t.Fatalf("StartConnectorSandbox: %v", err)
	}
	// Wait removes the cgroup dir, so read the limits while it is live.
	dir := filepath.Join(cgroupFSRoot, "spindrift-"+name)
	pids, _ := os.ReadFile(filepath.Join(dir, "pids.max"))
	mem, _ := os.ReadFile(filepath.Join(dir, "memory.max"))
	_ = sb.Stdin.Close()
	if err := sb.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if string(pids) != "128" {
		t.Errorf("pids.max = %q, want 128", pids)
	}
	if string(mem) != "536870912" {
		t.Errorf("memory.max = %q, want 536870912 (512 MiB)", mem)
	}
}

// An empty leftover dir under an earlier anchor must not hide the live cgroup
// of the same name, or Kill would signal nothing and the orphan reap would
// report a sandbox it never stopped.
func TestKillCgroup_TargetsLiveDirNotEmptyLeftover(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()
	stale := filepath.Join(cgroupFSRoot, "a-anchor", "spindrift-orphan")
	live := filepath.Join(cgroupFSRoot, "b-anchor", "spindrift-orphan")
	for _, dir := range []string{stale, live} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// cgroup.kill must pre-exist: killCgroup opens it without O_CREATE.
		if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(live, "cgroup.procs"), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := (&bwrapAdapter{}).Kill("orphan"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(live, "cgroup.kill")); string(got) != "1" {
		t.Errorf("live cgroup.kill = %q, want 1", got)
	}
	if got, _ := os.ReadFile(filepath.Join(stale, "cgroup.kill")); len(got) != 0 {
		t.Errorf("empty leftover cgroup.kill = %q, want untouched", got)
	}
}

// A Box a previous Launcher started has no tracked process, so Kill must reach
// it through its cgroup, as the OCI runners kill by name.
func TestKill_UntrackedBoxNameKillsItsCgroup(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()
	dir := filepath.Join(cgroupFSRoot, "anchor", "spindrift-agent-issue-42")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// cgroup.kill must pre-exist: killCgroup opens it without O_CREATE.
	for file, body := range map[string]string{"cgroup.kill": "", "cgroup.procs": "4242\n"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := (&bwrapAdapter{}).Kill("agent-issue-42"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "cgroup.kill")); string(got) != "1" {
		t.Errorf("cgroup.kill = %q, want 1", got)
	}
}
