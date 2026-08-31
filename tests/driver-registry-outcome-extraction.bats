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
  local want='SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=fixture'
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

@test "every registered driver's _driver_extract_outcome/_driver_extract_near_miss_outcome behave identically across valid, markdown-wrapped, colon-delimited, and near-miss lines" {
  : "${DRIVER_OUTCOME_MANIFEST:?DRIVER_OUTCOME_MANIFEST must be set -- run via nix (checks-inbox), not a bare bats invocation}"
  local canonical='SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=ok'
  local near_miss_text='SPINDRIFT_OUTCOME: Complete -- nothing more to report'
  local driver preamble tmpfixture got near text

  # _build_event wraps a driver's raw final-message text in that driver's own
  # stream envelope (claude: a "result" event; opencode: a "text" part
  # event) -- this is per-Driver wire-format knowledge the unified extractor
  # (lib/drivers/outcome-extractor.nix) never sees, so branching on $driver
  # here isn't the copy-paste the Nix refactor (issue #2977 slice 2)
  # eliminated; it's test-fixture plumbing standing in for each Driver's own
  # CLI emitting its own event shape.
  _build_event() {
    local d="$1" t="$2"
    case "$d" in
      claude) jq -cn --arg t "$t" '{type: "result", result: $t}' ;;
      opencode) jq -cn --arg t "$t" '{type: "text", part: {text: $t}}' ;;
      *) echo "_build_event: unrecognized driver '$d' -- add its event envelope shape here" >&2; return 1 ;;
    esac
  }

  for driver in $(jq -r 'keys[]' "$DRIVER_OUTCOME_MANIFEST"); do
    preamble="$(jq -r --arg d "$driver" '.[$d].preamble' "$DRIVER_OUTCOME_MANIFEST")"

    # 1. valid: verbatim canonical line -> outcome returns it verbatim, near-miss empty.
    tmpfixture="$(mktemp)"
    _build_event "$driver" "$canonical" >"$tmpfixture"
    got="$(bash -c 'source "$1"; _driver_extract_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ "$got" = "$canonical" ] || { echo "driver=$driver valid: _driver_extract_outcome got [$got], want [$canonical]" >&2; rm -f "$tmpfixture"; return 1; }
    near="$(bash -c 'source "$1"; _driver_extract_near_miss_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ -z "$near" ] || { echo "driver=$driver valid: _driver_extract_near_miss_outcome unexpectedly non-empty: [$near]" >&2; rm -f "$tmpfixture"; return 1; }
    rm -f "$tmpfixture"

    # 2. markdown-wrapped valid: bold-wrapped canonical line -> outcome strips
    # the markdown and returns the unwrapped canonical line, near-miss empty.
    tmpfixture="$(mktemp)"
    text="**${canonical}**"
    _build_event "$driver" "$text" >"$tmpfixture"
    got="$(bash -c 'source "$1"; _driver_extract_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ "$got" = "$canonical" ] || { echo "driver=$driver markdown-wrapped: _driver_extract_outcome got [$got], want [$canonical]" >&2; rm -f "$tmpfixture"; return 1; }
    near="$(bash -c 'source "$1"; _driver_extract_near_miss_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ -z "$near" ] || { echo "driver=$driver markdown-wrapped: _driver_extract_near_miss_outcome unexpectedly non-empty: [$near]" >&2; rm -f "$tmpfixture"; return 1; }
    rm -f "$tmpfixture"

    # 3. colon-delimited valid: SPINDRIFT_OUTCOME: ... -> outcome normalizes
    # the colon to a space, returning the same canonical line as case 1; near-miss empty.
    tmpfixture="$(mktemp)"
    text="SPINDRIFT_OUTCOME: issue=7 landing=agent/issue-7 status=ready note=ok"
    _build_event "$driver" "$text" >"$tmpfixture"
    got="$(bash -c 'source "$1"; _driver_extract_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ "$got" = "$canonical" ] || { echo "driver=$driver colon-delimited: _driver_extract_outcome got [$got], want [$canonical]" >&2; rm -f "$tmpfixture"; return 1; }
    near="$(bash -c 'source "$1"; _driver_extract_near_miss_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ -z "$near" ] || { echo "driver=$driver colon-delimited: _driver_extract_near_miss_outcome unexpectedly non-empty: [$near]" >&2; rm -f "$tmpfixture"; return 1; }
    rm -f "$tmpfixture"

    # 4. near-miss: leading token present but no landing=/status= fields at
    # all -> outcome empty, near-miss returns the line VERBATIM (colon NOT
    # normalized -- deliberate, see outcome-extractor.nix's variant doc comment).
    tmpfixture="$(mktemp)"
    _build_event "$driver" "$near_miss_text" >"$tmpfixture"
    got="$(bash -c 'source "$1"; _driver_extract_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ -z "$got" ] || { echo "driver=$driver near-miss: _driver_extract_outcome unexpectedly non-empty: [$got]" >&2; rm -f "$tmpfixture"; return 1; }
    near="$(bash -c 'source "$1"; _driver_extract_near_miss_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ "$near" = "$near_miss_text" ] || { echo "driver=$driver near-miss: _driver_extract_near_miss_outcome got [$near], want [$near_miss_text]" >&2; rm -f "$tmpfixture"; return 1; }
    rm -f "$tmpfixture"
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
