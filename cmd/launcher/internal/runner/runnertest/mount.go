package runnertest

import (
	"os"
	"strings"
	"testing"
)

// AssertNoMountDecisionMarkers fails if adapterFile holds an operator message
// that belongs only to the shared BuildMountSpecs. The driver-cache gate has no
// unique string to pin, since its rationale comment legitimately differs per
// adapter, so this pins the two mounts that carry operator messages.
func AssertNoMountDecisionMarkers(t *testing.T, adapterFile string) {
	t.Helper()
	data, err := os.ReadFile(adapterFile)
	if err != nil {
		t.Fatalf("read %s: %v", adapterFile, err)
	}
	for _, marker := range []string{"SPINDRIFT_PROMPT_DIR set", "SPINDRIFT_SKILLS_DIR set"} {
		if strings.Contains(string(data), marker) {
			t.Errorf("%s contains mount-decision marker %q; operator messages must come from the shared BuildMountSpecs, not be duplicated in the adapter", adapterFile, marker)
		}
	}
}

// DriverSessionCacheTarget is the in-Box driver session-cache path the mount
// fixture asks for and expects back.
const DriverSessionCacheTarget = "/home/agent/.claude/projects"

// MountFixture is the host-side input of the mount table both families render.
type MountFixture struct {
	PromptDir, SkillsDir, CacheDir, ProxySocket string
}

func NewMountFixture(t *testing.T) MountFixture {
	t.Helper()
	return MountFixture{
		PromptDir:   t.TempDir(),
		SkillsDir:   t.TempDir(),
		CacheDir:    t.TempDir(),
		ProxySocket: NewTestSocket(t, "registry-proxy.sock"),
	}
}

// AssertRendered fails for each fixture mount missing from args, where sep joins
// a source to its target in the family's argv spelling (":" for OCI -v, " " for
// bwrap --bind).
func (f MountFixture) AssertRendered(t *testing.T, family, args, sep string) {
	t.Helper()
	for _, mount := range []struct{ source, target string }{
		{f.PromptDir, "/agent/prompts"},
		{f.SkillsDir, "/operator-skills"},
		{f.CacheDir, DriverSessionCacheTarget},
		{f.ProxySocket, "/registry-proxy.sock"},
	} {
		if !strings.Contains(args, mount.source+sep+mount.target) {
			t.Errorf("%s missing mount %s -> %s in args: %s", family, mount.source, mount.target, args)
		}
	}
}
