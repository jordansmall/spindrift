#!/usr/bin/env bats

# Build-time/runtime parity check (issue #2320, parent #2244; widened to warn
# rows by issue #2356): drives the real runtime validator (entrypoint.sh's
# phase_prompt_assembly, now the Go `driver-exec assemble-prompt` verb) against
# the same fixtures lib/prompt-contract.nix's parityFixtures already resolved,
# so each row's runtime exit code is checked, not just Nix's pinning of the fold.

# One @test loops over every row: bats cannot generate tests from data, and a
# per-row split would hardcode the (id, gate, markerPresent) combinations a
# second time in bash. The loop records every failing row and fails once at the
# end so one broken row does not hide the others. Each row's `verdict` is Nix's
# precomputed fold result, so bash never holds a second copy of the fold logic.

load helper

setup() {
  setup_entrypoint_env
  : "${PROMPT_CONTRACT_PARITY_FIXTURE:?PROMPT_CONTRACT_PARITY_FIXTURE must be set (JSON fixture file rendered from lib/prompt-contract.nix parityFixtures)}"
  # Keep the heartbeat write out of the shared /tmp/heartbeat.log default
  # (issue #2320): on the nix build host a concurrently building derivation's
  # sandbox user can already own that path, turning every fixture here into a
  # spurious EACCES block.
  export HEARTBEAT_LOG="$BATS_TEST_TMPDIR/heartbeat.log"
}

# Duplicates tests/entrypoint-prompt-validator.bats's _stub_prompt_dir rather
# than sharing it, since bats loads one helper file per suite. review-prompt.md
# carries a VERDICT: line by default so an iteration that does not target the
# reviewer-verdict row never trips it, and the same gate also reads
# worker-prompt.md (issue #2059, #2058), so without a stub Assemble hard-fails.
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
    # Each entrypoint invocation clones into WORK_DIR, so a stale clone from a
    # prior iteration would make the next `git clone` fail.
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
          # FILER_FILE_RELAY requires BOX_FILER_ENABLED (issue #2533),
          # !BOX_WRITE_ENABLED and BOX_REVIEW_LOOP_ORCHESTRATOR at once, so
          # turning on BOX_WRITE_ENABLED alone closes the gate while
          # .filer.prompt stays populated and markerPresent still matters.
          export AGENTS_JSON_TEMPLATE='{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
          export BOX_FILER_ENABLED=1
          export ORCHESTRATOR_ENABLED=1
          export BOX_REVIEW_LOOP_ORCHESTRATOR=1
          unset BOX_REVIEW_LOOP_INLINE
          export BOX_WRITE_ENABLED=1
        fi
        ;;
      research-issue-intent)
        # FILER_FILE_RELAY's research case (ADR 0041, issue #2593) fires on
        # DISPATCH_KIND=research plus BOX_FILER_ENABLED, and that same condition
        # also activates the verdict-comment-relay reject row, which scans this
        # research-prompt.md for SPINDRIFT_COMMENT. Append that marker whenever
        # gate=true so the unrelated row never fires against this row.
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
