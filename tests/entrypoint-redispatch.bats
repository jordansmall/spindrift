#!/usr/bin/env bats
# Re-dispatch idempotency: force-with-lease push and stale-branch force-reset (issue #217).

load helper

setup() {
  setup_entrypoint_env
}

# --- re-dispatch idempotency (issue #217) ------------------------------------
# The in-box push must use --force-with-lease so a retry from a different base
# replaces the prior run's branch state rather than colliding non-fast-forward.

@test "default prompt instructs agent to push with --force-with-lease" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--force-with-lease' "$CLAUDE_PROMPT_FILE"
}

@test "re-dispatched box force-resets a stale remote branch (no open PR)" {
  # Simulate a prior run that pushed agent/issue-7 with a commit, then died
  # before opening a PR.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "stale content from prior run" > "$prior/stale.txt"
  git -C "$prior" add -A
  git -C "$prior" commit -q -m "feat: prior run commit"
  git -C "$prior" push -q origin "agent/issue-7"
  # No FAKE_GH_PR_LIST_7 → gh pr list returns empty → no open PR

  # A re-dispatch should succeed and start clean from main.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Entrypoint logged the force-reset.
  [[ "$output" == *"force-resetting"* ]]

  # The remote branch was force-reset, so a plain push from the clean
  # work-tree succeeds without a non-fast-forward rejection.
  echo "new work" > "$WORK_DIR/new.txt"
  git -C "$WORK_DIR" add -A
  git -C "$WORK_DIR" commit -q -m "feat: new work"
  run git -C "$WORK_DIR" push origin "agent/issue-7"
  [ "$status" -eq 0 ]
}

@test "re-dispatched box skips force-reset when an open PR exists on the stale branch" {
  # Simulate a prior run that pushed commits AND opened a PR, then died before
  # printing SPINDRIFT_OUTCOME.  The entrypoint must not destroy the branch so
  # the #122 adoption path can still recover the run.
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

  # Entrypoint logged that it skipped the force-reset.
  [[ "$output" == *"skipping force-reset"* ]]

  # The stale commit is still on the remote branch (not force-reset).
  stale_sha="$(git -C "$BATS_TEST_TMPDIR/prior" rev-parse HEAD)"
  run git -C "$WORK_DIR" ls-remote origin "refs/heads/agent/issue-7"
  [ "$status" -eq 0 ]
  [[ "$output" == "$stale_sha"* ]]
}

