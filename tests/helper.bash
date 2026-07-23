# Shared bats helpers. Sourced by every *.bats file.
#
# The nix `checks.<system>.bats` derivation exports the paths these helpers
# depend on: FAKES_DIR (the fake runtime/gh/claude), RUN_CMD / BUILD_CMD (the
# nix-generated commands with the image store path baked in), ENTRYPOINT (the
# in-container script), PROMPTS_DIR (the baked prompt templates), and IMAGE_PATH
# (the image archive store path substituted into the commands).

# Prints issue-prompt.md's OUTCOME section (issue #1901): several prompt.bats
# tests assert on this slice, so extracting it once keeps the sed anchor in
# a single place instead of copy-pasted per test.
issue_prompt_outcome_section() {
  local prompts="${PROMPTS_DIR:-$BATS_TEST_DIRNAME/../templates/default/prompts}"
  sed -n '/^# OUTCOME$/,/^# IF BLOCKED$/p' "$prompts/issue-prompt.md"
}

# Shared setup for the split entrypoint-*.bats suites (issue #518): every
# concern file needs its own setup() hook per bats semantics, so the body
# entrypoint.bats used to run once now lives here instead.
setup_entrypoint_env() {
  setup_fakes
  setup_bare_repo
  set_box_env
  # Pinned away from the schema default (claude-sonnet-5) so the MODEL-flag
  # assertions below stay stable regardless of what the schema defaults to.
  export MODEL="claude-opus-4-8"
  export ISSUE_NUMBER="7"
  export ISSUE_TITLE="Do the thing"
  export WORK_DIR="$BATS_TEST_TMPDIR/work"
}

# Shared setup for the split run-*.bats suites (issue #519): every concern
# file needs its own setup() hook per bats semantics, so the body run.bats
# used to run once now lives here instead.
setup_run_env() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR" || exit
  export FAKE_GH_ISSUES=$'1\tFirst issue\n2\tSecond issue'
}

setup_fakes() {
  : "${FAKES_DIR:?FAKES_DIR must be set (dir holding fake runtime/gh/claude)}"
  FAKE_BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$FAKE_BIN"
  cp "$FAKES_DIR/runtime" "$FAKE_BIN/podman"
  cp "$FAKES_DIR/runtime" "$FAKE_BIN/docker"
  cp "$FAKES_DIR/runtime" "$FAKE_BIN/bwrap"
  cp "$FAKES_DIR/gh" "$FAKES_DIR/claude" "$FAKES_DIR/nix" \
     "$FAKES_DIR/driver-exec" "$FAKE_BIN/"
  chmod +x "$FAKE_BIN"/*
  export PATH="$FAKE_BIN:$PATH"

  export PODMAN_LOG="$BATS_TEST_TMPDIR/podman.log"
  export DOCKER_LOG="$BATS_TEST_TMPDIR/docker.log"
  export BWRAP_LOG="$BATS_TEST_TMPDIR/bwrap.log"
  export GH_LOG="$BATS_TEST_TMPDIR/gh.log"
  export GIT_LOG="$BATS_TEST_TMPDIR/git.log"
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

  # The nix check derivation exports OUTCOME_CONTRACT_FILE (the real
  # mkHarness-built canonical contract); a bare `bats` run outside nix has no
  # such file, so fall back to a minimal fixture -- entrypoint.sh reads this
  # whenever a rendered issue prompt lacks the contract (issue #420). A test
  # exercising the injection itself overrides this with its own fixture.
  : "${OUTCOME_CONTRACT_FILE:=$BATS_TEST_TMPDIR/outcome-contract.md}"
  export OUTCOME_CONTRACT_FILE
  if [ ! -s "$OUTCOME_CONTRACT_FILE" ]; then
    printf '# LAND THE CHANGE\n\ncanonical outcome contract fixture\n' >"$OUTCOME_CONTRACT_FILE"
  fi

  # Same fallback, for the COMMS and CHECK blocks fix-prompt.md shares with
  # issue-prompt.md (issue #455). A test exercising the injection itself
  # overrides these with its own fixture.
  : "${COMMS_CONTRACT_FILE:=$BATS_TEST_TMPDIR/comms-contract.md}"
  export COMMS_CONTRACT_FILE
  if [ ! -s "$COMMS_CONTRACT_FILE" ]; then
    printf '# COMMS\n\ncanonical comms contract fixture\n' >"$COMMS_CONTRACT_FILE"
  fi
  : "${CHECK_CONTRACT_FILE:=$BATS_TEST_TMPDIR/check-contract.md}"
  export CHECK_CONTRACT_FILE
  if [ ! -s "$CHECK_CONTRACT_FILE" ]; then
    printf '# CHECK\n\ncanonical check contract fixture\n' >"$CHECK_CONTRACT_FILE"
  fi

  # Same fallback, for the research dispatch kind's outcome contract (issue
  # #640). A test exercising the injection itself overrides this with its own
  # fixture.
  : "${RESEARCH_OUTCOME_CONTRACT_FILE:=$BATS_TEST_TMPDIR/research-outcome-contract.md}"
  export RESEARCH_OUTCOME_CONTRACT_FILE
  if [ ! -s "$RESEARCH_OUTCOME_CONTRACT_FILE" ]; then
    printf '# POST THE VERDICT\n\ncanonical research outcome contract fixture\n' >"$RESEARCH_OUTCOME_CONTRACT_FILE"
  fi

  # The pre-wrap entrypoint path, preserved before ENTRYPOINT is reassigned
  # below, so a test that needs its own custom-wrapped variant (e.g. the
  # Conditional fragment registry fixture-row test, issue #622) can still
  # build one from the real source.
  export ENTRYPOINT_SRC="$ENTRYPOINT"

  # DRIVER_PREAMBLE_FILE is the registry-rendered Driver preamble -- the
  # DRIVER_* variable block and function definitions alike (issue #624,
  # #433) -- and FRAGMENT_REGISTRY_FILE is the registry-rendered Conditional
  # fragment loop input and substitution allowlist (issue #622): prepend
  # both to the entrypoint so the suite exercises the same bytes and data
  # the image bakes in, not any hand-copied duplicates. The nix check
  # derivation sets these; a bare bats run outside nix leaves ENTRYPOINT
  # as-is (functions/registry undefined → tests fail, by design: use nix
  # flake check).
  if [ -n "${DRIVER_PREAMBLE_FILE:-}" ]; then
    local _wrapped="$BATS_TEST_TMPDIR/entrypoint.sh"
    {
      cat "$DRIVER_PREAMBLE_FILE"
      # Test-only override, appended after the registry-rendered preamble
      # above rather than folded into it (issue #624): the baked
      # DRIVER_SKILLS_DIR is the absolute /home/agent path a real Box always
      # has, byte-identical to what mkHarness.nix bakes into the image, but
      # a bats sandbox has no such directory to write into. Redirect it at
      # this test's own $HOME instead, by stripping the baked /home/agent/
      # prefix the line just above sets and re-rooting the same relative
      # suffix under $HOME -- no second hand-copied ".claude/skills" here,
      # just the one the registry already rendered. Written as literal
      # unexpanded text so it resolves against whatever HOME setup_bare_repo
      # below sets, not whatever HOME happens to be while this file is
      # assembled.
      # shellcheck disable=SC2016 # intentionally unexpanded -- written verbatim into $_wrapped
      echo 'DRIVER_SKILLS_DIR="$HOME/${DRIVER_SKILLS_DIR#/home/agent/}"'
      if [ -n "${FRAGMENT_REGISTRY_FILE:-}" ]; then
        cat "$FRAGMENT_REGISTRY_FILE"
      fi
      tail -n +2 "$ENTRYPOINT"
    } >"$_wrapped"
    chmod +x "$_wrapped"
    ENTRYPOINT="$_wrapped"
    export ENTRYPOINT
  fi
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

# set_box_env: every lib/env-schema.nix knob with boxEnv = true, at its
# schema default, so the entrypoint-*.bats suites exercise the same defaults
# the nix preamble bakes into the image at build time. Generated by
# lib/renderers.nix renderSetBoxEnvFixture (see tests/box_env_gen.bash);
# nix/checks/schema-drift.nix box-env-fixture-coverage guards against drift.
# Individual tests override any of these before invoking $ENTRYPOINT; a
# deliberate divergence from the schema default (e.g. a model pinned for
# stable assertions) is stated at its override site, not buried here.
# shellcheck source=tests/box_env_gen.bash disable=SC1091
source "${BATS_TEST_DIRNAME}/box_env_gen.bash"

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

# Push a named lockfile to the remote's main branch. Call after setup_bare_repo.
# Usage: seed_lockfile "go.sum"
seed_lockfile() {
  local lockfile="$1"
  local seed="$BATS_TEST_TMPDIR/seed-lockfile"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  touch "$seed/$lockfile"
  git -C "$seed" add "$lockfile"
  git -C "$seed" commit -q -m "chore: add $lockfile"
  git -C "$seed" push -q origin HEAD:main
}
