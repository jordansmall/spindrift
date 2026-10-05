# Shared bats helpers. Sourced by every *.bats file.
#
# The nix bats check derivations export the paths these helpers depend on:
# FAKES_DIR, SPINDRIFT_CMD, ENTRYPOINT, PROMPTS_DIR, and
# SPINDRIFT_SEAM_FIXTURES_DIR (the rendered contracts and preambles).

# The fixtures dir holds the same bytes the Go seam tests read. Each per-file
# var only defaults from it, so an explicit export still wins, e.g. a
# non-claude Driver's own DRIVER_PREAMBLE_FILE.
if [ -n "${SPINDRIFT_SEAM_FIXTURES_DIR:-}" ]; then
  : "${OUTCOME_CONTRACT_FILE:=$SPINDRIFT_SEAM_FIXTURES_DIR/outcome-contract.md}"
  : "${COMMS_CONTRACT_FILE:=$SPINDRIFT_SEAM_FIXTURES_DIR/comms-contract.md}"
  : "${CHECK_CONTRACT_FILE:=$SPINDRIFT_SEAM_FIXTURES_DIR/check-contract.md}"
  : "${RESEARCH_OUTCOME_CONTRACT_FILE:=$SPINDRIFT_SEAM_FIXTURES_DIR/research-outcome-contract.md}"
  : "${DRIVER_PREAMBLE_FILE:=$SPINDRIFT_SEAM_FIXTURES_DIR/driver-preamble.sh}"
  : "${AGENT_PATHS_PREAMBLE_FILE:=$SPINDRIFT_SEAM_FIXTURES_DIR/agent-paths-preamble.sh}"
  export OUTCOME_CONTRACT_FILE COMMS_CONTRACT_FILE CHECK_CONTRACT_FILE \
    RESEARCH_OUTCOME_CONTRACT_FILE DRIVER_PREAMBLE_FILE \
    AGENT_PATHS_PREAMBLE_FILE
  # The file names are duplicated from nix/seam-fixtures.nix; a rename there
  # must fail loudly here.
  for _f in "$OUTCOME_CONTRACT_FILE" "$COMMS_CONTRACT_FILE" \
    "$CHECK_CONTRACT_FILE" "$RESEARCH_OUTCOME_CONTRACT_FILE" \
    "$DRIVER_PREAMBLE_FILE" "$AGENT_PATHS_PREAMBLE_FILE"; do
    [ -r "$_f" ] || { echo "helper.bash: seam fixture not found: $_f" >&2; exit 1; }
  done
  unset _f
fi

# Shared setup for the dispatch-env bats suite (tests/harness-env.bats).
setup_dispatch_env() {
  setup_fakes
  set_dispatch_env
  cd "$BATS_TEST_TMPDIR" || exit
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
  cp "$FAKES_DIR/gh" "$FAKES_DIR/$DRIVER" \
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
  export ORCHESTRATOR_LOG="$BATS_TEST_TMPDIR/orchestrator.log"
  export DRIVER_PROMPT_FILE="$BATS_TEST_TMPDIR/$DRIVER-prompt.txt"
  : >"$PODMAN_LOG"
  : >"$DOCKER_LOG"
  : >"$BWRAP_LOG"
  : >"$PASTA_LOG"
  : >"$GH_LOG"
  : >"$DRIVER_LOG"
  : >"$ORCHESTRATOR_LOG"

  # Defaulted from SPINDRIFT_SEAM_FIXTURES_DIR at the top of this file
  # (box reads it, issue #420); a bare bats run has none, so fall
  # back to a fixture.
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

  # Prepend whichever of the Driver preamble (issues #624, #433) and the baked
  # /agent/* path defaults (issue #2531) are set, each independently guarded,
  # in lib/image.nix's own concatenation order so the suite sees the bytes the
  # image bakes. Outside nix none are set, the entrypoint stays unwrapped, and
  # tests fail by design.
  if [ -n "${DRIVER_PREAMBLE_FILE:-}" ] || [ -n "${AGENT_PATHS_PREAMBLE_FILE:-}" ]; then
    local _wrapped="$BATS_TEST_TMPDIR/entrypoint.sh"
    {
      if [ -n "${DRIVER_PREAMBLE_FILE:-}" ]; then
        cat "$DRIVER_PREAMBLE_FILE"
        # Re-root the baked absolute /home/agent skills dir under the shard's $HOME
        # (batsBuilderSetup's $TMPDIR/home), which a bats sandbox can write to
        # (issue #624). Stripping the prefix reuses the suffix the registry
        # rendered. Written unexpanded so it resolves against $HOME at $_wrapped's
        # runtime, not the one in effect here.
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
      tail -n +2 "$ENTRYPOINT"
    } >"$_wrapped"
    chmod +x "$_wrapped"
    ENTRYPOINT="$_wrapped"
    export ENTRYPOINT
  fi
}

# Minimal env so `dispatch`'s required-var guards pass.
set_dispatch_env() {
  export REPO_SLUG="owner/repo"
  export GH_TOKEN="fake-token"
  export CLAUDE_CODE_OAUTH_TOKEN="fake-oauth"
  export GIT_USER_NAME="Test Bot"
  export GIT_USER_EMAIL="bot@example.com"
  # The fake runtime cannot answer the socket carrier's transport probe, so
  # launcher-level suites pin the log carrier rather than take the schema
  # default.
  export BOX_SIGNAL_CARRIER=log
}
