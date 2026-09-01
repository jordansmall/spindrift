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

  # The git-hook guard installs at BOTH $WORK_DIR (via --extra-repo-dir,
  # covering a push to any explicit URL or non-origin remote) and the decoy (via
  # --repo-dir, covering a plain `git push`/origin push).
  [ -x "$WORK_DIR/.git/hooks/pre-push" ]

  # pushurl is repointed at a throwaway bare decoy repo, never $WORK_DIR itself
  # (a pushurl pointed at $WORK_DIR makes a same-branch push resolve as
  # "Everything up-to-date" and exit 0 without ever invoking a hook) -- assert
  # the decoy exists, is bare, and carries the installed pre-receive hook.
  local pushurl
  pushurl="$(git -C "$WORK_DIR" config remote.origin.pushurl)"
  [ -n "$pushurl" ]
  [ "$pushurl" != "$WORK_DIR" ]
  [ -x "$pushurl/hooks/pre-receive" ]

  # The rejection's wording lives in lib/prompt-contract.nix's forbiddenMarkers
  # registry, rendered verbatim into the hook by driver-exec readonly-guards --
  # assert the stable "push" substring the row's RuntimeMessage always names,
  # plus the relay-naming "outbox" text that distinguishes this row from
  # boilerplate.
  run git -C "$WORK_DIR" push origin HEAD:some-branch
  [ "$status" -ne 0 ]
  [[ "$output" == *"push"* ]]
  [[ "$output" == *"outbox"* ]]

  run git -C "$REMOTE_ROOT/owner/repo.git" rev-parse --verify some-branch
  [ "$status" -ne 0 ]
}

@test "read-only Box's guard still blocks git push --no-verify (hook bypass) locally" {
  unset BOX_WRITE_ENABLED # issue #2463: read-only Box
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
  # Regression coverage for the #2509 port itself: the test above pushes a
  # brand-new ref name, which forces a genuine ref update -- and so still fires
  # pre-receive -- even under the buggy pushurl=$WORK_DIR wiring this guards
  # against. A real dispatch never does that: it always pushes the SAME branch
  # name phase_branch_recovery checked out in $WORK_DIR, whose destination ref
  # already sits at that exact commit -- "Everything up-to-date", exit 0, no
  # hook, `--no-verify` or not: a silent fake success.
  unset BOX_WRITE_ENABLED # issue #2463: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -x "$WORK_DIR/.git/hooks/pre-push" ]

  # BRANCH is computed inside entrypoint.sh's main and never exported --
  # reproduce the computation here so this test targets the exact branch
  # phase_branch_recovery leaves checked out.
  local branch="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"
  [ "$(git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD)" = "$branch" ]

  run git -C "$WORK_DIR" push --no-verify -u origin "$branch"
  [ "$status" -ne 0 ]
  [[ "$output" != *"Everything up-to-date"* ]]

  run git -C "$REMOTE_ROOT/owner/repo.git" rev-parse --verify "$branch"
  [ "$status" -ne 0 ]
}

@test "read-only Box's guard blocks push before any network call reaches the real remote" {
  # Issue #2463 finding: a pre-push hook alone can't stop this -- git's push
  # machinery lists the remote's refs (a network round trip) before it runs
  # pre-push, so on a real transport a hook-only guard still lets the attempt
  # reach the forge and 403 there. Point origin at a reserved, unresolvable host
  # (RFC 2606 .invalid, deliberately outside setup_bare_repo's insteadOf
  # rewrite) and assert the guard's message appears with no network-failure text
  # -- proving the block happens locally.
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
  # Issue #2463 finding: the gate used to re-derive "does this Box have an
  # outbox" from CODE_FORGE=="local" instead of consulting the forwarded
  # BOX_HOST_MEDIATED_REMOTE directly. A Box with BOX_HOST_MEDIATED_REMOTE=1 but
  # CODE_FORGE left at "github" is exactly the case separating the old gate
  # (which would wrongly skip the guard) from the new one: this Box's hand-off
  # is host-mediated, not a real push, so `git push` must still be blocked.
  unset BOX_WRITE_ENABLED # issue #2463: read-only Box
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
  # Issue #2463 finding: a backend the registry marks outboxRelayCapable=false
  # never gets an outbox -- its only hand-off IS a real `git push`, so
  # installing the guard would block the only way its work ever lands. No
  # backend valid under read-only leaves that false today (issue #2927 closed
  # forgejo's asymmetry), so this pins a hypothetical backend shape rather than
  # any current backend's behavior.
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
