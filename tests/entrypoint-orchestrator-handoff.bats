#!/usr/bin/env bats
# ORCHESTRATOR_ENABLED (issue #1996) swaps run_driver_in_env's target from
# driver-exec to the in-box orchestrator without changing any flag it passes.

load helper

setup() {
  setup_entrypoint_env
}

@test "entrypoint takes the direct driver-exec path when ORCHESTRATOR_ENABLED is unset" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$ORCHESTRATOR_LOG" ]
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
}

@test "entrypoint hands the pass off to the orchestrator when ORCHESTRATOR_ENABLED is set" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$ORCHESTRATOR_LOG" ]
  # The driver bin rides the Handoff descriptor's .DriverBin (issue #2975), not
  # a --driver-bin argv flag.
  grep -q -- '--handoff-file' "$ORCHESTRATOR_LOG"
  [ "$(jq -r .DriverBin "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "claude" ]
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}

# Issue #2241: EFFORT reaches the invoker on the Handoff descriptor's Effort
# field since issue #2975, not a per-call --effort argv flag. The fake
# orchestrator echoes its raw argv to ORCHESTRATOR_LOG (issue #1996), so the
# assertion pulls --handoff-file out of that log.
@test "entrypoint forwards EFFORT to the orchestrator via the handoff" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export EFFORT="high"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .Effort "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "high" ]
}

# Issue #2011: CLAUDE_CODE_DISABLE_BACKGROUND_TASKS makes claude omit
# run_in_background from the Bash/Agent/Task/PowerShell tool schemas, so a
# Driver can never park on async work and stop coming back. It reaches the
# Driver as an export in the driverPreamble text ORCHESTRATOR_ENABLED swaps the
# invoker under, so this pair of tests pins that both paths see it identically.
@test "direct driver-exec path exports CLAUDE_CODE_DISABLE_BACKGROUND_TASKS to the Driver" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q '^env: CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1$' "$DRIVER_LOG"
}

@test "orchestrator path exports CLAUDE_CODE_DISABLE_BACKGROUND_TASKS to the Driver identically" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q '^env: CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1$' "$DRIVER_LOG"
}

# Issue #2037: entrypoint.sh renders review-prompt.md to a real file and records
# its path in the Handoff descriptor's ReviewPromptFile field (issue #2975),
# only on this fresh-issue work-dispatch path.
@test "orchestrator path carries a real ReviewPromptFile path in the handoff" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local handoff review_prompt_file
  handoff="$(handoff_path_from_log "$ORCHESTRATOR_LOG")"
  review_prompt_file="$(jq -r '.ReviewPromptFile' "$handoff")"
  # phase_prompt_assembly keeps the rendered file alive past assembly, because
  # the orchestrator reads it at review-pass time, so it must still exist here.
  [ -n "$review_prompt_file" ]
  [ -s "$review_prompt_file" ]
}

# Issue #2975 restores issue #2065's per-call review-prompt downgrade: every
# run_driver_in_env call shares the one on-disk Handoff document, so each
# required-marker gate's corrective resume passes a throwaway copy with
# ReviewPromptFile cleared through run_driver_in_env's 2nd positional override.
# Without that, the resume re-enters the whole implement/review/fix loop.
@test "orchestrator path strips ReviewPromptFile from the corrective resume's own handoff" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # The first pass forgets its outcome, so the SPINDRIFT_OUTCOME gate resumes
  # once and the orchestrator is invoked twice, one argv line each (issue #1996).
  export FAKE_DRIVER_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c . "$ORCHESTRATOR_LOG")" -eq 2 ]
  local first_handoff second_handoff
  first_handoff="$(head -1 "$ORCHESTRATOR_LOG" | grep -oE -- '--handoff-file [^ ]+' | awk '{print $2}')"
  second_handoff="$(tail -1 "$ORCHESTRATOR_LOG" | grep -oE -- '--handoff-file [^ ]+' | awk '{print $2}')"
  [ -n "$first_handoff" ]
  [ -n "$(jq -r '.ReviewPromptFile' "$first_handoff")" ]
  [ -n "$second_handoff" ]
  [ -f "$second_handoff" ]
  [ "$(jq -r '.ReviewPromptFile' "$second_handoff")" = "" ]
}

# Issue #2277: the reviewer's configured model (nix-baked into
# AGENTS_JSON_TEMPLATE's .reviewer.model) must be extracted before
# phase_prompt_assembly's del(.reviewer) drops that entry from --agents.
@test "orchestrator path forwards --review-model from the reviewer's configured model" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewModel "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "haiku" ]
}

# With no reviewer entry the orchestrator's review pass falls back to the
# coordinator model itself (run.go's runWithReviewPass), so entrypoint.sh must
# omit --review-model rather than pass it empty.
@test "orchestrator path leaves ReviewModel empty when no reviewer model is configured" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewModel "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "" ]
}

# Issue #2512: the reviewer's configured effort must reach the Handoff's
# ReviewEffort field the way --review-model does above, extracted before
# phase_prompt_assembly's del(.reviewer) drops that entry from --agents.
@test "orchestrator path forwards --review-effort from the reviewer's configured effort" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","effort":"high","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewEffort "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "high" ]
}

# With no reviewer entry the orchestrator falls back to its own default effort,
# so entrypoint.sh must omit --review-effort rather than pass it empty.
@test "orchestrator path leaves ReviewEffort empty when no reviewer effort is configured" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewEffort "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "" ]
}

# assemble.go only extracts .reviewer.effort when ORCHESTRATOR_ENABLED is set,
# so a driver-exec run leaves Handoff.ReviewEffort empty however the reviewer is
# configured. The direct path logs no argv, so the assertion reads the field off
# the handoff this run produced, which since issue #2975 is what driver-exec
# consumes.
@test "direct driver-exec path leaves ReviewEffort empty even with a reviewer effort configured" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","effort":"high","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$ORCHESTRATOR_LOG" ]
  [ "$(jq -r .ReviewEffort "$DRIVER_HANDOFF_FILE")" = "" ]
}

# Issue #2694 / #2975: MAX_BUDGET_TOKENS/MAX_BUDGET_USD reach the orchestrator
# through the Handoff's Caps fields, not per-call --max-budget-* flags. Both
# come off the environment (boxEnv, lib/env-schema.nix). MaxBudgetUSD is a JSON
# number, so 4.44 decodes back as 4.44.
@test "orchestrator path forwards MAX_BUDGET_TOKENS/MAX_BUDGET_USD via the handoff Caps" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export MAX_BUDGET_TOKENS="500000"
  export MAX_BUDGET_USD="4.44"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local handoff
  handoff="$(handoff_path_from_log "$ORCHESTRATOR_LOG")"
  [ "$(jq -r .Caps.MaxBudgetTokens "$handoff")" = "500000" ]
  [ "$(jq -r .Caps.MaxBudgetUSD "$handoff")" = "4.44" ]
}

# MAX_BUDGET_* are boxEnv (lib/env-schema.nix), so set_box_env always exports
# them at their schema default ("0"/"0.000000") even when the operator never
# overrides them. assemble-prompt parses MAX_BUDGET_USD as a float64 and
# json.Marshal encodes 0 as bare `0`, so .Caps.MaxBudgetUSD reads back "0".
@test "orchestrator path carries schema-default budget Caps when not overridden" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local handoff
  handoff="$(handoff_path_from_log "$ORCHESTRATOR_LOG")"
  [ "$(jq -r .Caps.MaxBudgetTokens "$handoff")" = "0" ]
  [ "$(jq -r .Caps.MaxBudgetUSD "$handoff")" = "0" ]
}

# Since issue #2975 phase_prompt_assembly forwards MAX_BUDGET_* to
# assemble-prompt whatever the invoker, so the direct path's handoff carries the
# budget in Caps too and driver-exec never consults it (only the
# orchestrator loop does, covered in cmd/launcher/orchestrator). What stays
# assertable here is that the budget never leaks into the Driver's own argv.
@test "direct driver-exec path still carries the budget in the handoff Caps but never in the Driver argv" {
  export MAX_BUDGET_TOKENS="500000"
  export MAX_BUDGET_USD="4.44"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$ORCHESTRATOR_LOG" ]
  [ "$(jq -r .Caps.MaxBudgetTokens "$DRIVER_HANDOFF_FILE")" = "500000" ]
  ! grep -q -- '--max-budget-tokens' "$DRIVER_LOG"
  ! grep -q -- '--max-budget-usd' "$DRIVER_LOG"
}

# The argv shape from the nix-rendered DRIVER_ARGV_* preamble vars (issue #2534)
# is forwarded on both paths, ungated on $_driver_invoker: both binaries declare
# the same flags and always need the driver's argv shape. This suite's DRIVER is
# claude, whose registry entry (lib/drivers/claude.nix) bakes the values
# asserted below into DRIVER_PREAMBLE_FILE.
@test "orchestrator path forwards claude's argv shape via the handoff ArgvShape" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Since issue #2975 the argv shape rides the Handoff's ArgvShape sub-object,
  # not seven per-call --argv-* flags.
  local handoff
  handoff="$(handoff_path_from_log "$ORCHESTRATOR_LOG")"
  [ "$(jq -r .ArgvShape.PromptStyle "$handoff")" = "flag" ]
  [ "$(jq -r .ArgvShape.PromptFlag "$handoff")" = "-p" ]
  [ "$(jq -r .ArgvShape.ModelFlag "$handoff")" = "--model" ]
  [ "$(jq -r .ArgvShape.AgentsFlag "$handoff")" = "--agents" ]
  [ "$(jq -r .ArgvShape.EffortFlag "$handoff")" = "--effort" ]
  [ "$(jq -r '.ArgvShape.Order | join(" ")' "$handoff")" = "prompt model agents session driverFlags effort" ]
}

# DRIVER_ARGV_MODEL_OMIT_EMPTY is a bare-boolean gate set only when the
# Driver's argvShape sets modelOmitEmpty true. claude sets it false
# (lib/drivers/claude.nix), so this pins the gate's unset side. The true side
# (opencode) has no bats coverage anywhere in this repo.
@test "orchestrator path carries ArgvShape.ModelOmitEmpty false for claude" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ArgvShape.ModelOmitEmpty "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "false" ]
}

# Issue #2983: --manifest-path belongs only on the orchestrator invocation
# (driver-exec has no such flag) and only when the outbox is mounted host-side
# (needsOutbox in cmd/launcher/internal/dispatch/box.go).
# BOX_HOST_MEDIATED_REMOTE=1 mirrors a real CODE_FORGE=local box, the first of
# needsOutbox's two disjuncts.
@test "orchestrator path forwards --manifest-path under OUTBOX_DIR when the box is host-mediated-remote" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export BOX_HOST_MEDIATED_REMOTE=1
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qE -- '--manifest-path [^ ]*/outbox/manifest\.json' "$ORCHESTRATOR_LOG"
}

# The default fixture is BOX_WRITE_ENABLED=1 and BOX_OUTBOX_RELAY_CAPABLE=1, so
# neither needsOutbox disjunct holds (_is_readonly_outbox_relay requires
# BOX_WRITE_ENABLED unset) and the outbox is never mounted.
@test "orchestrator path omits --manifest-path when the outbox is not mounted" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--manifest-path' "$ORCHESTRATOR_LOG"
}

# The second needsOutbox disjunct: a read-only, outbox-relay-capable box, which
# _is_readonly_outbox_relay matches independently of BOX_HOST_MEDIATED_REMOTE.
@test "orchestrator path forwards --manifest-path for a read-only outbox-relay-capable box" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  unset BOX_WRITE_ENABLED
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qE -- '--manifest-path [^ ]*/outbox/manifest\.json' "$ORCHESTRATOR_LOG"
}

# driver-exec has no --manifest-path flag and would hard-fail on it, even when
# the box is host-mediated-remote, the same env fact that flips the flag on for
# the orchestrator above.
@test "direct driver-exec path never forwards --manifest-path even when host-mediated-remote" {
  export BOX_HOST_MEDIATED_REMOTE=1
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$ORCHESTRATOR_LOG" ]
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
  ! grep -q -- '--manifest-path' "$DRIVER_LOG"
}
