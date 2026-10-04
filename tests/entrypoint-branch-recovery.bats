#!/usr/bin/env bats
# Pre-work rebase publication and branch recovery (issues #215, #1979, #3942).

load helper

setup() {
  setup_entrypoint_env
}

# The box rebases the working branch onto the latest origin/BASE_BRANCH before
# the agent starts, so the agent works against current main rather than the
# state of origin at clone time (issue #215).

@test "entrypoint rebases prior work onto latest origin/BASE_BRANCH before agent starts" {
  # A prior run pushed agent/issue-7, then main advanced without conflicting.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  echo "main advance" > "$advance/main_advance.txt"
  git -C "$advance" add main_advance.txt
  git -C "$advance" commit -q -m "chore: advance main"
  git -C "$advance" push -q origin HEAD:main

  # Open PR so the adoption path is taken (no force-reset).
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # A branch rebased on top of the latest main has both the prior branch work
  # and the main advance.
  [ -f "$WORK_DIR/branch.txt" ]
  [ -f "$WORK_DIR/main_advance.txt" ]

  # The rebased branch must have been force-pushed so the agent's first
  # incremental push is a fast-forward, not a non-fast-forward rejection.
  echo "agent work" > "$WORK_DIR/agent.txt"
  git -C "$WORK_DIR" add agent.txt
  git -C "$WORK_DIR" commit -q -m "feat: agent work on rebased branch"
  run git -C "$WORK_DIR" push origin "agent/issue-7"
  [ "$status" -eq 0 ]
}

@test "entrypoint bundles rebased branch to outbox instead of force-pushing when read-only" {
  # Same setup as the read-write case above, but with BOX_WRITE_ENABLED unset
  # (issue #1979): the box holds no push-capable token, so publishing the
  # rebased branch must relay via the outbox bundle instead of a direct
  # force-push that would 403.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  echo "main advance" > "$advance/main_advance.txt"
  git -C "$advance" add main_advance.txt
  git -C "$advance" commit -q -m "chore: advance main"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
  unset BOX_WRITE_ENABLED
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"

  local before_sha
  before_sha="$(git -C "$prior" rev-parse "agent/issue-7")"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The remote branch must be untouched: no direct push in read-only mode.
  local after_sha
  after_sha="$(git --git-dir="$REMOTE_ROOT/owner/repo.git" rev-parse "refs/heads/agent/issue-7")"
  [ "$before_sha" = "$after_sha" ]

  [ -f "$OUTBOX_DIR/seam.bundle" ]
  run git -C "$WORK_DIR" bundle verify "$OUTBOX_DIR/seam.bundle"
  [ "$status" -eq 0 ]
}

@test "entrypoint bundling a rebase with no commits ahead of base is a no-op, not a failure" {
  # The adopted branch tip already equals origin/main, so the rebase is a no-op
  # and the outbox range origin/BASE_BRANCH..BRANCH is empty. `git bundle
  # create` refuses to write an empty bundle and exits non-zero. The read-write
  # push path tolerates that, so the read-only bundle path must too rather than
  # failing the whole box over nothing to relay (issue #1979).
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  git -C "$prior" push -q origin "agent/issue-7"

  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  echo "main advance" > "$advance/main_advance.txt"
  git -C "$advance" add main_advance.txt
  git -C "$advance" commit -q -m "chore: advance main"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
  unset BOX_WRITE_ENABLED
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$OUTBOX_DIR/seam.bundle" ]
}

# CODE_FORGE=forgejo has PRs, but never on github.com and the Box carries no
# GH_TOKEN, so gh must never be consulted (issue #3942) -- the exact same
# start-fresh-no-gh contract as CODE_FORGE=local, just over a Forgejo remote.
@test "CODE_FORGE=forgejo starts fresh and calls no gh, even with a stale origin branch" {
  local forge_root="$BATS_TEST_TMPDIR/forge"
  mkdir -p "$forge_root/owner"
  git init --bare -q "$forge_root/owner/repo.git"
  local fseed="$BATS_TEST_TMPDIR/forge-seed"
  git clone -q "$forge_root/owner/repo.git" "$fseed"
  (
    cd "$fseed" || exit 1
    echo "# forge repo" >README.md
    git add -A
    git commit -q -m "chore: seed forge remote"
    git push -q origin HEAD:main
  )
  git config --global "url.file://$forge_root/.insteadOf" "https://fjtok@forge.test/"

  local stale_seed="$BATS_TEST_TMPDIR/forge-stale-seed"
  git clone -q "$forge_root/owner/repo.git" "$stale_seed"
  (
    cd "$stale_seed" || exit 1
    git checkout -q -b agent/issue-7
    echo "stale" >stale.txt
    git add -A
    git commit -q -m "chore: stale prior attempt"
    git push -q origin agent/issue-7
  )
  local before_sha
  before_sha="$(git --git-dir="$forge_root/owner/repo.git" rev-parse refs/heads/agent/issue-7)"

  export CODE_FORGE="forgejo"
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$GH_LOG" ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "agent/issue-7" ]
  [ ! -f "$WORK_DIR/stale.txt" ]

  # No force-reset publish: the stale remote branch is left untouched.
  local after_sha
  after_sha="$(git --git-dir="$forge_root/owner/repo.git" rev-parse refs/heads/agent/issue-7)"
  [ "$before_sha" = "$after_sha" ]
}

# CODE_FORGE=git is a plain push-only remote with no PR concept at all (ADR
# 0013), so gh must never be consulted here either (issue #3942).
@test "CODE_FORGE=git starts fresh and never calls gh pr list, even with a stale origin branch" {
  local other_remote="$BATS_TEST_TMPDIR/other-remote.git"
  git init --bare -q "$other_remote"
  local seed="$BATS_TEST_TMPDIR/seed-other"
  git clone -q "$other_remote" "$seed"
  (
    cd "$seed" || exit 1
    echo "# other repo" >README.md
    git add -A
    git commit -q -m "chore: seed other remote"
    git push -q origin HEAD:main
  )

  local stale_seed="$BATS_TEST_TMPDIR/other-stale-seed"
  git clone -q "$other_remote" "$stale_seed"
  (
    cd "$stale_seed" || exit 1
    git checkout -q -b agent/issue-7
    echo "stale" >stale.txt
    git add -A
    git commit -q -m "chore: stale prior attempt"
    git push -q origin agent/issue-7
  )
  local before_sha
  before_sha="$(git --git-dir="$other_remote" rev-parse refs/heads/agent/issue-7)"

  export CODE_FORGE="git"
  export CODE_FORGE_REMOTE_URL="$other_remote"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # clone_repo still runs gh auth setup-git for CODE_FORGE=git; only the PR
  # query is ruled out here.
  run grep -q "pr list" "$GH_LOG"
  [ "$status" -eq 1 ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "agent/issue-7" ]
  [ ! -f "$WORK_DIR/stale.txt" ]

  # No force-reset publish: the stale remote branch is left untouched.
  local after_sha
  after_sha="$(git --git-dir="$other_remote" rev-parse refs/heads/agent/issue-7)"
  [ "$before_sha" = "$after_sha" ]
}
