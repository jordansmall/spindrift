package promptassembly

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeBakedSkills(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("caveman/SKILL.md")
	mk("tdd/other.md")
	mk("commit.md")

	var e Env
	e.ProbeBakedSkills(dir)
	if !e.CavemanSkillBaked {
		t.Error("CavemanSkillBaked = false; want true for caveman/SKILL.md")
	}
	if e.TDDSkillBaked || e.CommitSkillBaked {
		t.Errorf("TDD=%v Commit=%v; want false without a SKILL.md", e.TDDSkillBaked, e.CommitSkillBaked)
	}
}

func TestProbeBakedSkillsMissingDir(t *testing.T) {
	var e Env
	e.ProbeBakedSkills(filepath.Join(t.TempDir(), "absent"))
	if e.CavemanSkillBaked {
		t.Error("CavemanSkillBaked = true for a missing dir")
	}
}
