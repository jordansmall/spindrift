package bwrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"spindrift.dev/launcher/internal/runner/runnertest"
)

func TestNixGCRootsRoot_IsSiblingOfSnapshotRoot(t *testing.T) {
	got := NixGCRootsRoot("/pwd")
	if want := filepath.Join("/pwd", ".spindrift", "nix-gcroots"); got != want {
		t.Errorf("NixGCRootsRoot = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, NixVarSnapshotRoot("/pwd")+string(filepath.Separator)) || got == NixVarSnapshotRoot("/pwd") {
		t.Errorf("NixGCRootsRoot %q must not live inside the snapshot root: reclaim would read it as a generation", got)
	}
}

// outLinkCalls returns the argv of every recorded `nix build --out-link` call.
func outLinkCalls(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		if slices.Contains(c, "--out-link") {
			out = append(out, c)
		}
	}
	return out
}

// wantOutLinkCalls is the per-path shape registerGenerationRoot must produce:
// one build per store path, each with its own link under genDir.
func wantOutLinkCalls(genDir string, paths ...string) [][]string {
	var want [][]string
	for i, p := range paths {
		want = append(want, []string{"nix", "build", "--out-link", filepath.Join(genDir, "artifact-"+strconv.Itoa(i)), "--", p})
	}
	return want
}

// assertOutLinkHoldsSnapshotLock makes every out-link call probe lockPath with
// a non-blocking exclusive flock, which must fail while the shared lock is
// held. The returned flag reports whether a probe ran.
func assertOutLinkHoldsSnapshotLock(t *testing.T, lockPath string) *bool {
	t.Helper()
	probed := new(bool)
	inner := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if slices.Contains(args, "--out-link") {
			*probed = true
			lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
			if err != nil {
				t.Fatalf("open lock %q: %v", lockPath, err)
			}
			defer lf.Close()
			if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
				_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
				t.Error("exclusive probe succeeded during root registration: the shared snapshot lock was not held")
			}
		}
		return inner(name, args...)
	}
	return probed
}

func stubNix(t *testing.T, failOutLink bool) *[][]string {
	t.Helper()
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var calls [][]string
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if name == "nix" && slices.Contains(args, "--out-link") && failOutLink {
			return exec.Command("sh", "-c", "echo boom >&2; exit 1")
		}
		return exec.Command("true")
	}
	return &calls
}

func rootedAdapter(t *testing.T, generation string) (*bwrapBuildAdapter, string) {
	t.Helper()
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	pwd := t.TempDir()
	return &bwrapBuildAdapter{
		agentFilesDrv:      "/fake/files.drv",
		agentEnvDrv:        "/fake/env.drv",
		passwdFileDrv:      "/fake/passwd.drv",
		groupFileDrv:       "/fake/group.drv",
		nixConfigFileDrv:   "/fake/nix-config.drv",
		syscallFilterDrv:   "/fake/filter.drv",
		agentFiles:         "/nix/store/aaa-agent-files",
		agentEnv:           "/nix/store/bbb-agent-env",
		passwdFile:         "/nix/store/ccc-passwd",
		groupFile:          "/nix/store/ddd-group",
		nixConfigFile:      "/nix/store/eee-nix.conf",
		syscallFilterPath:  "/nix/store/fff-filter.bpf",
		nixVarSnapshotDir:  NixVarSnapshotDir(pwd, generation),
		nixVarSnapshotRoot: NixVarSnapshotRoot(pwd),
		nixVarGeneration:   generation,
		nixGCRootsRoot:     NixGCRootsRoot(pwd),
	}, pwd
}

func TestBwrapBuildEnsureReady_RootsEveryArtifactUnderGenerationDir(t *testing.T) {
	calls := stubNix(t, false)
	a, pwd := rootedAdapter(t, "gen-a")

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady() = %v, want nil", err)
	}

	genDir := filepath.Join(pwd, ".spindrift", "nix-gcroots", "gen-a")
	want := wantOutLinkCalls(genDir,
		"/nix/store/aaa-agent-files", "/nix/store/bbb-agent-env", "/nix/store/eee-nix.conf",
		"/nix/store/ccc-passwd", "/nix/store/ddd-group", "/nix/store/fff-filter.bpf")
	if got := outLinkCalls(*calls); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("out-link calls = %v, want %v", got, want)
	}
	if st, err := os.Stat(genDir); err != nil || !st.IsDir() {
		t.Errorf("generation root dir %q = %v, %v; want an existing dir", genDir, st, err)
	}
}

func TestBwrapBuildEnsureReady_RootRegistrationHoldsSnapshotLock(t *testing.T) {
	a, _ := rootedAdapter(t, "gen-a")
	stubNix(t, false)
	lockPath := SnapshotLockPath(a.nixVarSnapshotDir)
	probed := assertOutLinkHoldsSnapshotLock(t, lockPath)
	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady() = %v, want nil", err)
	}
	if !*probed {
		t.Fatal("out-link call never made; probe never ran")
	}
}

func TestBwrapBuildEnsureReady_RootFailureWarnsAndSucceeds(t *testing.T) {
	stubNix(t, true)
	a, _ := rootedAdapter(t, "gen-a")

	var err error
	out := runnertest.CaptureStdoutDuring(t, func() { err = a.EnsureReady() })

	if err != nil {
		t.Fatalf("EnsureReady() = %v, want nil (a root failure must not fail the build)", err)
	}
	if !strings.Contains(out, "warning") || !strings.Contains(out, "gen-a") {
		t.Errorf("stdout = %q, want a warning naming generation gen-a", out)
	}
}

func TestBwrapBuildEnsureReady_EmptyGenerationRegistersNoRoot(t *testing.T) {
	calls := stubNix(t, false)
	a, pwd := rootedAdapter(t, "")

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady() = %v, want nil", err)
	}
	if got := outLinkCalls(*calls); got != nil {
		t.Errorf("out-link calls %v made for the flat/legacy path, want none", got)
	}
	if _, err := os.Stat(NixGCRootsRoot(pwd)); !os.IsNotExist(err) {
		t.Errorf("os.Stat(gcroots root) = %v, want not-exist", err)
	}
}

func TestRegisterGenerationRoot_SkipsEmptyPathsAndNoopsWithoutPaths(t *testing.T) {
	calls := stubNix(t, false)
	root := filepath.Join(t.TempDir(), "nix-gcroots")

	registerGenerationRoot(root, "gen-a", []string{"", "/nix/store/x", ""})
	want := wantOutLinkCalls(filepath.Join(root, "gen-a"), "/nix/store/x")
	if got := outLinkCalls(*calls); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("out-link calls = %v, want %v (empty paths skipped)", got, want)
	}

	*calls = nil
	registerGenerationRoot(root, "gen-b", []string{"", ""})
	if len(*calls) != 0 {
		t.Errorf("calls = %v, want none when every path is empty", *calls)
	}
}

func TestRegisterGenerationRoot_OneFailedPathDoesNotUnrootTheRest(t *testing.T) {
	calls := stubNix(t, false)
	stub := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if slices.Contains(args, "/nix/store/bad") {
			*calls = append(*calls, append([]string{name}, args...))
			return exec.Command("sh", "-c", "exit 1")
		}
		return stub(name, args...)
	}
	root := filepath.Join(t.TempDir(), "nix-gcroots")

	out := runnertest.CaptureStdoutDuring(t, func() {
		registerGenerationRoot(root, "gen-a", []string{"/nix/store/a", "/nix/store/bad", "/nix/store/c"})
	})

	want := wantOutLinkCalls(filepath.Join(root, "gen-a"), "/nix/store/a", "/nix/store/bad", "/nix/store/c")
	if got := outLinkCalls(*calls); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("out-link calls = %v, want %v (every path attempted after a failure)", got, want)
	}
	if strings.Count(out, "warning") != 1 || !strings.Contains(out, "/nix/store/bad") {
		t.Errorf("stdout = %q, want exactly one warning naming /nix/store/bad", out)
	}
}
