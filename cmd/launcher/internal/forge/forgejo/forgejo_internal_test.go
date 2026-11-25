package forgejo

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The git credential reads FORGEJO_TOKEN each time git is run, so a rotated
// token reaches the next call and an unset one is anonymous.
func TestGitCredential_ReadsFORGEJOTOKENAtUse(t *testing.T) {
	envOf := func() []string {
		cmd := gitCredential.Apply(exec.Command("git", "ls-remote"), "https://forge.example/o/r.git")
		return cmd.Env
	}
	t.Setenv(ForgejoTokenEnv, "tok")
	env := envOf()
	for _, want := range []string{"GITEXEC_USERNAME=oauth2", "GITEXEC_PASSWORD=tok"} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	t.Setenv(ForgejoTokenEnv, "rotated")
	if !slices.Contains(envOf(), "GITEXEC_PASSWORD=rotated") {
		t.Error("env lacks the rotated token")
	}
	t.Setenv(ForgejoTokenEnv, "")
	if env := envOf(); slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "GITEXEC_PASSWORD=") }) {
		t.Error("an unset token still reached git")
	}
}

// An unset or unrecognized method falls back to "rebase", matching the github
// adapter's default.
func TestMergeStyle(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{"merge", "merge"},
		{"squash", "squash"},
		{"rebase", "rebase"},
		{"", "rebase"},
		{"bogus", "rebase"},
	}
	for _, tt := range tests {
		if got := MergeStyle(tt.method); got != tt.want {
			t.Errorf("MergeStyle(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}
