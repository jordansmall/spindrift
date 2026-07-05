# Shared bats helpers. Sourced by every *.bats file.
#
# The nix `checks.<system>.bats` derivation exports the paths these helpers
# depend on: FAKES_DIR (the fake podman/gh/claude), RUN_CMD / BUILD_CMD (the
# nix-generated commands with the image store path baked in), ENTRYPOINT (the
# in-container script), PROMPTS_DIR (the baked prompt templates), and IMAGE_PATH
# (the image archive store path substituted into the commands).

setup_fakes() {
  : "${FAKES_DIR:?FAKES_DIR must be set (dir holding fake podman/gh/claude)}"
  FAKE_BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$FAKE_BIN"
  cp "$FAKES_DIR"/podman "$FAKES_DIR"/docker "$FAKES_DIR"/bwrap "$FAKES_DIR"/gh \
    "$FAKES_DIR"/claude "$FAKES_DIR"/nix "$FAKE_BIN"/
  chmod +x "$FAKE_BIN"/*
  export PATH="$FAKE_BIN:$PATH"

  export PODMAN_LOG="$BATS_TEST_TMPDIR/podman.log"
  export DOCKER_LOG="$BATS_TEST_TMPDIR/docker.log"
  export BWRAP_LOG="$BATS_TEST_TMPDIR/bwrap.log"
  export GH_LOG="$BATS_TEST_TMPDIR/gh.log"
  export CLAUDE_LOG="$BATS_TEST_TMPDIR/claude.log"
  export NIX_LOG="$BATS_TEST_TMPDIR/nix.log"
  export CLAUDE_PROMPT_FILE="$BATS_TEST_TMPDIR/claude-prompt.txt"
  export CLAUDE_AGENTS_FILE="$BATS_TEST_TMPDIR/claude-agents.json"
  : >"$PODMAN_LOG"
  : >"$DOCKER_LOG"
  : >"$BWRAP_LOG"
  : >"$GH_LOG"
  : >"$CLAUDE_LOG"
  : >"$NIX_LOG"
}

# Minimal env so the `run` command's required-var guards pass. Individual tests
# override any of these before invoking RUN_CMD.
set_run_env() {
  export REPO_SLUG="owner/repo"
  export GH_TOKEN="fake-token"
  export CLAUDE_CODE_OAUTH_TOKEN="fake-oauth"
  export GIT_USER_NAME="Test Bot"
  export GIT_USER_EMAIL="bot@example.com"
}

# Stand up a local bare "GitHub" repo and rewrite https://github.com/ to it via
# git's insteadOf, so the entrypoint's real `git clone`/`push` stay offline.
# Seeds an initial commit on `main`. Exports REMOTE_ROOT.
setup_bare_repo() {
  export HOME="$BATS_TEST_TMPDIR/home"
  mkdir -p "$HOME"
  export REMOTE_ROOT="$BATS_TEST_TMPDIR/remote"
  mkdir -p "$REMOTE_ROOT/owner"

  # Configure git before `init` so the bare repo's HEAD tracks `main`, not the
  # built-in `master` default. A plain `git clone` (seed_flake_repo) resolves
  # the branch via remote HEAD; a `master` HEAD with a `main`-only ref leaves it
  # on an orphan branch, and the follow-up push is then non-fast-forward.
  git config --global init.defaultBranch main
  git config --global user.name "Seed"
  git config --global user.email "seed@example.com"
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://github.com/"

  git init --bare -q "$REMOTE_ROOT/owner/repo.git"

  local seed="$BATS_TEST_TMPDIR/seed"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  (
    cd "$seed" || exit 1
    echo "# repo" >README.md
    git add -A
    git commit -q -m "chore: seed"
    git push -q origin HEAD:main
  )
}

# Push a minimal flake.nix to the remote's main branch so the entrypoint clones
# a repo that exposes a devShell. Call after setup_bare_repo.
seed_flake_repo() {
  local seed="$BATS_TEST_TMPDIR/seed-flake"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  printf '{ outputs = _: { devShells.x86_64-linux.default = {}; }; }\n' \
    >"$seed/flake.nix"
  git -C "$seed" add flake.nix
  git -C "$seed" commit -q -m "chore: add flake"
  git -C "$seed" push -q origin HEAD:main
}
