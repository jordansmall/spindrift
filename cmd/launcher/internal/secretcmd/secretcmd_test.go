package secretcmd

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func exitError(t *testing.T, code string) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+code).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want ExitError, got %v", err)
	}
	return err
}

func TestResolve(t *testing.T) {
	const secret = "hunter2-partial"
	tests := []struct {
		name    string
		out     string
		runErr  error
		want    string
		wantErr []string
	}{
		{name: "trims trailing newlines", out: "tok\r\n\n", want: "tok"},
		{name: "keeps interior and leading space", out: " a b\n", want: " a b"},
		{name: "exit code error is value-free", out: secret, runErr: exitError(t, "7"),
			wantErr: []string{"GH_TOKEN", "exit 7", UnlockHint}},
		{name: "non-exit error is value-free", out: secret, runErr: errors.New("boom " + secret),
			wantErr: []string{"GH_TOKEN", "command failed", UnlockHint}},
		{name: "empty output", out: "\n", wantErr: []string{"GH_TOKEN", "no output", UnlockHint}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve("GH_TOKEN", "cmd", func(string) (string, error) { return tc.out, tc.runErr })
			if tc.wantErr == nil {
				if err != nil || got != tc.want {
					t.Fatalf("got (%q, %v), want %q", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error, got %q", got)
			}
			for _, s := range tc.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q missing %q", err, s)
				}
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error leaks command output: %q", err)
			}
		})
	}
}

func TestResolvePassesCommand(t *testing.T) {
	var seen string
	_, _ = Resolve("E", "rbw get x", func(c string) (string, error) { seen = c; return "v", nil })
	if seen != "rbw get x" {
		t.Fatalf("runner got %q", seen)
	}
}

// Template mirrors lib/renderers.nix's toKebab, which maps an env name to its
// kebab-case vault-item form.
func TestTemplate_SubstitutesKebabName(t *testing.T) {
	cases := map[string]string{
		"GH_TOKEN":                "gh-token",
		"CLAUDE_CODE_OAUTH_TOKEN": "claude-code-oauth-token",
		"ANTHROPIC_API_KEY":       "anthropic-api-key",
	}
	for env, want := range cases {
		if got := Template("rbw get spindrift-{name}", env); got != "rbw get spindrift-"+want {
			t.Errorf("Template(%q) = %q, want name %q", env, got, want)
		}
	}
}

func TestRun_NonInteractiveGivesNoStdin(t *testing.T) {
	out, err := Run(`read line; printf "value"`, false)
	if err != nil || out != "value" {
		t.Errorf("Run = %q, %v; want value, nil (stdin must be at EOF)", out, err)
	}
}
