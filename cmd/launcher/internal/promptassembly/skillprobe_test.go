package promptassembly

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSkillFile writes body to dir/rel, creating parent directories.
func writeSkillFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProbeBakedSkills(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "caveman/SKILL.md", "")
	writeSkillFile(t, dir, "tdd/other.md", "")
	writeSkillFile(t, dir, "commit.md", "")

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

func TestSkillBakedHumanOnlyFlag(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"empty file", "", true},
		{"no frontmatter", "disable-model-invocation: true\n", true},
		{"unflagged frontmatter", "---\nname: x\n---\nbody\n", true},
		{"flag true", "---\nname: x\ndisable-model-invocation: true\n---\nbody\n", false},
		{"flag true CRLF", "---\r\nname: x\r\ndisable-model-invocation: true\r\n---\r\nbody\r\n", false},
		{"flag True mixed case", "---\ndisable-model-invocation: True\n---\n", false},
		{"flag quoted true", "---\ndisable-model-invocation: \"true\"\n---\n", false},
		{"flag false", "---\ndisable-model-invocation: false\n---\n", true},
		{"flag only in body", "---\nname: x\n---\ndisable-model-invocation: true\n", true},
		{"unclosed fence", "---\nname: x\ndisable-model-invocation: true\n", true},
		{"indented key is not top-level", "---\nmeta:\n  disable-model-invocation: true\n---\n", true},
		{"BOM before opening fence", "\ufeff---\ndisable-model-invocation: true\n---\n", false},
		{"trailing space on fences", "--- \t\ndisable-model-invocation: true\n---  \n", false},
		{"trailing comment on flag", "---\ndisable-model-invocation: true # note\n---\n", false},
		{"trailing comment on quoted flag", "---\ndisable-model-invocation: \"true\" # note\n---\n", false},
		{"trailing comment on false flag", "---\ndisable-model-invocation: false # true\n---\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSkillFile(t, dir, "s/SKILL.md", tc.body)
			if got := SkillBaked(dir, "s"); got != tc.want {
				t.Errorf("SkillBaked = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSkillBakedUnreadableStaysBaked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a chmod 000 file")
	}
	dir := t.TempDir()
	writeSkillFile(t, dir, "s/SKILL.md", "---\ndisable-model-invocation: true\n---\n")
	p := filepath.Join(dir, "s", "SKILL.md")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	if !SkillBaked(dir, "s") {
		t.Error("SkillBaked = false for an unreadable SKILL.md; want baked")
	}
}

func TestProbeBakedSkillsHumanOnlyGate(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"tdd":     "---\nname: tdd\ndisable-model-invocation: true\n---\n",
		"caveman": "---\nname: caveman\n---\n",
	} {
		writeSkillFile(t, dir, name+"/SKILL.md", body)
	}
	var e Env
	e.ProbeBakedSkills(dir)
	if e.TDDSkillBaked {
		t.Error("TDDSkillBaked = true for a human-only skill")
	}
	if !e.CavemanSkillBaked {
		t.Error("CavemanSkillBaked = false for an unflagged skill")
	}
	if got, want := ScanSkillsFound(dir), "caveman"; got != want {
		t.Errorf("ScanSkillsFound = %q, want %q", got, want)
	}
}
