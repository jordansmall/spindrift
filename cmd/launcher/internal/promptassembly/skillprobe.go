package promptassembly

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// SkillBaked reports whether dir/<name>/SKILL.md is a regular file that a
// Box may invoke; os.Stat follows symlinks, like the shell `[ -f ]` this
// replaced. A skill whose frontmatter sets `disable-model-invocation: true` is
// human-only (a person typing /name) and inert in a Box, so it counts as not
// baked.
func SkillBaked(dir, name string) bool {
	path := filepath.Join(dir, name, "SKILL.md")
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && !humanOnly(path)
}

// humanOnly reports whether the SKILL.md frontmatter sets
// disable-model-invocation to true. Parsed by hand (no YAML dependency); a
// missing fence, unreadable file, or any other shape reads as false, leaving
// the skill baked.
func humanOnly(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() || fence(strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf")) != "---" { // UTF-8 BOM
		return false
	}
	flagged := false
	for sc.Scan() {
		line := sc.Text()
		if fence(line) == "---" {
			return flagged
		}
		if v, ok := strings.CutPrefix(line, "disable-model-invocation:"); ok {
			if i := strings.Index(v, " #"); i >= 0 {
				v = v[:i] // trailing YAML comment
			}
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			flagged = strings.EqualFold(v, "true")
		}
	}
	return false // no closing fence: not frontmatter
}

// fence trims the trailing whitespace and CR a frontmatter fence line may carry.
func fence(line string) string { return strings.TrimRight(line, " \t\r") }

// ProbeBakedSkills sets each *SkillBaked field from SkillBaked(dir, name): the
// skill's SKILL.md exists under dir (the Driver's DRIVER_SKILLS_DIR) and is not
// human-only.
func (e *Env) ProbeBakedSkills(dir string) {
	// BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT
	e.CavemanSkillBaked = SkillBaked(dir, "caveman")
	e.TDDSkillBaked = SkillBaked(dir, "tdd")
	e.CommitSkillBaked = SkillBaked(dir, "commit")
	e.CodeReviewSkillBaked = SkillBaked(dir, "code-review")
	e.AutoFormatSkillBaked = SkillBaked(dir, "auto-format")
	e.AutoLintSkillBaked = SkillBaked(dir, "auto-lint")
	e.CheckHygieneSkillBaked = SkillBaked(dir, "check-hygiene")
	e.CodeCommentsSkillBaked = SkillBaked(dir, "code-comments")
	e.NixChecksSkillBaked = SkillBaked(dir, "nix-checks")
	e.PrincipleFixRootCausesSkillBaked = SkillBaked(dir, "principle-fix-root-causes")
	e.PrincipleLazinessProtocolSkillBaked = SkillBaked(dir, "principle-laziness-protocol")
	e.PrincipleRedesignFromFirstPrinciplesSkillBaked = SkillBaked(dir, "principle-redesign-from-first-principles")
	// END GENERATED SKILL-BAKED PROBES
}
