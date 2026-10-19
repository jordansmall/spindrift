package promptassembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// slotFixture stages a prompts tree whose fragments/ holds exactly files
// (relative path -> content), so a synthetic row resolves against known bytes.
func slotFixture(t *testing.T, files map[string]string) Env {
	t.Helper()
	env := coveredEnv()
	env.PromptsDir = stagePromptsCopy(t)
	for p, content := range files {
		full := filepath.Join(env.PromptsDir, "fragments", p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return env
}

func slotVar(t *testing.T, e Env, rows ...FragmentRow) map[string]string {
	t.Helper()
	b, err := assemblePromptBodies(e, Registry{Rows: rows})
	if err != nil {
		t.Fatalf("assemblePromptBodies: %v", err)
	}
	out := map[string]string{}
	for k, v := range b.vars {
		out[k] = v.text()
	}
	return out
}

func TestAxisFolderFollowsTheAxisSignal(t *testing.T) {
	cases := []struct {
		name string
		axis string
		env  Env
		want string
	}{
		{"github tracker", "tracker", Env{TrackerAxisRead: "GITHUB"}, "github"},
		{"local tracker", "tracker", Env{TrackerAxisRead: "LOCAL"}, "local"},
		{"forgejo tracker", "tracker", Env{TrackerAxisRead: "FORGEJO"}, "forgejo"},
		{"jira tracker rides the github read arm", "tracker", Env{IssueTracker: "jira", TrackerAxisRead: "GITHUB"}, "github"},
		{"jira tracker, signal unset", "tracker", Env{IssueTracker: "jira"}, "github"},
		{"local tracker, signal unset", "tracker", Env{IssueTracker: "local"}, "local"},
		{"nothing set defaults to github", "tracker", Env{}, "github"},
		{"local tracker filer rides GH", "filer", Env{IssueTracker: "local", TrackerAxisRead: "LOCAL", TrackerAxisFiler: "GH"}, "github"},
		{"local tracker filer, signal unset", "filer", Env{IssueTracker: "local"}, "github"},
		{"forgejo tracker filer", "filer", Env{TrackerAxisRead: "FORGEJO", TrackerAxisFiler: "FORGEJO"}, "forgejo"},
		{"jira filer", "filer", Env{IssueTracker: "jira"}, "github"},
		{"github forge", "forge", Env{ForgeBackend: "GH"}, "github"},
		{"forgejo forge", "forge", Env{ForgeBackend: "FORGEJO"}, "forgejo"},
		{"git forge rides GH", "forge", Env{CodeForge: "git", ForgeBackend: "GH"}, "github"},
		{"git forge, signal unset", "forge", Env{CodeForge: "git"}, "github"},
		{"local forge, signal unset", "forge", Env{CodeForge: "local"}, "github"},
		{"forgejo forge, signal unset", "forge", Env{CodeForge: "forgejo"}, "forgejo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := FragmentRow{Axis: tc.axis, Fragment: "x.md", Var: "X"}
			got, err := row.resolvedFragment(tc.env)
			if err != nil {
				t.Fatal(err)
			}
			if want := tc.want + "/x.md"; got != want {
				t.Errorf("resolvedFragment = %q, want %q", got, want)
			}
		})
	}
}

func TestResolvedFragmentRejectsUnknownAxis(t *testing.T) {
	if _, err := (FragmentRow{Axis: "bogus", Fragment: "x.md", Var: "X"}).resolvedFragment(Env{}); err == nil {
		t.Error("unknown axis resolved without error")
	}
}

func TestSharedAxisRowFallsBackToRootFragment(t *testing.T) {
	env := slotFixture(t, map[string]string{"x.md": "ROOT\n"})
	row := FragmentRow{Axis: "tracker", Fragment: "x.md", Var: "X", Shared: true}
	if got := slotVar(t, env, row)["X"]; got != "ROOT\n\n" {
		t.Errorf("X = %q, want the root fragment", got)
	}
}

func TestSharedAxisRowBackendFolderOverridesRoot(t *testing.T) {
	env := slotFixture(t, map[string]string{"x.md": "ROOT\n", "github/x.md": "GITHUB-OWN\n"})
	row := FragmentRow{Axis: "tracker", Fragment: "x.md", Var: "X", Shared: true}
	if got := slotVar(t, env, row)["X"]; got != "GITHUB-OWN\n\n" {
		t.Errorf("X = %q, want the backend folder's file", got)
	}
}

func TestNonSharedAxisRowIgnoresRootFragment(t *testing.T) {
	env := slotFixture(t, map[string]string{"x.md": "ROOT\n"})
	row := FragmentRow{Axis: "tracker", Fragment: "x.md", Var: "X"}
	if got := slotVar(t, env, row)["X"]; got != "" {
		t.Errorf("X = %q, want the empty step", got)
	}
}

func TestSharedAxisRowWithNoFileIsEmptyStep(t *testing.T) {
	env := slotFixture(t, nil)
	row := FragmentRow{Axis: "tracker", Fragment: "absent.md", Var: "X", Shared: true}
	if got := slotVar(t, env, row)["X"]; got != "" {
		t.Errorf("X = %q, want the empty step", got)
	}
}

func TestSharedAxisRowPrefersRootOverLegacy(t *testing.T) {
	env := slotFixture(t, map[string]string{"x.md": "ROOT\n", "old.md": "LEGACY\n"})
	row := FragmentRow{Axis: "tracker", Fragment: "x.md", Var: "X", Shared: true, LegacyFragment: "old.md"}
	if got := slotVar(t, env, row)["X"]; got != "ROOT\n\n" {
		t.Errorf("X = %q, want the root fragment ahead of the legacy one", got)
	}
}

func TestForgeDisplayNameIsBoundPerRow(t *testing.T) {
	const body = "[${FORGE_DISPLAY_NAME}]\n"
	env := slotFixture(t, map[string]string{"t.md": body, "f.md": body, "p.md": body, "l.md": body})
	env.IssueTracker, env.TrackerAxisRead, env.TrackerAxisWrite, env.TrackerAxisFiler = "forgejo", "FORGEJO", "FORGEJO", "FORGEJO"
	env.CodeForge, env.ForgeBackend = "github", "GH"
	got := slotVar(t, env,
		FragmentRow{Axis: "tracker", Fragment: "t.md", Var: "T", Shared: true, ExtraSubstVars: []string{"FORGE_DISPLAY_NAME"}},
		FragmentRow{Axis: "forge", Fragment: "f.md", Var: "F", Shared: true, ExtraSubstVars: []string{"FORGE_DISPLAY_NAME"}},
		FragmentRow{Axis: "filer", Fragment: "l.md", Var: "L", Shared: true, ExtraSubstVars: []string{"FORGE_DISPLAY_NAME"}},
		FragmentRow{Fragment: "p.md", Var: "P", ExtraSubstVars: []string{"FORGE_DISPLAY_NAME"}},
	)
	for v, want := range map[string]string{"T": "[Forgejo]", "F": "[GitHub]", "L": "[Forgejo]", "P": "[Forgejo]"} {
		if !strings.HasPrefix(got[v], want) {
			t.Errorf("%s = %q, want prefix %q", v, got[v], want)
		}
	}
}

func TestForgeDisplayNameUsesLocalSpelling(t *testing.T) {
	env := slotFixture(t, map[string]string{"t.md": "${FORGE_DISPLAY_NAME}\n"})
	env.IssueTracker, env.TrackerAxisRead, env.TrackerAxisWrite, env.TrackerAxisFiler = "local", "LOCAL", "", "GH"
	got := slotVar(t, env, FragmentRow{Axis: "tracker", Fragment: "t.md", Var: "T", Shared: true})
	if got["T"] != "local\n\n" {
		t.Errorf("T = %q, want %q", got["T"], "local\n\n")
	}
}
