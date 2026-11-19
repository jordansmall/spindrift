package boxclone_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/boxclone"
	"spindrift.dev/launcher/internal/testutil"
)

type fixture struct {
	origin, work, globalCfg, ghLog string
	knobs                          map[string]string
	cfg                            boxclone.Config
}

// newFixture seeds a bare origin with one commit on main, a hermetic global
// git config holding a "Seed" identity, and a fake gh on PATH that records its
// argv to ghLog.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		knobs:     map[string]string{},
		origin:    filepath.Join(root, "origin.git"),
		work:      filepath.Join(root, "work"),
		globalCfg: filepath.Join(root, "gitconfig"),
		ghLog:     filepath.Join(root, "gh.log"),
	}
	if err := os.WriteFile(f.globalCfg, []byte("[user]\n\tname = Seed\n\temail = seed@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", f.globalCfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	seed := filepath.Join(root, "seed")
	testutil.GitRun(t, root, "init", "-q", "--bare", "-b", "main", f.origin)
	testutil.GitRun(t, root, "init", "-q", "-b", "main", seed)
	testutil.GitRun(t, seed, "config", "user.name", "Test")
	testutil.GitRun(t, seed, "config", "user.email", "t@example.com")
	if err := os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.GitRun(t, seed, "add", "base.txt")
	testutil.GitRun(t, seed, "commit", "-q", "-m", "base")
	testutil.GitRun(t, seed, "push", "-q", f.origin, "main")

	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	gh := "#!/bin/sh\necho \"$@\" >> " + f.ghLog + "\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	// Set only after seeding: it also makes git distrust the seed repo.
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	f.cfg = boxclone.Config{
		CodeForge: "github", RepoSlug: "o/r", RepoMountDir: f.origin, WorkDir: f.work,
		GitUserName: "Box Agent", GitUserEmail: "box@example.com",
	}
	f.cfg.Lookup = mapLookup(f.knobs)
	return f
}

// mapLookup serves m live, so a test can set knobs after building the fixture.
func mapLookup(m map[string]string) backendkit.Lookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func (f *fixture) ghCalls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.ghLog)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func (f *fixture) originURL(t *testing.T) string {
	t.Helper()
	return testutil.GitOutput(t, f.work, "config", "--get", "remote.origin.url")
}
func TestCloneLocalClonesMountSkipsGhAndTrustsBothPaths(t *testing.T) {
	f := newFixture(t)
	f.cfg.CodeForge = "local"
	if err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := f.originURL(t); got != f.origin {
		t.Errorf("origin url = %q, want %q", got, f.origin)
	}
	if got := testutil.GitOutput(t, f.work, "remote"); got != "origin" {
		t.Errorf("remotes = %q, want only origin (local has no forge URL)", got)
	}
	if got := f.ghCalls(t); got != "" {
		t.Errorf("gh was invoked: %q", got)
	}
	safe := testutil.GitOutput(t, filepath.Dir(f.work), "config", "--file", f.globalCfg, "--get-all", "safe.directory")
	for _, want := range []string{f.origin, f.work} {
		if !strings.Contains(safe, want) {
			t.Errorf("safe.directory %q missing %q", safe, want)
		}
	}
	testutil.GitRun(t, f.work, "rev-parse", "--verify", "refs/remotes/origin/main")
}

func TestCloneSetsIdentityRepoLocallyAndLeavesGlobalConfigAlone(t *testing.T) {
	f := newFixture(t)
	before, err := os.ReadFile(f.globalCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.GitOutput(t, f.work, "config", "--local", "user.name"); got != "Box Agent" {
		t.Errorf("local user.name = %q", got)
	}
	if got := testutil.GitOutput(t, f.work, "config", "--local", "user.email"); got != "box@example.com" {
		t.Errorf("local user.email = %q", got)
	}
	// safe.directory is the one global entry the clone adds; the identity
	// must stay out of the global file.
	after, err := os.ReadFile(f.globalCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) || bytes.Contains(bytes.TrimPrefix(after, before), []byte("user")) {
		t.Errorf("global config gained more than safe.directory:\n%s", after)
	}
}

func TestCloneGitFailureGoesToStderrNotStdout(t *testing.T) {
	f := newFixture(t)
	f.cfg.RepoMountDir = filepath.Join(t.TempDir(), "missing.git")
	var out, errOut bytes.Buffer
	if err := boxclone.Clone(f.cfg, &out, &errOut); err == nil {
		t.Fatal("Clone of a missing repo succeeded")
	}
	if !strings.Contains(errOut.String(), "fatal:") {
		t.Errorf("git's failure missing from stderr: %q", errOut.String())
	}
	if strings.Contains(out.String(), "fatal:") {
		t.Errorf("git's failure leaked to stdout: %q", out.String())
	}
	if !strings.Contains(out.String(), "==> cloning") {
		t.Errorf("banner missing from stdout: %q", out.String())
	}
}

// The mirror clone needs neither the forge's remote URL nor a credential: a
// git Box has no GH_TOKEN to authenticate with (#6898).
func TestCloneIgnoresForgeRemoteURLAndSkipsGh(t *testing.T) {
	f := newFixture(t)
	f.cfg.CodeForge = "git"
	f.knobs["CODE_FORGE_REMOTE_URL"] = "https://unreachable.invalid/o/r.git"
	if err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := f.originURL(t); got != f.origin {
		t.Errorf("origin url = %q, want the mirror %q", got, f.origin)
	}
	if got := f.ghCalls(t); got != "" {
		t.Errorf("gh was invoked: %q", got)
	}
}

func TestCloneNeedsNoForgeCredential(t *testing.T) {
	for _, forge := range []string{"github", "forgejo"} {
		t.Run(forge, func(t *testing.T) {
			f := newFixture(t)
			// No FORGEJO_TOKEN is set: a clone that reached the forge URL
			// would have nothing redirecting it to origin.
			f.cfg.CodeForge = forge
			var out bytes.Buffer
			if err := boxclone.Clone(f.cfg, &out, &out); err != nil {
				t.Fatalf("Clone: %v\n%s", err, out.String())
			}
			if got := f.originURL(t); got != f.origin {
				t.Errorf("origin url = %q, want the mirror %q", got, f.origin)
			}
			if got := f.ghCalls(t); got != "" {
				t.Errorf("gh was invoked: %q", got)
			}
			safe := testutil.GitOutput(t, filepath.Dir(f.work), "config", "--file", f.globalCfg, "--get-all", "safe.directory")
			for _, want := range []string{f.origin, f.work} {
				if !strings.Contains(safe, want) {
					t.Errorf("safe.directory %q missing %q", safe, want)
				}
			}
			testutil.GitRun(t, f.work, "rev-parse", "--verify", "refs/remotes/origin/main")
			want := map[string]string{"github": "https://github.com/o/r", "forgejo": "https://forgejo.invalid/o/r"}[forge]
			if got := testutil.GitOutput(t, f.work, "remote", "get-url", "forge"); got != want {
				t.Errorf("forge remote url = %q, want the credential-free %q", got, want)
			}
			if got := testutil.GitOutput(t, f.work, "config", "--bool", "remote.forge.skipFetchAll"); got != "true" {
				t.Errorf("remote.forge.skipFetchAll = %q, want true", got)
			}
			// A branch tracking origin, as branch recovery leaves it: gh finds its PR
			// through the push remote, which must still be forge.
			testutil.GitRun(t, f.work, "checkout", "-b", "feat", "origin/main")
			if got := testutil.GitOutput(t, f.work, "for-each-ref", "--format=%(push:remotename)", "refs/heads/feat"); got != "forge" {
				t.Errorf("push remote of a branch tracking origin = %q, want forge", got)
			}
		})
	}
}
