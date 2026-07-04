# Branch hygiene for the dogfood loop. Each iteration rebuilds the image from
# $PWD, so the loop must reset to the base branch before `git pull --ff-only`:
# a host left on a feature branch (e.g. after a prior merge) has no upstream to
# fast-forward, and the bare pull would fail or rebuild the wrong tree.

load helper

# A gh stub answering only dogfood's readiness probe (`issue list … --jq length`):
# 1 on the first call (one issue to drain) then 0, so the loop runs exactly once.
# Reuse the copied fake's shebang so this works both locally (`/usr/bin/env`) and
# under the sandboxed nix check (store-bash, shebangs already substituted).
_install_counting_gh() {
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/gh")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
if printf '%s ' "\$@" | grep -q 'issue list'; then
  n="\$(cat "$COUNTER" 2>/dev/null || echo 0)"
  if [ "\$n" -eq 0 ]; then echo 1; else echo 0; fi
  echo \$((n + 1)) >"$COUNTER"
fi
exit 0
EOF
  } >"$FAKE_BIN/gh.tmp"
  mv "$FAKE_BIN/gh.tmp" "$FAKE_BIN/gh"
  chmod +x "$FAKE_BIN/gh"
}

setup() {
  setup_fakes
  export COUNTER="$BATS_TEST_TMPDIR/gh-count"
  _install_counting_gh

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
}

@test "dogfood resets to the base branch before pulling" {
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" rev-parse --abbrev-ref HEAD)" = "main" ]
}
