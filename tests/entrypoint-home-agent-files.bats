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

# Shared staging for the three session-cache guard tests below (#2843,
# #2845 x2). Stages lib/image.nix's empty session-cache placeholder directory
# at the same HARNESS_HOME_AGENT_DIR-relative path a real dispatch bakes it
# at, so `find "$HARNESS_HOME_AGENT_DIR" -mindepth 1` actually enumerates it
# -- skipping this staging would leave the placeholder unseen and unable to
# catch a chmod that lands on it. Also stands in for a bwrap --bind mount of
# a Driver's session-cache dir directly under $HOME: a read-only file plus a
# directory chmod'd to the caller-given $1, mirroring a live host mount that
# already exists before the entrypoint runs. Callers own the rationale for
# which mode they pass -- it's the one thing that legitimately differs
# between the three tests. The session.json read-only check
# _assert_session_cache_untouched makes afterward guards a different
# regression (a recursive `chmod -R u+w`), not the directory-mode guard
# itself -- the chmod here is non-recursive and `find` only walks
# HARNESS_HOME_AGENT_DIR.
# DRIVER_SESSION_CACHE_DIR is deliberately left unexported here:
# setup_entrypoint_env's registry-rendered preamble re-roots it under $HOME
# to "$HOME/.claude/projects" (the path staged above) regardless, so
# exporting it in this helper would just be clobbered by that preamble.
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

# Shared assertion tail for those same three tests: runs the entrypoint, then
# checks the baked content still lands writable under $HOME and the staged
# mount stand-in came through untouched -- still holding a read-only
# session.json, and still at the mode its caller seeded it with ($1, again
# the one thing that legitimately differs between the three).
_assert_session_cache_untouched() {
  local mode="$1"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$HOME/.claude/settings.json" ]
  [ -w "$HOME/.claude/settings.json" ]

  [ ! -w "$HOME/.claude/projects/session.json" ]
  [ "$(stat -c %a "$HOME/.claude/projects")" = "$mode" ]
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
# placeholder directory at that exact relative path whenever the driver
# declares sessionCacheDirRelative, and bwrap.go ro-binds that whole tree --
# placeholder included -- at HARNESS_HOME_AGENT_DIR (see
# _stage_session_cache_fixture for why that placeholder has to be staged
# there). Seeds the $HOME stand-in at 0755, the realistic mode for a
# launcher-created driver-cache dir (cmd/launcher/internal/dispatch/
# factory.go's newCache) -- deliberately writable, not read-only, because a
# seed at the same read-only mode the Nix-store source already has would
# leave a `cp -r`-induced 755->555 mode overwrite of the mount root
# indistinguishable from "untouched" (555 in, 555 out either way); only a
# seed that differs from the source's mode can actually catch that
# mutation.
@test "pre-existing content elsewhere under HOME keeps its mode (issue #2843)" {
  _stage_session_cache_fixture 755
  _assert_session_cache_untouched 755
}

# Trailing-slash variant of the #2843 guard test above; together with the
# no-trailing-slash variant below, covers the issue's "both with/without
# trailing-slash" acceptance criterion. Seeded read-only (555), not 755 like
# the #2843 test above: `chmod u+w` on an already-owner-writable 755
# directory is a silent no-op, so a 755 seed here could never turn red even
# if the guard's trailing-slash mismatch let the chmod land on this
# directory. The trailing slash is delivered via TEST_SESSION_CACHE_DIR_SUFFIX
# (see tests/helper.bash).
@test "DRIVER_SESSION_CACHE_DIR with a trailing slash still matches the placeholder (issue #2845)" {
  _stage_session_cache_fixture 555
  export TEST_SESSION_CACHE_DIR_SUFFIX="/"
  _assert_session_cache_untouched 555
}

# No-trailing-slash variant of the guard test above
# (TEST_SESSION_CACHE_DIR_SUFFIX left unset, its default-empty state) -- the
# other half of the issue's "both with/without trailing-slash" acceptance
# criterion. This is the exact shape #2843's own test already exercises, but
# that test's 755 seed can't tell a correct guard from one regressed to
# require a trailing slash (e.g.
# `[ "${_target%/}/" = "$DRIVER_SESSION_CACHE_DIR" ]`); seeded 555 for the
# reason given above.
@test "DRIVER_SESSION_CACHE_DIR without a trailing slash still matches the placeholder (issue #2845)" {
  _stage_session_cache_fixture 555
  _assert_session_cache_untouched 555
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
