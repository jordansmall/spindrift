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
  [[ "$output" != *"SPINDRIFT_SKILLS_DIR"* ]]
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
}
