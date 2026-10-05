# Shared bats helpers. Sourced by every *.bats file.
#
# The nix bats check derivations export the paths these helpers depend on:
# FAKES_DIR and SPINDRIFT_SEAM_FIXTURES_DIR (the rendered contracts and
# preambles).

# The fixtures dir holds the same bytes the Go seam tests read. Each per-file
# var only defaults from it, so an explicit export still wins.
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
