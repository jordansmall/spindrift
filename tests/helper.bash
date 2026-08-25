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

# Polls "$file" for a line count matching "$pattern" (grep -c), instead of
# sampling it once (issue #2450 asked for exactly this: "make the assertion
# wait for the dispatches it expects rather than sampling the log at a
# moment that is not guaranteed to be after both writes"). The precise
# mechanism behind #2450's original flaky CI failure was never pinned down:
# a later investigation (commit 9e724bab, reverting an earlier attempted
# fix) found dispatchWave's wg.Wait() already blocks until every dispatch
# subprocess exits and its output is flushed, so by the time a test's `run
# "$RUN_CMD"` returns there is no late writer left to race a single sample
# against -- and an independent stress test (300 concurrent invocations of
# tests/fakes/runtime, plus 30 rounds of 40 concurrent invocations) found
# zero lost or interleaved log lines. So treat this as a defensive
# belt-and-suspenders wait, not a proven fix for a root cause that remains
# unidentified. Bounded by a timeout so a genuine under-count -- the log
# will never reach expected_count -- still fails, rather than hanging:
# default 2s (widen via WAIT_FOR_LOG_LINES_TIMEOUT), or a non-empty explicit
# 4th arg, which wins over both the env var and the 2s default (kept short in
# tests that intentionally exercise the timeout path). An unset OR
# empty-string 4th arg falls through to the env var/default instead, per
# bash's own "${4:-...}" fallback rule. That env var only reaches this
# process when the caller's shell is the one running bats directly -- Nix's
# build sandbox scrubs the environment before a derivation's builder runs,
# so a shell-level `WAIT_FOR_LOG_LINES_TIMEOUT=N nix flake check` never
# propagates down into this sourced bash process. That's why the Nix `bats`
# check (nix/checks/bats.nix, issue #2649) bakes its own wider 10s default
# directly into the derivation's environment instead of relying on a
# caller-supplied env var: a serially-run bats suite on a loaded host can
# outrun the tight 2s local-dev default, and the derivation's own env is the
# only place a human can move that number without editing every
# default-timeout call site -- the only place it's reachable from inside the
# sandbox. Whichever source it comes from, the timeout value must be a
# positive integer no more than 6 digits (up to 999999) -- it flows into a
# `timeout * 20` arithmetic context, so anything else is rejected outright
# rather than risking a bash syntax error, an arithmetic overflow (an 18+
# digit value can wrap the poll-count negative and silently skip the loop
# instead of erroring), or, worse, code execution. Zero is rejected too:
# timeout=0 collapses the main retry loop to zero retries, so only the
# first check ever runs, falling straight to either the confirm window
# (if it already matches) or the timeout error (if it doesn't) --
# defeating the point of a wait/poll helper (issue #2450), so issue #2759
# rejects 0 outright rather than treating it as a valid bound.
#
# Reaching expected_count mid-poll is not itself proof the count has
# settled: it may just be passing through on its way to a higher, wrong
# count (e.g. a genuine over-dispatch regression). So once a poll first
# observes expected_count, this does a handful of short additional confirm
# polls (fixed at ~150ms total, independent of the main timeout) before
# declaring success, and fails immediately -- without waiting out the
# timeout -- if the count ever exceeds expected_count, at any point.
#
# Missing/unreadable file reads as count 0, not an empty string, so callers'
# arithmetic comparisons never throw an integer-expression error.
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
# timeout must be a positive integer of at most 6 digits (up to 999999)
# (rejects fractional/negative/zero/oversized/injection values before they
# reach an arithmetic context).
wait_for_log_lines() {
  local file="$1" pattern="$2" expected="$3" timeout="${4:-${WAIT_FOR_LOG_LINES_TIMEOUT:-2}}"
  if ! [[ "$timeout" =~ ^[1-9][0-9]{0,5}$ ]]; then
    echo "wait_for_log_lines: timeout must be a positive integer of at most 6 digits, got '$timeout'" >&2
    return 1
  fi
  local interval="0.05"
  local confirm_tries=3
  local tries=$((timeout * 20)) # 20 == 1/interval (0.05s); keep in sync if interval changes -- also mirrored in tests/run-batch-limits.bats' "widen past the 2s default" test
  local actual i confirm

  for ((i = 0; i <= tries; i++)); do
    actual="$(_count_matches "$file" "$pattern")"
    if [ "$actual" -gt "$expected" ]; then
      echo "wait_for_log_lines: overshot -- $actual line(s) matching" \
        "'$pattern' in $file, expected $expected" >&2
      return 1
    fi
    if [ "$actual" -eq "$expected" ]; then
      # Reaching expected is not proof it has settled -- it may just be
      # passing through on its way to a higher, wrong count -- so run a
      # short, bounded confirmation before declaring success: a handful of
      # extra polls, not the full remaining timeout.
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

# Runs wait_for_log_lines with a deliberately invalid timeout and asserts
# it's rejected cleanly: status 1, the shared rejection message, and (if
# given) that a specific pre-fix symptom string is absent from the output.
# shellcheck disable=SC2154 # $status/$output are bats-provided by the `run` call above, not assigned directly
assert_timeout_rejected() {
  local log="$1" timeout_value="$2" absent_substring="${3:-}"
  run wait_for_log_lines "$log" '^run ' 1 "$timeout_value"
  [ "$status" -eq 1 ]
  [[ "$output" == *"timeout must be a positive integer"* ]]
  if [ -n "$absent_substring" ]; then
    [[ "$output" != *"$absent_substring"* ]]
  fi
}

# Shared setup for the split entrypoint-*.bats suites (issue #518): every
# concern file needs its own setup() hook per bats semantics, so the body
# entrypoint.bats used to run once now lives here instead.
setup_entrypoint_env() {
  setup_fakes
  setup_bare_repo
  set_box_env
  # BOX_WRITE_ENABLED is not a schema knob (issue #1951): dispatch.buildBoxEnv
  # computes it host-side from BOX_FORGE_AND_ISSUE_ACCESS and forwards it only
  # when writes are enabled, so box_env_gen.bash's codegen (boxEnv=true knobs
  # only) never exports it. Set it here to mirror what a real Box receives at
  # set_box_env's BOX_FORGE_AND_ISSUE_ACCESS=read-write default; individual
  # read-only tests unset it instead of overriding BOX_FORGE_AND_ISSUE_ACCESS.
  export BOX_WRITE_ENABLED=1
  # BOX_OUTBOX_RELAY_CAPABLE is not a schema knob either (same reasoning as
  # BOX_WRITE_ENABLED above): dispatch.buildBoxEnv computes it host-side from
  # the backend registry's outboxRelayCapable field and forwards it whenever
  # true, unconditional on read-only/read-write. Set it here to mirror what a
  # real Box receives under the suite's default CODE_FORGE=github (github's
  # row has outboxRelayCapable=true); the CODE_FORGE=local test below
  # overrides it via BOX_HOST_MEDIATED_REMOTE instead (checked first in the
  # backstop's switch, so this value becomes irrelevant there, same as
  # BOX_WRITE_ENABLED already staying set-but-irrelevant in that test today).
  export BOX_OUTBOX_RELAY_CAPABLE=1
  # BOX_TRACKER_AXIS_READ/WRITE/FILER, BOX_FORGE_BACKEND, and
  # BOX_REVIEW_LOOP_INLINE are not schema knobs either (same reasoning as
  # BOX_WRITE_ENABLED/BOX_OUTBOX_RELAY_CAPABLE above): nix derives them
  # host-side from ISSUE_TRACKER/CODE_FORGE/ORCHESTRATOR_ENABLED and forwards
  # them as a real launcher's own --tracker-axis-*/--forge-backend/
  # --review-loop-* flags. Set them here to mirror what a real Box receives
  # under this suite's default cell (ISSUE_TRACKER=github, CODE_FORGE=github,
  # ORCHESTRATOR_ENABLED unset/off, issue #2533); individual tests that
  # override one of those raw vars away from the default must also override
  # the matching BOX_* var(s) alongside it to stay consistent, the same way a
  # real dispatch always keeps them in sync.
  export BOX_TRACKER_AXIS_READ=GITHUB
  export BOX_TRACKER_AXIS_WRITE=GITHUB
  export BOX_TRACKER_AXIS_FILER=GH
  export BOX_FORGE_BACKEND=GH
  export BOX_REVIEW_LOOP_INLINE=1
  # Pinned to a value distinct from the schema default (issue #2055; the
  # schema default has since moved on to claude-sonnet-5) so the MODEL-flag
  # assertions below stay stable regardless of what the schema defaults to.
  export MODEL="claude-test-model"
  # Nix-baked from the roster (lib/mkHarness.nix): maps each --agents JSON
  # entry name to its prompt file under PROMPTS_DIR, so entrypoint.sh's
  # generic per-name injection loop (issue #264) resolves the same four
  # built-in prompt files it always has. Individual tests override this
  # to exercise a custom Nth agent or the "<name>-prompt.md" fallback.
  export AGENTS_PROMPT_FILES='{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md"}'
  export ISSUE_NUMBER="7"
  export ISSUE_TITLE="Do the thing"
  export WORK_DIR="$BATS_TEST_TMPDIR/work"
  # RUN_NONCE is not a schema knob either (same reasoning as BOX_WRITE_ENABLED
  # above): a real Box always receives one, so default it here rather than
  # leaving it unset -- fakes/claude's default SPINDRIFT_PR_INTENT emission,
  # and entrypoint.sh's own PR-intent required-marker gate row (issue #2045)
  # that scans for it, both key off this run's own nonce, so an unset one
  # here would make every read-only+github+status=ready fixture in this
  # suite look like a genuine #2036 repro and eat an unwanted resume pass.
  # Individual tests needing a specific value (e.g. to assert it's rendered
  # into the prompt) still override this before invoking $ENTRYPOINT.
  export RUN_NONCE="test-run-nonce-0001"
}

# Shared setup for the split run-*.bats suites (issue #519): every concern
# file needs its own setup() hook per bats semantics, so the body run.bats
# used to run once now lives here instead.
setup_run_env() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR" || exit
  export FAKE_GH_ISSUES=$'1\tFirst issue\n2\tSecond issue'
  # Guard (issue #2424): bound the merge gate's poll loop by default so any
  # test that reaches it without setting its own MERGE_POLL_INTERVAL /
  # MERGE_POLL_TIMEOUT can't inherit the launcher's real production default
  # (MERGE_POLL_TIMEOUT=1800s, MERGE_POLL_INTERVAL=30s) and real-sleep for up
  # to 30 minutes before failing (as happened in CI on PR #2410). A poll
  # interval of 0 keeps iterations instant; a small nonzero timeout still
  # lets a poll loop actually iterate at least once before its own deadline
  # fires. Individual tests (e.g. tests/run-merge-gate.bats,
  # tests/run-reconcile-recover.bats) override both explicitly where the
  # scenario needs a different bound -- leave those overrides as-is.
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
  : "${DRIVER:=claude}"
  cp "$FAKES_DIR/gh" "$FAKES_DIR/$DRIVER" "$FAKES_DIR/nix" \
     "$FAKES_DIR/driver-exec" "$FAKES_DIR/orchestrator" "$FAKE_BIN/"
  # tests/fakes/claude and tests/fakes/opencode both source their shared
  # control-flow block from tests/fakes/_driver-common.bash (relative to
  # their own directory at runtime) -- copy it alongside the driver fake
  # above so that resolves.
  cp "$FAKES_DIR/_driver-common.bash" "$FAKE_BIN/"
  chmod +x "$FAKE_BIN"/*
  export PATH="$FAKE_BIN:$PATH"

  export PODMAN_LOG="$BATS_TEST_TMPDIR/podman.log"
  export DOCKER_LOG="$BATS_TEST_TMPDIR/docker.log"
  export BWRAP_LOG="$BATS_TEST_TMPDIR/bwrap.log"
  export GH_LOG="$BATS_TEST_TMPDIR/gh.log"
  export GIT_LOG="$BATS_TEST_TMPDIR/git.log"
  export DRIVER_LOG="$BATS_TEST_TMPDIR/$DRIVER.log"
  export NIX_LOG="$BATS_TEST_TMPDIR/nix.log"
  export ORCHESTRATOR_LOG="$BATS_TEST_TMPDIR/orchestrator.log"
  export DRIVER_PROMPT_FILE="$BATS_TEST_TMPDIR/$DRIVER-prompt.txt"
  export DRIVER_AGENTS_FILE="$BATS_TEST_TMPDIR/$DRIVER-agents.json"
  # Test-only hook (issue #2395 slice 1): entrypoint.sh's phase_prompt_assembly
  # copies the raw Handoff JSON `driver-exec assemble-prompt --handoff-output`
  # produced here, right before it `rm -f`s its own tempfile, mirroring
  # DRIVER_PROMPT_FILE/DRIVER_AGENTS_FILE above -- a no-op in production,
  # where this var is never set.
  export DRIVER_HANDOFF_FILE="$BATS_TEST_TMPDIR/$DRIVER-handoff.json"
  : >"$PODMAN_LOG"
  : >"$DOCKER_LOG"
  : >"$BWRAP_LOG"
  : >"$GH_LOG"
  : >"$DRIVER_LOG"
  : >"$NIX_LOG"
  : >"$ORCHESTRATOR_LOG"

  # The nix check derivation exports OUTCOME_CONTRACT_FILE (the real
  # mkHarness-built canonical contract); a bare `bats` run outside nix has no
  # such file, so fall back to a minimal fixture -- entrypoint.sh reads this
  # whenever a rendered issue prompt lacks the contract (issue #420). A test
  # exercising the injection itself overrides this with its own fixture.
  # Coordination: spec #2244's registry slice also touches this
  # OUTCOME_CONTRACT_FILE fallback seam. Check for conflicts with that work
  # before changing this block further.
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
  # #433) -- AGENT_PATHS_PREAMBLE_FILE is the rendered fallback-default
  # preamble for the 8 baked /agent/* path literals (issue #2531), and
  # FRAGMENT_REGISTRY_FILE is the registry-rendered Conditional fragment
  # loop input and substitution allowlist (issue #622): prepend whichever of
  # the three are set to the entrypoint, in the same order lib/image.nix
  # concatenates them into the real image, so the suite exercises the same
  # bytes and data the image bakes in, not any hand-copied duplicates. Each
  # file has its own independent guard below, so a suite that sets only one
  # or two of the three still gets that subset prepended, instead of
  # silently getting none because it didn't also set DRIVER_PREAMBLE_FILE.
  # The nix check derivation sets these; a bare bats run outside nix leaves
  # ENTRYPOINT as-is (functions/registry undefined → tests fail, by design:
  # use nix flake check).
  if [ -n "${DRIVER_PREAMBLE_FILE:-}" ] || [ -n "${AGENT_PATHS_PREAMBLE_FILE:-}" ] \
    || [ -n "${FRAGMENT_REGISTRY_FILE:-}" ]; then
    local _wrapped="$BATS_TEST_TMPDIR/entrypoint.sh"
    {
      if [ -n "${DRIVER_PREAMBLE_FILE:-}" ]; then
        cat "$DRIVER_PREAMBLE_FILE"
        # Test-only override, appended after the registry-rendered preamble
        # above rather than folded into it (issue #624): the baked
        # DRIVER_SKILLS_DIR is the absolute /home/agent path a real Box
        # always has, byte-identical to what mkHarness.nix bakes into the
        # image, but a bats sandbox has no such directory to write into.
        # Redirect it at this test's own $HOME instead, by stripping the
        # baked /home/agent/ prefix the line just above sets and re-rooting
        # the same relative suffix under $HOME -- no second hand-copied
        # ".claude/skills" here, just the one the registry already
        # rendered. Written as literal unexpanded text so it resolves
        # against whatever HOME setup_bare_repo below sets, not whatever
        # HOME happens to be while this file is assembled.
        # shellcheck disable=SC2016 # intentionally unexpanded -- written verbatim into $_wrapped
        echo 'DRIVER_SKILLS_DIR="$HOME/${DRIVER_SKILLS_DIR#/home/agent/}"'
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

# Push main to a same-named remote branch, e.g. so a non-default BASE_BRANCH
# resolves to a real origin/${BASE_BRANCH} ref. phase_branch_recovery checks
# that ref out before the prompt is ever assembled (setup_bare_repo only
# seeds main), so any test setting BASE_BRANCH to something other than
# "main" needs this first. Call after setup_bare_repo.
# Usage: seed_release_branch "release-42" "seed-name"
seed_release_branch() {
  local branch="$1" seed_name="$2"
  local seed="$BATS_TEST_TMPDIR/$seed_name"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  git -C "$seed" push -q origin "main:$branch"
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
