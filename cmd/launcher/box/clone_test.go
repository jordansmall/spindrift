package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/boxclone"
	"spindrift.dev/launcher/internal/branchrecovery"
)

func TestCloneRunsWithTheEnvsConfigAndGithubDefault(t *testing.T) {
	f := newFixture(t)
	f.knobs["CODE_FORGE_REMOTE_URL"] = "https://git.example/r.git"
	f.knobs["FORGEJO_BASE_URL"] = "https://fj.example"
	f.knobs["FORGEJO_TOKEN"] = "fjtok"
	f.in.RepoMountDir = "/repo"
	f.run()
	want := boxclone.Config{
		CodeForge: "github", RepoSlug: "owner/repo", RemoteURL: "https://git.example/r.git",
		ForgejoBaseURL: "https://fj.example", ForgejoToken: "fjtok", RepoMountDir: "/repo",
		WorkDir: f.in.WorkDir, GitUserName: defaultGitUserName, GitUserEmail: "agent@example.com",
	}
	if len(f.cloneCfgs) != 1 || f.cloneCfgs[0] != want {
		t.Fatalf("clone configs = %+v, want [%+v]", f.cloneCfgs, want)
	}
}

func TestCloneUsesTheEnvsCodeForge(t *testing.T) {
	f := newFixture(t)
	f.env.CodeForge = "local"
	f.run()
	if got := f.cloneCfgs[0].CodeForge; got != "local" {
		t.Errorf("CodeForge = %q, want local", got)
	}
}

func TestCloneStandsInTheWorkDirWithPWDSet(t *testing.T) {
	f := newFixture(t)
	f.run()
	if len(f.chdirs) != 1 || f.chdirs[0] != f.in.WorkDir {
		t.Errorf("chdirs = %v, want [%s]", f.chdirs, f.in.WorkDir)
	}
	if got := f.knobs["PWD"]; got != f.in.WorkDir {
		t.Errorf("PWD = %q, want %q", got, f.in.WorkDir)
	}
}

func TestCloneSelfContainedCreatesTheWorkDirAndClonesNothing(t *testing.T) {
	f := newFixture(t)
	f.env.SelfContained = true
	f.run()
	if len(f.cloneCfgs) != 0 {
		t.Errorf("cloned %d times, want none", len(f.cloneCfgs))
	}
	if st, err := os.Stat(f.in.WorkDir); err != nil || !st.IsDir() {
		t.Errorf("work dir missing: %v", err)
	}
	if len(f.chdirs) != 1 || f.chdirs[0] != f.in.WorkDir {
		t.Errorf("chdirs = %v", f.chdirs)
	}
}

func TestCloneNotReachedForAnUnknownKind(t *testing.T) {
	f := newFixture(t)
	f.env.DispatchKind = "bogus"
	if _, err := run(f.in, f.env, f.d); err == nil {
		t.Fatal("run() succeeded")
	}
	if len(f.cloneCfgs) != 0 || len(f.chdirs) != 0 {
		t.Errorf("clone ran for an unknown kind: %v %v", f.cloneCfgs, f.chdirs)
	}
	if _, err := os.Stat(f.in.WorkDir); err == nil {
		t.Error("work dir created for an unknown kind")
	}
}

func TestCloneErrorAbortsWithTheClonePhase(t *testing.T) {
	f := newFixture(t)
	f.cloneErr = errors.New("boom")
	_, err := run(f.in, f.env, f.d)
	if err == nil || err.Error() != "clone: boom" {
		t.Fatalf("error = %v, want clone: boom", err)
	}
	if len(f.recoverCfgs) != 0 || f.assembled != 0 || len(f.chdirs) != 0 {
		t.Error("work continued after a failed clone")
	}
}

func TestCloneRunsBeforeBranchRecovery(t *testing.T) {
	f := newFixture(t)
	var order []string
	clone, recoverFn := f.d.Clone, f.d.Recover
	f.d.Clone = func(c boxclone.Config, stdout, stderr io.Writer) error {
		order = append(order, "clone")
		return clone(c, stdout, stderr)
	}
	f.d.Recover = func(c branchrecovery.Config, o func() (bool, error), w io.Writer) (branchrecovery.Outcome, error) {
		order = append(order, "recover")
		return recoverFn(c, o, w)
	}
	f.run()
	if strings.Join(order, ",") != "clone,recover" {
		t.Errorf("order = %v", order)
	}
}
