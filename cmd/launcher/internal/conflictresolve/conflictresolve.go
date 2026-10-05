// Package conflictresolve is the Box's pre-handoff conflict-resolve pass: the
// decision of whether a failed pre-work rebase needs the sessionless resolve
// agent and what follows it, and the rendering of that agent's prompt. Running
// the pass, inspecting the rebase and publishing stay with the caller, injected
// as Actions.
package conflictresolve

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/promptassembly"
)

// Config is what the decision reads from the Box's environment.
type Config struct {
	Conflict    bool // the pre-work rebase stopped on conflicts
	Publish     bool // the rebased branch must be force-pushed once resolved
	ResolveOnly bool // CONFLICT_RESOLVE_PR_URL is set: stop after resolving
	BaseBranch  string
	Branch      string
}

// Actions are the side effects the decision drives.
type Actions struct {
	// RunPass runs the sessionless resolve agent. The agent's own failure is
	// the caller's to swallow: a non-nil error means the pass could not be set
	// up (prompt rendering, handoff writing) and aborts the Box.
	RunPass func() error
	// RebaseInProgress reports whether a rebase is still unfinished.
	RebaseInProgress func() bool
	// Abort reverts in-tree bindings and aborts the rebase, best-effort.
	Abort func()
	// Publish pushes the rebased branch.
	Publish func() error
}

// Outcome says what the Box does next: continue into prompt assembly, or exit
// with ExitCode.
type Outcome struct {
	Continue bool
	ExitCode int
}

// Resolve runs the pass when cfg.Conflict, narrates to w, and reports the
// outcome. A resolve-only Box exits 0 even when the rebase was clean.
func Resolve(cfg Config, a Actions, w io.Writer) (Outcome, error) {
	if cfg.Conflict {
		fmt.Fprintln(w, "==> pre-work rebase conflict detected — invoking conflict-resolve agent")
		if err := a.RunPass(); err != nil {
			return Outcome{}, err
		}
		if a.RebaseInProgress() {
			a.Abort()
			fmt.Fprintf(w, "==> pre-work rebase onto origin/%s failed — conflict agent could not resolve\n", cfg.BaseBranch)
			return Outcome{ExitCode: 1}, nil
		}
		fmt.Fprintln(w, "==> pre-work rebase conflict resolved by agent")
		if cfg.Publish {
			fmt.Fprintf(w, "==> publishing rebased %s (post-conflict-resolve)\n", cfg.Branch)
			if err := a.Publish(); err != nil {
				fmt.Fprintf(w, "==> publishing rebased branch failed after conflict resolution on %s: %v\n", cfg.Branch, err)
				return Outcome{ExitCode: 1}, nil
			}
		}
	}
	if cfg.ResolveOnly {
		fmt.Fprintln(w, "==> CONFLICT_RESOLVE_PR_URL: conflict resolved — exiting without main agent")
		return Outcome{}, nil
	}
	return Outcome{Continue: true}, nil
}

// BaseVars are the substitution-allowlist names every prompt render accepts
// besides the fragment registry's.
var BaseVars = []string{
	"ISSUE_NUMBER", "ISSUE_TITLE", "BRANCH", "BASE_BRANCH",
	"IN_PROGRESS_LABEL", "COMPLETE_LABEL", "RUN_NONCE",
}

// SubstNames is the full allowlist: BaseVars plus every registry row's Var and
// ExtraSubstVars.
func SubstNames(reg promptassembly.Registry) []string {
	names := append([]string{}, BaseVars...)
	for _, r := range reg.Rows {
		names = append(names, r.Var)
		names = append(names, r.ExtraSubstVars...)
	}
	return names
}

// RenderPrompt renders conflict-resolve-prompt.md under promptsDir with
// envsubst semantics: both $NAME and ${NAME} for NAME in names are substituted,
// each read through lookup (empty when unset), and any other token passes
// through. SKILLS_FOUND, CAVEMAN_STEP and SKILL_PREAMBLE are computed here from
// skillsDir and override lookup. A baked caveman skill whose fragment is
// missing is an error rather than a silently thinner prompt.
func RenderPrompt(promptsDir, skillsDir string, names []string, lookup func(string) string) (string, error) {
	vars := make(map[string]string, len(names)+3)
	for _, n := range names {
		vars[n] = lookup(n)
	}
	vars["SKILLS_FOUND"] = promptassembly.ScanSkillsFound(skillsDir)
	vars["CAVEMAN_STEP"] = ""
	vars["SKILL_PREAMBLE"] = ""

	if _, err := os.Stat(filepath.Join(skillsDir, "caveman", "SKILL.md")); err == nil {
		text, err := render(filepath.Join(promptsDir, "fragments", "caveman-default.md"), vars)
		if err != nil {
			return "", err
		}
		vars["CAVEMAN_STEP"] = text + "\n\n"
	}
	if vars["SKILLS_FOUND"] != "" {
		text, err := render(filepath.Join(promptsDir, "fragments", "skill-preamble.md"), vars)
		if err != nil {
			return "", err
		}
		vars["SKILL_PREAMBLE"] = text + "\n\n"
	}
	return render(filepath.Join(promptsDir, "conflict-resolve-prompt.md"), vars)
}

func render(path string, vars map[string]string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return promptassembly.RenderText(string(b), vars, true /* bare $NAME too */), nil
}
