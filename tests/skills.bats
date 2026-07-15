#!/usr/bin/env bats
# The skills dir is empty by default; it is only bind-mounted when
# SPINDRIFT_SKILLS_DIR points at an existing directory.
# Driven through the fake podman (OCI) and fake bwrap.

load helper

setup() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR"
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

@test "SPINDRIFT_SKILLS_DIR mounts over /home/agent/.claude/skills (OCI)" {
  local skills="$BATS_TEST_TMPDIR/myskills"
  mkdir -p "$skills"
  echo '#!/bin/bash' >"$skills/my-skill.sh"
  export SPINDRIFT_SKILLS_DIR="$skills"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"SPINDRIFT_SKILLS_DIR"* ]]
  grep -q -- "-v $skills:/home/agent/.claude/skills:ro" "$PODMAN_LOG"
}

@test "SPINDRIFT_SKILLS_DIR pointing at a missing dir uses no mount (OCI)" {
  export SPINDRIFT_SKILLS_DIR="$BATS_TEST_TMPDIR/nope"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '/.claude/skills' "$PODMAN_LOG"
}

@test "SPINDRIFT_SKILLS_DIR mounts read-only over /home/agent/.claude/skills (bwrap)" {
  local skills="$BATS_TEST_TMPDIR/myskills-bwrap"
  mkdir -p "$skills"
  echo '#!/bin/bash' >"$skills/my-skill.sh"
  export SPINDRIFT_SKILLS_DIR="$skills"
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "--ro-bind $skills /home/agent/.claude/skills" "$BWRAP_LOG"
}

@test "run mounts no skills dir by default (bwrap)" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '/.claude/skills' "$BWRAP_LOG"
  [[ "$output" != *"SPINDRIFT_SKILLS_DIR"* ]]
}

# --- baked skills (issue #119) ------------------------------------------------
# Skills baked into the image at build time are exposed in the bwrap sandbox
# via a --ro-bind even without SPINDRIFT_SKILLS_DIR. For OCI the skills are
# in the image layer; no extra mount is added by the launcher.

@test "baked skills: mounted in bwrap sandbox without SPINDRIFT_SKILLS_DIR" {
  unset SPINDRIFT_SKILLS_DIR
  run "$SKILLS_BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "--ro-bind.*home/agent/.claude/skills /home/agent/.claude/skills" "$BWRAP_LOG"
}

@test "baked skills: SPINDRIFT_SKILLS_DIR takes precedence over baked skills (bwrap)" {
  # Runtime mount is applied; that it shadows baked skills is proven by
  # TestBwrapArgs_RuntimeSkillsTakePrecedence in the Go unit suite.
  local skills="$BATS_TEST_TMPDIR/runtime-override-bwrap"
  mkdir -p "$skills"
  export SPINDRIFT_SKILLS_DIR="$skills"
  run "$SKILLS_BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "--ro-bind $skills /home/agent/.claude/skills" "$BWRAP_LOG"
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
  grep -q -- "-v $skills:/home/agent/.claude/skills:ro" "$PODMAN_LOG"
}
