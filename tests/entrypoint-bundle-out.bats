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
  export FAKE_DRIVER_COMMIT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

@test "CODE_FORGE=local with real commits and a non-zero driver crash still writes a seam bundle and propagates the exit code" {
  # ADR 0039 (issue #2252): bundle-out must run before the box exits, even
  # when the driver itself crashed non-zero -- a committed branch is real
  # work worth relaying regardless of why the driver's own process died.
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_CRASH_EXIT=17
  run bash "$ENTRYPOINT"
  [ "$status" -eq 17 ]
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

@test "CODE_FORGE=local with no commits after a ready claim appends a corrective blocked outcome" {
  # Default fake claude claims status=ready but (with no
  # FAKE_DRIVER_COMMIT) never commits anything on the branch.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -f "$OUTBOX_DIR/seam.bundle" ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=none status=blocked note=.*ready.*no commits exist on agent/issue-7' <<<"$output"
}

@test "read-write github never invokes bundle-out" {
  unset CODE_FORGE            # default github
  # BOX_WRITE_ENABLED=1 from setup_entrypoint_env: a push-capable Box bundles
  # nothing itself -- the launcher merges its pushed branch. Read-only github
  # (BOX_WRITE_ENABLED unset) is the case that now DOES bundle-out, below.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$OUTBOX_DIR" ]
}

@test "read-only github with real commits writes a seam bundle to the outbox" {
  unset CODE_FORGE            # default github
  unset BOX_WRITE_ENABLED     # read-only
  export FAKE_DRIVER_COMMIT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

@test "read-only github with no commits after a ready claim appends a corrective blocked outcome" {
  unset CODE_FORGE
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -f "$OUTBOX_DIR/seam.bundle" ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=none status=blocked note=.*ready.*no commits exist on agent/issue-7' <<<"$output"
}

@test "read-only github research dispatch never invokes bundle-out" {
  unset CODE_FORGE            # default github
  unset BOX_WRITE_ENABLED     # read-only
  export DISPATCH_KIND=research
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$OUTBOX_DIR" ]
}

@test "read-only github research dispatch with a non-zero driver crash still never invokes bundle-out" {
  # ADR 0039 (issue #2252): bundle-out now runs for both zero and non-zero
  # claude_rc, but the !_is_research_kind guard on the bundle-out block
  # itself is unchanged -- a research dispatch never cuts a branch (ADR
  # 0022), so it must still emit no bundle even when the driver crashed.
  unset CODE_FORGE            # default github
  unset BOX_WRITE_ENABLED     # read-only
  export DISPATCH_KIND=research
  export FAKE_DRIVER_CRASH_EXIT=17
  run bash "$ENTRYPOINT"
  [ "$status" -eq 17 ]
  [ ! -e "$OUTBOX_DIR" ]
}
