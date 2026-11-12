package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/gitexec/gitexectest"
	"spindrift.dev/launcher/internal/testutil"
)

const (
	authUser  = "oauth2"
	authToken = "s3cr3t-token-6860"
)

// authFixture serves an authenticated remote whose bare repo holds main and an
// agent/issue-1 branch one commit ahead of it, and returns the same URL with
// the credential embedded the way an operator's CODE_FORGE_REMOTE_URL has it.
func authFixture(t *testing.T) (cleanURL, userinfoURL string) {
	t.Helper()
	cleanURL, bare := gitexectest.AuthRemote(t, authUser, authToken)
	work := filepath.Join(t.TempDir(), "work")
	testutil.GitRun(t, "", "clone", bare, work)
	testutil.GitRun(t, work, "config", "user.email", "test@example.com")
	testutil.GitRun(t, work, "config", "user.name", "Test")
	testutil.GitRun(t, work, "checkout", "-b", "agent/issue-1")
	testutil.WriteFixture(t, work, "feature.txt", "feature\n")
	testutil.GitRun(t, work, "add", "feature.txt")
	testutil.GitRun(t, work, "commit", "-m", "feature")
	testutil.GitRun(t, work, "push", "origin", "agent/issue-1")
	testutil.GitRun(t, work, "checkout", "main")
	testutil.WriteFixture(t, work, "later.txt", "later\n")
	testutil.GitRun(t, work, "add", "later.txt")
	testutil.GitRun(t, work, "commit", "-m", "later")
	testutil.GitRun(t, work, "push", "origin", "main")
	return cleanURL, strings.Replace(cleanURL, "http://", "http://"+authUser+":"+authToken+"@", 1)
}

// traceGit records every git argv (subprocesses included) for the rest of the
// test and returns a func that reads the log.
func traceGit(t *testing.T) func() string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "trace.log")
	t.Setenv("GIT_TRACE", f)
	return func() string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read GIT_TRACE: %v", err)
		}
		return string(b)
	}
}

func assertNoUserinfoInTrace(t *testing.T, trace string) {
	t.Helper()
	if trace == "" {
		t.Fatal("GIT_TRACE is empty: the tracer saw no git invocation")
	}
	if strings.Contains(trace, authToken) {
		t.Errorf("git trace carries the token:\n%s", trace)
	}
	for _, line := range strings.Split(trace, "\n") {
		if strings.Contains(line, "http://") && strings.Contains(line, "@") {
			t.Errorf("traced git argv carries URL userinfo: %s", line)
		}
	}
}

func TestGitClient_UserinfoRemote_AttachesCredentialOutOfBand(t *testing.T) {
	_, remote := authFixture(t)
	trace := traceGit(t)
	g := NewGitClient(remote, "main", "Test Bot", "bot@example.com")

	if _, err := g.Probe(); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if ok, err := g.BranchExists("agent/issue-1"); err != nil || !ok {
		t.Fatalf("BranchExists = %v, %v; want true, nil", ok, err)
	}
	if err := g.Rebase("agent/issue-1"); err != nil {
		t.Fatalf("Rebase (clone, fetch, force-push): %v", err)
	}
	if err := g.Merge("agent/issue-1"); err != nil {
		t.Fatalf("Merge (clone, fetch, push): %v", err)
	}

	assertNoUserinfoInTrace(t, trace())
}

func TestGitClient_UserinfoRemote_ProbeReturnsCleanURL(t *testing.T) {
	clean, remote := authFixture(t)
	g := NewGitClient(remote, "main", "Test Bot", "bot@example.com")

	got, err := g.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != clean {
		t.Errorf("Probe = %q, want %q", got, clean)
	}
}

func TestGitClient_WrongToken_ErrorsDoNotLeakIt(t *testing.T) {
	clean, _ := authFixture(t)
	const wrong = "wrong-token-6860"
	remote := strings.Replace(clean, "http://", "http://"+authUser+":"+wrong+"@", 1)
	g := NewGitClient(remote, "main", "Test Bot", "bot@example.com")

	_, probeErr := g.Probe()
	_, existsErr := g.BranchExists("agent/issue-1")
	for name, err := range map[string]error{
		"Probe":        probeErr,
		"BranchExists": existsErr,
		"Merge":        g.Merge("agent/issue-1"),
		"Rebase":       g.Rebase("agent/issue-1"),
	} {
		if err == nil {
			t.Errorf("%s with a wrong token: want error, got nil", name)
			continue
		}
		if strings.Contains(err.Error(), wrong) {
			t.Errorf("%s error leaks the token: %v", name, err)
		}
	}
}

func TestCloneToTemp_PersistsNoCredentialInRepositoryConfig(t *testing.T) {
	clean, _ := authFixture(t)
	cred := gitexec.Credential{Username: authUser, Password: authToken}

	dir, _, cleanup, err := cloneToTemp(clean, cred, "spindrift-git-forge-test-*", time.Minute)
	if err != nil {
		t.Fatalf("cloneToTemp: %v", err)
	}
	defer cleanup()

	cfg, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), authToken) {
		t.Errorf(".git/config carries the token:\n%s", cfg)
	}
	if got := testutil.GitOutput(t, dir, "config", "--get", "remote.origin.url"); got != clean {
		t.Errorf("remote.origin.url = %q, want %q", got, clean)
	}
}

func TestGitClient_TokenAsUsernameRemote_AttachesCredentialOutOfBand(t *testing.T) {
	url, _ := gitexectest.AuthRemote(t, authToken, "")
	remote := strings.Replace(url, "http://", "http://"+authToken+"@", 1)
	trace := traceGit(t)
	g := NewGitClient(remote, "main", "Test Bot", "bot@example.com")

	got, err := g.Probe()
	if err != nil || got != url {
		t.Fatalf("Probe = %q, %v; want %q, nil", got, err, url)
	}
	assertNoUserinfoInTrace(t, trace())
}
