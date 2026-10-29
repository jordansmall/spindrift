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
	"strings"

	"spindrift.dev/launcher/internal/promptassembly"
)

// SummaryLinePrefix starts every narrated pass-summary line.
const SummaryLinePrefix = "  | "

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
	// RunPass runs the sessionless resolve agent and returns the Driver's
	// result text (the agent's final message under claude), which Resolve
	// narrates as the pass summary (empty when there is none). The agent's own failure is the caller's to swallow: a non-nil
	// error means the pass could not be set up (prompt rendering, handoff
	// writing) and aborts the Box.
	RunPass func() (summary string, err error)
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
		summary, err := a.RunPass()
		if err != nil {
			return Outcome{}, err
		}
		if summary != "" {
			// The host scans Box stdout for leading-token SPINDRIFT_ lines (ADR 0055); the
			// marker on every line keeps an agent-authored summary from forging one.
			fmt.Fprintf(w, "==> conflict-resolve pass summary:\n%s\n", SummaryLinePrefix+strings.ReplaceAll(summary, "\n", "\n"+SummaryLinePrefix))
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
// through. SKILLS_FOUND, SKILL_PREAMBLE and the baked-skill steps
// (CAVEMAN_STEP, CHECK_HYGIENE_STEP, NIX_CHECKS_STEP) are computed here from
// skillsDir and override lookup. A baked skill whose fragment is missing is an
// error rather than a silently thinner prompt. A skill counts as baked by
// promptassembly.SkillBaked, so a human-only skill renders no step.
func RenderPrompt(promptsDir, skillsDir string, names []string, lookup func(string) string) (string, error) {
	rows := []struct{ skill, fragment, stepVar string }{
		{"caveman", "caveman-default.md", "CAVEMAN_STEP"},
		{"check-hygiene", "check-hygiene-default.md", "CHECK_HYGIENE_STEP"},
		{"nix-checks", "nix-checks-default.md", "NIX_CHECKS_STEP"},
	}
	// The extra 2 are the computed SKILLS_FOUND and SKILL_PREAMBLE.
	vars := make(map[string]string, len(names)+2+len(rows))
	for _, n := range names {
		vars[n] = lookup(n)
	}
	vars["SKILLS_FOUND"] = promptassembly.ScanSkillsFound(skillsDir)
	vars["SKILL_PREAMBLE"] = ""

	for _, s := range rows {
		vars[s.stepVar] = ""
		if !promptassembly.SkillBaked(skillsDir, s.skill) {
			continue
		}
		text, err := render(filepath.Join(promptsDir, "fragments", s.fragment), vars)
		if err != nil {
			return "", err
		}
		vars[s.stepVar] = text + "\n\n"
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
	// Raw bytes only: a substituted value is untrusted.
	if err := promptassembly.CheckRemovedVars(path, string(b)); err != nil {
		return "", err
	}
	return promptassembly.RenderText(string(b), vars, true /* bare $NAME too */), nil
}
