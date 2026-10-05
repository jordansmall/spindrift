package conflictresolve

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

type fake struct {
	calls      []string
	runErr     error
	inProgress bool
	publishErr error
}

func (f *fake) actions() Actions {
	return Actions{
		RunPass:          func() error { f.calls = append(f.calls, "pass"); return f.runErr },
		RebaseInProgress: func() bool { f.calls = append(f.calls, "check"); return f.inProgress },
		Abort:            func() { f.calls = append(f.calls, "abort") },
		Publish:          func() error { f.calls = append(f.calls, "publish"); return f.publishErr },
	}
}

func resolve(t *testing.T, cfg Config, f *fake) (Outcome, string, error) {
	t.Helper()
	var out strings.Builder
	o, err := Resolve(cfg, f.actions(), &out)
	return o, out.String(), err
}

func TestResolve(t *testing.T) {
	base := Config{Conflict: true, BaseBranch: "main", Branch: "agent/issue-1"}
	tests := []struct {
		name      string
		cfg       Config
		f         fake
		want      Outcome
		wantCalls string
		wantOut   []string
		wantNone  []string
	}{
		{name: "clean rebase continues without a pass", cfg: Config{}, want: Outcome{Continue: true}},
		{
			name:      "clean rebase with resolve-only exits 0 without a pass",
			cfg:       Config{ResolveOnly: true},
			want:      Outcome{ExitCode: 0},
			wantOut:   []string{"==> CONFLICT_RESOLVE_PR_URL: conflict resolved — exiting without main agent\n"},
			wantCalls: "",
		},
		{
			name:      "resolved conflict continues, no publish",
			cfg:       base,
			want:      Outcome{Continue: true},
			wantCalls: "pass,check",
			wantOut: []string{
				"==> pre-work rebase conflict detected — invoking conflict-resolve agent\n",
				"==> pre-work rebase conflict resolved by agent\n",
			},
			wantNone: []string{"publishing"},
		},
		{
			name:      "resolved conflict publishes when asked",
			cfg:       Config{Conflict: true, Publish: true, Branch: "agent/issue-1"},
			want:      Outcome{Continue: true},
			wantCalls: "pass,check,publish",
			wantOut:   []string{"==> publishing rebased agent/issue-1 (post-conflict-resolve)\n"},
		},
		{
			name:      "publish failure exits 1",
			cfg:       Config{Conflict: true, Publish: true, Branch: "agent/issue-1"},
			f:         fake{publishErr: errors.New("push rejected")},
			want:      Outcome{ExitCode: 1},
			wantCalls: "pass,check,publish",
			wantOut:   []string{"==> publishing rebased branch failed after conflict resolution on agent/issue-1: push rejected\n"},
		},
		{
			name:      "unresolved conflict aborts and exits 1",
			cfg:       Config{Conflict: true, Publish: true, ResolveOnly: true, BaseBranch: "main"},
			f:         fake{inProgress: true},
			want:      Outcome{ExitCode: 1},
			wantCalls: "pass,check,abort",
			wantOut:   []string{"==> pre-work rebase onto origin/main failed — conflict agent could not resolve\n"},
			wantNone:  []string{"resolved by agent", "exiting without main agent"},
		},
		{
			name:      "resolve-only after a resolved conflict exits 0",
			cfg:       Config{Conflict: true, ResolveOnly: true},
			want:      Outcome{ExitCode: 0},
			wantCalls: "pass,check",
			wantOut: []string{
				"==> pre-work rebase conflict resolved by agent\n",
				"==> CONFLICT_RESOLVE_PR_URL: conflict resolved — exiting without main agent\n",
			},
		},
		{
			name:      "resolve-only does not skip a failed publish",
			cfg:       Config{Conflict: true, Publish: true, ResolveOnly: true, Branch: "b"},
			f:         fake{publishErr: errors.New("x")},
			want:      Outcome{ExitCode: 1},
			wantCalls: "pass,check,publish",
			wantNone:  []string{"exiting without main agent"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.f
			got, out, err := resolve(t, tt.cfg, &f)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("outcome = %+v, want %+v", got, tt.want)
			}
			if c := strings.Join(f.calls, ","); c != tt.wantCalls {
				t.Errorf("calls = %q, want %q", c, tt.wantCalls)
			}
			for _, s := range tt.wantOut {
				if !strings.Contains(out, s) {
					t.Errorf("output %q missing %q", out, s)
				}
			}
			for _, s := range tt.wantNone {
				if strings.Contains(out, s) {
					t.Errorf("output %q must not contain %q", out, s)
				}
			}
		})
	}
}

func TestResolveSetupFailureAborts(t *testing.T) {
	boom := errors.New("write handoff")
	f := fake{runErr: boom}
	got, out, err := resolve(t, Config{Conflict: true, Publish: true, ResolveOnly: true}, &f)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if got != (Outcome{}) {
		t.Errorf("outcome = %+v, want zero", got)
	}
	if strings.Join(f.calls, ",") != "pass" {
		t.Errorf("calls = %v, want only the pass", f.calls)
	}
	if strings.Contains(out, "resolved") {
		t.Errorf("output %q claims resolution", out)
	}
}

func skillsDirWith(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var testNames = append(append([]string{}, BaseVars...), "SKILLS_FOUND", "CAVEMAN_STEP", "SKILL_PREAMBLE", "OTHER")

func lookup(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRenderPrompt(t *testing.T) {
	env := map[string]string{"BASE_BRANCH": "main", "BRANCH": "agent/issue-9"}

	t.Run("caveman baked", func(t *testing.T) {
		got, err := RenderPrompt(repopath.PromptsDir(), skillsDirWith(t, "caveman"), testNames, lookup(env))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"Default to the `/caveman` skill",
			"Skills available: caveman.",
			"onto `main`",
			"branch `agent/issue-9`",
			"non-obvious why",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("prompt missing %q", want)
			}
		}
		if !strings.HasPrefix(got, "# TASK\n\nSkills available: caveman.") {
			t.Errorf("unexpected prefix: %q", got[:60])
		}
		if strings.HasSuffix(got, "\n") {
			t.Error("trailing newline not trimmed")
		}
	})

	t.Run("no skills baked", func(t *testing.T) {
		got, err := RenderPrompt(repopath.PromptsDir(), t.TempDir(), testNames, lookup(env))
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"${CAVEMAN_STEP}", "${SKILL_PREAMBLE}", "caveman", "Skills available"} {
			if strings.Contains(got, bad) {
				t.Errorf("prompt contains %q", bad)
			}
		}
		if !strings.Contains(got, "non-obvious why") {
			t.Error("prompt missing comment policy")
		}
	})

	t.Run("skills without caveman", func(t *testing.T) {
		got, err := RenderPrompt(repopath.PromptsDir(), skillsDirWith(t, "tdd", "commit"), testNames, lookup(env))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "Skills available: commit, tdd.") || strings.Contains(got, "Default to the `/caveman`") {
			t.Errorf("unexpected skill rendering: %q", got[:120])
		}
	})

	t.Run("missing caveman fragment names the path", func(t *testing.T) {
		prompts := t.TempDir()
		_, err := RenderPrompt(prompts, skillsDirWith(t, "caveman"), testNames, lookup(env))
		if err == nil || !strings.Contains(err.Error(), filepath.Join("fragments", "caveman-default.md")) {
			t.Fatalf("err = %v, want one naming fragments/caveman-default.md", err)
		}
	})

	t.Run("allowlist semantics", func(t *testing.T) {
		prompts := t.TempDir()
		if err := os.WriteFile(filepath.Join(prompts, "conflict-resolve-prompt.md"),
			[]byte("${OTHER}|${UNSET_ALLOWED}|${NOT_ALLOWED}|$BRANCH|$NOT_ALLOWED|$BRANCHX|${BRANCH}X|$OTHER.$BASE_BRANCH/\n\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		names := append([]string{"UNSET_ALLOWED"}, testNames...)
		got, err := RenderPrompt(prompts, t.TempDir(), names, lookup(map[string]string{
			"OTHER": "o", "NOT_ALLOWED": "leak", "BRANCH": "b", "BASE_BRANCH": "main", "BRANCHX": "leak",
		}))
		if err != nil {
			t.Fatal(err)
		}
		// A bare $NAME takes the whole identifier, so $BRANCHX is the unlisted
		// BRANCHX rather than BRANCH followed by X.
		if want := "o||${NOT_ALLOWED}|b|$NOT_ALLOWED|$BRANCHX|bX|o.main/"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("a substituted value is not substituted again", func(t *testing.T) {
		prompts := t.TempDir()
		if err := os.WriteFile(filepath.Join(prompts, "conflict-resolve-prompt.md"),
			[]byte("$BRANCH ${BASE_BRANCH}"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := RenderPrompt(prompts, t.TempDir(), testNames, lookup(map[string]string{"BRANCH": "${BASE_BRANCH}", "BASE_BRANCH": "$BRANCH"}))
		if err != nil {
			t.Fatal(err)
		}
		if want := "${BASE_BRANCH} $BRANCH"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("removed fragment var in the template is rejected", func(t *testing.T) {
		prompts := t.TempDir()
		if err := os.WriteFile(filepath.Join(prompts, "conflict-resolve-prompt.md"),
			[]byte("step ${CODE_COMMENTS_STEP} end"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := RenderPrompt(prompts, t.TempDir(), testNames, lookup(env))
		if err == nil {
			t.Fatal("want an error for a removed var")
		}
		for _, want := range []string{"CODE_COMMENTS_STEP", "#3505", "MIGRATING.md"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err %q missing %q", err, want)
			}
		}
	})

	for _, tc := range []struct{ fragment, skill string }{
		{"caveman-default.md", "caveman"},
		{"skill-preamble.md", "tdd"},
	} {
		t.Run("removed fragment var in "+tc.fragment+" is rejected", func(t *testing.T) {
			prompts := t.TempDir()
			if err := os.MkdirAll(filepath.Join(prompts, "fragments"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(prompts, "fragments", tc.fragment),
				[]byte("${TDD_STEP}"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := RenderPrompt(prompts, skillsDirWith(t, tc.skill), testNames, lookup(env))
			if err == nil {
				t.Fatal("want an error for a removed var")
			}
			for _, want := range []string{"TDD_STEP", "#3219", "MIGRATING.md"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err %q missing %q", err, want)
				}
			}
		})
	}

	t.Run("a removed token in a substituted value is not rejected", func(t *testing.T) {
		prompts := t.TempDir()
		if err := os.WriteFile(filepath.Join(prompts, "conflict-resolve-prompt.md"),
			[]byte("title: ${ISSUE_TITLE}"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := RenderPrompt(prompts, t.TempDir(), testNames, lookup(map[string]string{"ISSUE_TITLE": "${CODE_COMMENTS_STEP}"}))
		if err != nil {
			t.Fatal(err)
		}
		if want := "title: ${CODE_COMMENTS_STEP}"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("locally computed vars override the environment", func(t *testing.T) {
		prompts := t.TempDir()
		if err := os.WriteFile(filepath.Join(prompts, "conflict-resolve-prompt.md"),
			[]byte("[${SKILLS_FOUND}][${CAVEMAN_STEP}][${SKILL_PREAMBLE}]"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := RenderPrompt(prompts, t.TempDir(), testNames, lookup(map[string]string{
			"SKILLS_FOUND": "stale", "CAVEMAN_STEP": "stale", "SKILL_PREAMBLE": "stale",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got != "[][][]" {
			t.Errorf("got %q", got)
		}
	})
}

// BaseVars must stay real Box env names: a rename in promptassembly would
// otherwise leave this pass substituting an always-empty variable.
func TestBaseVarsAreBoxEnvNames(t *testing.T) {
	known := map[string]bool{}
	for _, n := range promptassembly.BoxEnvVarNames {
		known[n] = true
	}
	for _, n := range BaseVars {
		if !known[n] {
			t.Errorf("BaseVars entry %q is not in promptassembly.BoxEnvVarNames", n)
		}
	}
}
