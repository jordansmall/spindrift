#!/usr/bin/env bats
# The launcher mounts SPINDRIFT_SKILLS_DIR at the fixed staging path
# /operator-skills, never on the Driver's skills dir; agent/entrypoint.sh
# merges it with the image's baked skills at box startup (issue #2489).

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

# Baked skills reach the box with no launcher-issued mount: they already sit
# under /agent/skills (bwrap ro-binds /agent; OCI has them in the image layer)
# and agent/entrypoint.sh merges them into the Driver skills dir at box startup
# (issues #119 and #2489; see tests/entrypoint-skills.bats).

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
  unset SPINDRIFT_SKILLS_DIR
  run "$SKILLS_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '/.claude/skills' "$PODMAN_LOG"
}

@test "baked skills: SPINDRIFT_SKILLS_DIR still mounts override for OCI" {
  local skills="$BATS_TEST_TMPDIR/runtime-override-oci"
  mkdir -p "$skills"
  export SPINDRIFT_SKILLS_DIR="$skills"
  run "$SKILLS_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "-v $skills:/operator-skills:ro" "$PODMAN_LOG"
}
