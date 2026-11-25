package rowcredential

import (
	"errors"
	"os"
	"slices"
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
// not, so reads does not depend on which backends the build links.
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
			if got := reads(row(c.row, env), c.forge, c.tracker); got != c.want {
				t.Fatalf("reads = %v, want %v", got, c.want)
			}
		})
	}
}

func TestApply(t *testing.T) {
	routed(t)
	cases := []struct {
		name      string
		setup     func(t *testing.T)
		res       func(r *runner) Resolution
		out       map[string]string
		wantEnv   string
		wantRecip []Recipe
		wantCalls []string
	}{
		{
			name:      "_CMD fills env and returns its recipe",
			setup:     func(t *testing.T) { t.Setenv(env+"_CMD", "vault get tok") },
			out:       map[string]string{"vault get tok": "secret"},
			wantEnv:   "secret",
			wantRecip: []Recipe{{env, "vault get tok"}},
			wantCalls: []string{"vault get tok"},
		},
		{
			name: "_CMD wins over a plain value",
			setup: func(t *testing.T) {
				t.Setenv(env, "plain")
				t.Setenv(env+"_CMD", "vault get tok")
			},
			out:       map[string]string{"vault get tok": "secret"},
			wantEnv:   "secret",
			wantRecip: []Recipe{{env, "vault get tok"}},
			wantCalls: []string{"vault get tok"},
		},
		{
			name:  "plain value is not run and not a recipe",
			setup: func(t *testing.T) { t.Setenv(env, "plain") },
			res: func(r *runner) Resolution {
				return Resolution{Forge: "github", Template: "vault get {name}", Run: r.run}
			},
			wantEnv: "plain",
		},
		{
			name: "template fills an unset env",
			res: func(r *runner) Resolution {
				return Resolution{Forge: "github", Template: "vault get {name}", Run: r.run}
			},
			out:       map[string]string{"vault get shadow-test-token": "templated"},
			wantEnv:   "templated",
			wantRecip: []Recipe{{env, "vault get shadow-test-token"}},
			wantCalls: []string{"vault get shadow-test-token"},
		},
		{
			name: "applied env with a set value runs nothing",
			setup: func(t *testing.T) {
				t.Setenv(env, "applied")
				t.Setenv(env+"_CMD", "vault get tok")
			},
			res: func(r *runner) Resolution {
				return Resolution{Forge: "github", Template: "vault get {name}", Run: r.run,
					Applied: func(e string) bool { return e == env }}
			},
			wantEnv: "applied",
		},
		{
			name: "applied env that is unset takes the template, not its _CMD",
			setup: func(t *testing.T) {
				t.Setenv(env+"_CMD", "vault get tok")
			},
			res: func(r *runner) Resolution {
				return Resolution{Forge: "github", Template: "vault get {name}", Run: r.run,
					Applied: func(e string) bool { return e == env }}
			},
			out:       map[string]string{"vault get shadow-test-token": "templated"},
			wantEnv:   "templated",
			wantRecip: []Recipe{{env, "vault get shadow-test-token"}},
			wantCalls: []string{"vault get shadow-test-token"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unset(t, env)
			if c.setup != nil {
				c.setup(t)
			}
			r := &runner{out: c.out}
			res := Resolution{Forge: "github", Run: r.run}
			if c.res != nil {
				res = c.res(r)
			}
			got, err := res.Apply([]backendrows.Row{row("github", env)})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if os.Getenv(env) != c.wantEnv {
				t.Errorf("%s = %q, want %q", env, os.Getenv(env), c.wantEnv)
			}
			if !slices.Equal(got, c.wantRecip) {
				t.Errorf("recipes = %v, want %v", got, c.wantRecip)
			}
			if strings.Join(r.calls, "|") != strings.Join(c.wantCalls, "|") {
				t.Errorf("ran %v, want %v", r.calls, c.wantCalls)
			}
		})
	}
}

func TestApplyUnreadRowRunsNothing(t *testing.T) {
	routed(t)
	unset(t, env)
	t.Setenv(env+"_CMD", "vault get tok")
	r := &runner{}
	res := Resolution{Forge: "github", Tracker: "github", Template: "vault get {name}", Run: r.run}
	got, err := res.Apply([]backendrows.Row{row("jira", env), row("plain", env)})
	if err != nil || len(got) != 0 || len(r.calls) != 0 {
		t.Fatalf("got %v, %v, ran %v; want nothing", got, err, r.calls)
	}
}

func TestApplyCommandFailureNamesEnvNotCommand(t *testing.T) {
	routed(t)
	unset(t, env)
	t.Setenv(env+"_CMD", "vault get hunter2")
	r := &runner{fail: map[string]bool{"vault get hunter2": true}}
	_, err := Resolution{Forge: "github", Run: r.run}.Apply([]backendrows.Row{row("github", env)})
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrUnset) {
		t.Error("a command failure must not be ErrUnset")
	}
	msg := err.Error()
	if !strings.Contains(msg, env) || !strings.Contains(msg, "github Connector row") {
		t.Errorf("error %q should name the row and env", msg)
	}
	if strings.Contains(msg, "hunter2") {
		t.Errorf("error %q leaks the command", msg)
	}
}

func TestApplyUnsetWrapsErrUnsetAndKeepsGoing(t *testing.T) {
	routed(t)
	const other = "SHADOW_TEST_OTHER"
	unset(t, env, other)
	t.Setenv(other+"_CMD", "vault get other")
	r := &runner{out: map[string]string{"vault get other": "o"}}
	rows := []backendrows.Row{
		{Name: "github", CredentialEnv: env, CredentialKnob: "GH_KNOB"},
		row("jira", other),
	}
	got, err := Resolution{Forge: "github", Tracker: "jira", Run: r.run}.Apply(rows)
	if !errors.Is(err, ErrUnset) {
		t.Fatalf("err = %v, want ErrUnset", err)
	}
	msg := err.Error()
	for _, want := range []string{"github Connector row", "set " + env + ", " + env + "_CMD, or SECRET_CMD", "(knob GH_KNOB)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if os.Getenv(other) != "o" || !slices.Equal(got, []Recipe{{other, "vault get other"}}) {
		t.Errorf("later row not resolved: env=%q recipes=%v", os.Getenv(other), got)
	}
}

func TestApplySharedEnvResolvesOnce(t *testing.T) {
	routed(t)
	unset(t, env)
	t.Setenv(env+"_CMD", "vault get tok")
	r := &runner{out: map[string]string{"vault get tok": "s"}}
	rows := []backendrows.Row{row("github", env), row("jira", env)}
	got, err := Resolution{Forge: "github", Tracker: "jira", Run: r.run}.Apply(rows)
	if err != nil || len(got) != 1 || len(r.calls) != 1 {
		t.Fatalf("got %v, %v, ran %v; want one resolution", got, err, r.calls)
	}
}

func TestApplySharedEnvResolvesForTheReadRow(t *testing.T) {
	routed(t)
	unset(t, env)
	t.Setenv(env+"_CMD", "vault get tok")
	r := &runner{out: map[string]string{"vault get tok": "s"}}
	rows := []backendrows.Row{row("github", env), row("jira", env)}
	got, err := Resolution{Forge: "plain", Tracker: "jira", Run: r.run}.Apply(rows)
	if err != nil || !slices.Equal(got, []Recipe{{env, "vault get tok"}}) || os.Getenv(env) != "s" {
		t.Fatalf("got %v, %v, env=%q; want the read jira row to resolve", got, err, os.Getenv(env))
	}
}

func TestRefresh(t *testing.T) {
	unset(t, env)
	t.Setenv(env, "old")
	recipes := []Recipe{{env, "vault get tok"}}

	var warned []string
	warn := func(e string, err error) { warned = append(warned, e+": "+err.Error()) }

	Resolution{Run: func(string) (string, error) { return "", errors.New("down") }}.Refresh(recipes, warn)
	if os.Getenv(env) != "old" {
		t.Errorf("failed refresh changed env to %q", os.Getenv(env))
	}
	if len(warned) != 1 || !strings.HasPrefix(warned[0], env+": ") {
		t.Errorf("warned = %v, want one warning for %s", warned, env)
	}

	warned = nil
	Resolution{Run: func(string) (string, error) { return "new\n", nil }}.Refresh(recipes, warn)
	if os.Getenv(env) != "new" || len(warned) != 0 {
		t.Errorf("env = %q, warned = %v; want new and no warning", os.Getenv(env), warned)
	}
}
