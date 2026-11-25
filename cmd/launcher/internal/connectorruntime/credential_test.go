package connectorruntime_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/credsource"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
)

// adapter is one way a row's credential is sourced, with how a test rotates it.
type adapter struct {
	name   string
	source func(t *testing.T, initial string) (src credsource.Source, rotate func(next string))
}

var adapters = []adapter{
	{"static", func(t *testing.T, initial string) (credsource.Source, func(string)) {
		return credsource.Static(initial), nil
	}},
	{"file", func(t *testing.T, initial string) (credsource.Source, func(string)) {
		path := filepath.Join(t.TempDir(), "token")
		write := func(v string) {
			t.Helper()
			if err := os.WriteFile(path, []byte(v+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write(initial)
		return credsource.File(path, nil), write
	}},
	{"command", func(t *testing.T, initial string) (credsource.Source, func(string)) {
		var current atomic.Value
		current.Store(initial)
		c, err := credsource.NewCommand("FIXTURE_TOKEN", "vault get tok", func(string) (string, error) { return current.Load().(string), nil })
		if err != nil {
			t.Fatal(err)
		}
		return c, func(v string) {
			current.Store(v)
			if err := c.Refresh(); err != nil {
				t.Fatal(err)
			}
		}
	}},
}

func (e *env) lastAuth() string {
	a, _ := e.auth.Load().(string)
	return a
}

// gitPassword is the password a git run under cred carries.
func gitPassword(t *testing.T, cred gitexec.Credential) string {
	t.Helper()
	for _, kv := range cred.Apply(exec.Command("git", "status"), "https://forge.example/o/r.git").Env {
		if v, ok := strings.CutPrefix(kv, "GITEXEC_PASSWORD="); ok {
			return v
		}
	}
	return ""
}

// A runtime built over any credential adapter authorizes Connector requests
// with its source's token and never the process environment's.
func TestRuntimeOverEachAdapterAuthorizesWithItsSource(t *testing.T) {
	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			t.Setenv("FIXTURE_TOKEN", "from-the-environment")
			src, _ := a.source(t, "tok-one")
			e := newEnvWith(t, "tracker.wasm", map[string]credsource.Row{"fixture": {Secret: src}})
			if err := readFirst(t, e.tracker(t)); err != nil {
				t.Fatal(err)
			}
			if got := e.lastAuth(); got != "Bearer tok-one" {
				t.Errorf("Authorization = %q, want %q", got, "Bearer tok-one")
			}
		})
	}
}

// A token rotated in a source reaches the next Connector request and the next
// core git operation, through one runtime whose Connector and Code Forge are
// already built.
func TestRotatedTokenReachesTheNextConnectorRequestAndGitCredential(t *testing.T) {
	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			src, rotate := a.source(t, "tok-one")
			if rotate == nil {
				t.Skip("a static source does not rotate")
			}
			e := newEnvWith(t, "forge.wasm", map[string]credsource.Row{"fixture": {Secret: src}})
			var cred gitexec.Credential
			spec := e.forgeSpec()
			spec.Plumbing = func(_ string, c gitexec.Credential) forge.CodeForge { cred = c; return nil }
			cf, err := e.rt.CodeForge(e.row, spec)
			if err != nil {
				t.Fatal(err)
			}
			gitCred := e.rt.GitCredential(e.row)

			if err := readMain(t, cf); err != nil {
				t.Fatal(err)
			}
			first, err := gitCred.Get()
			if got := e.lastAuth(); got != "Bearer tok-one" || err != nil || first.Password != "tok-one" || gitPassword(t, cred) != "tok-one" {
				t.Fatalf("before the rotation: Authorization %q, GitCredential (%v, %v), composed git password %q; want tok-one throughout", got, first, err, gitPassword(t, cred))
			}

			rotate("tok-two")
			if err := readMain(t, cf); err != nil {
				t.Fatal(err)
			}
			second, err := gitCred.Get()
			if got := e.lastAuth(); got != "Bearer tok-two" || err != nil || second.Password != "tok-two" || gitPassword(t, cred) != "tok-two" {
				t.Errorf("after the rotation: Authorization %q, GitCredential (%v, %v), composed git password %q; want tok-two throughout", got, second, err, gitPassword(t, cred))
			}
		})
	}
}

// An unset credential fails core's git by name, as it does the request, and a
// row without a source is unset whatever the process environment holds.
func TestGitCredentialOfAnUnsetRowNamesTheEnv(t *testing.T) {
	t.Setenv("FIXTURE_TOKEN", "from-the-environment")
	for name, creds := range map[string]map[string]credsource.Row{
		"no source":    nil,
		"empty source": {"fixture": {Secret: credsource.Static("")}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnvWith(t, "tracker.wasm", creds)
			cred, err := e.rt.GitCredential(e.row).Get()
			if err == nil || !strings.Contains(err.Error(), "FIXTURE_TOKEN is not set") || cred != (gitexec.Credential{}) {
				t.Errorf("GitCredential = (%v, %v), want the zero credential and an error naming FIXTURE_TOKEN", cred, err)
			}
			if err := readFirst(t, mustTracker(t, e)); err == nil || !strings.Contains(err.Error(), "FIXTURE_TOKEN is not set") {
				t.Errorf("request with an unset credential = %v, want one naming FIXTURE_TOKEN", err)
			}
		})
	}
}

func mustTracker(t *testing.T, e *env) forge.IssueTracker {
	t.Helper()
	tr, err := e.rt.Tracker(e.row, e.spec)
	if err != nil {
		t.Fatalf("Tracker: %v", err)
	}
	return tr
}

// core's git presents a placeholder username with the token, whichever forge
// the row names.
func TestGitCredentialCarriesThePlaceholderUsername(t *testing.T) {
	e := newEnvWith(t, "forge.wasm", map[string]credsource.Row{"fixture": {Secret: credsource.Static("tok")}})
	cred, err := e.rt.GitCredential(e.row).Get()
	if err != nil || cred.Username != "x-access-token" || cred.Password != "tok" {
		t.Errorf("GitCredential = (%v, %v), want x-access-token with the token", cred, err)
	}
}
