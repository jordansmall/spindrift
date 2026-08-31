#!/usr/bin/env bats
# Executes every registered Driver's outcome-extraction shell bodies
# (lib/drivers/*.nix outcomeExtractFnBody/outcomeExtractNearMissFnBody/
# resultTextExtractFnBody) against its own canonical fixture transcript (issue
# #2261, extended for #2978) -- the cross-half anchor: the Go half's tests
# (cmd/launcher/internal/driver/*/outcome_fixture_test.go) consume the
# identical fixture files. Driven entirely by DRIVER_OUTCOME_MANIFEST
# (nix/checks/bats.nix, registry-driven via lib.mapAttrs over
# lib/drivers/default.nix's entries) -- a new Driver registry entry with its own
# testdata/outcome-fixture.jsonl is covered with no edits to this file.

@test "every registered driver's _driver_extract_outcome/_driver_extract_near_miss_outcome/_driver_extract_result_text parse its own canonical fixture" {
  : "${DRIVER_OUTCOME_MANIFEST:?DRIVER_OUTCOME_MANIFEST must be set -- run via nix (checks-inbox), not a bare bats invocation}"
  local want='SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=fixture nonce=deadbeef'
  local driver preamble fixture got near result_text got_result
  for driver in $(jq -r 'keys[]' "$DRIVER_OUTCOME_MANIFEST"); do
    preamble="$(jq -r --arg d "$driver" '.[$d].preamble' "$DRIVER_OUTCOME_MANIFEST")"
    fixture="$(jq -r --arg d "$driver" '.[$d].fixture' "$DRIVER_OUTCOME_MANIFEST")"
    got="$(bash -c 'source "$1"; _driver_extract_outcome "$2"' _ "$preamble" "$fixture")"
    [ "$got" = "$want" ] || { echo "driver=$driver: _driver_extract_outcome got [$got], want [$want]" >&2; return 1; }
    near="$(bash -c 'source "$1"; _driver_extract_near_miss_outcome "$2"' _ "$preamble" "$fixture")"
    [ -z "$near" ] || { echo "driver=$driver: _driver_extract_near_miss_outcome unexpectedly non-empty: [$near]" >&2; return 1; }
    # _driver_extract_result_text does no grep/classification/tail -1
    # filtering itself (issue #2978, the Go side's
    # outcome.LastFieldedOutcomeLine/outcome.LastNearMissOutcomeLine does
    # that) -- its raw multi-line output differs per driver (claude: 2
    # lines total; opencode: several concatenated text events), so only its
    # last leading-token line is checked against the canonical outcome line.
    result_text="$(bash -c 'source "$1"; _driver_extract_result_text "$2"' _ "$preamble" "$fixture")"
    got_result="$(grep -E '^SPINDRIFT_OUTCOME ' <<<"$result_text" | tail -1)"
    [ "$got_result" = "$want" ] || { echo "driver=$driver: _driver_extract_result_text's last leading-token line got [$got_result], want [$want]" >&2; return 1; }
  done
}

@test "every registered driver's _driver_extract_result_text tolerates a malformed non-JSON tail line" {
  : "${DRIVER_OUTCOME_MANIFEST:?DRIVER_OUTCOME_MANIFEST must be set -- run via nix (checks-inbox), not a bare bats invocation}"
  # Regression guard: resultTextExtractFnBody's pipeline (lib/drivers/*.nix)
  # must end in `|| true` like its outcomeExtractFnBody/
  # outcomeExtractNearMissFnBody siblings, so a jq parse failure on a
  # malformed line in the driver's raw stream log doesn't propagate through
  # the pipe under `set -eo pipefail` and abort the caller
  # (agent/entrypoint.sh's _driver_extract_result_text call site runs under
  # `set -euo pipefail`) before the SPINDRIFT_OUTCOME line is ever handled.
  local driver preamble fixture tmpfixture status
  for driver in $(jq -r 'keys[]' "$DRIVER_OUTCOME_MANIFEST"); do
    preamble="$(jq -r --arg d "$driver" '.[$d].preamble' "$DRIVER_OUTCOME_MANIFEST")"
    fixture="$(jq -r --arg d "$driver" '.[$d].fixture' "$DRIVER_OUTCOME_MANIFEST")"
    tmpfixture="$(mktemp)"
    cat "$fixture" >"$tmpfixture"
    echo 'not json at all' >>"$tmpfixture"
    status=0
    bash -c 'set -eo pipefail; source "$1"; _driver_extract_result_text "$2" >/dev/null' _ "$preamble" "$tmpfixture" || status=$?
    rm -f "$tmpfixture"
    [ "$status" -eq 0 ] || { echo "driver=$driver: _driver_extract_result_text aborted under set -eo pipefail with a malformed tail line (exit=$status) -- resultTextExtractFnBody is missing its trailing || true" >&2; return 1; }
  done
}
