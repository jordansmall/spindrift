package rowcredential

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
)

const env = "SHADOW_TEST_TOKEN"

func row(name, credEnv string) backendrows.Row {
	return backendrows.Row{Name: name, CredentialEnv: credEnv}
}

// routed registers github, jira and forgejo as ConnectorRouted and "plain" as
// not, so Reads does not depend on which backends the build links.
func routed(t *testing.T) {
	t.Helper()
	var ss []backendkit.Strategy
	for _, n := range []string{"github", "jira", "forgejo"} {
		ss = append(ss, backendkit.Strategy{Descriptor: backend.Descriptor{Name: n}, ConnectorRouted: true})
	}
	ss = append(ss, backendkit.Strategy{Descriptor: backend.Descriptor{Name: "plain"}})
	t.Cleanup(backendkit.ReplaceRegistryForTest(ss))
}

// runner records each command and answers from out, so a test asserts what ran.
type runner struct {
	out   map[string]string
	fail  map[string]bool
	calls []string
}

func (r *runner) run(cmd string) (string, error) {
	r.calls = append(r.calls, cmd)
	if r.fail[cmd] {
		return "", errors.New("boom " + cmd)
	}
	return r.out[cmd] + "\n", nil
}

func unset(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		t.Setenv(n, "")
		t.Setenv(n+"_CMD", "")
	}
}

func TestReads(t *testing.T) {
	routed(t)
	cases := []struct {
		name, row, forge, tracker string
		want                      bool
	}{
		{"forge row", "github", "github", "jira", true},
		{"tracker row", "jira", "github", "jira", true},
		{"neither", "forgejo", "github", "jira", false},
		{"not connector routed", "plain", "plain", "plain", false},
		{"unknown backend", "nope", "nope", "nope", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Reads(row(c.row, env), c.forge, c.tracker); got != c.want {
				t.Fatalf("Reads = %v, want %v", got, c.want)
			}
		})
	}
}

func lookupOf(m map[string]string) func(string) string {
	return func(e string) string { return m[e] }
}

func TestSources(t *testing.T) {
	routed(t)
	tmpl := "vault get {name}"
	cases := []struct {
		name      string
		vals      map[string]string
		applied   func(string) bool
		out       map[string]string
		want      string
		wantCmds  int
		wantCalls []string
	}{
		{"_CMD becomes a command source", map[string]string{env + "_CMD": "vault get tok"}, nil, map[string]string{"vault get tok": "secret"}, "secret", 1, []string{"vault get tok"}},
		{"_CMD wins over a plain value", map[string]string{env: "plain", env + "_CMD": "vault get tok"}, nil, map[string]string{"vault get tok": "secret"}, "secret", 1, []string{"vault get tok"}},
		{"plain value is static and runs nothing", map[string]string{env: "plain"}, nil, nil, "plain", 0, nil},
		{"template fills an unset env", nil, nil, map[string]string{"vault get shadow-test-token": "templated"}, "templated", 1, []string{"vault get shadow-test-token"}},
		{"applied set value is final", map[string]string{env: "applied", env + "_CMD": "vault get tok"}, func(e string) bool { return e == env }, nil, "applied", 0, nil},
		{"applied unset env takes the template, not its _CMD", map[string]string{env + "_CMD": "vault get tok"}, func(e string) bool { return e == env }, map[string]string{"vault get shadow-test-token": "templated"}, "templated", 1, []string{"vault get shadow-test-token"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unset(t, env)
			r := &runner{out: c.out}
			res := Resolution{Forge: "github", Template: tmpl, Run: r.run, Lookup: lookupOf(c.vals), Applied: c.applied}
			got, err := res.Sources([]backendrows.Row{row("github", env)})
			if err != nil {
				t.Fatalf("Sources: %v", err)
			}
			if v := got.Rows["github"].Secret.Value(); v != c.want {
				t.Errorf("secret = %q, want %q", v, c.want)
			}
			if len(got.Commands) != c.wantCmds {
				t.Errorf("commands = %d, want %d", len(got.Commands), c.wantCmds)
			}
			if strings.Join(r.calls, "|") != strings.Join(c.wantCalls, "|") {
				t.Errorf("ran %v, want %v", r.calls, c.wantCalls)
			}
			if got.Rows["github"].Identity != nil {
				t.Error("a bearer row has no identity source")
			}
			if os.Getenv(env) != "" {
				t.Errorf("%s was exported to the process environment", env)
			}
		})
	}
}

func TestSourcesNilLookupReadsProcessEnv(t *testing.T) {
	routed(t)
	unset(t, env)
	t.Setenv(env, "proc")
	got, err := Resolution{Forge: "github"}.Sources([]backendrows.Row{row("github", env)})
	if err != nil || got.Rows["github"].Secret.Value() != "proc" {
		t.Fatalf("got %v, %v", got.Rows, err)
	}
}

func TestSourcesUnreadRowKeepsPlainValueAndRunsNothing(t *testing.T) {
	routed(t)
	r := &runner{}
	res := Resolution{Forge: "github", Template: "vault get {name}", Run: r.run, Lookup: lookupOf(map[string]string{env: "p", env + "_CMD": "vault get tok"})}
	got, err := res.Sources([]backendrows.Row{row("jira", env), row("plain", env)})
	if err != nil || len(got.Commands) != 0 || len(r.calls) != 0 {
		t.Fatalf("got %v, %v, ran %v; want nothing run", got, err, r.calls)
	}
	for _, n := range []string{"jira", "plain"} {
		if v := got.Rows[n].Secret.Value(); v != "p" {
			t.Errorf("%s secret = %q, want p", n, v)
		}
	}
}

func TestSourcesCommandFailureNamesEnvNotCommand(t *testing.T) {
	routed(t)
	r := &runner{fail: map[string]bool{"vault get hunter2": true}}
	res := Resolution{Forge: "github", Run: r.run, Lookup: lookupOf(map[string]string{env + "_CMD": "vault get hunter2"})}
	_, err := res.Sources([]backendrows.Row{row("github", env)})
	if err == nil || errors.Is(err, ErrUnset) {
		t.Fatalf("err = %v, want a non-ErrUnset error", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, env) || !strings.Contains(msg, "github Connector row") || strings.Contains(msg, "hunter2") {
		t.Errorf("error %q should name the row and env, not the command", msg)
	}
}

func TestSourcesUnsetWrapsErrUnsetAndKeepsGoing(t *testing.T) {
	routed(t)
	const other = "SHADOW_TEST_OTHER"
	r := &runner{out: map[string]string{"vault get other": "o"}}
	rows := []backendrows.Row{
		{Name: "github", CredentialEnv: env, CredentialKnob: "GH_KNOB"},
		row("jira", other),
	}
	res := Resolution{Forge: "github", Tracker: "jira", Run: r.run, Lookup: lookupOf(map[string]string{other + "_CMD": "vault get other"})}
	got, err := res.Sources(rows)
	if !errors.Is(err, ErrUnset) {
		t.Fatalf("err = %v, want ErrUnset", err)
	}
	for _, want := range []string{"github Connector row", "set " + env + ", " + env + "_CMD, or SECRET_CMD", "(knob GH_KNOB)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if got.Rows["jira"].Secret.Value() != "o" || got.Rows["github"].Secret.Value() != "" {
		t.Errorf("rows = %v; want jira resolved and github empty", got.Rows)
	}
}

func TestSourcesSharedEnvResolvesOnce(t *testing.T) {
	routed(t)
	r := &runner{out: map[string]string{"vault get tok": "s"}}
	res := Resolution{Forge: "github", Tracker: "jira", Run: r.run, Lookup: lookupOf(map[string]string{env + "_CMD": "vault get tok"})}
	got, err := res.Sources([]backendrows.Row{row("github", env), row("jira", env)})
	if err != nil || len(got.Commands) != 1 || len(r.calls) != 1 || got.Rows["github"].Secret != got.Rows["jira"].Secret {
		t.Fatalf("got %v, %v, ran %v; want one shared resolution", got, err, r.calls)
	}
}

func TestSourcesSharedEnvResolvesForTheReadRow(t *testing.T) {
	routed(t)
	r := &runner{out: map[string]string{"vault get tok": "s"}}
	res := Resolution{Forge: "plain", Tracker: "jira", Run: r.run, Lookup: lookupOf(map[string]string{env + "_CMD": "vault get tok"})}
	got, err := res.Sources([]backendrows.Row{row("github", env), row("jira", env)})
	if err != nil || len(got.Commands) != 1 || got.Rows["github"].Secret.Value() != "s" {
		t.Fatalf("got %v, %v; want the read jira row's command shared by github", got, err)
	}
}

func TestSourcesIdentityOnlyForBasicRows(t *testing.T) {
	routed(t)
	rw := backendrows.Row{Name: "jira", CredentialEnv: env, CredentialScheme: "basic", IdentityEnv: "SHADOW_TEST_EMAIL"}
	res := Resolution{Tracker: "jira", Lookup: lookupOf(map[string]string{env: "t", "SHADOW_TEST_EMAIL": "me@x"})}
	got, err := res.Sources([]backendrows.Row{rw})
	if err != nil || got.Rows["jira"].Identity.Value() != "me@x" {
		t.Fatalf("got %v, %v", got.Rows, err)
	}
}

// ghToken is the credential env the refresh-file pairing covers.
const ghToken = "GH_TOKEN"

func TestSourcesRefreshFile(t *testing.T) {
	routed(t)
	path := filepath.Join(t.TempDir(), "tok")
	res := Resolution{Forge: "github", Lookup: lookupOf(map[string]string{"GH_TOKEN_REFRESH_FILE": path})}
	got, err := res.Sources([]backendrows.Row{row("github", ghToken), row("jira", ghToken)})
	if err != nil {
		t.Fatalf("a refresh file supplies an unset value: %v", err)
	}
	secret := got.Rows["github"].Secret
	if secret.Value() != "" {
		t.Fatalf("before the file exists = %q", secret.Value())
	}
	if err := os.WriteFile(path, []byte("rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if secret.Value() != "rotated" || got.Rows["jira"].Secret.Value() != "rotated" {
		t.Errorf("rotation not seen: github=%q jira=%q", secret.Value(), got.Rows["jira"].Secret.Value())
	}
	if os.Getenv(ghToken) != "" {
		t.Errorf("%s was exported to the process environment", ghToken)
	}
}

func TestSourcesRefreshFileFallsBackToPlainValue(t *testing.T) {
	routed(t)
	path := filepath.Join(t.TempDir(), "tok")
	res := Resolution{Forge: "github", Lookup: lookupOf(map[string]string{ghToken: "startup", "GH_TOKEN_REFRESH_FILE": path})}
	got, _ := res.Sources([]backendrows.Row{row("github", ghToken)})
	if v := got.Rows["github"].Secret.Value(); v != "startup" {
		t.Fatalf("before the file exists = %q, want startup", v)
	}
}

func TestSourcedRefresh(t *testing.T) {
	routed(t)
	out, fail := "old\n", false
	run := func(string) (string, error) {
		if fail {
			return "", errors.New("down")
		}
		return out, nil
	}
	res := Resolution{Forge: "github", Run: run, Lookup: lookupOf(map[string]string{env + "_CMD": "c"})}
	got, err := res.Sources([]backendrows.Row{row("github", env)})
	if err != nil {
		t.Fatal(err)
	}
	var warned []string
	warn := func(e string, err error) { warned = append(warned, e+": "+err.Error()) }

	fail = true
	got.Refresh(warn)
	if v := got.Rows["github"].Secret.Value(); v != "old" || len(warned) != 1 || !strings.HasPrefix(warned[0], env+": ") {
		t.Errorf("failed refresh: value=%q warned=%v; want old and one warning for %s", v, warned, env)
	}
	fail, out, warned = false, "new\n", nil
	got.Refresh(warn)
	if v := got.Rows["github"].Secret.Value(); v != "new" || len(warned) != 0 {
		t.Errorf("value=%q warned=%v; want new and no warning", v, warned)
	}
}
