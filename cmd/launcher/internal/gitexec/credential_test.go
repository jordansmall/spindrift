package gitexec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/gitexec/gitexectest"
)

const (
	testUser  = "ci-bot"
	testToken = "s3cr3t-tok3n-d0-not-leak"
)

func TestSplitCredential(t *testing.T) {
	cases := []struct {
		name, in, wantURL string
		want              Credential
	}{
		{"https user and password", "https://u:p%40ss@forge.example/o/r.git", "https://forge.example/o/r.git", Credential{"u", "p@ss"}},
		{"http user only", "http://tok@forge.example:3000/o/r.git", "http://forge.example:3000/o/r.git", Credential{Username: "tok"}},
		{"https no userinfo", "https://forge.example/o/r.git", "https://forge.example/o/r.git", Credential{}},
		{"local path", "/srv/git/r.git", "/srv/git/r.git", Credential{}},
		{"relative path with colon-free name", "../r.git", "../r.git", Credential{}},
		{"file url", "file:///srv/git/r.git", "file:///srv/git/r.git", Credential{}},
		{"ssh url", "ssh://git@forge.example/o/r.git", "ssh://git@forge.example/o/r.git", Credential{}},
		{"scp style", "git@forge.example:o/r.git", "git@forge.example:o/r.git", Credential{}},
		{"unparseable", "https://u:p@for ge/%zz", "https://u:p@for ge/%zz", Credential{}},
		{"empty", "", "", Credential{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotURL, got := SplitCredential(c.in)
			if gotURL != c.wantURL || got != c.want {
				t.Fatalf("SplitCredential(%q) = (%q, %v), want (%q, %v)", c.in, gotURL, got, c.wantURL, c.want)
			}
		})
	}
}

// An ambient helper must neither store the token (git runs `credential approve`
// on every helper after success) nor see it on `get`, whichever field carries
// it.
func TestAmbientHelpersNeverSeeToken(t *testing.T) {
	// Each run gets its own dir so one run's writes cannot reach another's
	// assertions.
	ambient := func(remote string) (*exec.Cmd, []string) {
		dir := t.TempDir()
		stored, seen := filepath.Join(dir, "creds"), filepath.Join(dir, "seen")
		cmd := Cmd(nil, "ls-remote", remote)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=credential.helper",
			"GIT_CONFIG_VALUE_0=store --file="+stored,
			"GIT_CONFIG_KEY_1=credential.helper",
			"GIT_CONFIG_VALUE_1=!cat >> "+seen+" #",
		)
		return cmd, []string{stored, seen}
	}

	// Control: the same helpers, a user:password URL, no Apply. They must run
	// and see the token, or the assertions below would pass vacuously. A
	// token-as-username URL cannot serve: curl sends it without asking them.
	url, _ := gitexectest.AuthRemote(t, testUser, testToken)
	control, controlFiles := ambient(strings.Replace(url, "://", "://"+testUser+":"+testToken+"@", 1))
	if out, err := control.CombinedOutput(); err != nil {
		t.Fatalf("control ls-remote: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(controlFiles[1]); err != nil || !strings.Contains(string(b), testToken) {
		t.Fatalf("control: ambient helper never saw the token (%v): %q", err, b)
	}

	for _, c := range []struct{ name, user, pass, userinfo string }{
		{"user and password", testUser, testToken, testUser + ":" + testToken + "@"},
		{"token as username", testToken, "", testToken + "@"},
	} {
		t.Run(c.name, func(t *testing.T) {
			url, _ := gitexectest.AuthRemote(t, c.user, c.pass)
			cleaned, cred := SplitCredential(strings.Replace(url, "://", "://"+c.userinfo, 1))
			cmd, files := ambient(cleaned)
			out, err := cred.Apply(cmd).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "refs/heads/main") {
				t.Fatalf("ls-remote: %v\n%s", err, out)
			}
			for _, f := range files {
				if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), testToken) {
					t.Fatalf("ambient helper received the token via %s:\n%s", f, b)
				}
			}
		})
	}
}

// A token carried as the username (https://<token>@host) leaves the URL and
// still authenticates with an empty password, surfacing in no argv or trace.
func TestTokenAsUsernameAuthenticatesWithoutUserinfo(t *testing.T) {
	url, _ := gitexectest.AuthRemote(t, testToken, "")
	tokenURL := strings.Replace(url, "://", "://"+testToken+"@", 1)

	cleaned, cred := SplitCredential(tokenURL)
	if strings.Contains(cleaned, testToken) {
		t.Fatalf("cleaned URL still carries the token: %q", cleaned)
	}
	tmp := t.TempDir()
	trace := filepath.Join(tmp, "trace")
	clone := filepath.Join(tmp, "clone")
	cmd := cred.Apply(Cmd(nil, "clone", cleaned, clone))
	cmd.Env = append(cmd.Env, "GIT_TRACE="+trace)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), testToken) {
		t.Fatalf("token in argv: %v", cmd.Args)
	}
	for _, f := range []string{filepath.Join(clone, ".git", "config"), trace} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), testToken) {
			t.Fatalf("token in %s:\n%s", f, b)
		}
	}
	ls := cred.Apply(Cmd(nil, "ls-remote", cleaned))
	if out, err := ls.CombinedOutput(); err != nil || !strings.Contains(string(out), "refs/heads/main") {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
}

func TestCredentialZeroApplyIsNoOp(t *testing.T) {
	cmd := exec.Command("git", "status")
	cmd.Env = []string{"A=1"}
	if got := (Credential{}).Apply(cmd); got != cmd || len(cmd.Env) != 1 {
		t.Fatalf("zero Credential changed the command: env=%v", cmd.Env)
	}
	nilEnv := exec.Command("git", "status")
	(Credential{}).Apply(nilEnv)
	if nilEnv.Env != nil {
		t.Fatalf("zero Credential set Env: %v", nilEnv.Env)
	}
}

func TestCredentialDoesNotFormatPassword(t *testing.T) {
	c := Credential{testUser, testToken}
	for _, s := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprint(&c)} {
		if strings.Contains(s, testToken) {
			t.Fatalf("formatted Credential leaks the password: %s", s)
		}
	}
}

func TestCredentialApplyExtendsGitConfigCount(t *testing.T) {
	cmd := exec.Command("git", "status")
	cmd.Env = []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=ambient.key", "GIT_CONFIG_VALUE_0=ambient"}
	Credential{testUser, testToken}.Apply(cmd)

	env := lastEnv(cmd.Env)
	if env["GIT_CONFIG_COUNT"] != "3" {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want 3", env["GIT_CONFIG_COUNT"])
	}
	if env["GIT_CONFIG_KEY_0"] != "ambient.key" || env["GIT_CONFIG_VALUE_0"] != "ambient" {
		t.Fatalf("ambient entry 0 not preserved: %v", env)
	}
	if env["GIT_CONFIG_KEY_1"] != "credential.helper" || env["GIT_CONFIG_VALUE_1"] != "" {
		t.Fatalf("entry 1 must reset helpers: %q=%q", env["GIT_CONFIG_KEY_1"], env["GIT_CONFIG_VALUE_1"])
	}
	if env["GIT_CONFIG_KEY_2"] != "credential.helper" || !strings.HasPrefix(env["GIT_CONFIG_VALUE_2"], "!") {
		t.Fatalf("entry 2 must be the env-reading helper: %v", env)
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q", env["GIT_TERMINAL_PROMPT"])
	}
}

func TestCredentialApplyAmbientCountSurvivesRealGit(t *testing.T) {
	cmd := exec.Command("git", "config", "--get", "ambient.key")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=ambient.key", "GIT_CONFIG_VALUE_0=kept")
	Credential{testUser, testToken}.Apply(cmd)
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "kept" {
		t.Fatalf("ambient config lost: %q %v", out, err)
	}
}

func TestCredentialAuthenticatesGit(t *testing.T) {
	url, bare := gitexectest.AuthRemote(t, testUser, testToken)
	cred := Credential{testUser, testToken}
	gitArgs := []string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}

	run := func(c Credential, dir string, args ...string) (string, error) {
		cmd := c.Apply(Cmd(gitArgs, args...))
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("without credential fails", func(t *testing.T) {
		if out, err := run(Credential{}, "", "ls-remote", url); err == nil {
			t.Fatalf("ls-remote without credential succeeded: %s", out)
		}
		if out, err := run(Credential{testUser, "wrong"}, "", "ls-remote", url); err == nil {
			t.Fatalf("ls-remote with wrong password succeeded: %s", out)
		}
	})

	t.Run("ls-remote", func(t *testing.T) {
		out, err := run(cred, "", "ls-remote", url)
		if err != nil || !strings.Contains(out, "refs/heads/main") {
			t.Fatalf("ls-remote: %v\n%s", err, out)
		}
	})

	t.Run("clone and push leave no token behind", func(t *testing.T) {
		tmp := t.TempDir()
		trace := filepath.Join(tmp, "trace")
		clone := filepath.Join(tmp, "clone")

		cmds := []*exec.Cmd{
			cred.Apply(Cmd(gitArgs, "clone", url, clone)),
		}
		cmds[0].Env = append(cmds[0].Env, "GIT_TRACE="+trace)
		runAll(t, cmds)

		commit := Cmd(gitArgs, "-C", clone, "commit", "--allow-empty", "-m", "change")
		push := cred.Apply(Cmd(gitArgs, "-C", clone, "push", "origin", "main"))
		push.Env = append(push.Env, "GIT_TRACE="+trace)
		runAll(t, []*exec.Cmd{commit, push})

		for _, cmd := range []*exec.Cmd{cmds[0], push} {
			if strings.Contains(strings.Join(cmd.Args, " "), testToken) {
				t.Fatalf("token in argv: %v", cmd.Args)
			}
		}
		for _, f := range []string{filepath.Join(clone, ".git", "config"), trace} {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), testToken) {
				t.Fatalf("token in %s:\n%s", f, b)
			}
		}
		urlOut, err := exec.Command("git", "-C", clone, "config", "--get", "remote.origin.url").Output()
		if err != nil || strings.TrimSpace(string(urlOut)) != url {
			t.Fatalf("remote.origin.url = %q (%v), want %q", urlOut, err, url)
		}

		head, err := exec.Command("git", "-C", bare, "log", "-1", "--format=%s", "main").Output()
		if err != nil || strings.TrimSpace(string(head)) != "change" {
			t.Fatalf("push did not land: %q %v", head, err)
		}
	})
}

func runAll(t *testing.T, cmds []*exec.Cmd) {
	t.Helper()
	for _, cmd := range cmds {
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", cmd.Args, err, out)
		}
	}
}

func lastEnv(env []string) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}
