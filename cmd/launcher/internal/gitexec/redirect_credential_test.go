package gitexec

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/gitexec/gitexectest"
)

// lsRemoteScoped runs real git against target with the ambient helper chain
// reset and one inline helper registered only for scope.
func lsRemoteScoped(t *testing.T, scope, target string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", "ls-remote", target)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential."+scope+".helper",
		"GIT_CONFIG_VALUE_1=!f() { test \"$1\" = get && printf 'username="+testUser+"\\npassword="+testToken+"\\n'; }; f",
		"GIT_CONFIG_KEY_2=credential."+scope+".useHttpPath",
		"GIT_CONFIG_VALUE_2=true",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func authedPaths(r *gitexectest.Remote) []string {
	var paths []string
	for _, q := range r.Requests() {
		if q.HasAuth {
			paths = append(paths, q.Path)
		}
	}
	return paths
}

// Pins how real git matches a path-scoped credential helper: against the
// exact repository URL it was registered for, and not after an HTTP redirect
// to another path, nor for a sibling repository or another host.
func TestPathScopedHelperMatching(t *testing.T) {
	r := gitexectest.NewAuthRemote(t, testUser, testToken)
	elsewhere := gitexectest.NewAuthRemote(t, testUser, testToken)

	t.Run("exact URL authenticates", func(t *testing.T) {
		out, err := lsRemoteScoped(t, r.URL, r.URL)
		if err != nil {
			t.Fatalf("ls-remote %s: %v\n%s", r.URL, err, out)
		}
	})

	for _, c := range []struct {
		name, target string
		srv          *gitexectest.Remote
	}{
		{"sibling repository on the same host", r.OtherURL, r},
		{"repository on another host", elsewhere.URL, elsewhere},
	} {
		t.Run(c.name+" gets no credential", func(t *testing.T) {
			before := len(authedPaths(c.srv))
			out, err := lsRemoteScoped(t, r.URL, c.target)
			if err == nil {
				t.Fatalf("ls-remote %s succeeded under helper scoped to %s:\n%s", c.target, r.URL, out)
			}
			if got := authedPaths(c.srv)[before:]; len(got) != 0 {
				t.Fatalf("Authorization sent for %v:\n%s", got, out)
			}
		})
	}

	// git re-matches the helper against the redirect's destination, not the
	// URL the caller asked for: the scope must be the destination path.
	t.Run("helper scoped to the redirecting path is not consulted", func(t *testing.T) {
		before := len(authedPaths(r))
		out, err := lsRemoteScoped(t, r.MovedURL, r.MovedURL)
		if err == nil || !strings.Contains(out, "could not read Username") {
			t.Fatalf("ls-remote %s: err=%v, want a prompt-disabled failure:\n%s", r.MovedURL, err, out)
		}
		if got := authedPaths(r)[before:]; len(got) != 0 {
			t.Fatalf("Authorization sent for %v:\n%s", got, out)
		}
	})

	t.Run("helper scoped to the redirect destination authenticates", func(t *testing.T) {
		out, err := lsRemoteScoped(t, r.URL, r.MovedURL)
		if err != nil {
			t.Fatalf("ls-remote %s: %v\n%s", r.MovedURL, err, out)
		}
		if !strings.Contains(out, "redirecting to "+r.URL) {
			t.Fatalf("git did not report the redirect:\n%s", out)
		}
	})

	// Through Apply, git must not follow a redirect at all, whether the old path
	// 301s before it authenticates or after: the destination path never sees the
	// Authorization header, and Explain names the configured Target repo.
	for _, c := range []struct{ name, target string }{
		{"redirect before authentication", r.MovedURL},
		{"redirect after authentication", r.MovedAfterAuthURL},
	} {
		t.Run(c.name+" is not followed and fails naming the Target repo", func(t *testing.T) {
			before := len(r.Requests())
			cred := Credential{Username: testUser, Password: testToken}
			out, err := cred.Apply(Cmd(nil, "ls-remote", c.target), c.target).CombinedOutput()
			t.Logf("git output: %s", out)
			if err == nil {
				t.Fatalf("ls-remote %s succeeded:\n%s", c.target, out)
			}
			if got := cred.Explain(err, out, c.target).Error(); !strings.Contains(got, "configured Target repo "+c.target) {
				t.Errorf("Explain = %q, want it to name the configured Target repo", got)
			}
			for _, q := range r.Requests()[before:] {
				if q.HasAuth && strings.HasPrefix(q.Path, "/repo.git/") {
					t.Errorf("Authorization reached the redirect destination: %v", r.Requests()[before:])
				}
			}
		})
	}
}
