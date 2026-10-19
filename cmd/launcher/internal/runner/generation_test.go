package runner

import "testing"

// NewAgentGeneration treats its argument as the agent-closure linkFarm's own
// store path (res.TipTag under bwrap), not the agentFiles derivation: it
// derives the "files", "env", "nix-config" and "prefetch" children (issue
// #2954). Generation comes from the closure path via the same
// SafePathComponent rule bwrap.ClosureGeneration uses for a baked Config.ImageTag.
func TestNewAgentGeneration_DerivesFilesAndEnvFromAgentClosurePath(t *testing.T) {
	cases := []struct {
		name    string
		closure string
		want    AgentGeneration
	}{
		{
			"normal store path",
			"/nix/store/abc123-agent-closure",
			AgentGeneration{
				AgentFiles:    "/nix/store/abc123-agent-closure/files",
				AgentEnv:      "/nix/store/abc123-agent-closure/env",
				NixConfigFile: "/nix/store/abc123-agent-closure/nix-config",
				PrefetchFile:  "/nix/store/abc123-agent-closure/prefetch",
				Generation:    "abc123-agent-closure",
			},
		},
		// filepath.Join elides an empty first element rather than preserving
		// it, so an empty closure still yields relative "files"/"env"/
		// "nix-config"/"prefetch" (not ""); Generation still comes out ""
		// since SafePathComponent rejects an empty input directly.
		{"empty", "", AgentGeneration{AgentFiles: "files", AgentEnv: "env", NixConfigFile: "nix-config", PrefetchFile: "prefetch", Generation: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewAgentGeneration(tc.closure)
			if got != tc.want {
				t.Errorf("NewAgentGeneration(%q) = %+v, want %+v", tc.closure, got, tc.want)
			}
		})
	}
}
