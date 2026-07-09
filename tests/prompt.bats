#!/usr/bin/env bats
# The prompt is baked into the image, so `run` mounts nothing by default; it
# only bind-mounts a dir under the on-disk override (SPINDRIFT_PROMPT_DIR).
# Driven through the fake podman.

load helper

setup() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR"
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_IMAGE_PRESENT=1
  unset SPINDRIFT_PROMPT_DIR
}

@test "run mounts no prompt dir by default (prompt is baked into the image)" {
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- ':/agent/prompts' "$PODMAN_LOG"
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

@test "SPINDRIFT_PROMPT_DIR pointing at a missing dir uses the baked prompt (no mount)" {
  export SPINDRIFT_PROMPT_DIR="$BATS_TEST_TMPDIR/nope"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- ':/agent/prompts' "$PODMAN_LOG"
  [[ "$output" != *"SPINDRIFT_PROMPT_DIR"* ]]
}

@test "SPINDRIFT_PROMPT_DIR mount covers all three prompt files via directory bind" {
  local override="$BATS_TEST_TMPDIR/myprompts"
  mkdir -p "$override"
  echo "hot-override" >"$override/issue-prompt.md"
  echo "custom-scout" >"$override/scout-prompt.md"
  echo "custom-review" >"$override/review-prompt.md"
  export SPINDRIFT_PROMPT_DIR="$override"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- "-v $override:/agent/prompts" "$PODMAN_LOG"
}

@test "scout prompt forbids mid-turn narration between tool calls" {
  local prompts="${PROMPTS_DIR:-$BATS_TEST_DIRNAME/../templates/default/prompts}"
  local prompt="$prompts/scout-prompt.md"
  grep -qi 'between tool calls' "$prompt"
}

@test "review prompt forbids mid-turn narration between tool calls" {
  local prompts="${PROMPTS_DIR:-$BATS_TEST_DIRNAME/../templates/default/prompts}"
  local prompt="$prompts/review-prompt.md"
  grep -qi 'between tool calls' "$prompt"
}

@test "review prompt caps each finding at one line" {
  local prompts="${PROMPTS_DIR:-$BATS_TEST_DIRNAME/../templates/default/prompts}"
  local prompt="$prompts/review-prompt.md"
  grep -qi 'one line' "$prompt"
}

@test "WATCH CI section uses GraphQL statusCheckRollup not gh pr checks" {
  # gh pr checks uses the check-runs REST endpoint which 403s under
  # fine-grained PATs; the prompt must use statusCheckRollup (GraphQL).
  # PROMPTS_DIR is exported by the nix check derivation; fall back to the
  # source tree when running bats locally outside the nix harness.
  local prompts="${PROMPTS_DIR:-$BATS_TEST_DIRNAME/../templates/default/prompts}"
  local prompt="$prompts/issue-prompt.md"
  ! grep -q 'until gh pr checks' "$prompt"
  grep -q 'statusCheckRollup' "$prompt"
  grep -qi 'fine-grained' "$prompt"
}
