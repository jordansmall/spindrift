#!/usr/bin/env bats

# These pin the shell wrapper's harness.env sourcing (lib/mkHarness.nix
# runShellBody, shared by the spindrift CLI), which is shell no in-process Go
# test can reach. The files carry secrets only: a non-secret knob in the
# environment exits 6 (ADR 0020), so knobs go on the command line here.

load helper

setup() { setup_dispatch_env; }

# tokens_seen: the distinct GH_TOKEN values the fake gh was invoked with.
tokens_seen() { sort -u "$GH_LOG.token"; }

@test "dispatch reads secrets from \$PWD/harness.env" {
  unset GH_TOKEN
  echo 'GH_TOKEN=from-file-token' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_CMD" dispatch "${DISPATCH_FLAGS[@]}" --repo-slug from-flag/repo
  [ "$status" -eq 0 ]
  grep -q 'REPO_SLUG=from-flag/repo' "$PODMAN_LOG"
  [ "$(tokens_seen)" = from-file-token ]
}

@test "harness.env overrides the environment (parity with old bin/run)" {
  export GH_TOKEN=env-token
  echo 'GH_TOKEN=from-file-token' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_CMD" dispatch "${DISPATCH_FLAGS[@]}"
  [ "$status" -eq 0 ]
  [ "$(tokens_seen)" = from-file-token ]
}

# ADR 0020: a knob exported or assigned in harness.env is no longer read.
@test "an exported knob exits 6 naming its flag and settings path" {
  MERGE_MODE=manual run "$SPINDRIFT_CMD" dispatch "${DISPATCH_FLAGS[@]}"
  [ "$status" -eq 6 ]
  [[ "$output" == *'MERGE_MODE=manual set in environment'* ]]
  [[ "$output" == *'--merge-policy'* ]]
  [[ "$output" == *'git.merge.policy'* ]]
}

@test "a knob assigned in harness.env exits 6" {
  echo 'LABEL=file-label' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_CMD" dispatch "${DISPATCH_FLAGS[@]}"
  [ "$status" -eq 6 ]
  [[ "$output" == *'LABEL=file-label set in environment'* ]]
  [[ "$output" == *'--dispatch-label'* ]]
}

# Issue #5050: the baked Driver (claude for the bats harness) always wins over
# a DRIVER in harness.env; the mismatch warns exactly once.
@test "harness.env DRIVER differing from the baked Driver warns exactly once" {
  echo 'DRIVER=opencode' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_CMD" dispatch "${DISPATCH_FLAGS[@]}"
  [ "$status" -eq 0 ]
  [[ "$output" == *'DRIVER=opencode set in environment'* ]]
  [ "$(grep -c 'using the baked Driver' <<<"$output")" -eq 1 ]
}

@test "harness.env DRIVER equal to the baked Driver does not warn" {
  echo 'DRIVER=claude' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_CMD" dispatch "${DISPATCH_FLAGS[@]}"
  [ "$status" -eq 0 ]
  [[ "$output" != *'using the baked Driver'* ]]
}

# Issue #5053 (ADR 0064): in a multi-Driver flake harness.<driver>.env layers
# over harness.env for the Harness's own baked Driver (claude here); a
# single-Driver Harness ignores it.
@test "multi-Driver: harness.claude.env overrides harness.env" {
  echo 'GH_TOKEN=shared-token' >"$BATS_TEST_TMPDIR/harness.env"
  echo 'GH_TOKEN=claude-token' >"$BATS_TEST_TMPDIR/harness.claude.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch "${DISPATCH_FLAGS[@]}"
  [ "$status" -eq 0 ]
  [ "$(tokens_seen)" = claude-token ]
}

@test "multi-Driver: harness.opencode.env is not read by the claude app" {
  echo 'GH_TOKEN=shared-token' >"$BATS_TEST_TMPDIR/harness.env"
  echo 'GH_TOKEN=opencode-token' >"$BATS_TEST_TMPDIR/harness.opencode.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch "${DISPATCH_FLAGS[@]}"
  [ "$status" -eq 0 ]
  [ "$(tokens_seen)" = shared-token ]
}

@test "multi-Driver: harness.claude.env overrides the environment" {
  export GH_TOKEN=env-token
  echo 'GH_TOKEN=claude-token' >"$BATS_TEST_TMPDIR/harness.claude.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch "${DISPATCH_FLAGS[@]}"
  [ "$status" -eq 0 ]
  [ "$(tokens_seen)" = claude-token ]
}

@test "single-Driver: harness.claude.env is ignored" {
  echo 'GH_TOKEN=shared-token' >"$BATS_TEST_TMPDIR/harness.env"
  echo 'GH_TOKEN=claude-token' >"$BATS_TEST_TMPDIR/harness.claude.env"
  run "$SPINDRIFT_CMD" dispatch "${DISPATCH_FLAGS[@]}"
  [ "$status" -eq 0 ]
  [ "$(tokens_seen)" = shared-token ]
}
