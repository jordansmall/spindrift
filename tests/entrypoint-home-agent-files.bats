#!/usr/bin/env bats
# bwrap-mode copy-in of baked /home/agent content (issue #2843). bwrap.go
# ro-binds that content at HARNESS_HOME_AGENT_DIR; the entrypoint must copy it
# into the real tmpfs $HOME at startup, the same copy-not-mount pattern
# _populate_driver_skills_dir uses (tests/entrypoint-skills.bats).

load helper

setup() {
  setup_entrypoint_env
}

# bats' recursive cleanup of BATS_TEST_TMPDIR fails on the read-only
# directories these tests stage, so restore write permission first.
teardown() {
  [ -d "$HOME" ] && chmod -R u+w "$HOME"
  [ -n "${HARNESS_HOME_AGENT_DIR:-}" ] && [ -d "$HARNESS_HOME_AGENT_DIR" ] && chmod -R u+w "$HARNESS_HOME_AGENT_DIR"
  true
}

# Stages lib/image.nix's empty session-cache placeholder under
# HARNESS_HOME_AGENT_DIR so `find` enumerates it, plus a $HOME stand-in for a
# live bwrap --bind of a Driver's session-cache dir. Exporting
# DRIVER_SESSION_CACHE_DIR here would be pointless: setup_entrypoint_env's
# preamble re-roots it to "$HOME/.claude/projects" regardless.
_stage_session_cache_fixture() {
  local mode="$1"

  export HARNESS_HOME_AGENT_DIR="$BATS_TEST_TMPDIR/harness-home-agent"
  mkdir -p "$HARNESS_HOME_AGENT_DIR/.claude"
  echo '{"hooks": {}}' >"$HARNESS_HOME_AGENT_DIR/.claude/settings.json"
  chmod 444 "$HARNESS_HOME_AGENT_DIR/.claude/settings.json"
  mkdir -p "$HARNESS_HOME_AGENT_DIR/.claude/projects"

  mkdir -p "$HOME/.claude/projects"
  echo "host content" >"$HOME/.claude/projects/session.json"
  chmod 444 "$HOME/.claude/projects/session.json"
  chmod "$mode" "$HOME/.claude/projects"
}

_assert_session_cache_untouched() {
  local mode="$1"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$HOME/.claude/settings.json" ]
  [ -w "$HOME/.claude/settings.json" ]

  [ ! -w "$HOME/.claude/projects/session.json" ]
  [ "$(stat -c %a "$HOME/.claude/projects")" = "$mode" ]
}

# A plain `cp -r` from a read-only Nix-store-like source preserves the
# read-only mode bits (lib/image.nix hit the same subtlety, commit 8961b62e),
# so the entrypoint's copy step must follow up with an explicit
# `chmod -R u+w`.
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

# The chmod that makes copied-in content writable must never land on a live
# host bind mount, e.g. $HOME/.claude/projects for the claude driver
# (runner/mount.go's driver-cache MountSpec). This test seeds 755, not the
# source's read-only mode: an equal seed would leave a `cp -r` overwrite of
# the mount root (755 to 555) indistinguishable from untouched.
@test "pre-existing content elsewhere under HOME keeps its mode (issue #2843)" {
  _stage_session_cache_fixture 755
  _assert_session_cache_untouched 755
}

# This test seeds 555, not 755 like the #2843 test above: `chmod u+w` on an
# already-owner-writable directory is a silent no-op, so a 755 seed could
# never turn red even if a trailing-slash mismatch in the guard let the chmod
# land on this directory. tests/helper.bash delivers the trailing slash.
@test "DRIVER_SESSION_CACHE_DIR with a trailing slash still matches the placeholder (issue #2845)" {
  _stage_session_cache_fixture 555
  export TEST_SESSION_CACHE_DIR_SUFFIX="/"
  _assert_session_cache_untouched 555
}

# #2843's own test already exercises this shape, but its 755 seed can't tell a
# correct guard from one regressed to require a trailing slash (e.g.
# `[ "${_target%/}/" = "$DRIVER_SESSION_CACHE_DIR" ]`); seeded 555 for the
# reason given above.
@test "DRIVER_SESSION_CACHE_DIR without a trailing slash still matches the placeholder (issue #2845)" {
  _stage_session_cache_fixture 555
  _assert_session_cache_untouched 555
}

# The reported bug (#2843): a copied-in directory that is neither
# DRIVER_AGENT_FILES_DIR nor DRIVER_SESSION_CACHE_DIR kept the source's
# read-only mode after the copy. The explicit `chmod 555` is what exercises
# it: without it `cp -r` would create a normal writable directory in $HOME
# regardless of any fix.
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

# DRIVER_AGENT_FILES_DIR needs directory-level write access, not just writable
# files: promptassembly/assemble.go's rewriteAgentFiles does
# `os.Remove(reviewerPath)` on a file inside it, and removing a file needs
# write and execute on the containing directory.
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
