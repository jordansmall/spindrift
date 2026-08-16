package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/agentpaths"
	"spindrift.dev/launcher/internal/promptassembly"
)

// conflictResolveCherryPickPromptFile is the file name, under promptsDir(),
// of the cherry-pick-flavored counterpart to
// templates/default/prompts/conflict-resolve-prompt.md (issue #2060 review
// finding). That template is written for a `git rebase` conflict
// (${BASE_BRANCH}/${BRANCH} placeholders, ending in `git rebase
// --continue`) -- integrateSliceBranch's own `git cherry-pick --no-commit`
// conflict path needs a cherry-pick-shaped counterpart instead of a
// mis-shaped direct render of the rebase template, so this repo carries
// both, closely mirroring each other's structure/steps.
const conflictResolveCherryPickPromptFile = "conflict-resolve-cherry-pick-prompt.md"

// promptsDir resolves the directory conflictResolveGuidance reads
// conflictResolveCherryPickPromptFile from: the PROMPTS_DIR env var (a
// test-only override via t.Setenv -- entrypoint.sh does not export
// PROMPTS_DIR into the orchestrator process's own environment, unlike
// driver-exec's separate --prompts-dir flag) when set, else
// agentpaths.PromptsDir, the single-sourced baked-in-box constant
// generated from lib/agent-paths.nix (issue #2531) -- so this never drifts
// from the Nix image's own PROMPTS_DIR default the way a second hardcoded
// literal here would.
func promptsDir() string {
	if dir := os.Getenv("PROMPTS_DIR"); dir != "" {
		return dir
	}
	return agentpaths.PromptsDir
}

// conflictResolveGuidance renders conflict-resolve-cherry-pick-prompt.md
// (via promptassembly.RenderText -- the exact same ${NAME} substitution
// mechanism this template family is rendered through everywhere else,
// issue #2060 review finding, not a bespoke strings.ReplaceAll pass) with
// branch and revRange substituted in, for a human/coordinator reading a
// conflicted integration's own finding line. Falls back to a short
// hand-rolled instruction carrying the same information if the template
// can't be located or read, or renders empty -- a missing/unreadable
// template must never abort dispatch itself, since this function's only
// job is composing a human-facing finding line.
func conflictResolveGuidance(branch, revRange string) string {
	fallback := fmt.Sprintf("resolve manually: git cherry-pick --no-commit %s (branch %s)", revRange, branch)

	path := filepath.Join(promptsDir(), conflictResolveCherryPickPromptFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}

	rendered := strings.TrimSpace(promptassembly.RenderText(string(data), map[string]string{
		"BRANCH":    branch,
		"REV_RANGE": revRange,
	}))
	if rendered == "" {
		return fallback
	}
	return rendered
}
