#!/usr/bin/env bats
# ORCHESTRATOR_ENABLED handoff (issue #1996): the boolean knob (default off)
# that swaps run_driver_in_env's invocation target from driver-exec to the
# in-box orchestrator, without changing any flag it's called with.

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
  # The driver bin now rides the Handoff descriptor (issue #2975), not an
  # --driver-bin argv flag: entrypoint hands the orchestrator --handoff-file,
  # and the bin is read off .DriverBin there.
  grep -q -- '--handoff-file' "$ORCHESTRATOR_LOG"
  [ "$(jq -r .DriverBin "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "claude" ]
  # Behaviour stays identical to the direct path: the orchestrator's own
  # single pass still reaches the Driver and re-emits its outcome line.
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}

# Issue #2241/#2975: EFFORT threads through to the invoker on the Handoff
# descriptor's Effort field rather than a per-call --effort argv flag. The fake
# orchestrator echoes its raw argv to ORCHESTRATOR_LOG, so extract the
# --handoff-file it was handed and assert .Effort there.
@test "entrypoint forwards EFFORT to the orchestrator via the handoff" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export EFFORT="high"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .Effort "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "high" ]
}

# Issue #2011: reject-background-bash.sh's PreToolUse deny is Bash-only and
# fires after the fact; CLAUDE_CODE_DISABLE_BACKGROUND_TASKS instead makes
# claude omit run_in_background from the Bash/Agent/Task/PowerShell tool schemas,
# so a Driver can never request async in the first place. It reaches the Driver
# as an `export` in the same driverPreamble text ORCHESTRATOR_ENABLED swaps the
# invoker under, so both invocation paths must see it identically.
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

# The code-owned review pass (issue #2037): entrypoint.sh renders
# review-prompt.md to a real file and records its path in the Handoff
# descriptor's ReviewPromptFile field (issue #2975 -- previously a per-call
# --review-prompt-file argv flag), only on this fresh-issue work-dispatch path.
@test "orchestrator path carries a real ReviewPromptFile path in the handoff" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local handoff review_prompt_file
  handoff="$(handoff_path_from_log "$ORCHESTRATOR_LOG")"
  review_prompt_file="$(jq -r '.ReviewPromptFile' "$handoff")"
  # A real, non-empty path (not an omitted/empty field) proves entrypoint.sh
  # actually rendered and recorded review-prompt.md. The file survives the run,
  # so assert it exists and is non-empty too.
  [ -n "$review_prompt_file" ]
  [ -s "$review_prompt_file" ]
}

# Issue #2065's per-call review-prompt downgrade: every run_driver_in_env call
# shares the one on-disk Handoff document phase_prompt_assembly wrote, so
# handing a corrective resume that same ReviewPromptFile-bearing handoff would
# re-enter the full implement/review/fix loop a second time from whatever cap or
# park stopped the first attempt. Each required-marker gate's resume therefore
# builds its own throwaway copy with ReviewPromptFile cleared and passes its
# path as run_driver_in_env's handoff-file override, leaving the main pass's
# handoff untouched. This test pins that: the first pass's handoff carries a
# real ReviewPromptFile, the resume's own (distinct) handoff does not.
@test "orchestrator path strips ReviewPromptFile from the corrective resume's own handoff" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # First pass forgets its outcome; the SPINDRIFT_OUTCOME gate resumes once, so
  # the orchestrator is invoked exactly twice -- one ORCHESTRATOR_LOG line each.
  export FAKE_DRIVER_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c . "$ORCHESTRATOR_LOG")" -eq 2 ]
  local first_handoff second_handoff
  first_handoff="$(head -1 "$ORCHESTRATOR_LOG" | grep -oE -- '--handoff-file [^ ]+' | awk '{print $2}')"
  second_handoff="$(tail -1 "$ORCHESTRATOR_LOG" | grep -oE -- '--handoff-file [^ ]+' | awk '{print $2}')"
  [ -n "$first_handoff" ]
  # The first pass's own handoff carries the review-prompt path ...
  [ -n "$(jq -r '.ReviewPromptFile' "$first_handoff")" ]
  # ... but the corrective resume's own handoff -- a distinct file -- does not.
  [ -n "$second_handoff" ]
  [ -f "$second_handoff" ]
  [ "$(jq -r '.ReviewPromptFile' "$second_handoff")" = "" ]
}

# Issue #2277: the reviewer's configured model (AGENTS_JSON_TEMPLATE's
# .reviewer.model) must reach the orchestrator's --review-model, extracted
# before phase_prompt_assembly's del(.reviewer) drops the entry from --agents.
@test "orchestrator path forwards --review-model from the reviewer's configured model" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewModel "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "haiku" ]
}

# Without a reviewer entry in the template, there's no configured model to
# override with -- the orchestrator's review pass falls back to the
# coordinator model on its own (run.go's runWithReviewPass), so entrypoint.sh
# must omit --review-model entirely rather than pass it empty.
@test "orchestrator path leaves ReviewModel empty when no reviewer model is configured" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewModel "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "" ]
}

# Issue #2512: the reviewer's configured effort must reach --review-effort via
# the Handoff's ReviewEffort field, mirroring --review-model above -- extracted
# before del(.reviewer) drops the reviewer entry from --agents.
@test "orchestrator path forwards --review-effort from the reviewer's configured effort" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","effort":"high","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewEffort "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "high" ]
}

# Without a reviewer entry there's no configured effort to override with -- the
# review pass falls back to its own default, so entrypoint.sh must omit
# --review-effort entirely rather than pass it empty.
@test "orchestrator path leaves ReviewEffort empty when no reviewer effort is configured" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewEffort "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "" ]
}

# On the direct driver-exec path, Handoff.ReviewEffort stays empty even with a
# reviewer effort configured: assemble.go only lifts .reviewer.effort onto the
# field when ORCHESTRATOR_ENABLED is set. Read straight off the handoff this run
# produced -- the direct path logs no argv to grep.
@test "direct driver-exec path leaves ReviewEffort empty even with a reviewer effort configured" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","effort":"high","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$ORCHESTRATOR_LOG" ]
  [ "$(jq -r .ReviewEffort "$DRIVER_HANDOFF_FILE")" = "" ]
}

# Issue #2694/#2975: MAX_BUDGET_TOKENS/MAX_BUDGET_USD thread through via the
# Handoff's Caps.MaxBudgetTokens/MaxBudgetUSD, read by the orchestrator off
# --handoff-file, rather than per-call --max-budget-* argv flags. MaxBudgetUSD
# is a JSON number, so 4.44 decodes to 4.44.
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

# The schema default: MAX_BUDGET_* are boxEnv, so set_box_env always exports
# them at their schema default ("0"/"0.000000") even when the operator never
# overrides them. assemble-prompt parses them as Go int/float64, and
# json.Marshal encodes a float64 0 as bare `0`, so .Caps.MaxBudgetUSD reads "0".
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

# Since issue #2975 the budget rides the Handoff Caps unconditionally on every
# path; driver-exec simply never consults it (only the orchestrator's loop does,
# covered at the Go level). What remains assertable here is that the budget
# lands in the handoff Caps even on the direct path, and never leaks into the
# Driver's own argv ($DRIVER_LOG).
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

# The argv shape run_driver_in_env threads from the nix-rendered DRIVER_ARGV_*
# preamble vars (issue #2534) unconditionally, on both the direct driver-exec
# path and the orchestrator hand-off -- unlike the flags above, it isn't gated
# on the invoker, since both binaries need a driver's argv shape to assemble its
# invocation. Asserted via the orchestrator hand-off path so each rendered value
# is pinned without needing driver-exec's own flag parsing (which the fake
# orchestrator does not model).
@test "orchestrator path forwards claude's argv shape via the handoff ArgvShape" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Since issue #2975 the argv shape rides the Handoff's ArgvShape sub-object,
  # not seven per-call --argv-* flags. claude's registry entry bakes
  # promptStyle=flag, promptFlag=-p, modelFlag=--model, agentsFlag=--agents,
  # effortFlag=--effort, and the 6-slot order below.
  local handoff
  handoff="$(handoff_path_from_log "$ORCHESTRATOR_LOG")"
  [ "$(jq -r .ArgvShape.PromptStyle "$handoff")" = "flag" ]
  [ "$(jq -r .ArgvShape.PromptFlag "$handoff")" = "-p" ]
  [ "$(jq -r .ArgvShape.ModelFlag "$handoff")" = "--model" ]
  [ "$(jq -r .ArgvShape.AgentsFlag "$handoff")" = "--agents" ]
  [ "$(jq -r .ArgvShape.EffortFlag "$handoff")" = "--effort" ]
  [ "$(jq -r '.ArgvShape.Order | join(" ")' "$handoff")" = "prompt model agents session driverFlags effort" ]
}

# DRIVER_ARGV_MODEL_OMIT_EMPTY is a bare-boolean gate, present only when the
# selected Driver's argvShape sets modelOmitEmpty = true. claude sets it false,
# so the bare flag must stay entirely absent -- proving the gate's unset side.
# The true side (opencode) is outside this DRIVER=claude suite's scope and has
# no bats coverage anywhere.
@test "orchestrator path carries ArgvShape.ModelOmitEmpty false for claude" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ArgvShape.ModelOmitEmpty "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "false" ]
}
