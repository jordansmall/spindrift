#!/usr/bin/env bats
# A read-only Box fails `git push` locally, before any network call, when its
# hand-off is relay-based. A read-write Box, and a read-only Box whose backend
# hands off with a real push, get no guard (#2463).

load helper

setup() {
  setup_entrypoint_env
}

@test "read-only Box installs a pre-push guard that blocks git push locally" {
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]

  # The guard installs at both $WORK_DIR (via --extra-repo-dir, covering a push
  # to an explicit URL or a non-origin remote) and the decoy (via --repo-dir,
  # covering a plain `git push`). See issue #2509 Finding 1.
  [ -x "$WORK_DIR/.git/hooks/pre-push" ]

  # pushurl points at a throwaway bare decoy, never $WORK_DIR itself: a pushurl
  # pointed at $WORK_DIR makes a same-branch push resolve as "Everything
  # up-to-date" and exit 0 without ever invoking a hook (issue #2509).
  local pushurl
  pushurl="$(git -C "$WORK_DIR" config remote.origin.pushurl)"
  [ -n "$pushurl" ]
  [ "$pushurl" != "$WORK_DIR" ]
  [ -x "$pushurl/hooks/pre-receive" ]

  # The rejection wording comes from lib/prompt-contract.nix's forbiddenMarkers
  # registry (issue #2509). Assert the stable "push" substring the row's
  # RuntimeMessage always names, plus "outbox", which distinguishes this row
  # from a bare boilerplate rejection.
  run git -C "$WORK_DIR" push origin HEAD:some-branch
  [ "$status" -ne 0 ]
  [[ "$output" == *"push"* ]]
  [[ "$output" == *"outbox"* ]]

  run git -C "$REMOTE_ROOT/owner/repo.git" rev-parse --verify some-branch
  [ "$status" -ne 0 ]
}

@test "read-only Box's guard still blocks git push --no-verify (hook bypass) locally" {
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  [ -x "$WORK_DIR/.git/hooks/pre-push" ]

  run git -C "$WORK_DIR" push origin HEAD:some-branch --no-verify
  [ "$status" -ne 0 ]

  run git -C "$REMOTE_ROOT/owner/repo.git" rev-parse --verify some-branch
  [ "$status" -ne 0 ]
}

@test "read-only Box's guard still blocks git push --no-verify of the branch already checked out" {
  # The test above pushes a new ref name, which forces a real ref update and so
  # fires pre-receive even under the buggy pushurl=$WORK_DIR wiring. A real
  # dispatch pushes the same branch phase_branch_recovery checked out, whose
  # destination ref already sits at this commit: git calls it "Everything
  # up-to-date" and exits 0 without invoking pre-receive (issue #2509).
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -x "$WORK_DIR/.git/hooks/pre-push" ]

  # entrypoint.sh's main computes BRANCH but never exports it, so repeat the
  # computation here to target the branch phase_branch_recovery leaves checked
  # out.
  local branch="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"
  [ "$(git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD)" = "$branch" ]

  run git -C "$WORK_DIR" push --no-verify -u origin "$branch"
  [ "$status" -ne 0 ]
  [[ "$output" != *"Everything up-to-date"* ]]

  run git -C "$REMOTE_ROOT/owner/repo.git" rev-parse --verify "$branch"
  [ "$status" -ne 0 ]
}

@test "read-only Box's guard blocks push before any network call reaches the real remote" {
  # A pre-push hook alone cannot stop this: git lists the remote's refs over the
  # network before it runs the hook, so a hook-only guard still reaches the
  # forge (issue #2463). origin points at an unresolvable RFC 2606 .invalid
  # host that setup_bare_repo's insteadOf rewrite deliberately misses, so the
  # guard's message with no network-failure text proves the block is local.
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -x "$WORK_DIR/.git/hooks/pre-push" ]

  git -C "$WORK_DIR" remote set-url origin https://readonly-push-hook-test.invalid/owner/repo.git
  run git -C "$WORK_DIR" push origin HEAD:some-branch
  [ "$status" -ne 0 ]
  [[ "$output" == *"push"* ]]
  [[ "$output" == *"outbox"* ]]
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

@test "read-only Box with BOX_HOST_MEDIATED_REMOTE set installs the guard even when BOX_OUTBOX_RELAY_CAPABLE is unset" {
  # install_readonly_push_hook's gate used to re-derive "does this Box have an
  # outbox" from CODE_FORGE=="local" instead of reading the forwarded
  # BOX_HOST_MEDIATED_REMOTE directly (issue #2463). A host-mediated Box with
  # CODE_FORGE left at "github" separates the two gates: its hand-off is not a
  # real push, so `git push` must still be blocked locally.
  unset BOX_WRITE_ENABLED
  unset BOX_OUTBOX_RELAY_CAPABLE
  export BOX_HOST_MEDIATED_REMOTE=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  [ -x "$WORK_DIR/.git/hooks/pre-push" ]

  run git -C "$WORK_DIR" push origin HEAD:some-host-mediated-branch
  [ "$status" -ne 0 ]
  [[ "$output" == *"push"* ]]
  [[ "$output" == *"outbox"* ]]

  run git -C "$REMOTE_ROOT/owner/repo.git" rev-parse --verify some-host-mediated-branch
  [ "$status" -ne 0 ]
}

@test "read-only Box whose backend is not outbox-relay-capable installs no guard" {
  # A backend the registry marks outboxRelayCapable=false gets no outbox: its
  # only hand-off is a real `git push`, so installing the guard would block the
  # only way its work lands (issue #2463). No backend valid as a CODE_FORGE
  # under read-only leaves the flag false today (issue #2927), so this pins a
  # backend shape rather than any real backend's behavior.
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
