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

# Issue #2241: EFFORT threads through to the invoker; since issue #2975 it
# rides the Handoff descriptor's own Effort field (forwarded to assemble-prompt
# once, in phase_prompt_assembly) rather than a per-call --effort argv flag.
# The fake orchestrator echoes its raw argv to ORCHESTRATOR_LOG verbatim (issue
# #1996), so extract the --handoff-file it was handed and assert .Effort there.
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
# claude itself omit run_in_background from the Bash/Agent/Task/PowerShell
# tools' own schema, so a Driver can never request async in the first place --
# the fix for a Driver that backgrounds work or parks on a subagent and never
# comes back. It reaches the Driver as an `export` in the same driverPreamble
# text ORCHESTRATOR_ENABLED just swaps the invoker under (agent/entrypoint.sh's
# run_driver_in_env), so both invocation paths must see it identically -- a
# structural guarantee this pair of tests pins directly, rather than trusting
# it by inspection.
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
  # actually rendered and recorded review-prompt.md. The file survives the run
  # now (phase_prompt_assembly keeps it alive so the orchestrator can read it
  # at review-pass time), so assert it exists and is non-empty too.
  [ -n "$review_prompt_file" ]
  [ -s "$review_prompt_file" ]
}

# Issue #2975 restores issue #2065's per-call review-prompt downgrade, which a
# prior slice of this same issue's own work had accidentally reverted: every
# run_driver_in_env call now shares the ONE on-disk Handoff document
# phase_prompt_assembly wrote ($_handoff_file), so run_driver_in_env's own
# unused 4th positional slot can no longer narrow which file reaches the
# invoker the way the old per-call review_prompt arg once did. Rather than
# leave the corrective resume handing the orchestrator the very same
# ReviewPromptFile-bearing handoff as the first pass -- and so re-entering the
# full implement/review/fix loop a second time from whatever cap or park
# stopped the first attempt, exactly the regression issue #2065 was written to
# prevent -- run_driver_in_env's previously-dead 2nd positional slot is
# repurposed into an optional handoff-file override. Each required-marker
# gate's corrective resume now builds its own throwaway copy of the shared
# handoff with ReviewPromptFile cleared and passes its path as that override,
# so the resume stays a narrow single pass while the main pass's own handoff
# (and its on-disk ReviewPromptFile) is left untouched. This test pins the
# restored behaviour: the first pass's handoff carries a real
# ReviewPromptFile, and the corrective resume's own (distinct) handoff does
# not.
@test "orchestrator path strips ReviewPromptFile from the corrective resume's own handoff" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # First pass forgets its outcome; the SPINDRIFT_OUTCOME gate resumes once,
  # so the orchestrator is invoked exactly twice -- one line per invocation in
  # ORCHESTRATOR_LOG (the fake echoes its argv there, issue #1996).
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

# Issue #2277: the reviewer's own configured model (nix-baked into
# AGENTS_JSON_TEMPLATE's .reviewer.model) must reach the orchestrator's
# --review-model flag, extracted before phase_prompt_assembly's
# del(.reviewer) drops the reviewer entry from --agents entirely.
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

# Issue #2512: the reviewer's own configured effort (nix-baked into
# AGENTS_JSON_TEMPLATE's .reviewer.effort) must reach the orchestrator's
# --review-effort flag via the Handoff descriptor's own ReviewEffort field,
# mirroring --review-model just above exactly -- extracted before
# phase_prompt_assembly's del(.reviewer) drops the reviewer entry from
# --agents entirely.
@test "orchestrator path forwards --review-effort from the reviewer's configured effort" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","effort":"high","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewEffort "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "high" ]
}

# Without a reviewer entry in the template, there's no configured effort to
# override with -- the orchestrator's review pass falls back to its own
# default effort on its own, so entrypoint.sh must omit --review-effort
# entirely rather than pass it empty, mirroring the --review-model omit test
# above.
@test "orchestrator path leaves ReviewEffort empty when no reviewer effort is configured" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ReviewEffort "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "" ]
}

# On the direct driver-exec path, Handoff.ReviewEffort stays empty even with a
# reviewer effort configured: assemble.go only extracts .reviewer.effort when
# ORCHESTRATOR_ENABLED is set, so a driver-exec run never populates the field
# (the reviewer entry stays in the roster, its effort just part of that JSON,
# not lifted onto ReviewEffort). Read straight off the handoff this run
# produced ($DRIVER_HANDOFF_FILE) -- the direct path logs no argv to grep, and
# since issue #2975 the whole handoff is what driver-exec consumes, so the
# meaningful assertion is that the field is empty in the document itself.
@test "direct driver-exec path leaves ReviewEffort empty even with a reviewer effort configured" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","effort":"high","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$ORCHESTRATOR_LOG" ]
  [ "$(jq -r .ReviewEffort "$DRIVER_HANDOFF_FILE")" = "" ]
}

# Issue #2694 / #2975: MAX_BUDGET_TOKENS/MAX_BUDGET_USD thread through to the
# orchestrator via the Handoff descriptor's Caps.MaxBudgetTokens/MaxBudgetUSD
# fields (forwarded to assemble-prompt once in phase_prompt_assembly, then read
# by the orchestrator off --handoff-file), rather than the old per-call
# --max-budget-* argv flags. Both come straight off the environment (boxEnv,
# lib/env-schema.nix). MaxBudgetUSD is a JSON number, so 4.44 decodes to 4.44.
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

# The schema default: MAX_BUDGET_TOKENS/MAX_BUDGET_USD are boxEnv
# (lib/env-schema.nix), so set_box_env (helper.bash, mirroring the real nix
# preamble) always exports them at their schema default ("0"/"0.000000") even
# when the operator never overrides them. entrypoint.sh forwards those to
# assemble-prompt, which parses them as Go int/float64: json.Marshal encodes a
# float64 0 as bare `0`, not "0.000000", so .Caps.MaxBudgetUSD reads back "0".
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
# path -- phase_prompt_assembly forwards MAX_BUDGET_* to assemble-prompt
# regardless of the invoker, so a driver-exec-direct run's handoff carries the
# budget in Caps too; driver-exec simply never consults it (only the
# orchestrator's own loop does, covered at the Go level in
# cmd/launcher/orchestrator). The pre-#2975 "budget must not reach the direct
# invocation" distinction is therefore gone at the bash layer; what remains
# assertable here is that the budget lands in the handoff Caps even on the
# direct path, proving entrypoint forwards it unconditionally, and that it
# never leaks into the Driver's own argv ($DRIVER_LOG).
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

# The 7 --argv-* flags run_driver_in_env threads from the nix-rendered
# DRIVER_ARGV_* preamble vars (issue #2534) unconditionally, on both the
# direct driver-exec path and the orchestrator hand-off -- unlike every flag
# above them, they aren't gated on $_driver_invoker or an unset/empty
# override, since both binaries declare the same --argv-* flags and always
# need a driver's argv shape to assemble its invocation. This suite's DRIVER
# defaults to "claude" (setup_entrypoint_env), whose registry entry
# (lib/drivers/claude.nix) bakes promptStyle=flag, promptFlag=-p,
# modelFlag=--model, agentsFlag=--agents, effortFlag=--effort into
# DRIVER_PREAMBLE_FILE -- asserted here via the orchestrator hand-off path so
# each flag's real rendered value is pinned the same way --driver-bin claude
# is pinned above, without needing driver-exec's own flag parsing (which the
# fake orchestrator does not model) to forward it any further.
@test "orchestrator path forwards claude's argv shape via the handoff ArgvShape" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Since issue #2975 the argv shape rides the Handoff descriptor's ArgvShape
  # sub-object, not seven per-call --argv-* flags. Read each field off
  # --handoff-file; claude's registry entry (lib/drivers/claude.nix) bakes
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

# DRIVER_ARGV_MODEL_OMIT_EMPTY is a bare-boolean gate (agent/entrypoint.sh
# ~lines 904-910): present only when the selected Driver's argvShape sets
# modelOmitEmpty = true. claude's argvShape (lib/drivers/claude.nix) sets it
# false, so this suite's DRIVER_PREAMBLE_FILE never defines
# DRIVER_ARGV_MODEL_OMIT_EMPTY -- the bare --argv-model-omit-empty flag must
# stay entirely absent from the invocation, proving the gate's unset side
# (the true side -- opencode's argvShape.modelOmitEmpty -- is outside this
# DRIVER=claude suite's scope; no other bats suite in this repo currently
# threads DRIVER_ARGV_MODEL_OMIT_EMPTY through run_driver_in_env either, so
# that side of the gate has no bats coverage anywhere).
@test "orchestrator path carries ArgvShape.ModelOmitEmpty false for claude" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ArgvShape.ModelOmitEmpty "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "false" ]
}
