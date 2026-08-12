#!/usr/bin/env bats
# Read-only Box fails `git push` locally, before any network call, when its
# hand-off is relay-based; read-write, and a read-only backend whose hand-off
# IS a real push, install neither guard (#2463).

load helper

setup() {
  setup_entrypoint_env
}

@test "read-only Box installs a pre-push guard that blocks git push locally" {
  unset BOX_WRITE_ENABLED # issue #2463: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  [ -x "$WORK_DIR/.git/hooks/pre-push" ]

  run git -C "$WORK_DIR" push origin HEAD:some-branch
  [ "$status" -ne 0 ]
  [[ "$output" == *"blocked locally"* ]]

  run git -C "$REMOTE_ROOT/owner/repo.git" rev-parse --verify some-branch
  [ "$status" -ne 0 ]
}

@test "read-only Box's guard blocks push before any network call reaches the real remote" {
  # Issue #2463 finding: a pre-push hook alone can't stop this -- git's push
  # machinery lists the remote's refs (a network round trip) before it ever
  # runs the pre-push hook, so on a real network transport a hook-only guard
  # still lets the push attempt reach the forge and 403 there. Point origin
  # at a reserved, unresolvable host (RFC 2606 .invalid, deliberately not
  # covered by setup_bare_repo's https://github.com/ insteadOf rewrite) and
  # assert the guard's own message appears with no network-failure text --
  # proving the block happens locally, before git ever touches the network.
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  git -C "$WORK_DIR" remote set-url origin https://readonly-push-hook-test.invalid/owner/repo.git
  run git -C "$WORK_DIR" push origin HEAD:some-branch
  [ "$status" -ne 0 ]
  [[ "$output" == *"blocked locally"* ]]
  [[ "$output" != *"Could not resolve host"* ]]
  [[ "$output" != *"Failed to connect"* ]]
  [[ "$output" != *"unable to access"* ]]
  [[ "$output" != *"Temporary failure"* ]]
}

@test "read-write Box installs no pre-push guard and pushes normally" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  [ ! -e "$WORK_DIR/.git/hooks/pre-push" ]

  run git -C "$WORK_DIR" push origin HEAD:some-other-branch
  [ "$status" -eq 0 ]

  run git -C "$REMOTE_ROOT/owner/repo.git" rev-parse --verify some-other-branch
  [ "$status" -eq 0 ]
}

@test "read-only Box whose hand-off is a real push (not outbox-relay-capable) installs no guard" {
  # Issue #2463 finding: forgejo read-only (and any other backend the
  # backend registry marks outboxRelayCapable=false) never gets an outbox --
  # its only hand-off IS a real `git push` (outcomebackstop's pushWithRetry /
  # publish_rebased_branch). Installing the guard there would block the only
  # way this Box's work ever lands.
  unset BOX_WRITE_ENABLED
  unset BOX_OUTBOX_RELAY_CAPABLE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$WORK_DIR/.git/hooks/pre-push" ]

  run git -C "$WORK_DIR" push origin HEAD:some-relay-branch
  [ "$status" -eq 0 ]

  run git -C "$REMOTE_ROOT/owner/repo.git" rev-parse --verify some-relay-branch
  [ "$status" -eq 0 ]
}
