#!/usr/bin/env bats
# The `run` command mounts the configurable prompt template and honors the
# on-disk hot override (SPINDRIFT_PROMPT_DIR). Driven through the fake podman.

load helper

setup() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR"
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_IMAGE_PRESENT=1
  unset SPINDRIFT_PROMPT_DIR
}

@test "run mounts the baked prompt dir at /agent/prompts by default" {
  : "${PROMPT_PATH:?PROMPT_PATH must be set by the check}"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "-v $PROMPT_PATH:/agent/prompts" "$PODMAN_LOG"
  [[ "$output" != *"SPINDRIFT_PROMPT_DIR"* ]]
}

@test "SPINDRIFT_PROMPT_DIR overrides the baked prompt dir with a log line" {
  local override="$BATS_TEST_TMPDIR/myprompts"
  mkdir -p "$override"
  echo "hot-override" >"$override/issue-prompt.md"
  export SPINDRIFT_PROMPT_DIR="$override"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"SPINDRIFT_PROMPT_DIR"* ]]
  grep -q -- "-v $override:/agent/prompts" "$PODMAN_LOG"
  ! grep -q -- "-v $PROMPT_PATH:/agent/prompts" "$PODMAN_LOG"
}

@test "SPINDRIFT_PROMPT_DIR falls back to the baked default when the dir is missing" {
  export SPINDRIFT_PROMPT_DIR="$BATS_TEST_TMPDIR/nope"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "-v $PROMPT_PATH:/agent/prompts" "$PODMAN_LOG"
  [[ "$output" != *"SPINDRIFT_PROMPT_DIR"* ]]
}
