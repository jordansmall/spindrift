#!/usr/bin/env bats
# Clone, branch cut, and CODE_FORGE_REMOTE_URL override.

load helper

setup() {
  setup_entrypoint_env
}

@test "entrypoint clones the target repo and cuts the issue branch" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "agent/issue-7" ]
}

# CODE_FORGE=git: the Box clones from and pushes to a plain git remote instead
# of https://github.com/$REPO_SLUG.git (ADR 0013 / #330). REPO_SLUG still
# resolves the ISSUE_TRACKER (this slice demoes CODE_FORGE=git with the
# github tracker), so the two must be independently settable.
@test "CODE_FORGE_REMOTE_URL overrides the clone/push remote" {
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

  export CODE_FORGE="git"
  export CODE_FORGE_REMOTE_URL="$other_remote"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  run git -C "$WORK_DIR" remote get-url origin
  [ "$status" -eq 0 ]
  [ "$output" = "$other_remote" ]
}

@test "CODE_FORGE_REMOTE_URL is ignored when CODE_FORGE is unset (github default)" {
  # A stray CODE_FORGE_REMOTE_URL must not silently redirect a github
  # deployment's clone — only CODE_FORGE=git opts in. set_box_env exports
  # CODE_FORGE at its schema default ("github"), the same value this var
  # would carry if truly unset.
  local other_remote="$BATS_TEST_TMPDIR/other-remote.git"
  git init --bare -q "$other_remote"

  export CODE_FORGE_REMOTE_URL="$other_remote"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  run git -C "$WORK_DIR" remote get-url origin
  [ "$status" -eq 0 ]
  [ "$output" != "$other_remote" ]
}

