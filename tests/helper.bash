# Shared bats helpers (seam-fixture vars, dispatch-env setup), loaded with
# `load helper` by the suites that use them.
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
  : "${CLAUDE_SETTINGS_FILE:=$SPINDRIFT_SEAM_FIXTURES_DIR/agent-files-claude/home/agent/.claude/settings.json}"
  export OUTCOME_CONTRACT_FILE COMMS_CONTRACT_FILE CHECK_CONTRACT_FILE \
    RESEARCH_OUTCOME_CONTRACT_FILE DRIVER_PREAMBLE_FILE \
    AGENT_PATHS_PREAMBLE_FILE CLAUDE_SETTINGS_FILE
  # The file names are duplicated from nix/seam-fixtures.nix; a rename there
  # must fail loudly here.
  for _f in "$OUTCOME_CONTRACT_FILE" "$COMMS_CONTRACT_FILE" \
    "$CHECK_CONTRACT_FILE" "$RESEARCH_OUTCOME_CONTRACT_FILE" \
    "$DRIVER_PREAMBLE_FILE" "$AGENT_PATHS_PREAMBLE_FILE" \
    "$CLAUDE_SETTINGS_FILE"; do
    [ -r "$_f" ] || { echo "helper.bash: seam fixture not found: $_f" >&2; exit 1; }
  done
  unset _f
fi

# Shared setup for the dispatch-env bats suite (tests/harness-env.bats).
setup_dispatch_env() {
  : "${FAKES_DIR:?FAKES_DIR must be set (dir holding fake runtime)}"
  FAKE_BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$FAKE_BIN"
  cp "$FAKES_DIR/runtime" "$FAKE_BIN/podman"
  chmod +x "$FAKE_BIN/podman"
  export PATH="$FAKE_BIN:$PATH"

  export PODMAN_LOG="$BATS_TEST_TMPDIR/podman.log"
  : >"$PODMAN_LOG"
  : >"$PODMAN_LOG.token"
  set_dispatch_env
  cd "$BATS_TEST_TMPDIR" || exit
  # A github read needs a Connector row the bats harness does not apply, so
  # dispatch reads a local issue and lands through the git forge instead. The
  # host seeds the Box's mirror from that remote (issue #6898), so it needs a
  # main branch to clone.
  git init -q -b main remote-work
  git -C remote-work -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m seed
  git clone -q --bare remote-work remote.git
  mkdir -p .spindrift/issues
  printf -- '---\ntitle: First issue\nstate: ready-for-agent\n---\n' >.spindrift/issues/1.md
}

# Secrets only in the environment so `dispatch`'s required-var guards pass; the
# launcher exits 6 on a non-secret knob there (ADR 0020), so DISPATCH_FLAGS
# carries the rest as flags.
set_dispatch_env() {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export GH_TOKEN="fake-token"
  export CLAUDE_CODE_OAUTH_TOKEN="fake-oauth"
  # The fake runtime cannot answer the socket carrier's transport probe, so
  # launcher-level suites pin the log carrier rather than take the schema
  # default.
  # Bound the merge gate's poll loop (issue #2424): a test that
  # reaches it without its own values would inherit the production defaults
  # (3600s timeout, 30s interval) and really sleep, as CI did on PR #2410.
  # Interval 0 keeps iterations instant; a small nonzero timeout still lets it
  # iterate once.
  # shellcheck disable=SC2034 # read by the sourcing suites
  DISPATCH_FLAGS=(
    --repo-slug owner/repo
    --tracker local
    --forge-backend git
    --remote-url "$BATS_TEST_TMPDIR/remote.git"
    --user-name "Test Bot"
    --user-email bot@example.com
    --box-signal-carrier log
    --merge-poll-interval 0
    --merge-poll-timeout 2
  )
}
