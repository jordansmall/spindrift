package main

import (
	"errors"
	"io"
	"os/exec"
	"reflect"
	"testing"
)

// fjFixture scripts an fj on PATH and records every command RunCmd is handed
// along with the stdin each carried.
type fjFixture struct {
	*fixture
	reg   *regFake
	cmds  []*exec.Cmd
	stdin []string
	err   error
}

func newFJFixture(t *testing.T) *fjFixture {
	t.Helper()
	g := &fjFixture{fixture: newFixture(t)}
	g.reg = newRegFake()
	g.d.Registry = g.reg.deps()
	g.d.LookPath = func(name string) (string, error) {
		if name != "fj" {
			return "", exec.ErrNotFound
		}
		return "/bin/fj", nil
	}
	g.d.RunCmd = func(cmd *exec.Cmd) error {
		g.cmds = append(g.cmds, cmd)
		if cmd.Args[0] == "fj" {
			g.reg.events = append(g.reg.events, "fj")
			b, _ := io.ReadAll(cmd.Stdin)
			g.stdin = append(g.stdin, string(b))
			return g.err
		}
		return nil
	}
	g.knobs["FORGEJO_TOKEN"] = "s3cret"
	return g
}

func (g *fjFixture) fjArgs() [][]string {
	var out [][]string
	for _, c := range g.cmds {
		if c.Args[0] == "fj" {
			out = append(out, c.Args)
		}
	}
	return out
}

func TestForgejoCLI_AddsKeyWithDefaults(t *testing.T) {
	g := newFJFixture(t)
	g.run()
	want := [][]string{{"fj", "-H", "https://codeberg.org", "auth", "add-key", "spindrift-agent"}}
	if got := g.fjArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("fj argv = %v, want %v", got, want)
	}
}

func TestForgejoCLI_BaseAndNameFromEnv(t *testing.T) {
	for _, tc := range []struct{ base, wantBase string }{
		{"https://forge.example", "https://forge.example"},
		{"https://forge.example/", "https://forge.example"},
		{"https://forge.example//", "https://forge.example/"},
	} {
		g := newFJFixture(t)
		g.knobs["FORGEJO_BASE_URL"] = tc.base
		g.knobs["GIT_USER_NAME"] = "bot"
		g.run()
		want := [][]string{{"fj", "-H", tc.wantBase, "auth", "add-key", "bot"}}
		if got := g.fjArgs(); !reflect.DeepEqual(got, want) {
			t.Errorf("base %q: fj argv = %v, want %v", tc.base, got, want)
		}
	}
}

func TestForgejoCLI_TokenOnStdinNotArgv(t *testing.T) {
	g := newFJFixture(t)
	g.run()
	if !reflect.DeepEqual(g.stdin, []string{"s3cret"}) {
		t.Fatalf("stdin = %q, want the bare token", g.stdin)
	}
	for _, a := range g.fjArgs()[0] {
		if a == "s3cret" {
			t.Fatalf("token leaked into argv: %v", g.fjArgs()[0])
		}
	}
}

func TestForgejoCLI_NoFjOnPath_NoCommand(t *testing.T) {
	g := newFJFixture(t)
	g.d.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	g.run()
	if got := g.fjArgs(); len(got) != 0 {
		t.Fatalf("fj ran without fj on PATH: %v", got)
	}
}

func TestForgejoCLI_EmptyToken_NoCommand(t *testing.T) {
	g := newFJFixture(t)
	g.knobs["FORGEJO_TOKEN"] = ""
	g.run()
	if got := g.fjArgs(); len(got) != 0 {
		t.Fatalf("fj ran without a token: %v", got)
	}
}

func TestForgejoCLI_RunsForSelfContained(t *testing.T) {
	g := newFJFixture(t)
	g.env.SelfContained = true
	g.run()
	if got := g.fjArgs(); len(got) != 1 {
		t.Fatalf("fj ran %d times for a self-contained run, want 1", len(got))
	}
}

func TestForgejoCLI_RunsRegardlessOfWriteMode(t *testing.T) {
	g := newFJFixture(t)
	g.env.BoxWriteEnabled = false
	g.run()
	if got := g.fjArgs(); len(got) != 1 {
		t.Fatalf("fj ran %d times on a read-only Box, want 1", len(got))
	}
}

func TestForgejoCLI_RunsBeforeRegistryBindings(t *testing.T) {
	g := newFJFixture(t)
	g.run()
	if len(g.reg.events) < 2 || g.reg.events[0] != "fj" || g.reg.events[1] != "gate" {
		t.Fatalf("events = %v, want fj before the registry bindings", g.reg.events)
	}
}

func TestForgejoCLI_FailureAbortsBeforeAnythingElse(t *testing.T) {
	g := newFJFixture(t)
	g.err = errors.New("exit status 1")
	rc, err := run(g.in, g.env, g.d)
	var pe *phaseError
	if !errors.As(err, &pe) || pe.phase != "forgejo-cli" {
		t.Fatalf("run() = (%d, %v), want a forgejo-cli phase error", rc, err)
	}
	if len(g.reg.events) != 1 || g.reg.events[0] != "fj" {
		t.Fatalf("events = %v, want only fj", g.reg.events)
	}
	if len(g.calls) != 0 || g.assembled != 0 {
		t.Fatalf("Driver ran (%d calls) or assembled (%d) after fj failed", len(g.calls), g.assembled)
	}
}
