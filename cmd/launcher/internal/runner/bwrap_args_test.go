package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBwrapArgs_NoSecretOnArgv verifies that secret env var values are not
// passed as bwrap command-line arguments (which would expose them via ps/proc).
func TestBwrapArgs_NoSecretOnArgv(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := Box{
		Env: map[string]string{
			"GH_TOKEN":                "gh-secret-value",
			"CLAUDE_CODE_OAUTH_TOKEN": "claude-secret-value",
			"ANTHROPIC_API_KEY":       "anthropic-secret-value",
			"REPO_SLUG":               "owner/repo",
			"ISSUE_NUMBER":            "42",
		},
	}

	args := a.buildArgs("/tmp/fake-etc", box)

	secrets := []string{"gh-secret-value", "claude-secret-value", "anthropic-secret-value"}
	for _, arg := range args {
		for _, secret := range secrets {
			if strings.Contains(arg, secret) {
				t.Errorf("secret value %q found in bwrap argv: %v", secret, args)
			}
		}
	}
}

// TestBwrapArgs_NoClearEnv verifies that --clearenv is not in the args so that
// the sandbox inherits secrets from the launcher's process environment.
func TestBwrapArgs_NoClearEnv(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{"GH_TOKEN": "s"}})
	for _, arg := range args {
		if arg == "--clearenv" {
			t.Errorf("--clearenv found in bwrap argv; secrets would not reach sandbox")
		}
	}
}

// TestBwrapArgs_SkillsDirMounted verifies that a valid SPINDRIFT_SKILLS_DIR
// produces a --ro-bind entry for /home/agent/.claude/skills.
func TestBwrapArgs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:      "/fake/agent",
		agentEnv:        "/fake/env",
		bakedPrefetch:   "echo ok",
		skillsDir:       dir,
		driverSkillsDir: "/home/agent/.claude/skills",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	argStr := strings.Join(args, " ")
	want := "--ro-bind " + dir + " /home/agent/.claude/skills"
	if !strings.Contains(argStr, want) {
		t.Errorf("skills bind %q not found in args: %v", want, args)
	}
}

// TestBwrapArgs_SkillsMountTarget_FromDriverDeclaration verifies the box-side
// skills bind target comes from the adapter's driverSkillsDir field
// (populated by the Driver declaration, ADR 0009) rather than a hardcoded
// ".claude/skills" literal.
func TestBwrapArgs_SkillsMountTarget_FromDriverDeclaration(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:      "/fake/agent",
		agentEnv:        "/fake/env",
		bakedPrefetch:   "echo ok",
		skillsDir:       dir,
		driverSkillsDir: "/home/agent/custom-driver/skills",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})

	argStr := strings.Join(args, " ")
	want := "--ro-bind " + dir + " /home/agent/custom-driver/skills"
	if !strings.Contains(argStr, want) {
		t.Errorf("skills bind %q not found in args: %v", want, args)
	}
}

// TestBwrapArgs_DriverCacheDirMountedWritable verifies that a Box.DriverCacheDir
// produces a writable --bind (not --ro-bind) entry for
// /home/agent/.claude/projects.
func TestBwrapArgs_DriverCacheDirMountedWritable(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})

	argStr := strings.Join(args, " ")
	want := "--bind " + dir + " /home/agent/.claude/projects"
	if !strings.Contains(argStr, want) {
		t.Errorf("driver cache bind %q not found in args: %v", want, args)
	}
	if strings.Contains(argStr, "--ro-bind "+dir+" /home/agent/.claude/projects") {
		t.Errorf("driver cache mount must be writable (--bind), not --ro-bind; args: %v", args)
	}
}

// TestBwrapArgs_DriverCacheDirMounted_HardeningPreserved verifies that the
// writable driver-cache bind does not disturb the unshare/uid hardening
// flags bwrap always applies.
func TestBwrapArgs_DriverCacheDirMounted_HardeningPreserved(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})

	for _, flag := range []string{"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts"} {
		if !containsArg(args, flag) {
			t.Errorf("writable driver cache bind must not weaken hardening; missing %q in args: %v", flag, args)
		}
	}
}

// TestBwrapArgs_DriverCacheDir_DotClaudeParentCreated verifies that a
// --dir /home/agent/.claude appears before the driver-cache bind so the
// parent directory is agent-owned in the tmpfs rather than fabricated as
// root by bwrap's bind-target auto-creation (issue #447).
func TestBwrapArgs_DriverCacheDir_DotClaudeParentCreated(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})

	dirIdx := -1
	bindIdx := -1
	for i, arg := range args {
		if arg == "/home/agent/.claude" && i > 0 && args[i-1] == "--dir" {
			dirIdx = i
		}
		if arg == "/home/agent/.claude/projects" && i > 0 && args[i-1] == dir {
			bindIdx = i
		}
	}
	if dirIdx == -1 {
		t.Errorf("--dir /home/agent/.claude not found in args: %v", args)
	}
	if bindIdx == -1 {
		t.Errorf("bind target /home/agent/.claude/projects not found in args: %v", args)
	}
	if dirIdx != -1 && bindIdx != -1 && dirIdx >= bindIdx {
		t.Errorf("--dir /home/agent/.claude (idx %d) must precede bind target (idx %d)", dirIdx, bindIdx)
	}
}

// TestBwrapArgs_DriverCacheMountTarget_FromDriverDeclaration verifies the
// box-side session-cache bind target, and the --dir parent it creates first,
// come from the adapter's driverSessionCacheDir field (populated by the
// Driver declaration, ADR 0009) rather than a hardcoded ".claude/projects"
// literal.
func TestBwrapArgs_DriverCacheMountTarget_FromDriverDeclaration(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/custom-driver/state",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})

	argStr := strings.Join(args, " ")
	wantBind := "--bind " + dir + " /home/agent/custom-driver/state"
	if !strings.Contains(argStr, wantBind) {
		t.Errorf("driver cache bind %q not found in args: %v", wantBind, args)
	}
	wantDir := "--dir /home/agent/custom-driver"
	if !strings.Contains(argStr, wantDir) {
		t.Errorf("parent %q not found in args: %v", wantDir, args)
	}
}

// TestBwrapArgs_DriverSessionCacheDirUndeclared_NoMount verifies that a
// Driver declaring no session-state dir yields no cache bind even when a
// host DriverCacheDir is present -- there is no in-box target to bind it
// over (issue #448).
func TestBwrapArgs_DriverSessionCacheDirUndeclared_NoMount(t *testing.T) {
	dir := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}, DriverCacheDir: dir})
	for _, arg := range args {
		if arg == dir {
			t.Errorf("unexpected driver cache bind in args when Driver declares no session-cache dir: %v", args)
		}
	}
}

// TestBwrapArgs_DriverCacheDirUnset_NoMount verifies that omitting
// Box.DriverCacheDir produces no /home/agent/.claude/projects bind.
func TestBwrapArgs_DriverCacheDirUnset_NoMount(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:            "/fake/agent",
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})
	argStr := strings.Join(args, " ")
	if strings.Contains(argStr, "/home/agent/.claude/projects") {
		t.Errorf("unexpected driver cache bind in args when DriverCacheDir is empty: %v", args)
	}
}

// TestBwrapArgs_SkillsDirUnset_NoMount verifies that omitting skillsDir
// produces no skills bind in the bwrap args.
func TestBwrapArgs_SkillsDirUnset_NoMount(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		skillsDir:     "",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})
	argStr := strings.Join(args, " ")
	if strings.Contains(argStr, ".claude/skills") {
		t.Errorf("unexpected skills bind in args when skillsDir is empty: %v", args)
	}
}

// TestBwrapArgs_BakedSkillsMounted verifies that when agentFiles contains a
// home/agent/.claude/skills directory, a --ro-bind is added even when skillsDir
// is empty (baked skills are exposed without a runtime mount).
func TestBwrapArgs_BakedSkillsMounted(t *testing.T) {
	dir := t.TempDir()
	skillsPath := filepath.Join(dir, "home", "agent", ".claude", "skills")
	if err := os.MkdirAll(skillsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &bwrapAdapter{
		agentFiles:      dir,
		agentEnv:        "/fake/env",
		bakedPrefetch:   "echo ok",
		skillsDir:       "",
		driverSkillsDir: "/home/agent/.claude/skills",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})
	argStr := strings.Join(args, " ")
	want := "--ro-bind " + skillsPath + " /home/agent/.claude/skills"
	if !strings.Contains(argStr, want) {
		t.Errorf("baked skills bind %q not found in args: %v", want, args)
	}
}

// TestBwrapArgs_RuntimeSkillsTakePrecedence verifies that when both baked skills
// exist in agentFiles and skillsDir is set, only the runtime mount is added
// (runtime wins; baked skills are not double-mounted).
func TestBwrapArgs_RuntimeSkillsTakePrecedence(t *testing.T) {
	agentDir := t.TempDir()
	bakedSkillsPath := filepath.Join(agentDir, "home", "agent", ".claude", "skills")
	if err := os.MkdirAll(bakedSkillsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeSkills := t.TempDir()
	a := &bwrapAdapter{
		agentFiles:      agentDir,
		agentEnv:        "/fake/env",
		bakedPrefetch:   "echo ok",
		skillsDir:       runtimeSkills,
		driverSkillsDir: "/home/agent/.claude/skills",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})
	argStr := strings.Join(args, " ")

	wantRuntime := "--ro-bind " + runtimeSkills + " /home/agent/.claude/skills"
	if !strings.Contains(argStr, wantRuntime) {
		t.Errorf("runtime skills bind %q not found in args: %v", wantRuntime, args)
	}
	if strings.Contains(argStr, bakedSkillsPath) {
		t.Errorf("baked skills path %q unexpectedly found in args (runtime should take precedence): %v", bakedSkillsPath, args)
	}
}

// TestBwrapArgs_SkillsDirInvalid_NoFallback verifies that a SPINDRIFT_SKILLS_DIR
// override pointing at a non-existent path is not silently replaced by the
// baked-skills fallback: an explicit but broken override must produce no
// skills bind at all, not a fallback to the image's own skills.
func TestBwrapArgs_SkillsDirInvalid_NoFallback(t *testing.T) {
	agentDir := t.TempDir()
	bakedSkillsPath := filepath.Join(agentDir, "home", "agent", ".claude", "skills")
	if err := os.MkdirAll(bakedSkillsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &bwrapAdapter{
		agentFiles:      agentDir,
		agentEnv:        "/fake/env",
		bakedPrefetch:   "echo ok",
		skillsDir:       filepath.Join(agentDir, "does-not-exist"),
		driverSkillsDir: "/home/agent/.claude/skills",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{}})
	argStr := strings.Join(args, " ")

	if strings.Contains(argStr, "/home/agent/.claude/skills") {
		t.Errorf("expected no skills bind for an invalid override (no silent fallback): %v", args)
	}
}

// TestBwrapArgs_NonSecretOnArgv verifies that non-secret env vars still reach
// the sandbox via --setenv (so they appear in argv).
func TestBwrapArgs_NonSecretOnArgv(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := Box{
		Env: map[string]string{
			"GH_TOKEN":     "gh-secret-value",
			"REPO_SLUG":    "owner/repo",
			"ISSUE_NUMBER": "42",
		},
	}

	args := a.buildArgs("/tmp/fake-etc", box)

	argStr := strings.Join(args, " ")
	for _, name := range []string{"REPO_SLUG", "ISSUE_NUMBER"} {
		if !strings.Contains(argStr, name) {
			t.Errorf("non-secret %q missing from bwrap argv", name)
		}
	}
}
