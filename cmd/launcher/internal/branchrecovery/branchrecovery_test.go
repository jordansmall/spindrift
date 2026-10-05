package branchrecovery_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/seambundle"
)

const branch = "agent/issue-7"

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, name, body string) {
	t.Helper()
	write(t, dir, name, body)
	git(t, dir, "add", name)
	git(t, dir, "commit", "-m", name)
}

type fixture struct {
	origin, work, outbox string
	cfg                  branchrecovery.Config
}

// newFixture seeds a bare origin with one commit on main and clones it.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	f := &fixture{
		origin: filepath.Join(root, "origin.git"),
		work:   filepath.Join(root, "work"),
		outbox: filepath.Join(root, "outbox"),
	}
	seed := filepath.Join(root, "seed")
	git(t, root, "init", "-q", "--bare", "-b", "main", f.origin)
	git(t, root, "init", "-q", "-b", "main", seed)
	git(t, seed, "config", "user.name", "Test")
	git(t, seed, "config", "user.email", "t@example.com")
	commit(t, seed, "base.txt", "base\n")
	git(t, seed, "push", "-q", f.origin, "main")
	git(t, root, "clone", "-q", f.origin, f.work)
	git(t, f.work, "config", "user.name", "Test")
	git(t, f.work, "config", "user.email", "t@example.com")
	f.cfg = branchrecovery.Config{
		WorkDir: f.work, Branch: branch, BaseBranch: "main",
		CodeForge: "github", QueryOpenPR: true, Push: true, OutboxDir: f.outbox,
	}
	return f
}

// withPriorBranch pushes branch to origin with one commit on file name.
func (f *fixture) withPriorBranch(t *testing.T, name, body string) {
	t.Helper()
	git(t, f.work, "checkout", "-q", "-b", branch)
	commit(t, f.work, name, body)
	git(t, f.work, "push", "-q", "origin", branch)
	git(t, f.work, "checkout", "-q", "main")
	git(t, f.work, "branch", "-q", "-D", branch)
}

// advanceMain lands a commit on origin/main and fetches it into the work clone.
func (f *fixture) advanceMain(t *testing.T, name, body string) {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "-q", f.origin, other)
	git(t, other, "config", "user.name", "Test")
	git(t, other, "config", "user.email", "t@example.com")
	commit(t, other, name, body)
	git(t, other, "push", "-q", "origin", "main")
	git(t, f.work, "fetch", "-q", "origin")
}

func (f *fixture) originRef(t *testing.T, ref string) string {
	t.Helper()
	return git(t, f.origin, "rev-parse", ref)
}

func (f *fixture) rebaseInProgress() bool {
	_, err := os.Stat(filepath.Join(f.work, ".git", "rebase-merge"))
	return err == nil
}

func (f *fixture) bundlePath() string { return filepath.Join(f.outbox, seambundle.FileName) }

func never(t *testing.T) func() (bool, error) {
	return func() (bool, error) {
		t.Helper()
		t.Error("openPR consulted")
		return false, nil
	}
}

func answer(open bool) func() (bool, error) { return func() (bool, error) { return open, nil } }

func TestRecoverNoPriorBranchStartsFresh(t *testing.T) {
	f := newFixture(t)
	var w bytes.Buffer
	out, err := branchrecovery.Recover(f.cfg, never(t), &w)
	if err != nil {
		t.Fatal(err)
	}
	if out != (branchrecovery.Outcome{}) {
		t.Errorf("outcome = %+v", out)
	}
	if got, want := git(t, f.work, "rev-parse", "HEAD"), f.originRef(t, "main"); got != want {
		t.Errorf("HEAD = %s, want origin/main %s", got, want)
	}
	if got := git(t, f.origin, "branch", "--list", branch); got != "" {
		t.Errorf("branch pushed: %q", got)
	}
	if !strings.Contains(w.String(), "==> rebasing "+branch+" onto latest origin/main") {
		t.Errorf("narration: %s", w.String())
	}
}

func TestRecoverAdoptsOpenPR(t *testing.T) {
	f := newFixture(t)
	f.withPriorBranch(t, "prior.txt", "prior\n")
	f.advanceMain(t, "main2.txt", "m2\n")
	before := f.originRef(t, "refs/heads/"+branch)

	var w bytes.Buffer
	out, err := branchrecovery.Recover(f.cfg, answer(true), &w)
	if err != nil {
		t.Fatal(err)
	}
	if out != (branchrecovery.Outcome{Adopted: true}) {
		t.Errorf("outcome = %+v", out)
	}
	head := git(t, f.work, "rev-parse", "HEAD")
	if got := f.originRef(t, "refs/heads/"+branch); got != head || got == before {
		t.Errorf("origin branch = %s, want rebased HEAD %s (was %s)", got, head, before)
	}
	git(t, f.work, "merge-base", "--is-ancestor", "origin/main", "HEAD")
	if _, err := os.Stat(filepath.Join(f.work, "prior.txt")); err != nil {
		t.Errorf("prior work lost: %v", err)
	}
	for _, s := range []string{"skipping force-reset", "==> publishing rebased " + branch} {
		if !strings.Contains(w.String(), s) {
			t.Errorf("narration missing %q: %s", s, w.String())
		}
	}
}

func TestRecoverAdoptReadOnlyRelaysBundle(t *testing.T) {
	f := newFixture(t)
	f.cfg.Push = false
	f.withPriorBranch(t, "prior.txt", "prior\n")
	f.advanceMain(t, "main2.txt", "m2\n")
	before := f.originRef(t, "refs/heads/"+branch)

	out, err := branchrecovery.Recover(f.cfg, answer(true), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Adopted || out.Conflict {
		t.Errorf("outcome = %+v", out)
	}
	if got := f.originRef(t, "refs/heads/"+branch); got != before {
		t.Errorf("origin branch moved: %s -> %s", before, got)
	}
	git(t, f.work, "bundle", "verify", f.bundlePath())
}

func TestRecoverAdoptConflictDefersPublish(t *testing.T) {
	f := newFixture(t)
	f.withPriorBranch(t, "clash.txt", "prior\n")
	f.advanceMain(t, "clash.txt", "main\n")
	before := f.originRef(t, "refs/heads/"+branch)

	out, err := branchrecovery.Recover(f.cfg, answer(true), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if out != (branchrecovery.Outcome{Conflict: true, Adopted: true}) {
		t.Errorf("outcome = %+v", out)
	}
	if !f.rebaseInProgress() {
		t.Error("rebase not left in progress")
	}
	if got := f.originRef(t, "refs/heads/"+branch); got != before {
		t.Errorf("published despite conflict: %s -> %s", before, got)
	}
}

func TestRecoverStaleBranchForceReset(t *testing.T) {
	for _, tc := range []struct {
		name       string
		push       bool
		wantOrigin func(f *fixture, t *testing.T) string
	}{
		{"push", true, func(f *fixture, t *testing.T) string { return f.originRef(t, "main") }},
		{"read-only", false, func(f *fixture, t *testing.T) string { return f.originRef(t, "refs/heads/"+branch) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.cfg.Push = tc.push
			f.withPriorBranch(t, "prior.txt", "prior\n")
			f.advanceMain(t, "main2.txt", "m2\n")
			want := tc.wantOrigin(f, t)

			var w bytes.Buffer
			out, err := branchrecovery.Recover(f.cfg, answer(false), &w)
			if err != nil {
				t.Fatal(err)
			}
			if out != (branchrecovery.Outcome{}) {
				t.Errorf("outcome = %+v", out)
			}
			if got := f.originRef(t, "refs/heads/"+branch); got != want {
				t.Errorf("origin branch = %s, want %s", got, want)
			}
			if got, want := git(t, f.work, "rev-parse", "HEAD"), f.originRef(t, "main"); got != want {
				t.Errorf("HEAD = %s, want origin/main %s", got, want)
			}
			if _, err := os.Stat(f.bundlePath()); !os.IsNotExist(err) {
				t.Errorf("bundle written for empty range: %v", err)
			}
			if !strings.Contains(w.String(), "force-resetting to main") {
				t.Errorf("narration: %s", w.String())
			}
		})
	}
}

// A concurrent Box that pushed the branch after this clone fetched makes the
// lease stale, so either publish must fail rather than clobber its work.
func TestRecoverPublishRejectedByLease(t *testing.T) {
	for _, tc := range []struct {
		name string
		open bool
		want string
	}{
		{"reset", false, "publishing reset branch failed"},
		{"adopted", true, "publishing rebased branch failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.withPriorBranch(t, "prior.txt", "prior\n")
			other := filepath.Join(t.TempDir(), "other")
			git(t, filepath.Dir(other), "clone", "-q", "-b", branch, f.origin, other)
			git(t, other, "config", "user.name", "Test")
			git(t, other, "config", "user.email", "t@example.com")
			commit(t, other, "concurrent.txt", "c\n")
			git(t, other, "push", "-q", "origin", branch)
			ahead := f.originRef(t, "refs/heads/"+branch)

			_, err := branchrecovery.Recover(f.cfg, answer(tc.open), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if got := f.originRef(t, "refs/heads/"+branch); got != ahead {
				t.Errorf("origin branch clobbered: %s -> %s", ahead, got)
			}
		})
	}
}

func TestRecoverOpenPRErrorAborts(t *testing.T) {
	f := newFixture(t)
	f.withPriorBranch(t, "prior.txt", "prior\n")
	before := f.originRef(t, "refs/heads/"+branch)
	boom := errors.New("network down")

	_, err := branchrecovery.Recover(f.cfg, func() (bool, error) { return false, boom }, &bytes.Buffer{})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "aborting to protect any open PR") {
		t.Fatalf("err = %v", err)
	}
	if got := f.originRef(t, "refs/heads/"+branch); got != before {
		t.Errorf("origin branch moved: %s -> %s", before, got)
	}
	if got := git(t, f.work, "branch", "--list", branch); got != "" {
		t.Errorf("local branch created: %q", got)
	}
}

func TestRecoverWithoutOpenPRQueryStartsFresh(t *testing.T) {
	f := newFixture(t)
	f.cfg.CodeForge = "github"
	f.cfg.QueryOpenPR = false
	f.withPriorBranch(t, "prior.txt", "prior\n")
	before := f.originRef(t, "refs/heads/"+branch)

	var w bytes.Buffer
	out, err := branchrecovery.Recover(f.cfg, never(t), &w)
	if err != nil {
		t.Fatal(err)
	}
	if out != (branchrecovery.Outcome{}) {
		t.Errorf("outcome = %+v", out)
	}
	if got, want := git(t, f.work, "rev-parse", "HEAD"), f.originRef(t, "main"); got != want {
		t.Errorf("HEAD = %s, want origin/main %s", got, want)
	}
	if got := f.originRef(t, "refs/heads/"+branch); got != before {
		t.Errorf("origin branch moved: %s -> %s", before, got)
	}
	if _, err := os.Stat(f.bundlePath()); !os.IsNotExist(err) {
		t.Errorf("bundle written: %v", err)
	}
	if want := "CODE_FORGE=github: starting " + branch + " fresh from origin/main"; !strings.Contains(w.String(), want) {
		t.Errorf("narration missing %q: %s", want, w.String())
	}
}

func TestPublish(t *testing.T) {
	for _, push := range []bool{true, false} {
		name := "bundle"
		if push {
			name = "push"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.cfg.Push = push
			git(t, f.work, "checkout", "-q", "-b", branch)
			commit(t, f.work, "work.txt", "work\n")

			if err := branchrecovery.Publish(f.cfg, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			_, bundleErr := os.Stat(f.bundlePath())
			pushed := git(t, f.origin, "branch", "--list", branch) != ""
			if push && (!pushed || bundleErr == nil) {
				t.Errorf("push mechanism: pushed=%v bundleErr=%v", pushed, bundleErr)
			}
			if !push {
				if pushed || bundleErr != nil {
					t.Errorf("bundle mechanism: pushed=%v bundleErr=%v", pushed, bundleErr)
				}
				git(t, f.work, "bundle", "verify", f.bundlePath())
			}
		})
	}
}
