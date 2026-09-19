#!/usr/bin/env bats
# Re-dispatch idempotency: force-with-lease push and stale-branch force-reset (issue #217).

load helper

setup() {
  setup_entrypoint_env
}

# The in-box push must use --force-with-lease so a retry from a different base
# replaces the prior run's branch state rather than colliding non-fast-forward.

@test "default prompt instructs agent to push with --force-with-lease" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--force-with-lease' "$DRIVER_PROMPT_FILE"
}

@test "re-dispatched box force-resets a stale remote branch (no open PR)" {
  # Stand in for a prior run that pushed agent/issue-7 and then died before
  # opening a PR.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "stale content from prior run" > "$prior/stale.txt"
  git -C "$prior" add -A
  git -C "$prior" commit -q -m "feat: prior run commit"
  git -C "$prior" push -q origin "agent/issue-7"
  # FAKE_GH_PR_LIST_7 stays unset, so the fake gh reports no open PR.

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [[ "$output" == *"force-resetting"* ]]

  # A plain push only succeeds here if the entrypoint force-reset the remote
  # branch; otherwise git rejects it as non-fast-forward.
  echo "new work" > "$WORK_DIR/new.txt"
  git -C "$WORK_DIR" add -A
  git -C "$WORK_DIR" commit -q -m "feat: new work"
  run git -C "$WORK_DIR" push origin "agent/issue-7"
  [ "$status" -eq 0 ]
}

@test "re-dispatched box read-only: never force-pushes the stale-branch reset" {
  # Same stale-branch-no-open-PR setup, but read-only (issue #1979): the box
  # holds no push-capable token, so even this housekeeping reset must never
  # force-push the remote. The reset branch matches base exactly, so the
  # empty-bundle no-op in publish_rebased_branch leaves nothing to relay.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "stale content from prior run" > "$prior/stale.txt"
  git -C "$prior" add -A
  git -C "$prior" commit -q -m "feat: prior run commit"
  git -C "$prior" push -q origin "agent/issue-7"
  # FAKE_GH_PR_LIST_7 stays unset, so the fake gh reports no open PR.

  unset BOX_WRITE_ENABLED
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"

  local before_sha
  before_sha="$(git -C "$prior" rev-parse "agent/issue-7")"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local after_sha
  after_sha="$(git --git-dir="$REMOTE_ROOT/owner/repo.git" rev-parse "refs/heads/agent/issue-7")"
  [ "$before_sha" = "$after_sha" ]
  [ ! -e "$OUTBOX_DIR/seam.bundle" ]
}

@test "re-dispatched box skips force-reset when an open PR exists on the stale branch" {
  # Stand in for a prior run that pushed commits and opened a PR, then died
  # before printing SPINDRIFT_OUTCOME. The entrypoint must not destroy the
  # branch, so the #122 adoption path can still recover the run.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "prior run work" > "$prior/prior.txt"
  git -C "$prior" add -A
  git -C "$prior" commit -q -m "feat: prior run commit"
  git -C "$prior" push -q origin "agent/issue-7"
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [[ "$output" == *"skipping force-reset"* ]]

  stale_sha="$(git -C "$BATS_TEST_TMPDIR/prior" rev-parse HEAD)"
  run git -C "$WORK_DIR" ls-remote origin "refs/heads/agent/issue-7"
  [ "$status" -eq 0 ]
  [[ "$output" == "$stale_sha"* ]]
}

