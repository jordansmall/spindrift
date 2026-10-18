package homelayout

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	{name: "opencode", skillsRel: ".claude/skills", sessionCacheRel: ".local/share/opencode/spindrift-session", agentFilesRel: ".config/opencode/agents"},
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

	for _, d := range drivers {
		if d.sessionCacheRel == "" {
			continue
		}
		t.Run(d.name+"/unmounted session cache is made writable (#5388)", func(t *testing.T) {
			skipIfRoot(t)
			tmp := t.TempDir()
			home, staged := filepath.Join(tmp, "home"), filepath.Join(tmp, "staged")
			restoreWritable(t, home, staged)
			if err := os.MkdirAll(filepath.Join(staged, d.sessionCacheRel), 0o755); err != nil {
				t.Fatal(err)
			}
			chmod(t, filepath.Join(staged, d.sessionCacheRel), 0o555)
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(home, d.sessionCacheRel)
			if err := PopulateHome(home, staged, cache); err != nil {
				t.Fatal(err)
			}
			if !writable(t, cache) {
				t.Fatal("unmounted session cache not owner-writable")
			}
			if err := os.WriteFile(filepath.Join(cache, "db"), nil, 0o644); err != nil {
				t.Errorf("create file in session cache: %v", err)
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

	for _, d := range drivers {
		if d.sessionCacheRel == "" {
			continue
		}
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
			t.Run(d.name+"/pre-existing session cache keeps its mode: "+tc.name, func(t *testing.T) {
				skipIfRoot(t)
				tmp := t.TempDir()
				home, staged := filepath.Join(tmp, "home"), filepath.Join(tmp, "staged")
				restoreWritable(t, home, staged)
				write(t, filepath.Join(staged, ".claude", "settings.json"), `{}`, 0o444)
				if err := os.MkdirAll(filepath.Join(staged, d.sessionCacheRel), 0o755); err != nil {
					t.Fatal(err)
				}
				cache := filepath.Join(home, d.sessionCacheRel)
				write(t, filepath.Join(cache, "session.json"), `{}`, 0o444)
				chmod(t, cache, tc.mode)

				sc := cache
				if tc.slash {
					sc += "/"
				}
				if err := PopulateHome(home, staged, sc); err != nil {
					t.Fatal(err)
				}
				if !writable(t, filepath.Join(home, ".claude", "settings.json")) {
					t.Error("settings.json not writable")
				}
				if writable(t, filepath.Join(cache, "session.json")) {
					t.Error("session.json was chmod'd")
				}
				info, err := os.Stat(cache)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != tc.mode {
					t.Errorf("session cache mode = %o, want %o", info.Mode().Perm(), tc.mode)
				}
			})
		}
	}

	t.Run("unstattable session cache dir is an error", func(t *testing.T) {
		skipIfRoot(t)
		tmp := t.TempDir()
		home, staged := filepath.Join(tmp, "home"), filepath.Join(tmp, "staged")
		locked := filepath.Join(home, "locked")
		restoreWritable(t, home, staged, locked)
		write(t, filepath.Join(staged, "f"), "x", 0o644)
		if err := os.MkdirAll(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		chmod(t, locked, 0o000)
		if err := PopulateHome(home, staged, filepath.Join(locked, "cache")); err == nil {
			t.Error("PopulateHome succeeded on an unstattable session cache dir")
		}
	})

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

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestWriteEnvFiles(t *testing.T) {
	skipIfRoot(t)

	t.Run("writes the value at the home-relative path, private, with its parent", func(t *testing.T) {
		home := t.TempDir()
		files := []EnvFile{{Env: "SECRET", Path: ".x/agent/auth.json"}}
		if err := WriteEnvFiles(home, files, envOf(map[string]string{"SECRET": `{"k":"v"}`})); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, ".x", "agent", "auth.json")
		got, err := os.ReadFile(path)
		if err != nil || string(got) != `{"k":"v"}` {
			t.Fatalf("ReadFile = %q, %v; want the env value", got, err)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
			t.Errorf("parent mode = %v, want 0700", fi.Mode().Perm())
		}
	})

	t.Run("an unset or empty value writes nothing", func(t *testing.T) {
		home := t.TempDir()
		files := []EnvFile{{Env: "UNSET", Path: ".x/a"}, {Env: "EMPTY", Path: ".y/b"}}
		if err := WriteEnvFiles(home, files, envOf(map[string]string{"EMPTY": ""})); err != nil {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(home)
		if len(entries) != 0 {
			t.Errorf("home has %d entries, want none", len(entries))
		}
	})

	t.Run("a path that is absolute or escapes home is refused", func(t *testing.T) {
		for _, p := range []string{"/etc/passwd", "../outside", "a/../../outside", ".."} {
			home := t.TempDir()
			err := WriteEnvFiles(home, []EnvFile{{Env: "S", Path: p}}, envOf(map[string]string{"S": "v"}))
			if err == nil {
				t.Errorf("path %q: WriteEnvFiles succeeded, want an error", p)
			}
		}
	})

	t.Run("overwriting an existing file leaves it private and replaced", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, "auth.json")
		if err := os.WriteFile(path, []byte("old-and-longer-content"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := WriteEnvFiles(home, []EnvFile{{Env: "S", Path: "auth.json"}}, envOf(map[string]string{"S": "new"})); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "new" {
			t.Errorf("content = %q, want %q", got, "new")
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
		}
	})

	t.Run("a write failure names the path but not the value", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "blocker"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		err := WriteEnvFiles(home, []EnvFile{{Env: "S", Path: "blocker/auth.json"}}, envOf(map[string]string{"S": "s3cret"}))
		if err == nil {
			t.Fatal("WriteEnvFiles succeeded under a non-directory parent")
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("error leaks the value: %v", err)
		}
	})
}

func TestParseEnvFiles(t *testing.T) {
	got, err := ParseEnvFiles("A=.a/x B=y.json")
	want := []EnvFile{{Env: "A", Path: ".a/x"}, {Env: "B", Path: "y.json"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ParseEnvFiles = %v, %v; want %v", got, err, want)
	}
	if got, err := ParseEnvFiles(""); err != nil || len(got) != 0 {
		t.Errorf("ParseEnvFiles(empty) = %v, %v; want none", got, err)
	}
	for _, bad := range []string{"NOEQUALS", "=path", "A=", "A=x B"} {
		if _, err := ParseEnvFiles(bad); err == nil {
			t.Errorf("ParseEnvFiles(%q) succeeded, want an error", bad)
		}
	}
}
