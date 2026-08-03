#!/usr/bin/env bats
# Executes every registered Driver's outcome-extraction shell bodies
# (lib/drivers/*.nix outcomeExtractFnBody/outcomeExtractNearMissFnBody) against
# its own canonical fixture transcript (issue #2261) -- the cross-half anchor:
# the Go half's tests (cmd/launcher/internal/driver/*/outcome_fixture_test.go)
# consume the identical fixture files. Driven entirely by DRIVER_OUTCOME_MANIFEST
# (nix/checks/bats.nix, registry-driven via lib.mapAttrs over
# lib/drivers/default.nix's entries) -- a new Driver registry entry with its own
# testdata/outcome-fixture.jsonl is covered with no edits to this file.

@test "every registered driver's _driver_extract_outcome/_driver_extract_near_miss_outcome parse its own canonical fixture" {
  : "${DRIVER_OUTCOME_MANIFEST:?DRIVER_OUTCOME_MANIFEST must be set -- run via nix (checks-inbox), not a bare bats invocation}"
  local want='SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=fixture nonce=deadbeef'
  local driver preamble fixture got near
  for driver in $(jq -r 'keys[]' "$DRIVER_OUTCOME_MANIFEST"); do
    preamble="$(jq -r --arg d "$driver" '.[$d].preamble' "$DRIVER_OUTCOME_MANIFEST")"
    fixture="$(jq -r --arg d "$driver" '.[$d].fixture' "$DRIVER_OUTCOME_MANIFEST")"
    got="$(bash -c 'source "$1"; _driver_extract_outcome "$2"' _ "$preamble" "$fixture")"
    [ "$got" = "$want" ] || { echo "driver=$driver: _driver_extract_outcome got [$got], want [$want]" >&2; return 1; }
    near="$(bash -c 'source "$1"; _driver_extract_near_miss_outcome "$2"' _ "$preamble" "$fixture")"
    [ -z "$near" ] || { echo "driver=$driver: _driver_extract_near_miss_outcome unexpectedly non-empty: [$near]" >&2; return 1; }
  done
}
