package runner

import (
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestBwrapRun_LaunchesViaSeamAndSurfacesFailure verifies that Run invokes
// bwrap through the package-level execCommand seam (rather than a hardcoded
// exec.Command("bwrap", ...)) and that a scripted failure surfaces as an
// error. networkMode="host" keeps this test's exec target bare bwrap
// (execTarget's non-pasta branch); TestBwrapRun_PastaIsTopLevelProgramByDefault
// covers the pasta-wrapped default.
func TestBwrapRun_LaunchesViaSeamAndSurfacesFailure(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost}
	err := a.Run(Box{Env: map[string]string{}})

	if gotName != "bwrap" {
		t.Errorf("execCommand called with %q, want %q", gotName, "bwrap")
	}
	if err == nil {
		t.Error("expected error from scripted bwrap failure, got nil")
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1", got)
	}
}

// TestBwrapRun_PastaIsTopLevelProgramByDefault verifies that Run invokes
// execCommand with "pasta" as the top-level program for the default
// (zero-value) networkMode — the fix for the pre-#2666-fix-up bug where
// bwrap was always the literal top-level command even when isolating with
// pasta, leaving pasta buried in bwrap's own trailing argv where it had no
// namespace left to configure (issue #2666 review finding).
func TestBwrapRun_PastaIsTopLevelProgramByDefault(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotName != "pasta" {
		t.Errorf("execCommand called with %q, want %q", gotName, "pasta")
	}
}

// TestBwrapRun_WritesSynthesizedResolvConfForPastaPath verifies that Run
// writes <etcDir>/resolv.conf (the file buildArgs ro-binds to
// /etc/resolv.conf under the pasta path) before launching, pointed at
// pastaDNSForwardAddr — without this file the guest has no resolv.conf at
// all, since nothing else writes one into the sandbox for the bwrap runtime
// (unlike the OCI runner, podman writes its own). The content is read from
// inside the execCommand seam override, synchronously before Start/Wait,
// since Run's own deferred os.RemoveAll(etcDir) has already fired by the
// time Run returns.
func TestBwrapRun_WritesSynthesizedResolvConfForPastaPath(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotResolvConf []byte
	var readErr error
	execCommand = func(name string, args ...string) *exec.Cmd {
		for i, arg := range args {
			if arg == "--ro-bind" && i+2 < len(args) && args[i+2] == "/etc/resolv.conf" {
				gotResolvConf, readErr = os.ReadFile(args[i+1])
			}
		}
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if readErr != nil {
		t.Fatalf("read synthesized resolv.conf: %v", readErr)
	}
	if gotResolvConf == nil {
		t.Fatal("no --ro-bind ... /etc/resolv.conf found in exec args")
	}
	want := "nameserver " + pastaDNSForwardAddr + "\n"
	if string(gotResolvConf) != want {
		t.Errorf("synthesized resolv.conf = %q, want %q", gotResolvConf, want)
	}
}

// TestBwrapRun_PastaChildEnvCarriesPathToFindBwrap verifies that when Run
// wraps with pasta (the default networkMode), pasta's own process env
// carries a PATH entry -- without one, pasta's own execvp("bwrap") (a bare
// name, resolved by pasta itself at runtime, not by Go's exec.Command
// LookPath, which only ever resolved "pasta" itself) would fail with ENOENT
// even though pasta launched fine, defeating the whole fix one process hop
// later. TestResolvedRunEnv_DropsUndeclaredAmbientVariable already pins that
// this doesn't widen the ambient-leak guarantee for anything else.
func TestBwrapRun_PastaChildEnvCarriesPathToFindBwrap(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	found := false
	for _, kv := range gotCmd.Env {
		if strings.HasPrefix(kv, "PATH=") {
			found = true
		}
	}
	if !found {
		t.Errorf("Run's cmd.Env for the pasta-wrapped path has no PATH entry, pasta's own execvp(\"bwrap\") would fail: %v", gotCmd.Env)
	}
}

// TestBwrapRun_ExitCodeSurfacedAsRunError verifies that a non-zero exit from
// the scripted bwrap invocation surfaces as a *RunError carrying that exit
// code, so later slices can detect signal-kill exit codes (128+N) through a
// runtime-agnostic type instead of a raw *exec.ExitError.
func TestBwrapRun_ExitCodeSurfacedAsRunError(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 137})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	err := a.Run(Box{Env: map[string]string{}})

	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run: want error to unwrap to *RunError, got %v (%T)", err, err)
	}
	if runErr.ExitCode != 137 {
		t.Errorf("RunError.ExitCode: want 137, got %d", runErr.ExitCode)
	}
}

// TestBwrapBuildEnsureReady_NixBuildFailureWrapsError verifies that a
// scripted `nix build` failure on the agent-files realization surfaces as a
// wrapped error via the execCommand seam.
func TestBwrapBuildEnsureReady_NixBuildFailureWrapsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{agentFilesDrv: "/fake/files.drv", agentEnvDrv: "/fake/env.drv"}
	err := a.EnsureReady()

	if gotName != "nix" {
		t.Errorf("execCommand called with %q, want %q", gotName, "nix")
	}
	if err == nil {
		t.Fatal("expected error from scripted nix build failure, got nil")
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1 (must not proceed to agent-env build after failure)", got)
	}
}

// TestBwrapBuildEnsureReady_NixBuildSuccessReturnsNil verifies that
// EnsureReady returns nil when all four scripted nix build calls succeed
// (agent-files, agent-env, passwd-file, group-file — issue #2663).
func TestBwrapBuildEnsureReady_NixBuildSuccessReturnsNil(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv: "/fake/files.drv",
		agentEnvDrv:   "/fake/env.drv",
		passwdFileDrv: "/fake/passwd.drv",
		groupFileDrv:  "/fake/group.drv",
	}
	err := a.EnsureReady()

	if err != nil {
		t.Errorf("EnsureReady() = %v, want nil", err)
	}
	if got := callCount(t, dir); got != 4 {
		t.Errorf("callCount = %d, want 4 (agent-files + agent-env + passwd-file + group-file)", got)
	}
}

// TestBwrapBuildEnsureReady_PasswdFileFailureWrapsErrorAndStops verifies that
// a scripted `nix build` failure on the passwd-file realization (the third
// closure) surfaces as a wrapped "nix build passwd-file" error and stops
// before the group-file closure runs (issue #2663).
func TestBwrapBuildEnsureReady_PasswdFileFailureWrapsErrorAndStops(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 1})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv: "/fake/files.drv",
		agentEnvDrv:   "/fake/env.drv",
		passwdFileDrv: "/fake/passwd.drv",
		groupFileDrv:  "/fake/group.drv",
	}
	err := a.EnsureReady()

	if err == nil || !strings.Contains(err.Error(), "nix build passwd-file") {
		t.Errorf("EnsureReady() = %v, want error containing %q", err, "nix build passwd-file")
	}
	if got := callCount(t, dir); got != 3 {
		t.Errorf("callCount = %d, want 3 (must not proceed to group-file build after failure)", got)
	}
}

// TestBwrapBuildEnsureReady_GroupFileFailureWrapsError verifies that a
// scripted `nix build` failure on the group-file realization (the fourth and
// final closure) surfaces as a wrapped "nix build group-file" error (issue
// #2663).
func TestBwrapBuildEnsureReady_GroupFileFailureWrapsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 1})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv: "/fake/files.drv",
		agentEnvDrv:   "/fake/env.drv",
		passwdFileDrv: "/fake/passwd.drv",
		groupFileDrv:  "/fake/group.drv",
	}
	err := a.EnsureReady()

	if err == nil || !strings.Contains(err.Error(), "nix build group-file") {
		t.Errorf("EnsureReady() = %v, want error containing %q", err, "nix build group-file")
	}
	if got := callCount(t, dir); got != 4 {
		t.Errorf("callCount = %d, want 4", got)
	}
}

// TestBwrapKill_TerminatesRunningProcess verifies Kill (issue #649) reaches
// a bwrap sandbox's live process — the one Runner an external caller has no
// other way to observe, since IsRunning/Reap are both no-ops for bwrap.
func TestBwrapKill_TerminatesRunningProcess(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sleep", "5")
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	done := make(chan error, 1)
	go func() { done <- a.Run(Box{Name: "agent-issue-9", Env: map[string]string{}}) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		_, tracked := a.running["agent-issue-9"]
		a.mu.Unlock()
		if tracked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never tracked its process")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := a.Kill("agent-issue-9"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run: want error from killed process, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Kill")
	}
}

// TestBwrapKill_UnknownNameIsNoop verifies Kill on a name Run never tracked
// (already exited, or never launched) returns nil rather than erroring.
func TestBwrapKill_UnknownNameIsNoop(t *testing.T) {
	a := &bwrapAdapter{}
	if err := a.Kill("agent-issue-404"); err != nil {
		t.Errorf("Kill on unknown name: want nil, got %v", err)
	}
}

// TestResolvedRunEnv_DropsUndeclaredAmbientVariable characterizes the
// allowlist invariant the denylist version leaked: a name set on the
// launcher's own real ambient process environment, absent from box.Env
// entirely, must never appear in the env the bwrap child actually receives
// -- while a real bwrapSecrets key present in box.Env still does. This
// drives through Run itself (not resolvedRunEnv in isolation with an empty
// box.Env, which would pin only the drop half) to pin the real seam:
// bwrap.go's `cmd.Env = resolvedRunEnv(box.Env)`.
func TestResolvedRunEnv_DropsUndeclaredAmbientVariable(t *testing.T) {
	t.Setenv("SOME_UNDECLARED_SECRET", "leaked-value")
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{"GH_TOKEN": "box-token"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sawGHToken := false
	for _, kv := range gotCmd.Env {
		if strings.HasPrefix(kv, "SOME_UNDECLARED_SECRET=") {
			t.Errorf("Run's cmd.Env leaked an ambient var absent from box.Env: %v", gotCmd.Env)
		}
		if kv == "GH_TOKEN=box-token" {
			sawGHToken = true
		}
	}
	if !sawGHToken {
		t.Errorf("Run's cmd.Env dropped a real bwrapSecrets key present in box.Env: %v", gotCmd.Env)
	}
}

// TestResolvedRunEnv_ForwardsGHTokenFromBoxEnv verifies opt-in two-actor
// separation (ADR 0016, issue #380) still works under the allowlist: when
// box.Env carries a resolved GH_TOKEN (reflecting any BOX_GH_TOKEN override
// dispatchConfig's ResolveEnv chain applied), resolvedRunEnv forwards it
// verbatim -- buildArgs's --setenv loop skips GH_TOKEN (bwrapSecrets) to
// keep it off argv, and bwrap has no --clearenv, so this is the only path
// left for it to reach the sandbox at all.
func TestResolvedRunEnv_ForwardsGHTokenFromBoxEnv(t *testing.T) {
	boxEnv := map[string]string{"GH_TOKEN": "box-token"}

	got := resolvedRunEnv(boxEnv)

	want := []string{"GH_TOKEN=box-token"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolvedRunEnv = %v, want %v", got, want)
	}
}

// TestResolvedRunEnv_ForwardsAllBwrapSecrets verifies every bwrapSecrets
// name (not just GH_TOKEN) is forwarded from box.Env through the process
// environment, since buildArgs's --setenv loop excludes all of them from
// argv identically.
func TestResolvedRunEnv_ForwardsAllBwrapSecrets(t *testing.T) {
	boxEnv := map[string]string{
		"GH_TOKEN":                "gh-token-value",
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth-token-value",
		"ANTHROPIC_API_KEY":       "anthropic-key-value",
		"OPENCODE_AUTH_CONTENT":   "opencode-auth-value",
	}

	got := resolvedRunEnv(boxEnv)

	if len(got) != len(boxEnv) {
		t.Fatalf("resolvedRunEnv returned %d entries, want %d: %v", len(got), len(boxEnv), got)
	}
	for k, v := range boxEnv {
		want := k + "=" + v
		found := false
		for _, kv := range got {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Errorf("resolvedRunEnv missing %q, got %v", want, got)
		}
	}
}

// TestResolvedRunEnv_ExcludesKeysNotInBwrapSecrets covers two ways a key can
// be legitimately excluded from resolvedRunEnv's output: BOX_GH_TOKEN is
// never a bwrapSecrets key at all (lib/env-schema.nix's boxGhToken entry is
// boxEnv=false, so it would never actually be a box.Env key in production
// either -- this just proves resolvedRunEnv would still drop it if it somehow
// were); ISSUE_NUMBER is a legitimate box.Env key but not a bwrapSecrets one,
// so buildArgs's --setenv loop already delivers it to the sandbox on argv,
// and resolvedRunEnv correctly leaves it out to avoid delivering it twice.
func TestResolvedRunEnv_ExcludesKeysNotInBwrapSecrets(t *testing.T) {
	tests := []struct {
		name      string
		boxEnv    map[string]string
		absentKey string
	}{
		{"BOX_GH_TOKEN is not a bwrapSecrets key", map[string]string{"BOX_GH_TOKEN": "box-token"}, "BOX_GH_TOKEN"},
		{"non-secret box.Env key already delivered via --setenv", map[string]string{"GH_TOKEN": "gh-token-value", "ISSUE_NUMBER": "42"}, "ISSUE_NUMBER"},
	}
	for _, tc := range tests {
		got := resolvedRunEnv(tc.boxEnv)
		for _, kv := range got {
			if strings.HasPrefix(kv, tc.absentKey+"=") {
				t.Errorf("%s: resolvedRunEnv forwarded %s, want absent: %v", tc.name, tc.absentKey, got)
			}
		}
	}
}

// TestBwrapRun_SandboxGHTokenReflectsBoxEnvOverride verifies Run itself (not
// just resolvedRunEnv in isolation) sets the launched bwrap process's GH_TOKEN
// from box.Env, not from the launcher's ambient GH_TOKEN -- proving the
// two-actor override (ADR 0016, issue #380) actually reaches the sandbox,
// the gap a box-env-assembly test alone would miss (cmd.Env=nil previously
// meant the sandbox inherited the launcher's ambient value regardless of
// what buildBoxEnv computed).
func TestBwrapRun_SandboxGHTokenReflectsBoxEnvOverride(t *testing.T) {
	t.Setenv("GH_TOKEN", "launcher-token")
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{"GH_TOKEN": "box-token"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, kv := range gotCmd.Env {
		if kv == "GH_TOKEN=launcher-token" {
			t.Error("sandbox process env carries the launcher's ambient GH_TOKEN, want the box-resolved override")
		}
	}
	found := false
	for _, kv := range gotCmd.Env {
		if kv == "GH_TOKEN=box-token" {
			found = true
		}
	}
	if !found {
		t.Error("sandbox process env missing GH_TOKEN=box-token")
	}
}

// TestBwrapRun_OpencodeAuthContentOffArgvButInProcessEnv verifies
// OPENCODE_AUTH_CONTENT (the opencode github-copilot credential, issue #263)
// never appears on the bwrap command line -- ps/proc on the host would
// otherwise expose it to other local users -- while still reaching the
// sandbox via process-environment inheritance (bwrap has no --clearenv),
// mirroring how GH_TOKEN and the other bwrapSecrets entries are delivered.
func TestBwrapRun_OpencodeAuthContentOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "opencode-auth-content-sentinel-value"
	t.Setenv("OPENCODE_AUTH_CONTENT", sentinel)

	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := Box{Env: map[string]string{"OPENCODE_AUTH_CONTENT": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box) {
		if strings.Contains(arg, sentinel) {
			t.Errorf("OPENCODE_AUTH_CONTENT sentinel found in bwrap argv: %v", arg)
		}
	}

	found := false
	for _, kv := range gotCmd.Env {
		if kv == "OPENCODE_AUTH_CONTENT="+sentinel {
			found = true
		}
	}
	if !found {
		t.Error("sandbox process env missing OPENCODE_AUTH_CONTENT sentinel")
	}
}
