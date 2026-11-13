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
  # No gh is staged on PATH: the launcher pins the real gh, so nix/fixtures.nix
  # overlays the recording fake in, and that fake reads GH_LOG from here.
  export GH_LOG="$BATS_TEST_TMPDIR/gh.log"
  : >"$PODMAN_LOG"
  : >"$GH_LOG"
  : >"$GH_LOG.token"
  set_dispatch_env
  cd "$BATS_TEST_TMPDIR" || exit
  export FAKE_GH_ISSUES=$'1\tFirst issue\n2\tSecond issue'
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
  # default. They pin read-write box access for the same reason: the fake
  # runtime returns no bundle for the read-only relay, and the read-only
  # default's host-side mirror seed would reach the network (issue #6899).
  # Bound the merge gate's poll loop (issue #2424): a test that
  # reaches it without its own values would inherit the production defaults
  # (3600s timeout, 30s interval) and really sleep, as CI did on PR #2410.
  # Interval 0 keeps iterations instant; a small nonzero timeout still lets it
  # iterate once.
  # shellcheck disable=SC2034 # read by the sourcing suites
  DISPATCH_FLAGS=(
    --repo-slug owner/repo
    --user-name "Test Bot"
    --user-email bot@example.com
    --box-signal-carrier log
    --box-access read-write
    --merge-poll-interval 0
    --merge-poll-timeout 2
  )
}
