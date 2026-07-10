package runner

import "testing"

// TestBuildMountSpecs_PromptDirMounted verifies that a valid PromptDir
// produces a MountSpec targeting /agent/prompts, read-only, with the
// SPINDRIFT_PROMPT_DIR operator message — computed once, independent of
// backend.
func TestBuildMountSpecs_PromptDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{PromptDir: dir}, Box{})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/agent/prompts" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a /agent/prompts spec in %+v", specs)
	}
	if found.Source != dir {
		t.Errorf("Source = %q, want %q", found.Source, dir)
	}
	if !found.ReadOnly {
		t.Errorf("prompt-dir mount must be read-only")
	}
	want := "==> SPINDRIFT_PROMPT_DIR set; mounting " + dir + " over the baked prompt\n"
	if found.Message != want {
		t.Errorf("Message = %q, want %q", found.Message, want)
	}
}

// TestBuildMountSpecs_DriverCacheDirMountedWritable verifies that a declared
// DriverSessionCacheDir plus a present Box.DriverCacheDir produce a writable
// MountSpec with no operator message — computed once, independent of backend.
func TestBuildMountSpecs_DriverCacheDirMountedWritable(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"}, Box{DriverCacheDir: dir})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/home/agent/.claude/projects" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a driver-cache spec in %+v", specs)
	}
	if found.Source != dir {
		t.Errorf("Source = %q, want %q", found.Source, dir)
	}
	if found.ReadOnly {
		t.Errorf("driver-cache mount must be writable, not read-only")
	}
	if found.Message != "" {
		t.Errorf("driver-cache mount must be silent; got Message = %q", found.Message)
	}
}

// TestBuildMountSpecs_DriverSessionCacheDirUndeclared_NoMount verifies that a
// Driver declaring no session-state dir yields no cache spec even when a host
// DriverCacheDir is present — there is no in-box target to mount it over
// (issue #448).
func TestBuildMountSpecs_DriverSessionCacheDirUndeclared_NoMount(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{}, Box{DriverCacheDir: dir})

	for _, s := range specs {
		if s.Source == dir {
			t.Errorf("unexpected driver-cache spec when DriverSessionCacheDir is undeclared: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_SkillsDirMounted verifies that a runtime SkillsDir
// override plus a declared DriverSkillsDir produce a read-only MountSpec with
// the SPINDRIFT_SKILLS_DIR operator message — computed once, independent of
// backend.
func TestBuildMountSpecs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{SkillsDir: dir, DriverSkillsDir: "/home/agent/.claude/skills"}, Box{})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/home/agent/.claude/skills" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a skills-dir spec in %+v", specs)
	}
	if found.Source != dir {
		t.Errorf("Source = %q, want %q", found.Source, dir)
	}
	if !found.ReadOnly {
		t.Errorf("skills-dir mount must be read-only")
	}
	want := "==> SPINDRIFT_SKILLS_DIR set; mounting " + dir + " over /home/agent/.claude/skills\n"
	if found.Message != want {
		t.Errorf("Message = %q, want %q", found.Message, want)
	}
}

// TestBuildMountSpecs_SkillsDirUnset_NoMount verifies that omitting SkillsDir
// produces no skills spec.
func TestBuildMountSpecs_SkillsDirUnset_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{DriverSkillsDir: "/home/agent/.claude/skills"}, Box{})

	for _, s := range specs {
		if s.Target == "/home/agent/.claude/skills" {
			t.Errorf("unexpected skills-dir spec when SkillsDir is empty: %+v", specs)
		}
	}
}
