#!/usr/bin/env bats
# CODE_FORGE=local harness-owned code-out (issue #1808): the entrypoint, not
# the Agent, produces the seam bundle after the Driver exits. A Box that
# committed nothing yet claimed ready gets a corrective blocked outcome
# instead of settling as a false ready.

load helper

setup() {
  setup_entrypoint_env
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
}

@test "CODE_FORGE=local with real commits writes a seam bundle to the outbox" {
  # The launcher forwards BOX_HOST_MEDIATED_REMOTE to a real CODE_FORGE=local
  # Box (lib/backends/default.nix's "local" row sets hostMediatedRemote=true),
  # and the bundle-out gate keys on that, not on CODE_FORGE's name. Exported
  # per test so the CODE_FORGE=github tests below keep it unset and exercise
  # the gate's other disjunct, _is_readonly_outbox_relay (issue #2527).
  export BOX_HOST_MEDIATED_REMOTE=1
  export FAKE_DRIVER_COMMIT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

@test "CODE_FORGE=local with real commits and a non-zero driver crash still writes a seam bundle and propagates the exit code" {
  # ADR 0039 (issue #2252): bundle-out runs before the box exits even when the
  # driver crashed non-zero, because a committed branch is real work worth
  # relaying.
  export BOX_HOST_MEDIATED_REMOTE=1
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_CRASH_EXIT=17
  run bash "$ENTRYPOINT"
  [ "$status" -eq 17 ]
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

@test "CODE_FORGE=local with no commits after a ready claim appends a corrective blocked outcome" {
  # The default fake claude claims status=ready but, without
  # FAKE_DRIVER_COMMIT, commits nothing on the branch.
  export BOX_HOST_MEDIATED_REMOTE=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -f "$OUTBOX_DIR/seam.bundle" ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=none status=blocked note=.*ready.*no commits exist on agent/issue-7' <<<"$output"
}

@test "read-write github never invokes bundle-out" {
  unset CODE_FORGE            # default github
  # BOX_WRITE_ENABLED=1 comes from setup_entrypoint_env: a push-capable Box
  # bundles nothing itself, since the launcher merges its pushed branch.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$OUTBOX_DIR" ]
}

@test "read-only github with real commits writes a seam bundle to the outbox" {
  unset CODE_FORGE            # default github
  unset BOX_WRITE_ENABLED
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
  unset BOX_WRITE_ENABLED
  export DISPATCH_KIND=research
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$OUTBOX_DIR" ]
}

# Issue #3901: the posture comes from DISPATCH_KIND's descriptor alone, so a
# stale ADVISE_ONLY in the env never leaks the read-only posture into work.
@test "a stale ADVISE_ONLY=1 is ignored under DISPATCH_KIND unset (work)" {
  unset CODE_FORGE            # default github
  unset BOX_WRITE_ENABLED
  unset DISPATCH_KIND
  export ADVISE_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -f "$OUTBOX_DIR/seam.bundle" ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=none status=blocked note=.*ready.*no commits exist on agent/issue-7' <<<"$output"
}

# Issue #3901: an unrecognized kind fails closed before clone_repo rather than
# defaulting to work's (or any other) posture.
@test "an unrecognized DISPATCH_KIND fails the Box before clone" {
  unset CODE_FORGE            # default github
  unset BOX_WRITE_ENABLED
  export DISPATCH_KIND="bogus-kind"
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"bogus-kind"* ]]
  [ ! -d "$WORK_DIR" ]
  [ ! -e "$OUTBOX_DIR" ]
}

@test "read-only github research dispatch with a non-zero driver crash still never invokes bundle-out" {
  # ADR 0039 (issue #2252) made bundle-out run for non-zero claude_rc too, but
  # the !_is_advise_only guard is unchanged: a research dispatch never cuts a
  # branch (ADR 0022), so it emits no bundle even when the driver crashed.
  unset CODE_FORGE            # default github
  unset BOX_WRITE_ENABLED
  export DISPATCH_KIND=research
  export FAKE_DRIVER_CRASH_EXIT=17
  run bash "$ENTRYPOINT"
  [ "$status" -eq 17 ]
  [ ! -e "$OUTBOX_DIR" ]
}

# Already-resolved backstop (issue #4016), outbox-relay variant: a read-only
# Box holds no push token, so a commit-carrying already-resolved claim must be
# demoted and its commits relayed the same seam.bundle way every other
# status=blocked-with-commits path uses, not silently dropped.
@test "read-only github + status=already-resolved with commits -> demoted to blocked and bundled to outbox" {
  unset CODE_FORGE            # default github
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_OUTCOME_STATUS=already-resolved
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local last_line
  last_line="$(grep '^SPINDRIFT_OUTCOME ' <<<"$output" | tail -1)"
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked synthetic=true note=.*already-resolved but 1 commits exist on agent/issue-7.*relayed via outbox bundle (read-only Box)' <<<"$last_line"
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

# Same demotion, CODE_FORGE=local variant: no writable remote at all, so the
# already-resolved claim's commits must still reach the outbox bundle.
@test "CODE_FORGE=local + status=already-resolved with commits -> demoted to blocked and bundled to outbox" {
  export BOX_HOST_MEDIATED_REMOTE=1
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_OUTCOME_STATUS=already-resolved
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local last_line
  last_line="$(grep '^SPINDRIFT_OUTCOME ' <<<"$output" | tail -1)"
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked synthetic=true note=.*already-resolved but 1 commits exist on agent/issue-7.*relayed via outbox bundle (no writable remote under CODE_FORGE=local)' <<<"$last_line"
  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}
