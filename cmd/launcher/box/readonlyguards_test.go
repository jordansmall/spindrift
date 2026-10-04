package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/readonlyguards"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

const guardsRegistry = `[
 {"id":"gh-pr-create","marker":"gh pr create","kind":"substring","enforce":"command-shim","message":"m","runtimeMessage":"blocked gh"},
 {"id":"fj-pr-create","marker":"fj pr create","kind":"substring","enforce":"command-shim","message":"m","runtimeMessage":"blocked fj"},
 {"id":"git-push","marker":"git push","kind":"substring","enforce":"git-hook","message":"m","runtimeMessage":"blocked push"}
]`

// guardsFixture is a read-only Box with a real work repo, real git, a fake gh
// on a PATH the test owns and the real install, so what lands on disk and in
// the environment is what the Driver would inherit.
type guardsFixture struct {
	*fixture
	reg     *regFake
	bin     string
	shimDir string
	path    string
}

func newGuardsFixture(t *testing.T) *guardsFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	g := &guardsFixture{fixture: newFixture(t)}
	g.reg = newRegFake()
	g.firstRun(blockedLine+"\n", 0)
	g.d.Registry = g.reg.deps()
	g.env.BoxWriteEnabled = false
	g.write(g.in.ForbiddenMarkersFile, guardsRegistry)
	g.bin = filepath.Join(g.dir, "bin")
	if err := os.MkdirAll(g.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	g.fakeBinary("gh")
	g.path = g.bin + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", g.path)
	// box reads PATH through the injected Getenv, so the knob mirrors the process.
	g.knobs["PATH"] = g.path
	g.shimDir = readonlyShimDir(g.knobs["HOME"])

	if err := os.MkdirAll(g.in.WorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	g.git(g.in.WorkDir, "init", "-q")
	g.d.Git = func(dir string, args ...string) error {
		g.reg.events = append(g.reg.events, "git "+args[0])
		return exec.Command("git", append([]string{"-C", dir}, args...)...).Run()
	}
	g.d.LookPath = exec.LookPath
	g.d.GitOutput = func(dir string, args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		return string(out), err
	}
	g.d.Guards = guardsDeps{
		Install: func(rows []promptassembly.ForbiddenMarkerRow, cfg readonlyguards.Config, out io.Writer) (readonlyguards.Result, error) {
			g.reg.events = append(g.reg.events, "guards")
			return readonlyguards.Install(rows, cfg, out)
		},
		Setenv: os.Setenv,
	}
	return g
}

func (g *guardsFixture) git(dir string, args ...string) string {
	g.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *guardsFixture) fakeBinary(name string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(g.bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		g.t.Fatal(err)
	}
}

func (g *guardsFixture) pushurl() string {
	out, err := exec.Command("git", "-C", g.in.WorkDir, "config", "--get", "remote.origin.pushurl").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func assertExecutable(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("%s is not an executable file (err=%v)", path, err)
	}
}

func TestReadonlyGuards_ReadWriteInstallsNothing(t *testing.T) {
	g := newGuardsFixture(t)
	g.env.BoxWriteEnabled = true
	g.env.OutboxRelayCapable = true
	g.run()
	if _, err := os.Stat(g.shimDir); err == nil {
		t.Fatal("shim dir exists on a read-write Box")
	}
	if os.Getenv("PATH") != g.path || g.pushurl() != "" {
		t.Fatalf("PATH or pushurl changed on a read-write Box: PATH=%q pushurl=%q", os.Getenv("PATH"), g.pushurl())
	}
}

func TestReadonlyGuards_SelfContainedInstallsNothing(t *testing.T) {
	g := newGuardsFixture(t)
	g.env.SelfContained = true
	g.env.OutboxRelayCapable = true
	g.run()
	if _, err := os.Stat(g.shimDir); err == nil {
		t.Fatal("shim dir exists on a self-contained run")
	}
	if os.Getenv("PATH") != g.path {
		t.Fatalf("PATH = %q, want it unchanged", os.Getenv("PATH"))
	}
}

func TestReadonlyGuards_OutboxRelay_ShimAndHooksAndPath(t *testing.T) {
	g := newGuardsFixture(t)
	g.env.OutboxRelayCapable = true
	g.run()

	assertExecutable(t, filepath.Join(g.shimDir, "gh"))
	if _, err := os.Stat(filepath.Join(g.shimDir, "fj")); err == nil {
		t.Error("fj shim installed without fj on PATH")
	}
	decoy := g.pushurl()
	if filepath.Base(decoy) != "readonly-push-guard.git" || !strings.HasPrefix(decoy, g.dir) {
		t.Fatalf("pushurl = %q, want a decoy under the temp dir", decoy)
	}
	for _, hook := range []string{
		filepath.Join(decoy, "hooks", "pre-push"), filepath.Join(decoy, "hooks", "pre-receive"),
		filepath.Join(g.in.WorkDir, ".git", "hooks", "pre-push"), filepath.Join(g.in.WorkDir, ".git", "hooks", "pre-receive"),
	} {
		assertExecutable(t, hook)
	}
	if got := os.Getenv("PATH"); got != g.shimDir+string(os.PathListSeparator)+g.path {
		t.Fatalf("PATH = %q, want the shim dir prefixed", got)
	}
	if want := "readonly-guards: installed 1 command-shim(s) ([gh]), hook installed=true"; !strings.Contains(g.stdout(), want) {
		t.Fatalf("stdout lacks %q:\n%s", want, g.stdout())
	}
}

func TestReadonlyGuards_HostMediatedOnly_InstallsHook(t *testing.T) {
	g := newGuardsFixture(t)
	g.env.HostMediatedRemote = true
	g.run()
	if g.pushurl() == "" {
		t.Fatal("pushurl not pointed at a decoy")
	}
	assertExecutable(t, filepath.Join(g.in.WorkDir, ".git", "hooks", "pre-push"))
}

func TestReadonlyGuards_NeitherCapability_ShimsOnly(t *testing.T) {
	g := newGuardsFixture(t)
	g.run()
	assertExecutable(t, filepath.Join(g.shimDir, "gh"))
	if g.pushurl() != "" {
		t.Errorf("pushurl = %q, want it untouched", g.pushurl())
	}
	if _, err := os.Stat(filepath.Join(g.in.WorkDir, ".git", "hooks", "pre-push")); err == nil {
		t.Error("git hook installed without outbox capability")
	}
	if !strings.Contains(g.stdout(), "hook installed=false") {
		t.Errorf("stdout lacks the hook=false summary:\n%s", g.stdout())
	}
}

func TestReadonlyGuards_FjOnPath_GetsItsShimInTheSameDir(t *testing.T) {
	g := newGuardsFixture(t)
	g.fakeBinary("fj")
	g.run()
	assertExecutable(t, filepath.Join(g.shimDir, "gh"))
	assertExecutable(t, filepath.Join(g.shimDir, "fj"))
}

func TestReadonlyGuards_RunsAfterForgejoCLIBeforeBindings(t *testing.T) {
	g := newGuardsFixture(t)
	g.env.OutboxRelayCapable = true
	g.knobs["FORGEJO_TOKEN"] = "s3cret"
	g.d.LookPath = func(name string) (string, error) {
		if name == "fj" {
			return "/bin/fj", nil
		}
		return exec.LookPath(name)
	}
	g.d.RunCmd = func(cmd *exec.Cmd) error {
		if cmd.Args[0] == "fj" {
			g.reg.events = append(g.reg.events, "fj")
		}
		return nil
	}
	g.run()
	want := []string{"fj", "git init", "git config", "guards", "gate"}
	if got := g.reg.events[:len(want)]; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want a prefix of %v", g.reg.events, want)
	}
}

func TestReadonlyGuards_PathReachesChildren(t *testing.T) {
	g := newGuardsFixture(t)
	var seen string
	orchestrate := g.d.Orchestrate
	g.d.Orchestrate = func(argv []string) int {
		seen = os.Getenv("PATH")
		return orchestrate(argv)
	}
	g.run()
	if !strings.HasPrefix(seen, g.shimDir) {
		t.Fatalf("PATH at the Driver run = %q, want the shim dir first", seen)
	}
}

func TestReadonlyGuards_FailureAbortsBeforeBindingsAndDriver(t *testing.T) {
	g := newGuardsFixture(t)
	g.in.ForbiddenMarkersFile = filepath.Join(g.dir, "missing.json")
	rc, err := run(g.in, g.env, g.d)
	var pe *phaseError
	if !errors.As(err, &pe) || pe.phase != "readonly-guards" {
		t.Fatalf("run() = (%d, %v), want a readonly-guards phase error", rc, err)
	}
	if len(g.reg.events) != 0 || len(g.calls) != 0 || g.assembled != 0 {
		t.Fatalf("work ran after the guards failed: events=%v calls=%d assembled=%d", g.reg.events, len(g.calls), g.assembled)
	}
}

func TestReadonlyGuards_DecoyFailureAborts(t *testing.T) {
	g := newGuardsFixture(t)
	g.env.OutboxRelayCapable = true
	g.d.Git = func(string, ...string) error { return errors.New("exit status 128") }
	_, err := run(g.in, g.env, g.d)
	var pe *phaseError
	if !errors.As(err, &pe) || pe.phase != "readonly-guards" {
		t.Fatalf("run() error = %v, want a readonly-guards phase error", err)
	}
}

func TestReadonlyGuards_VerificationCatchesAMissingShim(t *testing.T) {
	g := newGuardsFixture(t)
	install := g.d.Guards.Install
	g.d.Guards.Install = func(rows []promptassembly.ForbiddenMarkerRow, cfg readonlyguards.Config, out io.Writer) (readonlyguards.Result, error) {
		res, err := install(rows, cfg, out)
		res.Shims = append(res.Shims, "never-installed")
		return res, err
	}
	_, err := run(g.in, g.env, g.d)
	var pe *phaseError
	if !errors.As(err, &pe) || pe.phase != "readonly-guards" || !strings.Contains(err.Error(), "never-installed") {
		t.Fatalf("run() error = %v, want the readonly-guards phase naming the missing shim", err)
	}
	if len(g.calls) != 0 {
		t.Fatal("Driver ran with a guard missing")
	}
}

func TestReadonlyGuards_VerificationCatchesAShadowedShim(t *testing.T) {
	g := newGuardsFixture(t)
	// A Setenv that fails to put the shim dir first leaves the real gh winning.
	g.d.Guards.Setenv = func(string, string) error { return nil }
	_, err := run(g.in, g.env, g.d)
	if err == nil || !strings.Contains(err.Error(), "not the guard shim") {
		t.Fatalf("run() error = %v, want the shadowed shim reported", err)
	}
}

func TestReadonlyGuards_VerificationCatchesAMissingHook(t *testing.T) {
	g := newGuardsFixture(t)
	g.env.OutboxRelayCapable = true
	install := g.d.Guards.Install
	g.d.Guards.Install = func(rows []promptassembly.ForbiddenMarkerRow, cfg readonlyguards.Config, out io.Writer) (readonlyguards.Result, error) {
		res, err := install(rows, cfg, out)
		_ = os.Remove(filepath.Join(g.in.WorkDir, ".git", "hooks", "pre-receive"))
		return res, err
	}
	_, err := run(g.in, g.env, g.d)
	if err == nil || !strings.Contains(err.Error(), "git hook missing") {
		t.Fatalf("run() error = %v, want the missing hook reported", err)
	}
}

func TestReadonlyGuards_VerificationCatchesARetargetedPushurl(t *testing.T) {
	g := newGuardsFixture(t)
	g.env.OutboxRelayCapable = true
	install := g.d.Guards.Install
	g.d.Guards.Install = func(rows []promptassembly.ForbiddenMarkerRow, cfg readonlyguards.Config, out io.Writer) (readonlyguards.Result, error) {
		res, err := install(rows, cfg, out)
		g.git(g.in.WorkDir, "config", "remote.origin.pushurl", "https://forge.example/owner/repo.git")
		return res, err
	}
	_, err := run(g.in, g.env, g.d)
	if err == nil || !strings.Contains(err.Error(), "not the decoy") {
		t.Fatalf("run() error = %v, want the retargeted pushurl reported", err)
	}
	if len(g.calls) != 0 {
		t.Fatal("Driver ran with the pushurl guard defeated")
	}
}

// The guard must fail a real `git push` locally. A pre-push hook alone is not
// enough: git lists the remote's refs over the network before it runs the
// hook, so origin here is an unresolvable .invalid host and the guard's message
// with no network-failure text proves the block is local (issue #2463). Pushing
// the branch already checked out, with --no-verify, is the dispatch's real
// shape: a pushurl pointed at the work dir would resolve that as "Everything
// up-to-date" and exit 0 without firing a hook (issue #2509).
func TestReadonlyGuards_RealGitPushBlockedLocally(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"new ref", []string{"push", "origin", "HEAD:some-branch"}},
		{"new ref, --no-verify", []string{"push", "origin", "HEAD:some-branch", "--no-verify"}},
		{"checked-out branch, --no-verify", []string{"push", "--no-verify", "-u", "origin", "agent/issue-42"}},
		{"unresolvable origin", []string{"push", "origin", "HEAD:some-branch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGuardsFixture(t)
			g.env.OutboxRelayCapable = true
			rows, err := os.ReadFile(repopath.ForbiddenMarkersJSON())
			if err != nil {
				t.Fatal(err)
			}
			g.write(g.in.ForbiddenMarkersFile, string(rows))

			work := g.in.WorkDir
			g.git(work, "checkout", "-q", "-b", "agent/issue-42")
			g.git(work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "work")
			if err := os.MkdirAll(remote, 0o755); err != nil {
				t.Fatal(err)
			}
			g.git(remote, "init", "-q", "--bare")
			origin := remote
			if tc.name == "unresolvable origin" {
				origin = "https://readonly-push-hook-test.invalid/owner/repo.git"
			}
			g.git(work, "remote", "add", "origin", origin)

			g.run()

			out, err := exec.Command("git", append([]string{"-C", work}, tc.args...)...).CombinedOutput()
			if err == nil {
				t.Fatalf("git %v succeeded; want it blocked:\n%s", tc.args, out)
			}
			for _, want := range []string{"push", "outbox"} {
				if !strings.Contains(string(out), want) {
					t.Errorf("rejection = %q, want it to mention %q", out, want)
				}
			}
			for _, bad := range []string{"Everything up-to-date", "Could not resolve host", "Failed to connect", "unable to access", "Temporary failure"} {
				if strings.Contains(string(out), bad) {
					t.Errorf("rejection = %q, want a local block with no %q", out, bad)
				}
			}
			// Nothing reached the real remote.
			if refs := g.git(remote, "for-each-ref"); refs != "" {
				t.Errorf("remote refs = %q, want none", refs)
			}
		})
	}
}

// Without outbox capability the hand-off is a real push, so nothing blocks it.
func TestReadonlyGuards_NeitherCapability_PushesNormally(t *testing.T) {
	g := newGuardsFixture(t)
	work := g.in.WorkDir
	g.git(work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "work")
	remote := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	g.git(remote, "init", "-q", "--bare")
	g.git(work, "remote", "add", "origin", remote)

	g.run()

	g.git(work, "push", "origin", "HEAD:some-relay-branch")
	g.git(remote, "rev-parse", "--verify", "some-relay-branch")
}
