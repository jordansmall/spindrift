#!/usr/bin/env bats

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

# CODE_FORGE=git clones from and pushes to a plain git remote instead of
# https://github.com/$REPO_SLUG.git (ADR 0013 / #330), but REPO_SLUG still
# resolves the ISSUE_TRACKER, so the two must be independently settable.
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

# CODE_FORGE=forgejo clones over a FORGEJO_TOKEN-authenticated URL (ADR 0038),
# never github.com. An insteadOf rewrite keeps the clone offline, and because
# the rewrite prefix carries the token, the clone only resolves if the
# entrypoint really embedded FORGEJO_TOKEN as the remote's userinfo.
@test "CODE_FORGE=forgejo clones from a FORGEJO_TOKEN-authenticated Forgejo remote" {
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

  export CODE_FORGE="forgejo"
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  # `git remote get-url` re-applies the url.insteadOf rewrite at read time and
  # would echo back the file:// redirect. `git config --get` returns what the
  # clone wrote to .git/config: the token-authenticated https URL.
  run git -C "$WORK_DIR" config --get remote.origin.url
  [ "$status" -eq 0 ]
  [ "$output" = "https://fjtok@forge.test/owner/repo.git" ]
}

# clone_repo pipes the token to `fj auth add-key` on stdin, never argv, so it
# never shows up in `ps` (issue #1963). A fake fj on PATH records its args and
# stdin so the test can assert both without a real forgejo-cli binary.
@test "CODE_FORGE=forgejo configures fj via auth add-key with the token on stdin, not argv" {
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

  local fj_args_file="$BATS_TEST_TMPDIR/fj-args.txt"
  local fj_stdin_file="$BATS_TEST_TMPDIR/fj-stdin.txt"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<EOF
echo "\$@" >"$fj_args_file"
cat >"$fj_stdin_file"
EOF
  } >"$FAKE_BIN/fj"
  chmod +x "$FAKE_BIN/fj"

  export CODE_FORGE="forgejo"
  export BOX_FORGE_BACKEND=FORGEJO
  export ISSUE_TRACKER="forgejo"
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  run cat "$fj_args_file"
  [ "$status" -eq 0 ]
  [[ "$output" == *"-H https://forge.test auth add-key"* ]]
  # The token must arrive on stdin, never as an argv word.
  [[ "$output" != *"fjtok"* ]]

  run cat "$fj_stdin_file"
  [ "$status" -eq 0 ]
  [ "$output" = "fjtok" ]
}

# Without the matching strip in configure_forgejo_cli, fj stores the token
# under "https://forge.test/" while the git remote resolves to "forge.test",
# silently breaking `fj issue view` and auth lookup (issue #1963).
@test "CODE_FORGE=forgejo strips a trailing slash from FORGEJO_BASE_URL when keying fj" {
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

  local fj_args_file="$BATS_TEST_TMPDIR/fj-args.txt"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<EOF
echo "\$@" >"$fj_args_file"
cat >/dev/null
EOF
  } >"$FAKE_BIN/fj"
  chmod +x "$FAKE_BIN/fj"

  export CODE_FORGE="forgejo"
  export BOX_FORGE_BACKEND=FORGEJO
  export ISSUE_TRACKER="forgejo"
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test/"
  export FORGEJO_TOKEN="fjtok"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  run cat "$fj_args_file"
  [ "$status" -eq 0 ]
  [[ "$output" == *"-H https://forge.test auth add-key"* ]]
  [[ "$output" != *"forge.test/ auth"* ]]
}

# CODE_FORGE=local clones from the read-only Accumulation-repo mount instead of
# a network remote (ADR 0033 / #1698). REPO_MOUNT_DIR replaces the container's
# fixed /repo target here.
@test "CODE_FORGE=local clones from REPO_MOUNT_DIR instead of a network remote" {
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  run git -C "$WORK_DIR" remote get-url origin
  [ "$status" -eq 0 ]
  [ "$output" = "$REPO_MOUNT_DIR" ]
}

# Under rootless podman the Box's mapped uid never matches the host-owned bind
# mount's uid, so git's dubious-ownership guard rejects the mount (#1720).
# GIT_TEST_ASSUME_DIFFERENT_OWNER=1 is git's own test-suite knob for faking that
# mismatch. An unprivileged bats sandbox cannot create a real differently-owned
# mount.
@test "CODE_FORGE=local clones a differently-owned mount without tripping the dubious-ownership guard" {
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  export GIT_TEST_ASSUME_DIFFERENT_OWNER=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  run git config --global --get-all safe.directory
  [ "$status" -eq 0 ]
  [[ "$output" == *"$REPO_MOUNT_DIR"* ]]
  [[ "$output" == *"$WORK_DIR"* ]]
}

# A ref left at origin/agent/issue-7 by an abandoned attempt must not trigger
# the github/git adoption path's `gh pr list`, a forge network call
# CODE_FORGE=local must never make (ADR 0033 / #1698), nor a push back to the
# read-only mount. The Box starts BRANCH fresh from base every time instead.
@test "CODE_FORGE=local starts fresh and calls no gh, even with a stale origin branch" {
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"

  local seed="$BATS_TEST_TMPDIR/seed-stale-branch"
  git clone -q "$REPO_MOUNT_DIR" "$seed"
  (
    cd "$seed" || exit 1
    git checkout -q -b agent/issue-7
    echo "stale" >stale.txt
    git add -A
    git commit -q -m "chore: stale prior attempt"
    git push -q origin agent/issue-7
  )

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$GH_LOG" ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "agent/issue-7" ]
  [ ! -f "$WORK_DIR/stale.txt" ]
}

@test "CODE_FORGE_REMOTE_URL is ignored when CODE_FORGE is unset (github default)" {
  # Only CODE_FORGE=git opts in, so a stray CODE_FORGE_REMOTE_URL must not
  # redirect a github deployment's clone. set_box_env exports CODE_FORGE at its
  # schema default ("github"), the same value this var would carry if unset.
  local other_remote="$BATS_TEST_TMPDIR/other-remote.git"
  git init --bare -q "$other_remote"

  export CODE_FORGE_REMOTE_URL="$other_remote"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  run git -C "$WORK_DIR" remote get-url origin
  [ "$status" -eq 0 ]
  [ "$output" != "$other_remote" ]
}

