package boxclone_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/boxclone"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct {
	origin, work, globalCfg, ghLog string
	cfg                            boxclone.Config
}

// newFixture seeds a bare origin with one commit on main, a hermetic global
// git config holding a "Seed" identity, and a fake gh on PATH that records its
// argv to ghLog.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
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
	git(t, root, "init", "-q", "--bare", "-b", "main", f.origin)
	git(t, root, "init", "-q", "-b", "main", seed)
	git(t, seed, "config", "user.name", "Test")
	git(t, seed, "config", "user.email", "t@example.com")
	if err := os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "base.txt")
	git(t, seed, "commit", "-q", "-m", "base")
	git(t, seed, "push", "-q", f.origin, "main")

	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	gh := "#!/bin/sh\necho \"$@\" >> " + f.ghLog + "\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	f.cfg = boxclone.Config{
		CodeForge: "github", RepoSlug: "o/r", WorkDir: f.work,
		GitUserName: "Box Agent", GitUserEmail: "box@example.com",
	}
	return f
}

// redirect makes git rewrite urlPrefix to the local origin, so a clone only
// succeeds when the Box built exactly that URL.
func (f *fixture) redirect(t *testing.T, urlPrefix string) {
	t.Helper()
	git(t, filepath.Dir(f.work), "config", "--file", f.globalCfg, "url."+f.origin+".insteadOf", urlPrefix)
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
	return git(t, f.work, "config", "--get", "remote.origin.url")
}

func TestCloneGithubDefaultClonesGithubURLAndSetsUpGhCredentials(t *testing.T) {
	f := newFixture(t)
	f.redirect(t, "https://github.com/o/r.git")
	var out bytes.Buffer
	if err := boxclone.Clone(f.cfg, &out, &out); err != nil {
		t.Fatalf("Clone: %v\n%s", err, out.String())
	}
	if got := f.originURL(t); got != "https://github.com/o/r.git" {
		t.Errorf("origin url = %q", got)
	}
	if got := f.ghCalls(t); got != "auth setup-git" {
		t.Errorf("gh argv = %q, want %q", got, "auth setup-git")
	}
	git(t, f.work, "rev-parse", "--verify", "refs/remotes/origin/main")
}

func TestCloneGitForgeClonesFromRemoteURL(t *testing.T) {
	f := newFixture(t)
	f.redirect(t, "https://git.example.test/o/r.git")
	f.cfg.CodeForge = "git"
	f.cfg.RemoteURL = "https://git.example.test/o/r.git"
	if err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := f.originURL(t); got != f.cfg.RemoteURL {
		t.Errorf("origin url = %q, want %q", got, f.cfg.RemoteURL)
	}
}

func TestCloneGitForgeRequiresRemoteURL(t *testing.T) {
	f := newFixture(t)
	f.cfg.CodeForge = "git"
	err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "CODE_FORGE_REMOTE_URL is required when CODE_FORGE=git") {
		t.Fatalf("err = %v", err)
	}
}

func TestCloneGithubIgnoresStrayRemoteURL(t *testing.T) {
	f := newFixture(t)
	f.redirect(t, "https://github.com/o/r.git")
	f.cfg.RemoteURL = "https://stray.example.test/x.git"
	if err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := f.originURL(t); got != "https://github.com/o/r.git" {
		t.Errorf("origin url = %q, want the github URL", got)
	}
}

func TestCloneForgejoEmbedsTokenAndSkipsGh(t *testing.T) {
	f := newFixture(t)
	f.redirect(t, "https://tok123@forge.example.test/o/r.git")
	f.cfg.CodeForge = "forgejo"
	f.cfg.ForgejoToken = "tok123"
	f.cfg.ForgejoBaseURL = "https://forge.example.test/"
	var out bytes.Buffer
	if err := boxclone.Clone(f.cfg, &out, &out); err != nil {
		t.Fatalf("Clone: %v\n%s", err, out.String())
	}
	if got := f.originURL(t); got != "https://tok123@forge.example.test/o/r.git" {
		t.Errorf("origin url = %q", got)
	}
	if got := f.ghCalls(t); got != "" {
		t.Errorf("gh was invoked: %q", got)
	}
}

func TestCloneForgejoDefaultsToCodeberg(t *testing.T) {
	f := newFixture(t)
	f.redirect(t, "https://tok123@codeberg.org/o/r.git")
	f.cfg.CodeForge = "forgejo"
	f.cfg.ForgejoToken = "tok123"
	if err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := f.originURL(t); got != "https://tok123@codeberg.org/o/r.git" {
		t.Errorf("origin url = %q", got)
	}
}

func TestCloneForgejoRequiresToken(t *testing.T) {
	f := newFixture(t)
	f.cfg.CodeForge = "forgejo"
	err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "FORGEJO_TOKEN is required when CODE_FORGE=forgejo") {
		t.Fatalf("err = %v", err)
	}
}

func TestCloneLocalClonesMountSkipsGhAndTrustsBothPaths(t *testing.T) {
	f := newFixture(t)
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
	f.cfg.CodeForge = "local"
	f.cfg.RepoMountDir = f.origin
	if err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := f.originURL(t); got != f.origin {
		t.Errorf("origin url = %q, want %q", got, f.origin)
	}
	if got := f.ghCalls(t); got != "" {
		t.Errorf("gh was invoked: %q", got)
	}
	safe := git(t, filepath.Dir(f.work), "config", "--file", f.globalCfg, "--get-all", "safe.directory")
	for _, want := range []string{f.origin, f.work} {
		if !strings.Contains(safe, want) {
			t.Errorf("safe.directory %q missing %q", safe, want)
		}
	}
}

func TestCloneSetsIdentityRepoLocallyAndLeavesGlobalConfigAlone(t *testing.T) {
	for _, forge := range []string{"github", "git", "forgejo"} {
		t.Run(forge, func(t *testing.T) {
			f := newFixture(t)
			f.cfg.CodeForge = forge
			switch forge {
			case "github":
				f.redirect(t, "https://github.com/o/r.git")
			case "git":
				f.cfg.RemoteURL = "https://git.example.test/o/r.git"
				f.redirect(t, f.cfg.RemoteURL)
			case "forgejo":
				f.cfg.ForgejoToken = "tok"
				f.redirect(t, "https://tok@codeberg.org/o/r.git")
			}
			before, err := os.ReadFile(f.globalCfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := boxclone.Clone(f.cfg, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if got := git(t, f.work, "config", "--local", "user.name"); got != "Box Agent" {
				t.Errorf("local user.name = %q", got)
			}
			if got := git(t, f.work, "config", "--local", "user.email"); got != "box@example.com" {
				t.Errorf("local user.email = %q", got)
			}
			after, err := os.ReadFile(f.globalCfg)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("global config changed:\n%s", after)
			}
		})
	}
}

func TestCloneBannerRedactsTokenBearingURL(t *testing.T) {
	f := newFixture(t)
	f.redirect(t, "https://sekrit@codeberg.org/o/r.git")
	f.cfg.CodeForge = "forgejo"
	f.cfg.ForgejoToken = "sekrit"
	var out, errOut bytes.Buffer
	if err := boxclone.Clone(f.cfg, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "==> cloning https://codeberg.org/o/r.git") {
		t.Errorf("banner missing: %q", out.String())
	}
	if strings.Contains(out.String()+errOut.String(), "sekrit") {
		t.Errorf("token leaked: stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestCloneGitFailureGoesToStderrNotStdout(t *testing.T) {
	f := newFixture(t)
	f.cfg.CodeForge = "git"
	f.cfg.RemoteURL = filepath.Join(t.TempDir(), "missing.git")
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

func TestCloneURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  boxclone.Config
		want string
	}{
		{"github", boxclone.Config{CodeForge: "github", RepoSlug: "o/r"}, "https://github.com/o/r.git"},
		{"empty forge is github", boxclone.Config{RepoSlug: "o/r"}, "https://github.com/o/r.git"},
		{"git", boxclone.Config{CodeForge: "git", RemoteURL: "ssh://h/x.git"}, "ssh://h/x.git"},
		{"forgejo default base", boxclone.Config{CodeForge: "forgejo", RepoSlug: "o/r", ForgejoToken: "t"}, "https://t@codeberg.org/o/r.git"},
		{"forgejo trims slash", boxclone.Config{CodeForge: "forgejo", RepoSlug: "o/r", ForgejoToken: "t", ForgejoBaseURL: "http://f.test:3000/"}, "http://t@f.test:3000/o/r.git"},
		{"local", boxclone.Config{CodeForge: "local", RepoMountDir: "/mnt/repo"}, "/mnt/repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := boxclone.CloneURL(tc.cfg)
			if err != nil || got != tc.want {
				t.Errorf("CloneURL = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
