package homelayout

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
)

// driver mirrors the per-Driver facts the layout depends on (lib/drivers).
type driver struct {
	name            string
	skillsRel       string
	sessionCacheRel string // "" when the Driver has no session cache dir
	agentFilesRel   string // "" when the Driver has no agent-files dir
}

var drivers = []driver{
	{name: "claude", skillsRel: ".claude/skills", sessionCacheRel: ".claude/projects"},
	{name: "opencode", skillsRel: ".claude/skills", agentFilesRel: ".config/opencode/agents"},
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// restoreWritable lets TempDir cleanup remove read-only trees.
func restoreWritable(t *testing.T, roots ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, r := range roots {
			_ = filepath.WalkDir(r, func(p string, d os.DirEntry, err error) error {
				if d != nil && d.Type()&os.ModeSymlink == 0 {
					_ = os.Chmod(p, 0o755)
				}
				return nil
			})
		}
	})
}

func writable(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()&0o200 != 0
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// roSkill stages <root>/<name>/SKILL.md read-only inside a read-only dir, as a
// Nix-store ro-bind presents it.
func roSkill(t *testing.T, root, name, content string) {
	t.Helper()
	write(t, filepath.Join(root, name, "SKILL.md"), content, 0o444)
	chmod(t, filepath.Join(root, name), 0o555)
}

func TestPopulateSkills(t *testing.T) {
	for _, d := range drivers {
		t.Run(d.name+"/harness skill survives an operator skills override (#2489)", func(t *testing.T) {
			tmp := t.TempDir()
			harness, operator := filepath.Join(tmp, "harness"), filepath.Join(tmp, "operator")
			dst := filepath.Join(tmp, "home", d.skillsRel)
			write(t, filepath.Join(harness, "auto-format", "SKILL.md"), "fmt", 0o644)
			write(t, filepath.Join(operator, "my-skill", "SKILL.md"), "mine", 0o644)

			if err := PopulateSkills(dst, harness, operator); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, filepath.Join(dst, "auto-format", "SKILL.md")); got != "fmt" {
				t.Errorf("harness skill = %q", got)
			}
			if got := readFile(t, filepath.Join(dst, "my-skill", "SKILL.md")); got != "mine" {
				t.Errorf("operator skill = %q", got)
			}
			if got := promptassembly.ScanSkillsFound(dst); got != "auto-format, my-skill" {
				t.Errorf("SKILLS_FOUND = %q", got)
			}
		})
	}

	t.Run("operator skill wins a name collision", func(t *testing.T) {
		tmp := t.TempDir()
		harness, operator, dst := filepath.Join(tmp, "h"), filepath.Join(tmp, "o"), filepath.Join(tmp, "d")
		write(t, filepath.Join(harness, "s", "SKILL.md"), "harness", 0o644)
		write(t, filepath.Join(operator, "s", "SKILL.md"), "operator", 0o644)
		if err := PopulateSkills(dst, harness, operator); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(dst, "s", "SKILL.md")); got != "operator" {
			t.Errorf("SKILL.md = %q", got)
		}
	})

	t.Run("absent operator mount leaves harness skills and no error", func(t *testing.T) {
		tmp := t.TempDir()
		harness, dst := filepath.Join(tmp, "h"), filepath.Join(tmp, "d")
		write(t, filepath.Join(harness, "auto-format", "SKILL.md"), "fmt", 0o644)
		if err := PopulateSkills(dst, harness, filepath.Join(tmp, "nonexistent")); err != nil {
			t.Fatal(err)
		}
		if got := promptassembly.ScanSkillsFound(dst); got != "auto-format" {
			t.Errorf("SKILLS_FOUND = %q", got)
		}
	})

	t.Run("both sources absent creates an empty dst", func(t *testing.T) {
		tmp := t.TempDir()
		dst := filepath.Join(tmp, "d")
		if err := PopulateSkills(dst, filepath.Join(tmp, "a"), filepath.Join(tmp, "b")); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(dst)
		if err != nil || len(entries) != 0 {
			t.Errorf("dst entries = %v, err = %v", entries, err)
		}
	})

	t.Run("read-only harness skill survives two populate calls (#3941)", func(t *testing.T) {
		skipIfRoot(t)
		tmp := t.TempDir()
		harness, dst := filepath.Join(tmp, "h"), filepath.Join(tmp, "d")
		restoreWritable(t, harness, dst)
		roSkill(t, harness, "auto-format", "fmt")
		for i := range 2 {
			if err := PopulateSkills(dst, harness); err != nil {
				t.Fatalf("call %d: %v", i+1, err)
			}
		}
		if got := readFile(t, filepath.Join(dst, "auto-format", "SKILL.md")); got != "fmt" {
			t.Errorf("SKILL.md = %q", got)
		}
		if !writable(t, filepath.Join(dst, "auto-format", "SKILL.md")) || !writable(t, filepath.Join(dst, "auto-format")) {
			t.Error("copied skill not owner-writable")
		}
	})

	t.Run("read-only operator skill overrides same-named read-only harness skill (#3941)", func(t *testing.T) {
		skipIfRoot(t)
		tmp := t.TempDir()
		harness, operator, dst := filepath.Join(tmp, "h"), filepath.Join(tmp, "o"), filepath.Join(tmp, "d")
		restoreWritable(t, harness, operator, dst)
		roSkill(t, harness, "s", "harness")
		roSkill(t, operator, "s", "operator")
		if err := PopulateSkills(dst, harness, operator); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(dst, "s", "SKILL.md")); got != "operator" {
			t.Errorf("SKILL.md = %q", got)
		}
	})

	t.Run("src whose stat fails with EACCES is an error, not a skip", func(t *testing.T) {
		skipIfRoot(t)
		tmp := t.TempDir()
		locked := filepath.Join(tmp, "locked")
		restoreWritable(t, locked)
		write(t, filepath.Join(locked, "src", "s", "SKILL.md"), "x", 0o644)
		chmod(t, locked, 0o000)
		if err := PopulateSkills(filepath.Join(tmp, "d"), filepath.Join(locked, "src")); err == nil {
			t.Error("PopulateSkills succeeded on an unstattable src")
		}
	})

	t.Run("symlinks are copied as symlinks", func(t *testing.T) {
		tmp := t.TempDir()
		harness, dst := filepath.Join(tmp, "h"), filepath.Join(tmp, "d")
		write(t, filepath.Join(harness, "s", "SKILL.md"), "x", 0o644)
		if err := os.Symlink("SKILL.md", filepath.Join(harness, "s", "link.md")); err != nil {
			t.Fatal(err)
		}
		if err := PopulateSkills(dst, harness); err != nil {
			t.Fatal(err)
		}
		if got, err := os.Readlink(filepath.Join(dst, "s", "link.md")); err != nil || got != "SKILL.md" {
			t.Errorf("readlink = %q, %v", got, err)
		}
	})
}

func TestPopulateHome(t *testing.T) {
	for _, d := range drivers {
		t.Run(d.name+"/baked home-agent content is copied and made writable (#2843)", func(t *testing.T) {
			skipIfRoot(t)
			tmp := t.TempDir()
			home, staged := filepath.Join(tmp, "home"), filepath.Join(tmp, "staged")
			restoreWritable(t, home, staged)
			write(t, filepath.Join(staged, ".claude", "settings.json"), `{}`, 0o444)
			if d.agentFilesRel != "" {
				agents := filepath.Join(staged, d.agentFilesRel)
				write(t, filepath.Join(agents, "scout.md"), "scout", 0o444)
				chmod(t, agents, 0o555)
			}
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			sc := ""
			if d.sessionCacheRel != "" {
				sc = filepath.Join(home, d.sessionCacheRel)
			}
			if err := PopulateHome(home, staged, sc); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(home, ".claude", "settings.json")
			if got := readFile(t, p); got != "{}" {
				t.Errorf("settings.json = %q", got)
			}
			if !writable(t, p) {
				t.Error("settings.json not writable")
			}
			if d.agentFilesRel != "" {
				agent := filepath.Join(home, d.agentFilesRel, "scout.md")
				if got := readFile(t, agent); got != "scout" {
					t.Errorf("scout.md = %q", got)
				}
				if !writable(t, agent) {
					t.Error("scout.md not writable")
				}
			}
		})
	}

	t.Run("staged dir absent is a no-op", func(t *testing.T) {
		home := t.TempDir()
		if err := PopulateHome(home, filepath.Join(home, "nope"), ""); err != nil {
			t.Fatal(err)
		}
		if entries, _ := os.ReadDir(home); len(entries) != 0 {
			t.Errorf("home gained %v", entries)
		}
	})

	for _, tc := range []struct {
		name string
		mode os.FileMode
		// slash: sessionCacheDir given with a trailing slash (#2845).
		slash bool
	}{
		{"755", 0o755, false},
		{"555", 0o555, false},
		{"555 with trailing slash", 0o555, true},
	} {
		t.Run("pre-existing session cache keeps its mode: "+tc.name, func(t *testing.T) {
			skipIfRoot(t)
			tmp := t.TempDir()
			home, staged := filepath.Join(tmp, "home"), filepath.Join(tmp, "staged")
			restoreWritable(t, home, staged)
			write(t, filepath.Join(staged, ".claude", "settings.json"), `{}`, 0o444)
			if err := os.MkdirAll(filepath.Join(staged, ".claude", "projects"), 0o755); err != nil {
				t.Fatal(err)
			}
			projects := filepath.Join(home, ".claude", "projects")
			write(t, filepath.Join(projects, "session.json"), `{}`, 0o444)
			chmod(t, projects, tc.mode)

			sc := projects
			if tc.slash {
				sc += "/"
			}
			if err := PopulateHome(home, staged, sc); err != nil {
				t.Fatal(err)
			}
			if !writable(t, filepath.Join(home, ".claude", "settings.json")) {
				t.Error("settings.json not writable")
			}
			if writable(t, filepath.Join(projects, "session.json")) {
				t.Error("session.json was chmod'd")
			}
			info, err := os.Stat(projects)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tc.mode {
				t.Errorf("projects mode = %o, want %o", info.Mode().Perm(), tc.mode)
			}
		})
	}

	t.Run("ordinary copied-in directory is made writable", func(t *testing.T) {
		skipIfRoot(t)
		tmp := t.TempDir()
		home, staged := filepath.Join(tmp, "home"), filepath.Join(tmp, "staged")
		restoreWritable(t, home, staged)
		write(t, filepath.Join(staged, ".config", "some-file.json"), `{}`, 0o444)
		chmod(t, filepath.Join(staged, ".config"), 0o555)
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := PopulateHome(home, staged, ""); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(home, ".config", "gh"), 0o755); err != nil {
			t.Errorf("mkdir under copied dir: %v", err)
		}
	})

	t.Run("opencode agent files can be removed after populate", func(t *testing.T) {
		skipIfRoot(t)
		tmp := t.TempDir()
		home, staged := filepath.Join(tmp, "home"), filepath.Join(tmp, "staged")
		restoreWritable(t, home, staged)
		agents := filepath.Join(staged, ".config", "opencode", "agents")
		write(t, filepath.Join(agents, "reviewer.md"), "r", 0o444)
		chmod(t, agents, 0o555)
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := PopulateHome(home, staged, ""); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(home, ".config", "opencode", "agents", "reviewer.md")); err != nil {
			t.Errorf("remove reviewer.md: %v", err)
		}
		if !writable(t, filepath.Join(home, ".config", "opencode", "agents")) {
			t.Error("agents dir not owner-writable")
		}
	})
}
