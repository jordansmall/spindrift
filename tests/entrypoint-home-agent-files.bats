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
# subtlety, commit 8961b62e), so the entrypoint's copy step must follow up
# with an explicit `chmod -R u+w` -- not just existence, but writability of
# the copied file under $HOME.
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
# writable must never land on a directory that mirrors a live host bind
# mount -- because under bwrap $HOME can also hold a live writable --bind
# mount for a Driver's session-cache dir
# (cmd/launcher/internal/runner/mount.go's driver-cache MountSpec, e.g.
# $HOME/.claude/projects for the claude driver) that points at a directory
# on the HOST filesystem. lib/image.nix (300-303) pre-creates an EMPTY
# placeholder directory at that exact relative path inside the baked
# home/agent tree whenever the driver declares sessionCacheDirRelative, and
# bwrap.go ro-binds that whole tree -- placeholder included -- at
# HARNESS_HOME_AGENT_DIR. So `find "$HARNESS_HOME_AGENT_DIR" -mindepth 1`
# genuinely enumerates that placeholder directory in a real dispatch: this
# test stages the same empty placeholder inside HARNESS_HOME_AGENT_DIR (not
# just the stand-in directory directly under $HOME) so the placeholder is
# actually enumerated, mirroring lib/image.nix's `mkdir -p` and reproducing
# the real bug -- a version of this test that skips staging the placeholder
# never enumerates the mirrored $HOME path and can't catch a chmod that
# lands on it. Simulates the live host mount by pre-creating a read-only
# file and a writable directory directly under $HOME (0755, the realistic
# mode for a launcher-created driver-cache dir -- cmd/launcher/internal/
# dispatch/factory.go's newCache), outside anything HARNESS_HOME_AGENT_DIR's
# real (non-placeholder) content stages, and asserts neither one's mode is
# touched by the run. The directory is deliberately seeded writable, not
# read-only: seeding it at the same read-only mode the Nix-store source
# already has would leave a `cp -r`-induced 755->555 mode overwrite of the
# mount root indistinguishable from "untouched" (555 in, 555 out either
# way) -- only a seed that differs from the source's mode can actually
# catch that mutation.
@test "pre-existing content elsewhere under HOME keeps its mode (issue #2843)" {
  export HARNESS_HOME_AGENT_DIR="$BATS_TEST_TMPDIR/harness-home-agent"
  mkdir -p "$HARNESS_HOME_AGENT_DIR/.claude"
  echo '{"hooks": {}}' >"$HARNESS_HOME_AGENT_DIR/.claude/settings.json"
  chmod 444 "$HARNESS_HOME_AGENT_DIR/.claude/settings.json"

  # lib/drivers/default.nix's renderPreamble exports this whenever the
  # selected driver declares sessionCacheDirRelative (e.g. the claude
  # driver); it's what identifies the session-cache placeholder below as
  # the one directory the entrypoint must never chmod.
  export DRIVER_SESSION_CACHE_DIR="$HOME/.claude/projects"

  # lib/image.nix's empty session-cache placeholder directory, staged at the
  # same HARNESS_HOME_AGENT_DIR-relative path as the live host bind mount
  # simulated below, so `find` actually enumerates it.
  mkdir -p "$HARNESS_HOME_AGENT_DIR/.claude/projects"

  # Stand in for a bwrap --bind mount of a Driver's session-cache dir: a
  # read-only file and a read-only directory that already exist under $HOME
  # before the entrypoint runs, and that HARNESS_HOME_AGENT_DIR never stages
  # any real (non-placeholder) content into.
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

# Proves the fix doesn't overcorrect: DRIVER_AGENT_FILES_DIR (e.g.
# /home/agent/.config/opencode/agents for the opencode driver, set by
# lib/drivers/default.nix's renderPreamble) is the one directory that
# genuinely needs directory-level write access from this copy-in --
# cmd/launcher/internal/promptassembly/assemble.go's rewriteAgentFiles does
# `os.Remove(reviewerPath)` on a file inside it when the orchestrator is on,
# and removing a file needs write+execute on its *containing* directory, not
# just the file's own write bit. Stages a read-only file inside it (as a
# read-only `cp -r` source would preserve) and asserts both that the
# directory itself ends up writable and that removing a file from it -- the
# exact operation rewriteAgentFiles performs -- actually succeeds.
# Proves the actual reported bug (#2843): the old code only chmod'd a
# directory when it happened to be the DRIVER_AGENT_FILES_DIR allowlist
# entry, so every OTHER copied-in directory -- e.g. ~/.config, staged here
# with a read-only file inside it and the directory itself chmod'd 555 the
# same way a real Nix-store directory (or bwrap ro-bind) is read-only --
# kept the source's read-only mode after the copy. Without the directory
# itself also chmod'd read-only here, `cp -r` would just create a normal
# writable directory in $HOME regardless of any fix, since a plain `mkdir
# -p` default mode is already writable -- the explicit `chmod 555` is what
# makes this test actually exercise the bug. This directory is neither
# DRIVER_AGENT_FILES_DIR nor DRIVER_SESSION_CACHE_DIR (both left unset
# here), so under the old allowlist logic it's never chmod'd. Proves it
# functionally the way the real bug manifests: `gh` needing to `mkdir
# ~/.config/gh` to write its own config.
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
