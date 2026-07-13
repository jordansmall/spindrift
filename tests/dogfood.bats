# Branch hygiene and exit-code contract for the dogfood loop.
#
# The loop must reset to the base branch before `git pull --ff-only` because a
# host left on a feature branch (e.g. after a prior merge) has no upstream to
# fast-forward. Termination is driven by the launcher's exit code rather than a
# separate gh probe: exit 2 (queue empty) breaks the loop cleanly; any other
# non-zero exit aborts with an error.

load helper

# Replaces the fake nix with one that exits $1 on `nix run .# -- dispatch` calls and
# exits 0 on all other nix calls (build, etc.).
_install_exit_code_nix() {
  local code="$1"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- dispatch'; then
  exit $code
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"
}

# Replaces the fake nix with one that exits each of $@ in order on successive
# `nix run .# -- dispatch` calls (running past the list re-exits the last
# code); exits 0 on all other nix calls.
_install_sequence_exit_nix() {
  local codes=("$@")
  local counter="$BATS_TEST_TMPDIR/dispatch-call-count"
  echo 0 >"$counter"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- dispatch'; then
  codes=(${codes[*]})
  n=\$(cat "$counter")
  last=\$(( \${#codes[@]} - 1 ))
  [ "\$n" -gt "\$last" ] && n="\$last"
  echo \$((n + 1)) >"$counter"
  exit "\${codes[\$n]}"
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"
}

# Replaces the fake nix with one that logs MAX_JOBS on every `nix run .# -- dispatch`
# call, then exits 2 so the dogfood loop terminates cleanly.
_install_env_logging_nix() {
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<'EOF'
: "${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "$*" >>"$NIX_LOG"
if printf '%s ' "$@" | grep -q -- '-- dispatch'; then
  printf 'MAX_JOBS=%s\n' "${MAX_JOBS:-}" >>"${NIX_ENV_LOG:-/dev/null}"
  exit 2
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"
}

setup() {
  setup_fakes

  export HOME="$BATS_TEST_TMPDIR/home"
  mkdir -p "$HOME"
  git config --global init.defaultBranch main
  git config --global user.name "Dogfood Test"
  git config --global user.email "dogfood@example.com"

  local remote="$BATS_TEST_TMPDIR/remote.git"
  git init --bare -q "$remote"

  export WORK="$BATS_TEST_TMPDIR/work"
  git clone -q "$remote" "$WORK"
  # The nix check injects DOGFOOD_SH (only tests/ is copied into the sandbox);
  # locally fall back to the repo-root script beside tests/.
  cp "${DOGFOOD_SH:-$BATS_TEST_DIRNAME/../dogfood.sh}" "$WORK/dogfood.sh"
  printf 'harness.env\n' >"$WORK/.gitignore"
  printf 'REPO_SLUG=owner/repo\n' >"$WORK/harness.env"
  git -C "$WORK" add dogfood.sh .gitignore
  git -C "$WORK" commit -q -m "seed"
  git -C "$WORK" push -q origin HEAD:main
  git -C "$WORK" branch --set-upstream-to=origin/main main

  # Land the host on a feature branch with no upstream — the state that broke a
  # bare `git pull --ff-only`.
  git -C "$WORK" checkout -q -b feat/leftover

  # Default nix exits 2 on `nix run .# -- dispatch` so tests terminate after one cycle.
  _install_exit_code_nix 2
}

@test "dogfood resets to the base branch before pulling" {
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" rev-parse --abbrev-ref HEAD)" = "main" ]
}

@test "dogfood exits cleanly when launcher exits 2 (queue empty)" {
  _install_exit_code_nix 2
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"done"* ]]
}

@test "dogfood aborts when launcher exits non-zero (not 2)" {
  _install_exit_code_nix 1
  run bash -c "BASE_BRANCH=main bash '$WORK/dogfood.sh' 2>&1"
  [ "$status" -ne 0 ]
  printf '%s\n' "$output" | grep -q "launcher failed"
}

@test "dogfood does not pin MAX_JOBS=1" {
  export NIX_ENV_LOG="$BATS_TEST_TMPDIR/nix-env.log"
  : >"$NIX_ENV_LOG"
  _install_env_logging_nix
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  ! grep -q '^MAX_JOBS=1$' "$NIX_ENV_LOG"
}

@test "dogfood pulls, rebuilds, and re-invokes when launcher exits 4 (image stale)" {
  _install_sequence_exit_nix 4 2
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"image stale"* ]]
  [ "$(grep -c -- '-- dispatch' "$NIX_LOG")" -eq 2 ]
}

@test "dogfood terminates cleanly with triage message when launcher exits 3" {
  _install_exit_code_nix 3
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"none are dispatchable"* ]]
}

@test "dogfood exports MAX_JOBS defaulting to MAX_PARALLEL" {
  export NIX_ENV_LOG="$BATS_TEST_TMPDIR/nix-env.log"
  : >"$NIX_ENV_LOG"
  _install_env_logging_nix
  run env BASE_BRANCH=main MAX_PARALLEL=5 bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -q '^MAX_JOBS=5$' "$NIX_ENV_LOG"
}

@test "dogfood aborts when podman machine RAM is below MEMORY_LIMIT" {
  export FAKE_PODMAN_MACHINE_MEMORY_MIB=2048
  run env BASE_BRANCH=main MEMORY_LIMIT=4g bash "$WORK/dogfood.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"2048"* ]]
  [[ "$output" == *"4g"* ]]
  [[ "$output" == *"podman machine set --memory"* ]]
}

@test "dogfood proceeds when podman machine RAM meets MEMORY_LIMIT" {
  export FAKE_PODMAN_MACHINE_MEMORY_MIB=4096
  run env BASE_BRANCH=main MEMORY_LIMIT=4g bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" != *"podman machine set --memory"* ]]
}

@test "dogfood skips the memory preflight when no podman machine exists" {
  run env BASE_BRANCH=main MEMORY_LIMIT=4g bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" != *"podman machine set --memory"* ]]
}

