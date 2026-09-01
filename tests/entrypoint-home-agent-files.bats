#!/usr/bin/env bats
# bwrap-mode copy-in of baked /home/agent content (issue #2843). bwrap.go
# ro-binds baked /home/agent content (Claude hooks, settings.json, opencode
# agent files) at HARNESS_HOME_AGENT_DIR; the entrypoint must copy it into the
# real (tmpfs) $HOME at startup, mirroring _populate_driver_skills_dir's
# copy-not-mount pattern (tests/entrypoint-skills.bats).

load helper

setup() {
  setup_entrypoint_env
}

# Restores write permission on any read-only content the tests below stage
# directly under $HOME (simulating a host-owned bind mount) or under
# HARNESS_HOME_AGENT_DIR (simulating a read-only Nix-store/bwrap-ro-bind
# source directory), so bats' own recursive cleanup of BATS_TEST_TMPDIR
# doesn't fail trying to remove a directory it can no longer write to.
teardown() {
  [ -d "$HOME" ] && chmod -R u+w "$HOME"
  [ -n "${HARNESS_HOME_AGENT_DIR:-}" ] && [ -d "$HARNESS_HOME_AGENT_DIR" ] && chmod -R u+w "$HARNESS_HOME_AGENT_DIR"
  true
}

# Proves the crux of the fix: a plain `cp -r` from a read-only Nix-store-like
# source preserves the read-only mode bits (lib/image.nix hit the same
# subtlety), so the entrypoint's copy step must follow up with an explicit
# `chmod u+w` -- assert writability of the copied file, not just existence.
@test "baked home-agent content is copied into HOME and made writable (issue #2843)" {
  export HARNESS_HOME_AGENT_DIR="$BATS_TEST_TMPDIR/harness-home-agent"
  mkdir -p "$HARNESS_HOME_AGENT_DIR/.claude"
  echo '{"hooks": {}}' >"$HARNESS_HOME_AGENT_DIR/.claude/settings.json"
  chmod 444 "$HARNESS_HOME_AGENT_DIR/.claude/settings.json"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$HOME/.claude/settings.json" ]
  [ -w "$HOME/.claude/settings.json" ]
}

# Proves the fix's scoping invariant: the chmod that makes copied-in content
# writable must never land on a directory that mirrors a live host bind mount.
# Under bwrap $HOME can hold a live writable --bind for a Driver's session-cache
# dir (mount.go's driver-cache MountSpec, e.g. $HOME/.claude/projects) pointing
# at a HOST directory, and lib/image.nix pre-creates an EMPTY placeholder at
# that same relative path inside the baked home/agent tree, which bwrap.go
# ro-binds wholesale -- so `find "$HARNESS_HOME_AGENT_DIR" -mindepth 1` really
# does enumerate it in a real dispatch. This test stages that same placeholder
# (a version that skips it never enumerates the mirrored $HOME path and can't
# catch a chmod that lands on it), simulates the live mount with a read-only
# file and a writable directory directly under $HOME, and asserts neither one's
# mode is touched. The directory is deliberately seeded writable (0755, the
# realistic mode for a launcher-created cache dir): seeding it read-only would
# leave a `cp -r`-induced 755->555 overwrite indistinguishable from "untouched".
@test "pre-existing content elsewhere under HOME keeps its mode (issue #2843)" {
  export HARNESS_HOME_AGENT_DIR="$BATS_TEST_TMPDIR/harness-home-agent"
  mkdir -p "$HARNESS_HOME_AGENT_DIR/.claude"
  echo '{"hooks": {}}' >"$HARNESS_HOME_AGENT_DIR/.claude/settings.json"
  chmod 444 "$HARNESS_HOME_AGENT_DIR/.claude/settings.json"

  # lib/drivers/default.nix's renderPreamble exports this whenever the selected
  # driver declares sessionCacheDirRelative; it identifies the session-cache
  # placeholder below as the one directory the entrypoint must never chmod.
  export DRIVER_SESSION_CACHE_DIR="$HOME/.claude/projects"

  # lib/image.nix's empty session-cache placeholder directory, staged at the
  # same HARNESS_HOME_AGENT_DIR-relative path as the live host bind mount
  # simulated below, so `find` actually enumerates it.
  mkdir -p "$HARNESS_HOME_AGENT_DIR/.claude/projects"

  # Stand in for a bwrap --bind of a Driver's session-cache dir: a read-only
  # file and directory that already exist under $HOME before the entrypoint
  # runs, and that HARNESS_HOME_AGENT_DIR stages no real content into.
  mkdir -p "$HOME/.claude/projects"
  echo "host content" >"$HOME/.claude/projects/session.json"
  chmod 444 "$HOME/.claude/projects/session.json"
  chmod 755 "$HOME/.claude/projects"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$HOME/.claude/settings.json" ]
  [ -w "$HOME/.claude/settings.json" ]

  [ ! -w "$HOME/.claude/projects/session.json" ]
  [ "$(stat -c %a "$HOME/.claude/projects")" = "755" ]
}

# Proves the fix doesn't overcorrect: DRIVER_AGENT_FILES_DIR is the one
# directory that genuinely needs directory-level write access from this copy-in
# -- promptassembly's rewriteAgentFiles does `os.Remove` on a file inside it
# when the orchestrator is on, and removing a file needs write+execute on its
# *containing* directory. Stages a read-only file inside it and asserts both
# that the directory ends up writable and that removing a file from it
# succeeds.
# Also proves the actual reported bug (#2843): the old code chmod'd a directory
# only when it was the DRIVER_AGENT_FILES_DIR allowlist entry, so every OTHER
# copied-in directory kept the source's read-only mode. ~/.config is staged here
# with an explicit `chmod 555` -- without that the `cp -r` would just create a
# normally-writable directory and the test wouldn't exercise the bug at all --
# and is neither DRIVER_AGENT_FILES_DIR nor DRIVER_SESSION_CACHE_DIR, so the old
# allowlist logic never chmod'd it. Asserted functionally, the way the bug
# manifests: `gh` needing to `mkdir ~/.config/gh`.
@test "ordinary copied-in directory is made writable (issue #2843)" {
  export HARNESS_HOME_AGENT_DIR="$BATS_TEST_TMPDIR/harness-home-agent"
  mkdir -p "$HARNESS_HOME_AGENT_DIR/.config"
  echo '{}' >"$HARNESS_HOME_AGENT_DIR/.config/some-file.json"
  chmod 444 "$HARNESS_HOME_AGENT_DIR/.config/some-file.json"
  chmod 555 "$HARNESS_HOME_AGENT_DIR/.config"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -w "$HOME/.config" ]
  mkdir "$HOME/.config/gh"
}

@test "DRIVER_AGENT_FILES_DIR directory is made writable so a file can be removed from it (issue #2843)" {
  export HARNESS_HOME_AGENT_DIR="$BATS_TEST_TMPDIR/harness-home-agent"
  mkdir -p "$HARNESS_HOME_AGENT_DIR/.config/opencode/agents"
  echo "# reviewer" >"$HARNESS_HOME_AGENT_DIR/.config/opencode/agents/reviewer.md"
  chmod 444 "$HARNESS_HOME_AGENT_DIR/.config/opencode/agents/reviewer.md"

  export DRIVER_AGENT_FILES_DIR="$HOME/.config/opencode/agents"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -w "$HOME/.config/opencode/agents" ]
  rm "$HOME/.config/opencode/agents/reviewer.md"
}
