#!/usr/bin/env bats
# CODE_FORGE=local harness-owned code-out (issue #1808): the entrypoint, not
# the Agent, produces the seam bundle after the Driver exits, via
# driver-exec's bundle-out verb. A Box that committed nothing yet claimed
# ready gets a corrective blocked outcome instead of settling as a false
# ready.

load helper

setup() {
  setup_entrypoint_env
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
}

@test "CODE_FORGE=local with real commits writes a seam bundle to the outbox" {
  export FAKE_CLAUDE_COMMIT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

@test "CODE_FORGE=local with no commits after a ready claim appends a corrective blocked outcome" {
  # Default fake claude claims status=ready but (with no
  # FAKE_CLAUDE_COMMIT) never commits anything on the branch.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -f "$OUTBOX_DIR/seam.bundle" ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=none status=blocked note=.*ready.*no commits exist on agent/issue-7' <<<"$output"
}

@test "CODE_FORGE=github never invokes bundle-out" {
  unset CODE_FORGE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$OUTBOX_DIR" ]
}
