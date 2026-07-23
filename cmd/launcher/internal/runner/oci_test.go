package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCall scripts one invocation of a fake CLI binary: the exit code it
// returns and the stdout it prints.
type fakeCall struct {
	exit   int
	stdout string
}

// newFakeCLI writes a stub runtime binary that records each invocation's
// argv to call-NN.txt (zero-indexed, mirroring prependFakeGH's convention in
// internal/forge/exec_test.go) inside a temp dir, and exits/prints per the
// scripted calls in order. Once the number of invocations exceeds len(calls),
// the last scripted call repeats. Returns the script path (for assignment to
// ociAdapter.cli) and the dir (for reading back recorded calls).
func newFakeCLI(t *testing.T, calls ...fakeCall) (script, dir string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("newFakeCLI: at least one scripted call required")
	}
	dir = t.TempDir()
	script = filepath.Join(dir, "fake-cli")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "n=$(ls %q/call-*.txt 2>/dev/null | wc -l)\n", dir)
	fmt.Fprintf(&b, "printf '%%s\\n' \"$@\" > %q/call-$(printf '%%02d' $n).txt\n", dir)
	b.WriteString("case $n in\n")
	for i, c := range calls {
		pattern := fmt.Sprintf("%d", i)
		if i == len(calls)-1 {
			pattern += "|*"
		}
		fmt.Fprintf(&b, "%s) printf '%%s' %q; exit %d ;;\n", pattern, c.stdout, c.exit)
	}
	b.WriteString("esac\n")

	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

// readCall returns the argv (split on newline) recorded for the n-th
// (zero-indexed) invocation of a fake CLI built by newFakeCLI.
func readCall(t *testing.T, dir string, n int) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("call-%02d.txt", n)))
	if err != nil {
		t.Fatalf("call-%02d.txt not written: %v", n, err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// callCount returns the number of invocations recorded for a fake CLI built
// by newFakeCLI.
func callCount(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// TestEnsureReady_ImagePresentPrintsMessage verifies that EnsureReady emits
// the "image present — no rebuild needed" line when the image is already loaded,
// so every loop iteration records whether a rebuild was required.
func TestEnsureReady_ImagePresentPrintsMessage(t *testing.T) {
	// Fake CLI: exits 0 for any invocation (simulates "image inspect" success).
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-podman")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

	// Capture os.Stdout — EnsureReady uses fmt.Printf which writes there.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	ensureErr := a.EnsureReady()

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if ensureErr != nil {
		t.Fatalf("EnsureReady: %v", ensureErr)
	}
	out := buf.String()
	if !strings.Contains(out, "present") {
		t.Errorf("expected 'present' in EnsureReady output when image loaded; got: %q", out)
	}
	if !strings.Contains(out, "no rebuild needed") {
		t.Errorf("expected 'no rebuild needed' in EnsureReady output; got: %q", out)
	}
}

func TestReapOrphanedRebaseDirs_RemovesStaleAndKeepsOthers(t *testing.T) {
	root := t.TempDir()
	stale := []string{
		filepath.Join(root, "spindrift-rebase-abc123"),
		filepath.Join(root, "spindrift-rebase-def456"),
	}
	for _, d := range stale {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(root, "not-a-rebase-dir")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	reapOrphanedRebaseDirs(root)

	for _, d := range stale {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("expected stale dir %s to be removed", filepath.Base(d))
		}
	}
	if _, err := os.Stat(other); os.IsNotExist(err) {
		t.Errorf("expected non-rebase dir %s to be kept", filepath.Base(other))
	}
}

func TestReapOrphanedRebaseDirs_NoopOnMissingRoot(t *testing.T) {
	// Should not panic when root does not exist.
	reapOrphanedRebaseDirs("/tmp/spindrift-test-nonexistent-root-xyz")
}

func TestIsDigestPinned(t *testing.T) {
	tests := []struct {
		image string
		want  bool
	}{
		{"docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8", true},
		{"docker.io/nixos/nix:latest", false},
		{"docker.io/nixos/nix:2.24.9", false},
		{"nixos/nix@sha256:abc123", true},
		{"", false},
	}
	for _, tc := range tests {
		if got := isDigestPinned(tc.image); got != tc.want {
			t.Errorf("isDigestPinned(%q) = %v, want %v", tc.image, got, tc.want)
		}
	}
}

func TestIsNoBuilderError(t *testing.T) {
	tests := []struct {
		stderr string
		want   bool
	}{
		{"error: a Linux system is required to build a Linux derivation", true},
		{"error: no build machines available", true},
		{"error: attribute 'nonexistent' missing", false},
		{"", false},
		{`error: Cannot build '/nix/store/y56hw02v3fqnirf98aabalgvparlcasr-spindrift-base.json.drv'.
       Reason: platform mismatch
       Required system: 'aarch64-linux'
       Current system: 'aarch64-darwin'`, true},
	}
	for _, tc := range tests {
		if got := isNoBuilderError(tc.stderr); got != tc.want {
			t.Errorf("isNoBuilderError(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}

func TestBuildRunArgsIncludesHardeningFlags(t *testing.T) {
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		pidsLimit:   "512",
		memoryLimit: "4g",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{"ISSUE_NUMBER": "1"}}
	args := a.buildRunArgs(box)

	for _, flag := range []string{
		"--cap-drop=all",
		"--security-opt=no-new-privileges",
		"--pids-limit=512",
		"--memory=4g",
	} {
		if !containsArg(args, flag) {
			t.Errorf("missing flag %q in args: %v", flag, args)
		}
	}
}

func TestBuildRunArgsEmptyLimitsOmitted(t *testing.T) {
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		pidsLimit:   "",
		memoryLimit: "",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	// cap-drop and no-new-privileges are unconditional
	if !containsArg(args, "--cap-drop=all") {
		t.Errorf("--cap-drop=all always required; args: %v", args)
	}
	if !containsArg(args, "--security-opt=no-new-privileges") {
		t.Errorf("--security-opt=no-new-privileges always required; args: %v", args)
	}

	// resource limits must be absent when unset
	for _, flag := range []string{"--pids-limit", "--memory"} {
		for _, arg := range args {
			if arg == flag {
				t.Errorf("unexpected flag %q when limit is empty; args: %v", flag, args)
			}
		}
	}
}

func TestBuildRunArgsImageIsLast(t *testing.T) {
	a := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:abc123",
		pidsLimit:   "256",
		memoryLimit: "2g",
	}
	box := Box{Name: "agent-issue-99", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	// image must appear before the entrypoint and after all flags
	imageIdx := -1
	for i, arg := range args {
		if arg == "spindrift:abc123" {
			imageIdx = i
			break
		}
	}
	if imageIdx < 0 {
		t.Fatalf("image not found in args: %v", args)
	}
	// security flags must precede the image
	for _, flag := range []string{"--cap-drop=all", "--security-opt=no-new-privileges"} {
		flagIdx := -1
		for i, arg := range args {
			if arg == flag {
				flagIdx = i
				break
			}
		}
		if flagIdx >= imageIdx {
			t.Errorf("flag %q (idx %d) must appear before image (idx %d)", flag, flagIdx, imageIdx)
		}
	}
}

func TestBuildRunArgs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:             "podman",
		image:           "spindrift:test",
		skillsDir:       dir,
		driverSkillsDir: "/home/agent/.claude/skills",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	want := dir + ":/home/agent/.claude/skills:ro"
	if !containsArg(args, want) {
		t.Errorf("skills mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_SkillsMountTarget_FromDriverDeclaration verifies the
// box-side skills mount target comes from the adapter's driverSkillsDir
// field (populated by the Driver declaration, ADR 0009) rather than a
// hardcoded ".claude/skills" literal.
func TestBuildRunArgs_SkillsMountTarget_FromDriverDeclaration(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:             "podman",
		image:           "spindrift:test",
		skillsDir:       dir,
		driverSkillsDir: "/home/agent/custom-driver/skills",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	want := dir + ":/home/agent/custom-driver/skills:ro"
	if !containsArg(args, want) {
		t.Errorf("skills mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_IssuesDirMounted verifies that ISSUE_TRACKER=local plus a
// resolved localIssuesDir renders a read-only -v <dir>:/issues:ro entry
// (issue #1691, ADR 0032).
func TestBuildRunArgs_IssuesDirMounted(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:            "podman",
		image:          "spindrift:test",
		issueTracker:   "local",
		localIssuesDir: dir,
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	want := dir + ":/issues:ro"
	if !containsArg(args, want) {
		t.Errorf("issues mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_IssuesDirNonLocalTracker_NoMount verifies that a
// non-local tracker never renders an /issues mount.
func TestBuildRunArgs_IssuesDirNonLocalTracker_NoMount(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:            "podman",
		image:          "spindrift:test",
		issueTracker:   "github",
		localIssuesDir: dir,
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.Contains(arg, ":/issues") {
			t.Errorf("unexpected /issues mount for a non-local tracker: %v", args)
		}
	}
}

func TestBuildRunArgs_DriverCacheDirMountedWritable(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:                   "podman",
		image:                 "spindrift:test",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	want := dir + ":/home/agent/.claude/projects"
	if !containsArg(args, want) {
		t.Errorf("driver cache mount %q not found in args: %v", want, args)
	}
	if containsArg(args, want+":ro") {
		t.Errorf("driver cache mount must be writable, not :ro; args: %v", args)
	}
}

// TestBuildRunArgs_DriverCacheDirMounted_BakedSkillsSurvive verifies that the
// writable cache mount, scoped to /home/agent/.claude/projects, does not
// shadow /home/agent/.claude/skills baked into the image — the regression a
// mount at the parent /home/agent/.claude would cause (OCI has no host-side
// path to re-mount baked skills over, unlike bwrap's agentFiles fallback).
func TestBuildRunArgs_DriverCacheDirMounted_BakedSkillsSurvive(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:                   "podman",
		image:                 "spindrift:test",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if arg == "/home/agent/.claude" || strings.HasSuffix(arg, ":/home/agent/.claude") || strings.HasSuffix(arg, ":/home/agent/.claude:ro") {
			t.Errorf("cache mount must not target the whole /home/agent/.claude (shadows baked skills); args: %v", args)
		}
	}
}

func TestBuildRunArgs_DriverCacheDirMounted_HardeningPreserved(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:                   "podman",
		image:                 "spindrift:test",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	for _, flag := range []string{"--cap-drop=all", "--security-opt=no-new-privileges"} {
		if !containsArg(args, flag) {
			t.Errorf("writable driver cache mount must not weaken hardening; missing %q in args: %v", flag, args)
		}
	}
}

func TestBuildRunArgs_DriverCacheDirUnset_NoMount(t *testing.T) {
	a := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.Contains(arg, "/home/agent/.claude/projects") {
			t.Errorf("unexpected driver cache mount in args when DriverCacheDir is empty: %v", args)
		}
	}
}

// TestBuildRunArgs_DriverCacheMountTarget_FromDriverDeclaration verifies the
// box-side session-cache mount target comes from the adapter's
// driverSessionCacheDir field (populated by the Driver declaration, ADR
// 0009) rather than a hardcoded ".claude/projects" literal.
func TestBuildRunArgs_DriverCacheMountTarget_FromDriverDeclaration(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:                   "podman",
		image:                 "spindrift:test",
		driverSessionCacheDir: "/home/agent/custom-driver/state",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	want := dir + ":/home/agent/custom-driver/state"
	if !containsArg(args, want) {
		t.Errorf("driver cache mount %q not found in args: %v", want, args)
	}
}

// TestBuildRunArgs_DriverSessionCacheDirUndeclared_NoMount verifies that a
// Driver declaring no session-state dir yields no cache mount even when a
// host DriverCacheDir is present -- there is no in-box target to mount it
// over (issue #448).
func TestBuildRunArgs_DriverSessionCacheDirUndeclared_NoMount(t *testing.T) {
	dir := t.TempDir()
	a := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: dir}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.HasPrefix(arg, dir+":") {
			t.Errorf("unexpected driver cache mount in args when Driver declares no session-cache dir: %v", args)
		}
	}
}

func TestBuildRunArgs_SkillsDirUnset_NoMount(t *testing.T) {
	a := &ociAdapter{
		cli:       "podman",
		image:     "spindrift:test",
		skillsDir: "",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	for _, arg := range args {
		if strings.Contains(arg, ".claude/skills") {
			t.Errorf("unexpected skills mount in args when skillsDir is empty: %v", args)
		}
	}
}

// TestRun_AlreadyRunningContainerSkipsLaunch verifies that Run detects a
// same-named container already in the "running" state and returns
// ErrAlreadyRunning without ever invoking `podman/docker run` — the
// collision must not be attempted, only recognized (issue #562).
func TestRun_AlreadyRunningContainerSkipsLaunch(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "run-invoked")
	script := filepath.Join(dir, "fake-podman")
	scriptContent := "#!/bin/sh\ncase \"$1\" in\n" +
		"  inspect) echo running ;;\n" +
		"  run) touch " + marker + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	err := a.Run(box)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run: want ErrAlreadyRunning, got %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("Run: launched the container despite it already running")
	}
}

// TestRun_ExitedContainerReapedThenLaunches verifies today's behavior stays
// intact for the non-collision case: a stale (exited/created, i.e. not
// running) same-named container is reaped with `rm -f`, and the launch
// proceeds normally (issue #562 acceptance criterion 3).
func TestRun_ExitedContainerReapedThenLaunches(t *testing.T) {
	dir := t.TempDir()
	rmMarker := filepath.Join(dir, "rm-invoked")
	runMarker := filepath.Join(dir, "run-invoked")
	script := filepath.Join(dir, "fake-podman")
	scriptContent := "#!/bin/sh\ncase \"$1\" in\n" +
		"  inspect) echo exited ;;\n" +
		"  rm) touch " + rmMarker + " ;;\n" +
		"  run) touch " + runMarker + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &ociAdapter{cli: script, image: "spindrift:test"}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, statErr := os.Stat(rmMarker); statErr != nil {
		t.Error("Run: did not reap the stale exited container")
	}
	if _, statErr := os.Stat(runMarker); statErr != nil {
		t.Error("Run: did not launch after reaping the stale container")
	}
}

func TestReapAfterSuccess(t *testing.T) {
	if !reapAfterSuccess(nil) {
		t.Error("exit 0 (nil error) must reap the container")
	}
	if reapAfterSuccess(errors.New("exit status 1")) {
		t.Error("non-zero exit must retain the container (not reap)")
	}
}

func TestBuildRunArgs_NoRmFlag(t *testing.T) {
	a := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}
	args := a.buildRunArgs(box)

	if containsArg(args, "--rm") {
		t.Errorf("--rm must not be in buildRunArgs (lifecycle is managed by Run); args: %v", args)
	}
}

func containsArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// TestReap_NeverRemovesRunningContainer verifies the safety guard: when the
// fake CLI reports the container is running, Reap must not issue `rm -f`.
func TestReap_NeverRemovesRunningContainer(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: "running"},
	)
	a := &ociAdapter{cli: script}

	if err := a.Reap("agent-issue-1"); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if calls := callCount(t, dir); calls != 1 {
		t.Errorf("Reap: want 1 call (inspect only), got %d", calls)
	}
}

// TestReap_RemovesStaleContainer verifies the other side of the guard: when
// the fake CLI reports the container is not running, Reap issues `rm -f`.
func TestReap_RemovesStaleContainer(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{stdout: "exited"},
		fakeCall{},
	)
	a := &ociAdapter{cli: script}

	if err := a.Reap("agent-issue-1"); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	rm := readCall(t, dir, 1)
	if !containsArg(rm, "rm") || !containsArg(rm, "-f") || !containsArg(rm, "agent-issue-1") {
		t.Errorf("Reap: want `rm -f agent-issue-1`, got %v", rm)
	}
}

// TestKill_MissingContainer_ReturnsNilNotError verifies the common
// settle-phase case (CI watch, merge gate): the initial Box already exited
// successfully and Run's own reapAfterSuccess already removed it, so there
// is no container left to kill. Runner.Kill's contract treats that as
// success, not a failure Terminate would otherwise misreport.
func TestKill_MissingContainer_ReturnsNilNotError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1}) // inspect: no such container
	a := &ociAdapter{cli: script}

	if err := a.Kill("agent-issue-1"); err != nil {
		t.Errorf("Kill: got %v, want nil for a missing container", err)
	}
	if calls := callCount(t, dir); calls != 1 {
		t.Errorf("Kill: want 1 call (inspect only, no rm attempted), got %d", calls)
	}
}

// TestKill_RemovesExistingContainerRegardlessOfRunningState verifies Kill's
// contract is the opposite of Reap's for a container that does exist: it
// issues `rm -f` unconditionally once existence is confirmed, so it reaches
// a genuinely live container Reap would refuse to touch.
func TestKill_RemovesExistingContainerRegardlessOfRunningState(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{}, fakeCall{}) // inspect: found; rm -f: ok
	a := &ociAdapter{cli: script}

	if err := a.Kill("agent-issue-1"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	if calls := callCount(t, dir); calls != 2 {
		t.Errorf("Kill: want 2 calls (inspect then rm -f), got %d", calls)
	}
	rm := readCall(t, dir, 1)
	if !containsArg(rm, "rm") || !containsArg(rm, "-f") || !containsArg(rm, "agent-issue-1") {
		t.Errorf("Kill: want `rm -f agent-issue-1`, got %v", rm)
	}
}

// TestKill_RemovalFailureOnExistingContainer_ReturnsError verifies a
// scripted rm failure against a container confirmed to exist is returned,
// not swallowed — Terminate needs to know a genuine reap failure happened.
func TestKill_RemovalFailureOnExistingContainer_ReturnsError(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{}, fakeCall{exit: 1}) // inspect: found; rm -f: fails
	a := &ociAdapter{cli: script}

	if err := a.Kill("agent-issue-1"); err == nil {
		t.Error("Kill: want error from scripted rm failure, got nil")
	}
}

// TestLoadImage_InvokesLoadThenTag verifies loadImage issues `load -i
// <archive>` followed by `tag spindrift:latest <imageTag>`, in that order.
func TestLoadImage_InvokesLoadThenTag(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{},
		fakeCall{},
	)
	a := &ociAdapter{cli: script, imageTag: "spindrift:abc123"}

	if err := a.loadImage("/tmp/spindrift-image.tar"); err != nil {
		t.Fatalf("loadImage: %v", err)
	}

	load := readCall(t, dir, 0)
	want := []string{"load", "-i", "/tmp/spindrift-image.tar"}
	if strings.Join(load, " ") != strings.Join(want, " ") {
		t.Errorf("load call: got %v, want %v", load, want)
	}

	tag := readCall(t, dir, 1)
	want = []string{"tag", "spindrift:latest", "spindrift:abc123"}
	if strings.Join(tag, " ") != strings.Join(want, " ") {
		t.Errorf("tag call: got %v, want %v", tag, want)
	}
}

// TestIsReady_ImageAbsentReturnsError verifies IsReady surfaces a descriptive
// error when `image inspect` fails (the image is not loaded).
func TestIsReady_ImageAbsentReturnsError(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 1})
	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

	if err := a.IsReady(); err == nil {
		t.Error("IsReady: want error when image absent, got nil")
	}
}

// TestIsReady_ImagePresentReturnsNil verifies IsReady succeeds when `image
// inspect` exits 0 (the image is loaded).
func TestIsReady_ImagePresentReturnsNil(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	a := &ociAdapter{cli: script, image: "spindrift:abc123"}

	if err := a.IsReady(); err != nil {
		t.Errorf("IsReady: want nil when image present, got %v", err)
	}
}

// TestIsRunning_ScriptedStatuses verifies IsRunning reports true only for
// the exact "running" status string, across scripted successive calls with
// different outputs and exit codes.
func TestIsRunning_ScriptedStatuses(t *testing.T) {
	script, _ := newFakeCLI(t,
		fakeCall{stdout: "running"},
		fakeCall{stdout: "exited"},
		fakeCall{exit: 1},
	)
	a := &ociAdapter{cli: script}

	if !a.IsRunning("c") {
		t.Error(`IsRunning: want true for "running" status`)
	}
	if a.IsRunning("c") {
		t.Error(`IsRunning: want false for "exited" status`)
	}
	if a.IsRunning("c") {
		t.Error("IsRunning: want false when inspect fails (exit 1)")
	}
}

// TestEnsureReady_ImageAbsentFallsBackToContainerBuild verifies the
// image-absent branch: EnsureReady tries a host build first, and when that
// fails with a builder-missing error, falls back to buildInContainer — which
// emits the non-digest-pinned supply-chain warning for an unpinned builder
// image.
func TestEnsureReady_ImageAbsentFallsBackToContainerBuild(t *testing.T) {
	cliScript, _ := newFakeCLI(t,
		fakeCall{exit: 1}, // image inspect: absent
		fakeCall{},        // run (container build)
		fakeCall{},        // load
		fakeCall{},        // tag
	)

	// nix build is invoked directly (not through the cli field); stub it on
	// PATH so the host build fails with a builder-missing error.
	nixDir := t.TempDir()
	nixStub := "#!/bin/sh\necho 'error: a Linux system is required to build a Linux derivation' 1>&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(nixDir, "nix"), []byte(nixStub), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", nixDir+":"+oldPath)

	a := &ociAdapter{
		cli:             cliScript,
		image:           "spindrift:abc123",
		imageDrv:        "/nix/store/fake.drv",
		imageTag:        "spindrift:abc123",
		nixBuilderImage: "docker.io/nixos/nix:latest", // unpinned -> triggers warning
		nixVolume:       "spindrift-nix",
		pwd:             "/work",
		flakeImageAttr:  ".#packages.aarch64-linux.agent-image",
	}

	// Capture stderr — the supply-chain warning is written there directly.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w

	ensureErr := a.EnsureReady()

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if ensureErr != nil {
		t.Fatalf("EnsureReady: %v", ensureErr)
	}
	stderr := buf.String()
	if !strings.Contains(stderr, "not digest-pinned") {
		t.Errorf("expected supply-chain warning in stderr; got: %q", stderr)
	}
}

// TestEnsureReady_HostNixBuildInvokedViaSeam verifies that the host `nix
// build` step in EnsureReady goes through the execCommand seam, and that a
// genuine (non-builder-missing) scripted failure surfaces as an error
// without falling back to the container build.
func TestEnsureReady_HostNixBuildInvokedViaSeam(t *testing.T) {
	cliScript, _ := newFakeCLI(t, fakeCall{exit: 1}) // image inspect: absent

	nixScript, nixDir := newFakeCLI(t, fakeCall{exit: 1, stdout: "genuine derivation error"})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(nixScript, args...)
	}

	a := &ociAdapter{
		cli:      cliScript,
		image:    "spindrift:abc123",
		imageDrv: "/nix/store/fake.drv",
	}

	err := a.EnsureReady()

	if gotName != "nix" {
		t.Errorf("execCommand called with %q, want %q", gotName, "nix")
	}
	if err == nil {
		t.Error("expected error from scripted nix build failure, got nil")
	}
	if got := callCount(t, nixDir); got != 1 {
		t.Errorf("callCount = %d, want 1 (no container-build fallback for a genuine error)", got)
	}
}
