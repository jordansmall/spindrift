#!/usr/bin/env bats
# Build-time/runtime parity check (issue #2320, parent #2244): drives the ACTUAL
# runtime validator (phase_prompt_assembly, which forwards to the Go
# `driver-exec assemble-prompt` verb's promptassembly.Validate) against the same
# 20 fixtures lib/prompt-contract.nix's `parityFixtures` already resolved -- for
# severity=="reject" rows via the real `buildTimeRejectVerdicts`, for
# severity=="warn" rows by construction, since a warn row never blocks. The
# point is that the runtime validator's exit code agrees with
# `parityFold(verdict)` for every (row x gate x markerPresent) combination, not
# merely that Nix's own pinning of the fold is self-consistent.
#
# A single @test loops over all 20 fixture rows read from
# PROMPT_CONTRACT_PARITY_FIXTURE (rendered by nix/checks/bats.nix from
# parityFixtures) rather than one @test per row: bats has no data-driven @test
# generation here, so a per-row split would mean hardcoding the 20 combinations
# a second time in bash -- exactly the duplication this suite exists to avoid.
# The loop accumulates every failing fixture before failing once at the end, so
# a broken row stays individually legible instead of bats stopping at the first.
#
# Deliberately does NOT re-derive parityFold in bash: each fixture's `verdict`
# is already lib/prompt-contract.nix's precomputed result, so this only reads
# the fold's *result* ("reject" -> must block, anything else -> must not),
# never reimplementing the fold as a second copy that could drift.

load helper

setup() {
  setup_entrypoint_env
  : "${PROMPT_CONTRACT_PARITY_FIXTURE:?PROMPT_CONTRACT_PARITY_FIXTURE must be set (JSON fixture file rendered from lib/prompt-contract.nix parityFixtures)}"
  # Route driver-exec/orchestrator's heartbeat write into this test's own tmpdir
  # instead of the shared /tmp/heartbeat.log default: this suite invokes the real
  # entrypoint -> driver-exec path 8 times per run directly on the nix build
  # host, where a concurrently-building derivation running as a different sandbox
  # user can own that shared path and turn every fixture into a spurious EACCES
  # "block".
  export HEARTBEAT_LOG="$BATS_TEST_TMPDIR/heartbeat.log"
}

# Same stub shape as tests/entrypoint-prompt-validator.bats's own
# _stub_prompt_dir (not `load`-shared, since bats loads only one helper file per
# suite): review-prompt.md carries a VERDICT: line by default so a fixture
# iteration that doesn't target the reviewer-verdict row never incidentally
# trips it, and worker-prompt.md needs a stub too or Assemble hard-fails on
# every fixture this path exercises.
_parity_stub_prompt_dir() {
  local dir="$1"
  mkdir -p "$dir"
  printf 'issue stub\n' >"$dir/issue-prompt.md"
  printf 'scout stub\n' >"$dir/scout-prompt.md"
  printf 'reviewer stub\n\nVERDICT: APPROVE or BLOCK\n' >"$dir/review-prompt.md"
  printf 'worker stub\n' >"$dir/worker-prompt.md"
}

@test "build-time/runtime parity: every fixture's exit code matches parityFold(verdict)" {
  local fixtures
  fixtures="$(jq -c '.[]' "$PROMPT_CONTRACT_PARITY_FIXTURE")"

  local failures=0
  local i=0
  local fixture id gate markerPresent verdict expect_block prompt_dir status_out comment_marker

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
    # WORK_DIR is fixed by setup_entrypoint_env and each entrypoint invocation
    # clones into it, so a stale clone from a prior iteration must be cleared or
    # the second-and-later `git clone` fails outright.
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
          export BOX_REVIEW_LOOP_ORCHESTRATOR=1
          unset BOX_REVIEW_LOOP_INLINE
        else
          unset ORCHESTRATOR_ENABLED
          unset BOX_REVIEW_LOOP_ORCHESTRATOR
          export BOX_REVIEW_LOOP_INLINE=1
        fi
        ;;
      pr-intent)
        if [ "$markerPresent" = true ]; then
          printf 'issue stub with SPINDRIFT_PR_INTENT here\n' >"$prompt_dir/issue-prompt.md"
        else
          printf 'issue stub, no PR-intent marker here\n' >"$prompt_dir/issue-prompt.md"
        fi
        if [ "$gate" = true ]; then
          unset BOX_WRITE_ENABLED
        else
          export BOX_WRITE_ENABLED=1
        fi
        ;;
      issue-intent)
        if [ "$markerPresent" = true ]; then
          printf 'filer stub with SPINDRIFT_ISSUE_INTENT here\n' >"$prompt_dir/filer-prompt.md"
        else
          printf 'filer stub, no issue-intent marker here\n' >"$prompt_dir/filer-prompt.md"
        fi
        if [ "$gate" = true ]; then
          export AGENTS_JSON_TEMPLATE='{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
          export BOX_FILER_ENABLED=1
          export ORCHESTRATOR_ENABLED=1
          export BOX_REVIEW_LOOP_ORCHESTRATOR=1
          unset BOX_REVIEW_LOOP_INLINE
          unset BOX_WRITE_ENABLED
        else
          # FILER_FILE_RELAY requires BOX_FILER_ENABLED AND !BOX_WRITE_ENABLED
          # AND BOX_REVIEW_LOOP_ORCHESTRATOR all at once -- toggle only
          # BOX_WRITE_ENABLED here (matching this suite's one-knob-per-row style)
          # so the gate goes false while .filer.prompt stays populated and
          # markerPresent is still meaningfully exercised.
          export AGENTS_JSON_TEMPLATE='{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
          export BOX_FILER_ENABLED=1
          export ORCHESTRATOR_ENABLED=1
          export BOX_REVIEW_LOOP_ORCHESTRATOR=1
          unset BOX_REVIEW_LOOP_INLINE
          export BOX_WRITE_ENABLED=1
        fi
        ;;
      research-issue-intent)
        # FILER_FILE_RELAY's research special case (ADR 0041) fires whenever
        # DISPATCH_KIND=research and BOX_FILER_ENABLED, regardless of
        # BOX_WRITE_ENABLED -- and that same condition also activates the
        # unrelated verdict-comment-relay reject row, which scans this same
        # research-prompt.md for SPINDRIFT_COMMENT. Append it whenever gate=true
        # so that row never spuriously fires against this one's scenario; when
        # gate=false, DISPATCH_KIND is unset below so kind reverts to "work" and
        # that row is never active anyway.
        comment_marker=""
        if [ "$gate" = true ]; then
          comment_marker=$'\n\nPost your verdict with SPINDRIFT_COMMENT here'
        fi
        if [ "$markerPresent" = true ]; then
          printf 'research stub with SPINDRIFT_ISSUE_INTENT here%s\n' "$comment_marker" >"$prompt_dir/research-prompt.md"
        else
          printf 'research stub, no issue-intent marker here%s\n' "$comment_marker" >"$prompt_dir/research-prompt.md"
        fi
        export BOX_WRITE_ENABLED=1
        if [ "$gate" = true ]; then
          export DISPATCH_KIND="research"
          export BOX_FILER_ENABLED=1
        else
          unset DISPATCH_KIND
          unset BOX_FILER_ENABLED
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
