package forgejo_test

import (
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

// fjFixture scripts an fj on PATH and records every command handed to Run
// along with the stdin each carried.
type fjFixture struct {
	env    map[string]string
	hasFJ  bool
	cmds   []*exec.Cmd
	stdin  []string
	runErr error
	stderr strings.Builder
}

func newFJFixture() *fjFixture {
	return &fjFixture{env: map[string]string{"FORGEJO_TOKEN": "s3cret"}, hasFJ: true}
}

func (g *fjFixture) configure() error {
	return forgejo.Strategy.ConfigureCLI(backendkit.BoxCLI{
		Getenv: func(k string) string { return g.env[k] },
		LookPath: func(name string) (string, error) {
			if name != "fj" || !g.hasFJ {
				return "", exec.ErrNotFound
			}
			return "/bin/fj", nil
		},
		Run: func(cmd *exec.Cmd) error {
			g.cmds = append(g.cmds, cmd)
			b, _ := io.ReadAll(cmd.Stdin)
			g.stdin = append(g.stdin, string(b))
			return g.runErr
		},
		Stderr: &g.stderr,
	})
}

func (g *fjFixture) argv() [][]string {
	var out [][]string
	for _, c := range g.cmds {
		out = append(out, c.Args)
	}
	return out
}

func TestForgejoConfigureCLI_AddsKeyWithDefaults(t *testing.T) {
	g := newFJFixture()
	if err := g.configure(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"fj", "-H", "https://codeberg.org", "auth", "add-key", "spindrift-agent"}}
	if got := g.argv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("fj argv = %v, want %v", got, want)
	}
}

func TestForgejoConfigureCLI_BaseAndNameFromEnv(t *testing.T) {
	for _, tc := range []struct{ base, wantBase string }{
		{"https://forge.example", "https://forge.example"},
		{"https://forge.example/", "https://forge.example"},
		{"https://forge.example//", "https://forge.example/"},
	} {
		g := newFJFixture()
		g.env["FORGEJO_BASE_URL"] = tc.base
		g.env["GIT_USER_NAME"] = "bot"
		if err := g.configure(); err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"fj", "-H", tc.wantBase, "auth", "add-key", "bot"}}
		if got := g.argv(); !reflect.DeepEqual(got, want) {
			t.Errorf("base %q: fj argv = %v, want %v", tc.base, got, want)
		}
	}
}

func TestForgejoConfigureCLI_TokenOnStdinNotArgv(t *testing.T) {
	g := newFJFixture()
	if err := g.configure(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.stdin, []string{"s3cret"}) {
		t.Fatalf("stdin = %q, want the bare token", g.stdin)
	}
	for _, a := range g.argv()[0] {
		if a == "s3cret" {
			t.Fatalf("token leaked into argv: %v", g.argv()[0])
		}
	}
}

func TestForgejoConfigureCLI_NoFjOnPathNoCommand(t *testing.T) {
	g := newFJFixture()
	g.hasFJ = false
	if err := g.configure(); err != nil {
		t.Fatal(err)
	}
	if got := g.argv(); len(got) != 0 {
		t.Fatalf("fj ran without fj on PATH: %v", got)
	}
}

func TestForgejoConfigureCLI_EmptyTokenNoCommand(t *testing.T) {
	g := newFJFixture()
	g.env["FORGEJO_TOKEN"] = ""
	if err := g.configure(); err != nil {
		t.Fatal(err)
	}
	if got := g.argv(); len(got) != 0 {
		t.Fatalf("fj ran without a token: %v", got)
	}
}

func TestForgejoConfigureCLI_ReturnsRunFailure(t *testing.T) {
	g := newFJFixture()
	g.runErr = errors.New("exit status 1")
	if err := g.configure(); !errors.Is(err, g.runErr) {
		t.Fatalf("err = %v, want the run failure", err)
	}
}

func TestForgejoConfigureCLI_WiresStderr(t *testing.T) {
	g := newFJFixture()
	if err := g.configure(); err != nil {
		t.Fatal(err)
	}
	if g.cmds[0].Stderr != io.Writer(&g.stderr) {
		t.Fatal("fj stderr is not the Box's stderr")
	}
}
