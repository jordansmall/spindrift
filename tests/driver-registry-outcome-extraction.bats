#!/usr/bin/env bats
# Runs each registered Driver's outcome-extraction shell bodies
# (lib/drivers/*.nix) against its own canonical fixture (issue #2261, extended
# for #2978); the Go half's outcome_fixture_test.go files read the same ones.
# DRIVER_OUTCOME_MANIFEST drives the loop, so a new driver needs no edit here.

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
    # _driver_extract_result_text does no filtering or classification itself
    # (issue #2978 moved that to the Go side), so its raw output differs per
    # driver. Only the last leading-token line is comparable.
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

  # Each driver's CLI emits its final message in its own stream envelope, so
  # the fixture builder must branch on $driver. This is not the copy-paste the
  # Nix refactor (issue #2977 slice 2) removed: the unified extractor in
  # lib/drivers/outcome-extractor.nix never sees a wire format.
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

    tmpfixture="$(mktemp)"
    _build_event "$driver" "$canonical" >"$tmpfixture"
    got="$(bash -c 'source "$1"; _driver_extract_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ "$got" = "$canonical" ] || { echo "driver=$driver valid: _driver_extract_outcome got [$got], want [$canonical]" >&2; rm -f "$tmpfixture"; return 1; }
    near="$(bash -c 'source "$1"; _driver_extract_near_miss_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ -z "$near" ] || { echo "driver=$driver valid: _driver_extract_near_miss_outcome unexpectedly non-empty: [$near]" >&2; rm -f "$tmpfixture"; return 1; }
    rm -f "$tmpfixture"

    tmpfixture="$(mktemp)"
    text="**${canonical}**"
    _build_event "$driver" "$text" >"$tmpfixture"
    got="$(bash -c 'source "$1"; _driver_extract_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ "$got" = "$canonical" ] || { echo "driver=$driver markdown-wrapped: _driver_extract_outcome got [$got], want [$canonical]" >&2; rm -f "$tmpfixture"; return 1; }
    near="$(bash -c 'source "$1"; _driver_extract_near_miss_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ -z "$near" ] || { echo "driver=$driver markdown-wrapped: _driver_extract_near_miss_outcome unexpectedly non-empty: [$near]" >&2; rm -f "$tmpfixture"; return 1; }
    rm -f "$tmpfixture"

    # The extractor normalizes a colon after the leading token to a space, so
    # this line must come back as the canonical one.
    tmpfixture="$(mktemp)"
    text="SPINDRIFT_OUTCOME: issue=7 landing=agent/issue-7 status=ready note=ok"
    _build_event "$driver" "$text" >"$tmpfixture"
    got="$(bash -c 'source "$1"; _driver_extract_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ "$got" = "$canonical" ] || { echo "driver=$driver colon-delimited: _driver_extract_outcome got [$got], want [$canonical]" >&2; rm -f "$tmpfixture"; return 1; }
    near="$(bash -c 'source "$1"; _driver_extract_near_miss_outcome "$2"' _ "$preamble" "$tmpfixture")"
    [ -z "$near" ] || { echo "driver=$driver colon-delimited: _driver_extract_near_miss_outcome unexpectedly non-empty: [$near]" >&2; rm -f "$tmpfixture"; return 1; }
    rm -f "$tmpfixture"

    # A near miss comes back verbatim, colon and all. Not normalizing here is
    # deliberate; see outcome-extractor.nix's variant doc comment.
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
  # resultTextExtractFnBody's pipeline (lib/drivers/*.nix) must end in
  # `|| true` like its outcomeExtract siblings. Otherwise a jq parse failure
  # on a malformed line aborts agent/entrypoint.sh's call site, which runs
  # under `set -euo pipefail`, before the SPINDRIFT_OUTCOME line is handled.
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
