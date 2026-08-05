#!/usr/bin/env bats
# Build-time/runtime parity check (issue #2320, parent #2244): drives the
# ACTUAL agent/entrypoint.sh runtime validator (_validate_prompt_contract)
# against the same 8 fixtures lib/prompt-contract.nix's `parityFixtures`
# already resolved via the real `buildTimeRejectVerdicts` function -- proof
# the runtime bash validator's exit code agrees with `parityFold(verdict)`
# for every (severity=="reject" row) x (gate) x (markerPresent) combination,
# not just that Nix's own pinning of the fold (nix/checks/prompt-contract-
# parity.nix, slice 1) is self-consistent.
#
# A single @test loops over all 8 fixture rows read from
# PROMPT_CONTRACT_PARITY_FIXTURE (a JSON file nix/checks/bats.nix renders
# from lib/prompt-contract.nix's parityFixtures) rather than one @test per
# row: bats has no built-in data-driven-@test-generation this repo already
# uses elsewhere (tests/entrypoint-prompt-validator.bats's own "data-driven"
# case patches a single field and asserts a single outcome, it doesn't loop
# over a fixture list), so a hand-rolled per-@test-per-row split would mean
# hardcoding the 8 (id, gate, markerPresent) combinations a second time in
# bash -- exactly the duplication this slice exists to avoid. The loop
# accumulates every failing fixture's id/gate/markerPresent/verdict before
# failing once at the end, so a broken row is still individually legible in
# --print-output-on-failure output instead of bats stopping at the first
# failure and hiding the rest.
#
# Deliberately does NOT re-derive parityFold in bash: each fixture's
# `verdict` field is already lib/prompt-contract.nix's own precomputed
# buildTimeRejectVerdicts result, so this test only reads the fold's
# *result* ("reject" -> must block, anything else -> must not block) off
# each row, never reimplementing the fold logic as a second copy that could
# silently drift from the Nix source of truth.

load helper

setup() {
  setup_entrypoint_env
  : "${PROMPT_CONTRACT_PARITY_FIXTURE:?PROMPT_CONTRACT_PARITY_FIXTURE must be set (JSON fixture file rendered from lib/prompt-contract.nix parityFixtures)}"
  # Route driver-exec/orchestrator's heartbeat write into this test's own
  # tmpdir instead of the shared /tmp/heartbeat.log default (issue #2320):
  # this suite invokes the real entrypoint.sh -> driver-exec path 8 times
  # per run, directly on the nix build host, where a concurrently-building
  # derivation running as a different sandbox user can already own that
  # shared path and turn every one of this suite's fixtures into a spurious
  # EACCES-driven "block".
  export HEARTBEAT_LOG="$BATS_TEST_TMPDIR/heartbeat.log"
}

# Same stub shape as tests/entrypoint-prompt-validator.bats's own
# _stub_prompt_dir (not `load`-shared since bats' `load helper` only loads
# one file per suite and this stub is a handful of lines) -- review-
# prompt.md carries a VERDICT: line by default so a fixture iteration that
# doesn't target the reviewer-verdict row never incidentally trips it.
_parity_stub_prompt_dir() {
  local dir="$1"
  mkdir -p "$dir"
  printf 'issue stub\n' >"$dir/issue-prompt.md"
  printf 'scout stub\n' >"$dir/scout-prompt.md"
  printf 'reviewer stub\n\nVERDICT: APPROVE or BLOCK\n' >"$dir/review-prompt.md"
}

@test "build-time/runtime parity: every fixture's exit code matches parityFold(verdict)" {
  local fixtures
  fixtures="$(jq -c '.[]' "$PROMPT_CONTRACT_PARITY_FIXTURE")"

  local failures=0
  local i=0
  local fixture id gate markerPresent verdict expect_block prompt_dir status_out

  while IFS= read -r fixture; do
    i=$((i + 1))
    id="$(jq -r '.id' <<<"$fixture")"
    gate="$(jq -r '.gate' <<<"$fixture")"
    markerPresent="$(jq -r '.markerPresent' <<<"$fixture")"
    verdict="$(jq -r '.verdict' <<<"$fixture")"

    if [ "$verdict" = reject ]; then
      expect_block=1
    else
      expect_block=0
    fi

    prompt_dir="$BATS_TEST_TMPDIR/prompts-$i"
    _parity_stub_prompt_dir "$prompt_dir"
    export PROMPTS_DIR="$prompt_dir"
    # WORK_DIR is fixed by setup_entrypoint_env; each entrypoint invocation
    # clones into it, so a stale clone from a prior iteration must be
    # cleared first or the second-and-later `git clone` fails outright.
    export WORK_DIR="$BATS_TEST_TMPDIR/work-$i"

    case "$id" in
      verdict-comment-relay)
        if [ "$markerPresent" = true ]; then
          printf 'research stub\n\nPost your verdict with SPINDRIFT_COMMENT here\n' >"$prompt_dir/research-prompt.md"
        else
          printf 'research stub, no verdict-comment marker here\n' >"$prompt_dir/research-prompt.md"
        fi
        if [ "$gate" = true ]; then
          export DISPATCH_KIND="research"
          unset BOX_WRITE_ENABLED
        else
          unset DISPATCH_KIND
          export BOX_WRITE_ENABLED=1
        fi
        ;;
      reviewer-verdict)
        if [ "$markerPresent" = true ]; then
          printf 'reviewer stub\n\nVERDICT: APPROVE or BLOCK\n' >"$prompt_dir/review-prompt.md"
        else
          printf 'reviewer stub, no verdict line here\n' >"$prompt_dir/review-prompt.md"
        fi
        if [ "$gate" = true ]; then
          export ORCHESTRATOR_ENABLED=1
        else
          unset ORCHESTRATOR_ENABLED
        fi
        ;;
      *)
        echo "unhandled fixture id '$id' -- extend this suite's dispatch to cover it" >&2
        return 1
        ;;
    esac

    run bash "$ENTRYPOINT"
    status_out="$status"

    if [ "$expect_block" -eq 1 ] && [ "$status_out" -eq 0 ]; then
      echo "FAIL fixture #$i: id=$id gate=$gate markerPresent=$markerPresent verdict=$verdict -- expected non-zero exit (block), got 0" >&2
      failures=$((failures + 1))
    elif [ "$expect_block" -eq 0 ] && [ "$status_out" -ne 0 ]; then
      echo "FAIL fixture #$i: id=$id gate=$gate markerPresent=$markerPresent verdict=$verdict -- expected exit 0 (no block), got $status_out" >&2
      failures=$((failures + 1))
    fi
  done <<<"$fixtures"

  [ "$failures" -eq 0 ]
}
