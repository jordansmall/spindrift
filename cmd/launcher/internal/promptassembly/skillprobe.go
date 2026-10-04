package promptassembly

import (
	"os"
	"path/filepath"
)

// skillBaked reports whether dir/<name>/SKILL.md is a regular file; os.Stat
// follows symlinks, like the shell `[ -f ]` this replaced.
func skillBaked(dir, name string) bool {
	fi, err := os.Stat(filepath.Join(dir, name, "SKILL.md"))
	return err == nil && fi.Mode().IsRegular()
}

// ProbeBakedSkills sets each *SkillBaked field from whether the skill's
// SKILL.md exists under dir (the Driver's DRIVER_SKILLS_DIR).
func (e *Env) ProbeBakedSkills(dir string) {
	// BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT
	e.CavemanSkillBaked = skillBaked(dir, "caveman")
	e.TDDSkillBaked = skillBaked(dir, "tdd")
	e.CommitSkillBaked = skillBaked(dir, "commit")
	e.CodeReviewSkillBaked = skillBaked(dir, "code-review")
	e.AutoFormatSkillBaked = skillBaked(dir, "auto-format")
	e.AutoLintSkillBaked = skillBaked(dir, "auto-lint")
	e.CheckHygieneSkillBaked = skillBaked(dir, "check-hygiene")
	e.CodeCommentsSkillBaked = skillBaked(dir, "code-comments")
	e.NixChecksSkillBaked = skillBaked(dir, "nix-checks")
	e.PrincipleFixRootCausesSkillBaked = skillBaked(dir, "principle-fix-root-causes")
	e.PrincipleLazinessProtocolSkillBaked = skillBaked(dir, "principle-laziness-protocol")
	e.PrincipleRedesignFromFirstPrinciplesSkillBaked = skillBaked(dir, "principle-redesign-from-first-principles")
	// END GENERATED SKILL-BAKED PROBES
}
