package runner

import "path/filepath"

// NewAgentGeneration derives an AgentGeneration from closure, the just-realized
// tip agent-closure linkFarm's own store output path (issue #2682), NOT the
// agentFiles derivation. That linkFarm nests four children, so AgentFiles,
// AgentEnv, NixConfigFile and PrefetchFile are closure's "files", "env",
// "nix-config" and "prefetch" children (lib/mkHarness.nix, issue #2954).
func NewAgentGeneration(closure string) AgentGeneration {
	return AgentGeneration{
		AgentFiles:    filepath.Join(closure, "files"),
		AgentEnv:      filepath.Join(closure, "env"),
		NixConfigFile: filepath.Join(closure, "nix-config"),
		PrefetchFile:  filepath.Join(closure, "prefetch"),
		Generation:    SafePathComponent(closure),
	}
}

// SafePathComponent returns s unless it is unsafe as a single path component
// (empty, ".", "..", or a bare separator), in which case it returns "" so the
// caller falls back to a known-safe default. Shared by NewAgentGeneration,
// bwrap.ClosureGeneration and the bwrap adapter's snapshotDirFor (issue
// #2681) so every rejection path stays in lockstep.
func SafePathComponent(s string) string {
	if s == "" {
		return ""
	}
	base := filepath.Base(s)
	if base == "." || base == ".." || base == string(filepath.Separator) {
		return ""
	}
	return base
}
