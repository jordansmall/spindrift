package main

import "io"

// storeNotice must be loud: it prints when box starts. NIX_STORE_WRITABLE is
// baked by mkHarness's nixStoreWritable knob (ADR 0018, issue #469), and
// self-test mode trades hermeticity for in-box `nix flake check` feedback. New
// store paths land only in this container's ephemeral layer; the image and
// shared volumes are never mutated.
const storeNotice = "==> WARNING: /nix/store is writable (self-test mode) — this Box is not hermetic; do not use for untrusted issues"

func warnWritableStore(getenv func(string) string, w io.Writer) {
	if getenv("NIX_STORE_WRITABLE") == "true" {
		_, _ = io.WriteString(w, storeNotice+"\n")
	}
}

// withDirDefaults fills the work, outbox, repo-mount, skills and home directories.
// These are true runtime mount points or image paths, so they keep literal
// defaults here; an empty value counts as unset, like bash's `${X:-default}`.
func withDirDefaults(in inputs, getenv func(string) string) inputs {
	orDefault := func(name, def string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return def
	}
	if in.WorkDir == "" {
		in.WorkDir = "/work"
	}
	// The writable mount box's bundle-out step writes CODE_FORGE=local's seam
	// bundle into (ADR 0033, issue #1808).
	if in.OutboxDir == "" {
		in.OutboxDir = "/outbox"
	}
	// The read-only Accumulation-repo mount CODE_FORGE=local clones from.
	in.RepoMountDir = orDefault("REPO_MOUNT_DIR", "/repo")
	// HARNESS_SKILLS_DIR holds the baked harness-owned and Consumer-configured
	// skills (lib/image.nix).
	in.HarnessSkillsDir = orDefault("HARNESS_SKILLS_DIR", "/agent/skills")
	// OPERATOR_SKILLS_DIR is where SPINDRIFT_SKILLS_DIR's runtime override
	// mounts (issue #2489), distinct from DRIVER_SKILLS_DIR because a mount
	// directly onto that would replace its contents and hide the baked skills.
	in.OperatorSkillsDir = orDefault("OPERATOR_SKILLS_DIR", "/operator-skills")
	// HARNESS_HOME_AGENT_DIR is where bwrap.go stages baked /home/agent content
	// read-only (issue #2843). It is top-level rather than under /agent because
	// /agent is already bound read-only by then, and bwrap cannot fabricate a
	// mountpoint inside a read-only bind. The OCI image bakes the same content
	// writable at the real /home/agent.
	in.HarnessHomeAgentDir = orDefault("HARNESS_HOME_AGENT_DIR", "/home-agent-staged")
	return in
}
