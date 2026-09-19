# Shared bats helpers. Sourced by every *.bats file.
#
# The nix `checks.<system>.bats` derivation exports the paths these helpers
# depend on: FAKES_DIR, RUN_CMD / BUILD_CMD, ENTRYPOINT, PROMPTS_DIR, and
# IMAGE_PATH.

# Prints issue-prompt.md's OUTCOME section (issue #1901). Several prompt.bats
# tests assert on this slice, so the sed anchor lives in one place.
issue_prompt_outcome_section() {
  local prompts="${PROMPTS_DIR:-$BATS_TEST_DIRNAME/../templates/default/prompts}"
  sed -n '/^# OUTCOME$/,/^# IF BLOCKED$/p' "$prompts/issue-prompt.md"
}

# A missing or unreadable file counts as 0 rather than an empty string, so a
# caller's integer comparison never throws.
_count_matches() {
  local file="$1" pattern="$2" count
  if [ -r "$file" ]; then
    count="$(grep -c "$pattern" "$file" 2>/dev/null)" || count=0
  else
    count=0
  fi
  echo "$count"
}

# Usage: wait_for_log_lines <file> <pattern> <expected_count> [timeout_seconds]
# Polls rather than sampling the log once (issue #2450); the flaky CI failure
# behind it was never pinned down (commit 9e724bab), so the wait is defensive.
# Nix's build sandbox scrubs WAIT_FOR_LOG_LINES_TIMEOUT, so nix/checks/bats.nix
# bakes a wider 10s default into the derivation instead (issue #2649).
wait_for_log_lines() {
  local file="$1" pattern="$2" expected="$3" timeout="${4:-${WAIT_FOR_LOG_LINES_TIMEOUT:-2}}"
  # The timeout flows into a `timeout * 20` arithmetic context, so reject
  # anything but a small positive integer: 0 collapses the loop to a single
  # check, and an 18-digit value wraps the poll count negative (issue #2759).
  if ! [[ "$timeout" =~ ^[1-9][0-9]{0,5}$ ]]; then
    echo "wait_for_log_lines: timeout must be a positive integer of at most 6 digits, got '$timeout'" >&2
    return 1
  fi
  local interval="0.05"
  local confirm_tries=3
  local tries=$((timeout * 20)) # 20 == 1/interval (0.05s); also mirrored in tests/run-batch-limits.bats' "widen past the 2s default" test
  local actual i confirm

  for ((i = 0; i <= tries; i++)); do
    actual="$(_count_matches "$file" "$pattern")"
    if [ "$actual" -gt "$expected" ]; then
      echo "wait_for_log_lines: overshot -- $actual line(s) matching" \
        "'$pattern' in $file, expected $expected" >&2
      return 1
    fi
    if [ "$actual" -eq "$expected" ]; then
      # Reaching expected is not proof the count has settled: it may be passing
      # through on its way to a higher, wrong one (an over-dispatch regression).
      # Confirm over a few extra polls, not the full remaining timeout.
      for ((confirm = 0; confirm < confirm_tries; confirm++)); do
        sleep "$interval"
        actual="$(_count_matches "$file" "$pattern")"
        if [ "$actual" -gt "$expected" ]; then
          echo "wait_for_log_lines: overshot during confirmation --" \
            "$actual line(s) matching '$pattern' in $file, expected $expected" >&2
          return 1
        fi
      done
      return 0
    fi
    [ "$i" -lt "$tries" ] && sleep "$interval"
  done

  echo "wait_for_log_lines: timed out after ${timeout}s waiting for" \
    "$expected line(s) matching '$pattern' in $file (got $actual)" >&2
  return 1
}

# Asserts wait_for_log_lines rejects an invalid timeout cleanly: status 1, the
# shared rejection message, and no pre-fix symptom string. Variadic so one call
# can pin more than one symptom per malformed-timeout case.
# shellcheck disable=SC2154 # $status/$output are bats-provided by the `run` call above, not assigned directly
assert_timeout_rejected() {
  local log="$1" timeout_value="$2" absent_substring
  shift 2
  run wait_for_log_lines "$log" '^run ' 1 "$timeout_value"
  # Each assertion returns 1 explicitly instead of leaning on `set -e`: the
  # caller (tests/run-batch-limits.bats' malformed-timeout loop) suspends
  # errexit, so a bare failing statement would fall through.
  if [ "$status" -ne 1 ]; then
    echo "assert_timeout_rejected: expected status 1, got $status" >&2
    return 1
  fi
  if [[ "$output" != *"timeout must be a positive integer"* ]]; then
    echo "assert_timeout_rejected: output missing expected substring [timeout must be a positive integer]: $output" >&2
    return 1
  fi
  for absent_substring in "$@"; do
    if [ -n "$absent_substring" ] && [[ "$output" == *"$absent_substring"* ]]; then
      echo "assert_timeout_rejected: output unexpectedly contains [$absent_substring]: $output" >&2
      return 1
    fi
  done
}

# Extracts the --handoff-file path agent/entrypoint.sh's run_driver_in_env
# passed, from a verbatim-argv log such as $ORCHESTRATOR_LOG. Since issue #2975
# the driver, model, effort, argv-shape, review and caps facts live in that JSON
# rather than on argv. head -1 picks main's implement pass, deliberately skipping
# the stripped copy a corrective resume passes (only ReviewPromptFile differs).
handoff_path_from_log() {
  grep -oE -- '--handoff-file [^ ]+' "$1" | head -1 | awk '{print $2}'
}

# Kills a suite's backgrounded stand-in socat so it never survives the test.
# Call from each suite's own teardown(); bats requires that hook per file.
kill_stand_in_socat() {
  [ -n "${_test_socat_pid:-}" ] && kill "$_test_socat_pid" 2>/dev/null
  true
}

# Bounded poll for a stand-in socat's UNIX-LISTEN socket file: a freshly
# backgrounded socat may take a moment to bind.
wait_for_socket() {
  local _path="$1" _tries=0
  while [ "$_tries" -lt 50 ]; do
    [ -S "$_path" ] && return 0
    sleep 0.1
    _tries=$((_tries + 1))
  done
  return 1
}

# Shared setup for the split entrypoint-*.bats suites (issue #518): bats needs a
# setup() hook per file, so the shared body lives here.
setup_entrypoint_env() {
  setup_fakes
  setup_bare_repo
  set_box_env
  # Not a schema knob (issue #1951): dispatch.buildBoxEnv computes it host-side
  # from BOX_FORGE_AND_ISSUE_ACCESS and forwards it only when writes are
  # enabled, so box_env_gen.bash never exports it. Read-only tests unset it
  # instead of overriding BOX_FORGE_AND_ISSUE_ACCESS.
  export BOX_WRITE_ENABLED=1
  # Also host-computed rather than a schema knob: buildBoxEnv forwards it
  # whenever the backend registry's outboxRelayCapable is true, read-only or
  # not. The CODE_FORGE=local test overrides BOX_HOST_MEDIATED_REMOTE instead,
  # which the backstop's switch checks first.
  export BOX_OUTBOX_RELAY_CAPABLE=1
  # Also host-derived rather than schema knobs: nix derives them from
  # ISSUE_TRACKER/CODE_FORGE/ORCHESTRATOR_ENABLED and passes them as launcher
  # flags. These mirror the suite's default cell (issue #2533), so a test that
  # moves one of those raw vars must move the matching BOX_* var with it.
  export BOX_TRACKER_AXIS_READ=GITHUB
  export BOX_TRACKER_AXIS_WRITE=GITHUB
  export BOX_TRACKER_AXIS_FILER=GH
  export BOX_FORGE_BACKEND=GH
  export BOX_REVIEW_LOOP_INLINE=1
  # Pinned away from the schema default (issue #2055) so the MODEL-flag
  # assertions stay stable when that default moves.
  export MODEL="claude-test-model"
  # Nix bakes this from the roster (lib/mkHarness.nix), and entrypoint.sh's
  # per-name injection loop (issue #264) resolves prompt files through it.
  # Deliberately narrower than the real default roster, which also carries
  # review-axis (issue #3447): a test needing that sets the var itself.
  export AGENTS_PROMPT_FILES='{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md"}'
  export ISSUE_NUMBER="7"
  export ISSUE_TITLE="Do the thing"
  export WORK_DIR="$BATS_TEST_TMPDIR/work"
  # A real Box always receives a nonce, and both fakes/claude's
  # SPINDRIFT_PR_INTENT emission and entrypoint.sh's PR-intent marker gate
  # (issue #2045) key off it: unset, every read-only+github+status=ready
  # fixture here would look like a #2036 repro and eat a resume pass.
  export RUN_NONCE="test-run-nonce-0001"
}

# Stands in for `launcher build`'s VACUUMed host nix store DB snapshot
# (ADR 0042): bwrapAdapter.IsReady only checks the file exists and is not a
# directory, so a bare stub passes readiness without a real build (issue #2664).
# Must run after cd'ing into the test's own $BATS_TEST_TMPDIR, since the launcher
# resolves the snapshot dir relative to its working directory.
stub_nix_var_snapshot() {
  # The snapshot dir is generation-scoped under the agent-closure store path it
  # was built against (issue #2680), and $BWRAP_RUN_CMD and $SKILLS_BWRAP_RUN_CMD
  # come from separate mkHarness invocations, so each needs its own generation
  # subdir. The *_IMAGE_TAG vars carry those closure paths, letting this mirror
  # closureGeneration's filepath.Base(imageTag) in bash.
  local tag generation
  for tag in "$BWRAP_IMAGE_TAG" "$SKILLS_BWRAP_IMAGE_TAG"; do
    [ -n "$tag" ] || continue
    generation=$(basename "$tag")
    mkdir -p ".spindrift/nix-var-snapshot/$generation/nix/db"
    : >".spindrift/nix-var-snapshot/$generation/nix/db/db.sqlite"
  done
}

# Overwrites $FAKE_BIN/driver-exec with a wrapper that fails only the
# bind-registry verb and delegates the rest, for the two
# tests/entrypoint-toolchain-nudge.bats cases on that failure path. Must run
# after setup_fakes, which creates the file this overwrites.
stub_failing_bind_registry() {
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<FAKE
if [ "\$1" = "bind-registry" ]; then
  exit 3
fi
exec "$FAKES_DIR/driver-exec" "\$@"
FAKE
  } >"$FAKE_BIN/driver-exec"
  chmod +x "$FAKE_BIN/driver-exec"
}

# Shared setup for the split run-*.bats suites (issue #519): bats needs a
# setup() hook per file, so the shared body lives here.
setup_run_env() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR" || exit
  stub_nix_var_snapshot
  export FAKE_GH_ISSUES=$'1\tFirst issue\n2\tSecond issue'
  # Bound the merge gate's poll loop (issue #2424): a test that reaches it
  # without its own values would inherit the production defaults (3600s
  # timeout, 180s interval) and really sleep, as CI did on PR #2410. Interval 0
  # keeps iterations instant; a small nonzero timeout still lets it iterate once.
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=2
}

setup_fakes() {
  : "${FAKES_DIR:?FAKES_DIR must be set (dir holding fake runtime/gh/claude)}"
  FAKE_BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$FAKE_BIN"
  cp "$FAKES_DIR/runtime" "$FAKE_BIN/podman"
  cp "$FAKES_DIR/runtime" "$FAKE_BIN/docker"
  cp "$FAKES_DIR/runtime" "$FAKE_BIN/bwrap"
  # checkBwrapPastaGate (issue #2666) probes the launcher's PATH for pasta
  # before bwrap runs, and bwrap.go's execTarget makes this fake the top-level
  # exec target for any NetworkMode but "host", so tests/fakes/pasta must exec
  # through to the fake bwrap below.
  cp "$FAKES_DIR/pasta" "$FAKE_BIN/pasta"
  : "${DRIVER:=claude}"
  cp "$FAKES_DIR/gh" "$FAKES_DIR/$DRIVER" "$FAKES_DIR/nix" \
     "$FAKES_DIR/driver-exec" "$FAKES_DIR/orchestrator" "$FAKE_BIN/"
  # The claude and opencode fakes source _driver-common.bash relative to their
  # own directory at runtime, so it has to sit beside the copied driver fake.
  cp "$FAKES_DIR/_driver-common.bash" "$FAKE_BIN/"
  chmod +x "$FAKE_BIN"/*
  export PATH="$FAKE_BIN:$PATH"

  export PODMAN_LOG="$BATS_TEST_TMPDIR/podman.log"
  export DOCKER_LOG="$BATS_TEST_TMPDIR/docker.log"
  export BWRAP_LOG="$BATS_TEST_TMPDIR/bwrap.log"
  # tests/fakes/pasta (issue #2666) logs its own invocation here before exec'ing
  # the fake bwrap; $BWRAP_LOG only ever sees the argv pasta passes on.
  export PASTA_LOG="$BATS_TEST_TMPDIR/pasta.log"
  export GH_LOG="$BATS_TEST_TMPDIR/gh.log"
  export GIT_LOG="$BATS_TEST_TMPDIR/git.log"
  export DRIVER_LOG="$BATS_TEST_TMPDIR/$DRIVER.log"
  export NIX_LOG="$BATS_TEST_TMPDIR/nix.log"
  export ORCHESTRATOR_LOG="$BATS_TEST_TMPDIR/orchestrator.log"
  export DRIVER_PROMPT_FILE="$BATS_TEST_TMPDIR/$DRIVER-prompt.txt"
  export DRIVER_AGENTS_FILE="$BATS_TEST_TMPDIR/$DRIVER-agents.json"
  # Test-only hook (issue #2395): phase_prompt_assembly copies the raw Handoff
  # JSON here just before it `rm -f`s its own tempfile. A no-op in production,
  # where this var is never set.
  export DRIVER_HANDOFF_FILE="$BATS_TEST_TMPDIR/$DRIVER-handoff.json"
  : >"$PODMAN_LOG"
  : >"$DOCKER_LOG"
  : >"$BWRAP_LOG"
  : >"$PASTA_LOG"
  : >"$GH_LOG"
  : >"$DRIVER_LOG"
  : >"$NIX_LOG"
  : >"$ORCHESTRATOR_LOG"

  # The nix check derivation exports the real mkHarness-built contract, which
  # entrypoint.sh reads when a rendered issue prompt lacks one (issue #420);
  # a bare `bats` run outside nix has no such file, so fall back to a fixture.
  # A test exercising the injection overrides it. Spec #2244's registry slice
  # also touches this fallback, so check for conflicts.
  : "${OUTCOME_CONTRACT_FILE:=$BATS_TEST_TMPDIR/outcome-contract.md}"
  export OUTCOME_CONTRACT_FILE
  if [ ! -s "$OUTCOME_CONTRACT_FILE" ]; then
    printf '# LAND THE CHANGE\n\ncanonical outcome contract fixture\n' >"$OUTCOME_CONTRACT_FILE"
  fi

  # Same fallback, for the COMMS and CHECK blocks fix-prompt.md shares with
  # issue-prompt.md (issue #455).
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

  # Same fallback, for the CODE COMMENTS block fix-prompt.md shares with
  # issue-prompt.md (issue #2880).
  : "${CODE_COMMENTS_CONTRACT_FILE:=$BATS_TEST_TMPDIR/code-comments-contract.md}"
  export CODE_COMMENTS_CONTRACT_FILE
  if [ ! -s "$CODE_COMMENTS_CONTRACT_FILE" ]; then
    printf '# CODE COMMENTS\n\ncanonical code comments contract fixture\n' >"$CODE_COMMENTS_CONTRACT_FILE"
  fi

  # Same fallback, for the research dispatch kind's outcome contract (issue #640).
  : "${RESEARCH_OUTCOME_CONTRACT_FILE:=$BATS_TEST_TMPDIR/research-outcome-contract.md}"
  export RESEARCH_OUTCOME_CONTRACT_FILE
  if [ ! -s "$RESEARCH_OUTCOME_CONTRACT_FILE" ]; then
    printf '# POST THE VERDICT\n\ncanonical research outcome contract fixture\n' >"$RESEARCH_OUTCOME_CONTRACT_FILE"
  fi

  # The pre-wrap entrypoint path, saved before ENTRYPOINT is reassigned below,
  # so a test needing its own wrapped variant (issue #622) can build one from
  # the real source.
  export ENTRYPOINT_SRC="$ENTRYPOINT"

  # Prepend whichever of the Driver preamble (issues #624, #433), the baked
  # /agent/* path defaults (issue #2531), and the Conditional fragment registry
  # (issue #622) are set, each independently guarded, in lib/image.nix's own
  # concatenation order so the suite sees the bytes the image bakes. Outside
  # nix none are set, the entrypoint stays unwrapped, and tests fail by design.
  if [ -n "${DRIVER_PREAMBLE_FILE:-}" ] || [ -n "${AGENT_PATHS_PREAMBLE_FILE:-}" ] \
    || [ -n "${FRAGMENT_REGISTRY_FILE:-}" ]; then
    local _wrapped="$BATS_TEST_TMPDIR/entrypoint.sh"
    {
      if [ -n "${DRIVER_PREAMBLE_FILE:-}" ]; then
        cat "$DRIVER_PREAMBLE_FILE"
        # Re-root the baked absolute /home/agent skills dir under this test's own
        # $HOME, which a bats sandbox can write to (issue #624). Stripping the
        # prefix reuses the suffix the registry rendered. Written unexpanded so it
        # resolves against the HOME setup_bare_repo sets, not the one in effect here.
        # shellcheck disable=SC2016 # intentionally unexpanded -- written verbatim into $_wrapped
        echo 'DRIVER_SKILLS_DIR="$HOME/${DRIVER_SKILLS_DIR#/home/agent/}"'
        # Same re-rooting for DRIVER_SESSION_CACHE_DIR (issue #2843), guarded at
        # $_wrapped's runtime because only some Drivers set it and an unguarded
        # rewrite would leave it empty-but-set. This clobbers the var, so a test
        # lands a trailing-slash variant via TEST_SESSION_CACHE_DIR_SUFFIX (#2845).
        # shellcheck disable=SC2016 # intentionally unexpanded -- written verbatim into $_wrapped
        echo 'if [ -n "${DRIVER_SESSION_CACHE_DIR:-}" ]; then DRIVER_SESSION_CACHE_DIR="$HOME/${DRIVER_SESSION_CACHE_DIR#/home/agent/}${TEST_SESSION_CACHE_DIR_SUFFIX:-}"; fi'
      fi
      if [ -n "${AGENT_PATHS_PREAMBLE_FILE:-}" ]; then
        cat "$AGENT_PATHS_PREAMBLE_FILE"
      fi
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

# Minimal env so the `run` command's required-var guards pass.
set_run_env() {
  export REPO_SLUG="owner/repo"
  export GH_TOKEN="fake-token"
  export CLAUDE_CODE_OAUTH_TOKEN="fake-oauth"
  export GIT_USER_NAME="Test Bot"
  export GIT_USER_EMAIL="bot@example.com"
}

# set_box_env: every lib/env-schema.nix knob with boxEnv = true, at its schema
# default, so the entrypoint-*.bats suites see the defaults the nix preamble
# bakes into the image. Generated by lib/renderers.nix renderSetBoxEnvFixture;
# nix/checks/schema-drift.nix box-env-fixture-coverage guards against drift.
# shellcheck source=tests/box_env_gen.bash disable=SC1091
source "${BATS_TEST_DIRNAME}/box_env_gen.bash"

# Stands up a local bare "GitHub" repo and rewrites https://github.com/ to it
# via git's insteadOf, so the entrypoint's real `git clone`/`push` stay offline.
setup_bare_repo() {
  export HOME="$BATS_TEST_TMPDIR/home"
  mkdir -p "$HOME"
  export REMOTE_ROOT="$BATS_TEST_TMPDIR/remote"
  mkdir -p "$REMOTE_ROOT/owner"

  # Configure git before `init` so the bare repo's HEAD tracks `main`, not the
  # built-in `master`. A plain `git clone` resolves the branch via remote HEAD,
  # and a `master` HEAD with a `main`-only ref leaves the clone on an orphan
  # branch, making the follow-up push non-fast-forward.
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

# Pushes a minimal flake.nix to the remote's main branch so the entrypoint
# clones a repo that exposes a devShell. Call after setup_bare_repo.
seed_flake_repo() {
  local seed="$BATS_TEST_TMPDIR/seed-flake"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  printf '{ outputs = _: { devShells.x86_64-linux.default = {}; }; }\n' \
    >"$seed/flake.nix"
  git -C "$seed" add flake.nix
  git -C "$seed" commit -q -m "chore: add flake"
  git -C "$seed" push -q origin HEAD:main
}

# Pushes main to a same-named remote branch so a non-default BASE_BRANCH
# resolves to a real origin ref. phase_branch_recovery checks that ref out
# before the prompt is assembled and setup_bare_repo seeds only main, so any
# test setting BASE_BRANCH away from "main" needs this first.
# Usage: seed_release_branch "release-42" "seed-name"
seed_release_branch() {
  local branch="$1" seed_name="$2"
  local seed="$BATS_TEST_TMPDIR/$seed_name"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  git -C "$seed" push -q origin "main:$branch"
}

# Pushes a named dependency-manifest file (a lockfile, or a Gradle
# build/settings file) to the remote's main branch. Call after setup_bare_repo.
# Usage: seed_dependency_manifest "go.sum"
seed_dependency_manifest() {
  local manifest="$1"
  local seed="$BATS_TEST_TMPDIR/seed-dependency-manifest"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  touch "$seed/$manifest"
  git -C "$seed" add "$manifest"
  git -C "$seed" commit -q -m "chore: add $manifest"
  git -C "$seed" push -q origin HEAD:main
}
