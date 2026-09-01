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

# Polls "$file" for a line count matching "$pattern" (grep -c) rather than
# sampling it once (issue #2450). Defensive belt-and-suspenders, not a proven
# fix: a later investigation (commit 9e724bab) found dispatchWave's wg.Wait()
# already blocks until every dispatch subprocess exits and flushes, and a stress
# test found zero lost or interleaved log lines, so #2450's original flake was
# never root-caused.
#
# Bounded by a timeout so a genuine under-count still fails rather than hanging:
# default 2s, widened by WAIT_FOR_LOG_LINES_TIMEOUT, or by a non-empty explicit
# 4th arg which wins over both. That env var only reaches this process when the
# caller's shell runs bats directly -- Nix's build sandbox scrubs the
# environment -- so nix/checks/bats.nix bakes its own wider 10s default into the
# derivation instead. The value must be a positive integer of at most 6 digits:
# it flows into a `timeout * 20` arithmetic context, where anything else risks a
# syntax error, an overflow that wraps the poll count negative and silently
# skips the loop, or code execution. Zero is rejected too (#2759): it collapses
# the retry loop to a single check, defeating the point of a poll helper.
#
# Reaching expected_count mid-poll is not proof it has settled -- it may be
# passing through on its way to a higher, wrong count -- so success requires a
# few extra confirm polls (~150ms, independent of the main timeout), and any
# count above expected_count fails immediately.
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
  # Each assertion returns 1 explicitly rather than relying on `set -e`: the
  # caller (run-batch-limits.bats' malformed-timeout loop) suspends errexit for
  # this call, so a bare failing statement would just fall through.
  if [ "$status" -ne 1 ]; then
    echo "assert_timeout_rejected: expected status 1, got $status" >&2
    return 1
  fi
  if [[ "$output" != *"timeout must be a positive integer"* ]]; then
    echo "assert_timeout_rejected: output missing expected substring [timeout must be a positive integer]: $output" >&2
    return 1
  fi
  if [ -n "$absent_substring" ] && [[ "$output" == *"$absent_substring"* ]]; then
    echo "assert_timeout_rejected: output unexpectedly contains [$absent_substring]: $output" >&2
    return 1
  fi
}

# Extracts the --handoff-file path agent/entrypoint.sh's run_driver_in_env
# passed to the invoker, read from a verbatim-argv log ($1, e.g.
# $ORCHESTRATOR_LOG, whose fake echoes `$@`). Since issue #2975 every
# driver/model/effort/argv-shape/review/caps fact lives inside that handoff JSON
# rather than on the argv line, so a test asserting one of those facts extracts
# this path and `jq`s it. head -1 deliberately picks main's implement pass,
# which always hands over the shared, full-fidelity $_handoff_file; a later
# corrective resume passes a throwaway copy with ReviewPromptFile cleared, and
# head -1 skips that copy.
handoff_path_from_log() {
  grep -oE -- '--handoff-file [^ ]+' "$1" | head -1 | awk '{print $2}'
}

# Kills any backgrounded stand-in socat process a suite started (via its own
# _test_socat_pid), so a leaked process never survives past the test. Call from
# each suite's own teardown() -- bats requires that hook defined per file.
kill_stand_in_socat() {
  [ -n "${_test_socat_pid:-}" ] && kill "$_test_socat_pid" 2>/dev/null
  true
}

# Bounded poll for a stand-in socat's UNIX-LISTEN socket file to actually exist
# -- a freshly backgrounded socat may take a moment to bind.
wait_for_socket() {
  local _path="$1" _tries=0
  while [ "$_tries" -lt 50 ]; do
    [ -S "$_path" ] && return 0
    sleep 0.1
    _tries=$((_tries + 1))
  done
  return 1
}

# Shared setup for the split entrypoint-*.bats suites (issue #518): bats
# requires a setup() hook per file, so the shared body lives here.
setup_entrypoint_env() {
  setup_fakes
  setup_bare_repo
  set_box_env
  # BOX_WRITE_ENABLED and BOX_OUTBOX_RELAY_CAPABLE are not schema knobs (issue
  # #1951): dispatch.buildBoxEnv computes them host-side and forwards them, so
  # box_env_gen.bash's codegen never exports them. Set here to mirror what a
  # real Box receives at this suite's defaults (BOX_FORGE_AND_ISSUE_ACCESS=
  # read-write, CODE_FORGE=github); read-only tests unset BOX_WRITE_ENABLED
  # rather than overriding BOX_FORGE_AND_ISSUE_ACCESS.
  export BOX_WRITE_ENABLED=1
  export BOX_OUTBOX_RELAY_CAPABLE=1
  # BOX_TRACKER_AXIS_*, BOX_FORGE_BACKEND, and BOX_REVIEW_LOOP_INLINE are
  # host-derived the same way, from ISSUE_TRACKER/CODE_FORGE/
  # ORCHESTRATOR_ENABLED (issue #2533). A test that overrides one of those raw
  # vars must override the matching BOX_* var alongside it, the way a real
  # dispatch keeps them in sync.
  export BOX_TRACKER_AXIS_READ=GITHUB
  export BOX_TRACKER_AXIS_WRITE=GITHUB
  export BOX_TRACKER_AXIS_FILER=GH
  export BOX_FORGE_BACKEND=GH
  export BOX_REVIEW_LOOP_INLINE=1
  # Pinned to a value distinct from the schema default (issue #2055; the
  # schema default has since moved on to claude-sonnet-5) so the MODEL-flag
  # assertions below stay stable regardless of what the schema defaults to.
  export MODEL="claude-test-model"
  # Nix-baked from the roster (lib/mkHarness.nix): maps each --agents JSON entry
  # name to its prompt file under PROMPTS_DIR. Individual tests override this to
  # exercise a custom Nth agent or the "<name>-prompt.md" fallback.
  export AGENTS_PROMPT_FILES='{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md"}'
  export ISSUE_NUMBER="7"
  export ISSUE_TITLE="Do the thing"
  export WORK_DIR="$BATS_TEST_TMPDIR/work"
  # RUN_NONCE is host-supplied like the BOX_* vars above. fakes/claude's default
  # SPINDRIFT_PR_INTENT emission and entrypoint.sh's own PR-intent marker gate
  # both key off this run's nonce, so leaving it unset would make every
  # read-only+github+status=ready fixture look like a genuine #2036 repro and
  # eat an unwanted resume pass.
  export RUN_NONCE="test-run-nonce-0001"
}

stub_nix_var_snapshot() {
  # Stand in for `launcher build`'s VACUUMed host nix store DB snapshot (ADR
  # 0042, bwrap.go snapshotStoreDB): bwrapAdapter.IsReady only checks that this
  # file exists and isn't a directory, so a bare stub satisfies any
  # $BWRAP_RUN_CMD-family fixture. Must run after cd'ing into the test's own
  # $BATS_TEST_TMPDIR, since the launcher resolves the snapshot dir relative to
  # its own working directory.
  #
  # The snapshot dir is generation-scoped, nested under a subdir named for the
  # agent-closure store path (bwrap.go closureGeneration). $BWRAP_RUN_CMD and
  # $SKILLS_BWRAP_RUN_CMD are baked from separate mkHarness invocations, so each
  # closes over a different closure path and needs its own generation subdir --
  # one stub can't satisfy both.
  local tag generation
  for tag in "$BWRAP_IMAGE_TAG" "$SKILLS_BWRAP_IMAGE_TAG"; do
    [ -n "$tag" ] || continue
    generation=$(basename "$tag")
    mkdir -p ".spindrift/nix-var-snapshot/$generation/nix/db"
    : >".spindrift/nix-var-snapshot/$generation/nix/db/db.sqlite"
  done
}

# Overwrite $FAKE_BIN/driver-exec with a wrapper that fails only the
# bind-registry verb, delegating every other verb to the real fake. Must run
# after setup_fakes so $FAKE_BIN/driver-exec already exists to be overwritten.
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

setup_run_env() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR" || exit
  stub_nix_var_snapshot
  export FAKE_GH_ISSUES=$'1\tFirst issue\n2\tSecond issue'
  # Guard (issue #2424): bound the merge gate's poll loop by default, so a test
  # that reaches it without setting its own MERGE_POLL_INTERVAL/TIMEOUT can't
  # inherit the production defaults (3600s/180s) and real-sleep for up to an
  # hour before failing; a 30-minute version of this happened in CI on PR #2410.
  # Interval 0 keeps iterations instant; a small nonzero timeout still lets a
  # poll loop iterate at least once before its deadline. Tests that need a
  # different bound override both explicitly.
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
  # checkBwrapPastaGate (issue #2666) probes the launcher's own PATH for pasta
  # before bwrap ever runs, and bwrap.go's execTarget makes this fake the
  # top-level exec target by default, so it must exec through to the fake bwrap.
  cp "$FAKES_DIR/pasta" "$FAKE_BIN/pasta"
  : "${DRIVER:=claude}"
  cp "$FAKES_DIR/gh" "$FAKES_DIR/$DRIVER" "$FAKES_DIR/nix" \
     "$FAKES_DIR/driver-exec" "$FAKES_DIR/orchestrator" "$FAKE_BIN/"
  # tests/fakes/claude and tests/fakes/opencode both source
  # tests/fakes/_driver-common.bash relative to their own directory at runtime,
  # so copy it alongside the driver fake for that to resolve.
  cp "$FAKES_DIR/_driver-common.bash" "$FAKE_BIN/"
  chmod +x "$FAKE_BIN"/*
  export PATH="$FAKE_BIN:$PATH"

  export PODMAN_LOG="$BATS_TEST_TMPDIR/podman.log"
  export DOCKER_LOG="$BATS_TEST_TMPDIR/docker.log"
  export BWRAP_LOG="$BATS_TEST_TMPDIR/bwrap.log"
  # tests/fakes/pasta (issue #2666) execs through to the fake bwrap after
  # logging its own invocation here -- independent of $BWRAP_LOG, which only
  # ever sees what pasta's trailing argv hands to bwrap.
  export PASTA_LOG="$BATS_TEST_TMPDIR/pasta.log"
  export GH_LOG="$BATS_TEST_TMPDIR/gh.log"
  export GIT_LOG="$BATS_TEST_TMPDIR/git.log"
  export DRIVER_LOG="$BATS_TEST_TMPDIR/$DRIVER.log"
  export NIX_LOG="$BATS_TEST_TMPDIR/nix.log"
  export ORCHESTRATOR_LOG="$BATS_TEST_TMPDIR/orchestrator.log"
  export DRIVER_PROMPT_FILE="$BATS_TEST_TMPDIR/$DRIVER-prompt.txt"
  export DRIVER_AGENTS_FILE="$BATS_TEST_TMPDIR/$DRIVER-agents.json"
  # Test-only hook: entrypoint.sh's phase_prompt_assembly copies the raw Handoff
  # JSON here right before it `rm -f`s its own tempfile, mirroring
  # DRIVER_PROMPT_FILE/DRIVER_AGENTS_FILE above.
  export DRIVER_HANDOFF_FILE="$BATS_TEST_TMPDIR/$DRIVER-handoff.json"
  : >"$PODMAN_LOG"
  : >"$DOCKER_LOG"
  : >"$BWRAP_LOG"
  : >"$PASTA_LOG"
  : >"$GH_LOG"
  : >"$DRIVER_LOG"
  : >"$NIX_LOG"
  : >"$ORCHESTRATOR_LOG"

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

  # Same fallback, for the CODE COMMENTS block (issue #2880).
  : "${CODE_COMMENTS_CONTRACT_FILE:=$BATS_TEST_TMPDIR/code-comments-contract.md}"
  export CODE_COMMENTS_CONTRACT_FILE
  if [ ! -s "$CODE_COMMENTS_CONTRACT_FILE" ]; then
    printf '# CODE COMMENTS\n\ncanonical code comments contract fixture\n' >"$CODE_COMMENTS_CONTRACT_FILE"
  fi

  # Same fallback, for the research dispatch kind's outcome contract.
  : "${RESEARCH_OUTCOME_CONTRACT_FILE:=$BATS_TEST_TMPDIR/research-outcome-contract.md}"
  export RESEARCH_OUTCOME_CONTRACT_FILE
  if [ ! -s "$RESEARCH_OUTCOME_CONTRACT_FILE" ]; then
    printf '# POST THE VERDICT\n\ncanonical research outcome contract fixture\n' >"$RESEARCH_OUTCOME_CONTRACT_FILE"
  fi

  # The pre-wrap entrypoint path, preserved before ENTRYPOINT is reassigned
  # below, so a test needing its own custom-wrapped variant can still build one
  # from the real source.
  export ENTRYPOINT_SRC="$ENTRYPOINT"

  # DRIVER_PREAMBLE_FILE (the Driver preamble), AGENT_PATHS_PREAMBLE_FILE (the
  # baked /agent/* path defaults), and FRAGMENT_REGISTRY_FILE (the Conditional
  # fragment loop input and substitution allowlist) are prepended to the
  # entrypoint in the same order lib/image.nix concatenates them into the real
  # image, so the suite exercises the same bytes the image bakes in. Each has
  # its own guard, so a suite that sets only one still gets that subset
  # prepended. The nix check derivation sets these; a bare bats run outside nix
  # leaves ENTRYPOINT as-is (tests then fail, by design: use nix flake check).
  if [ -n "${DRIVER_PREAMBLE_FILE:-}" ] || [ -n "${AGENT_PATHS_PREAMBLE_FILE:-}" ] \
    || [ -n "${FRAGMENT_REGISTRY_FILE:-}" ]; then
    local _wrapped="$BATS_TEST_TMPDIR/entrypoint.sh"
    {
      if [ -n "${DRIVER_PREAMBLE_FILE:-}" ]; then
        cat "$DRIVER_PREAMBLE_FILE"
        # Test-only override, appended after the registry-rendered preamble
        # rather than folded into it: the baked DRIVER_SKILLS_DIR is the
        # absolute /home/agent path a real Box has, but a bats sandbox has no
        # such directory to write into -- so re-root the same relative suffix
        # under $HOME. Written as literal unexpanded text so it resolves against
        # whatever HOME setup_bare_repo sets, not whatever HOME is while this
        # file is assembled.
        # shellcheck disable=SC2016 # intentionally unexpanded -- written verbatim into $_wrapped
        echo 'DRIVER_SKILLS_DIR="$HOME/${DRIVER_SKILLS_DIR#/home/agent/}"'
        # Same re-rooting for DRIVER_SESSION_CACHE_DIR, which the preamble sets
        # only when the selected Driver declares sessionCacheDirRelative (claude;
        # unset for opencode) -- so this rewrite is itself conditional, evaluated
        # at $_wrapped's runtime, leaving it correctly unset rather than
        # empty-but-set for a driver that never sets it.
        # shellcheck disable=SC2016 # intentionally unexpanded -- written verbatim into $_wrapped
        echo 'if [ -n "${DRIVER_SESSION_CACHE_DIR:-}" ]; then DRIVER_SESSION_CACHE_DIR="$HOME/${DRIVER_SESSION_CACHE_DIR#/home/agent/}"; fi'
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

# set_box_env: every lib/env-schema.nix knob with boxEnv = true, at its schema
# default, so the entrypoint-*.bats suites exercise the same defaults the nix
# preamble bakes into the image. Generated by lib/renderers.nix
# renderSetBoxEnvFixture; nix/checks/schema-drift.nix guards against drift.
# A deliberate divergence from a schema default is stated at its override site,
# not buried here.
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
# resolves to a real origin/${BASE_BRANCH} ref that phase_branch_recovery can
# check out (setup_bare_repo only seeds main). Call after setup_bare_repo.
# Usage: seed_release_branch "release-42" "seed-name"
seed_release_branch() {
  local branch="$1" seed_name="$2"
  local seed="$BATS_TEST_TMPDIR/$seed_name"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  git -C "$seed" push -q origin "main:$branch"
}

# Push a named dependency-manifest file (a lockfile, or a Gradle
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
