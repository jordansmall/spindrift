#!/usr/bin/env bats
# The skills dir is empty by default; it is only bind-mounted when
# SPINDRIFT_SKILLS_DIR points at an existing directory. That mount now lands
# at the fixed staging path /operator-skills rather than directly on the
# Driver's skills dir; agent/entrypoint.sh merges it (and the image's own
# baked skills) into the real skills dir at box startup (issue #2489).
# Driven through the fake podman (OCI) and fake bwrap.

load helper

setup() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR"
  stub_nix_var_snapshot
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_IMAGE_PRESENT=1
  unset SPINDRIFT_SKILLS_DIR
}

@test "run mounts no skills dir by default (OCI)" {
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '/.claude/skills' "$PODMAN_LOG"
  [[ "$output" != *"SPINDRIFT_SKILLS_DIR"* ]]
}

@test "SPINDRIFT_SKILLS_DIR mounts over /operator-skills (OCI)" {
  local skills="$BATS_TEST_TMPDIR/myskills"
  mkdir -p "$skills"
  echo '#!/bin/bash' >"$skills/my-skill.sh"
  export SPINDRIFT_SKILLS_DIR="$skills"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"SPINDRIFT_SKILLS_DIR"* ]]
  grep -q -- "-v $skills:/operator-skills:ro" "$PODMAN_LOG"
}

@test "SPINDRIFT_SKILLS_DIR pointing at a missing dir uses no mount (OCI)" {
  export SPINDRIFT_SKILLS_DIR="$BATS_TEST_TMPDIR/nope"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '/.claude/skills' "$PODMAN_LOG"
}

@test "SPINDRIFT_SKILLS_DIR mounts read-only over /operator-skills (bwrap)" {
  local skills="$BATS_TEST_TMPDIR/myskills-bwrap"
  mkdir -p "$skills"
  echo '#!/bin/bash' >"$skills/my-skill.sh"
  export SPINDRIFT_SKILLS_DIR="$skills"
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "--ro-bind $skills /operator-skills" "$BWRAP_LOG"
}

@test "run mounts no skills dir by default (bwrap)" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '/.claude/skills' "$BWRAP_LOG"
  [[ "$output" != *"SPINDRIFT_SKILLS_DIR"* ]]
}

# --- baked skills (issue #119; mount mechanism updated by issue #2489) -------
# Skills baked into the image at build time reach the box without any
# launcher-issued mount at all: they land under /agent/skills (bwrap: via the
# existing top-level /agent ro-bind; OCI: already in the image layer), and
# agent/entrypoint.sh's own copy step merges /agent/skills into the real
# Driver skills dir at box startup (see tests/entrypoint-skills.bats). The
# launcher itself only ever mounts the SPINDRIFT_SKILLS_DIR operator override,
# at the fixed staging path /operator-skills -- never anything targeting
# ".claude/skills" directly, baked or not.

@test "baked skills: no launcher-issued .claude/skills mount in bwrap sandbox without SPINDRIFT_SKILLS_DIR" {
  unset SPINDRIFT_SKILLS_DIR
  run "$SKILLS_BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '/.claude/skills' "$BWRAP_LOG"
}

@test "baked skills: SPINDRIFT_SKILLS_DIR still mounts override over /operator-skills (bwrap)" {
  local skills="$BATS_TEST_TMPDIR/runtime-override-bwrap"
  mkdir -p "$skills"
  export SPINDRIFT_SKILLS_DIR="$skills"
  run "$SKILLS_BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "--ro-bind $skills /operator-skills" "$BWRAP_LOG"
}

@test "baked skills: no extra mount added for OCI (skills are in image)" {
  # The OCI image carries baked skills in its filesystem; the launcher adds
  # no extra volume mount when SPINDRIFT_SKILLS_DIR is unset.
  unset SPINDRIFT_SKILLS_DIR
  run "$SKILLS_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '/.claude/skills' "$PODMAN_LOG"
}

@test "baked skills: SPINDRIFT_SKILLS_DIR still mounts override for OCI" {
  # Runtime override is respected even when skills are baked into the image.
  local skills="$BATS_TEST_TMPDIR/runtime-override-oci"
  mkdir -p "$skills"
  export SPINDRIFT_SKILLS_DIR="$skills"
  run "$SKILLS_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "-v $skills:/operator-skills:ro" "$PODMAN_LOG"
}
