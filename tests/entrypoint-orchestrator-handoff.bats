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
  grep -q -- "--driver-bin claude" "$ORCHESTRATOR_LOG"
  # Behaviour stays identical to the direct path: the orchestrator's own
  # single pass still reaches the Driver and re-emits its outcome line.
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}

# Issue #2241: EFFORT threads through run_driver_in_env's shared invocation
# block to whichever binary $_driver_invoker names (orchestrator here,
# driver-exec on the direct path) alongside --model, using the same
# ${VAR:-} empty-default pattern. The fake orchestrator echoes its raw argv
# to ORCHESTRATOR_LOG verbatim (issue #1996), so this asserts the flag
# reaches the invoker without needing driver-exec's own flag parsing (which
# this fake, standing in for the real binary, does not model) to forward it
# any further.
@test "entrypoint forwards EFFORT to the orchestrator as --effort" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export EFFORT="high"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--effort high" "$ORCHESTRATOR_LOG"
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
# review-prompt.md and threads it to the orchestrator's own
# --review-prompt-file, only on this fresh-issue work-dispatch path.
@test "orchestrator path forwards --review-prompt-file carrying a real path" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--review-prompt-file' "$ORCHESTRATOR_LOG"
  local review_prompt_file
  review_prompt_file="$(grep -oE -- '--review-prompt-file [^ ]+' "$ORCHESTRATOR_LOG" | awk '{print $2}')"
  # run_driver_in_env removes its own temp files once the pass returns, so the
  # path itself no longer exists by the time bats inspects it here -- assert
  # it was a real, non-empty flag value (not an omitted/empty flag), which is
  # what proves entrypoint.sh actually rendered and threaded review-prompt.md
  # through, rather than skipping the flag or passing it empty.
  [ -n "$review_prompt_file" ]
}

# Issue #2065: the corrective resume the SPINDRIFT_OUTCOME required-marker gate
# fires (issue #2044) deliberately omits run_driver_in_env's 4th arg
# (review_prompt), so under the orchestrator that nudge-and-retry stays a
# narrow single pass rather than re-entering the full implement/review/fix loop
# a second time from whatever cap or park stopped the first attempt. The design
# decision recorded there is "keep the single-pass fallback": each
# run_driver_in_env call spawns a fresh orchestrator whose
# --max-review-rounds/--max-slices budgets reset to their defaults, so
# re-attaching --review-prompt-file would hand the last-resort nudge a
# brand-new full review budget and re-trigger the exact
# bounded-but-large loop the original run just exhausted. This test locks that
# downgrade: the first pass forwards --review-prompt-file (asserted above), the
# resume must not.
@test "orchestrator path omits --review-prompt-file on the corrective resume" {
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
  # The initial pass carries the review-prompt flag ...
  head -1 "$ORCHESTRATOR_LOG" | grep -q -- '--review-prompt-file'
  # ... and the corrective resume (the second, last line) does not.
  ! tail -1 "$ORCHESTRATOR_LOG" | grep -q -- '--review-prompt-file'
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
  grep -q -- '--review-model haiku' "$ORCHESTRATOR_LOG"
}

# Without a reviewer entry in the template, there's no configured model to
# override with -- the orchestrator's review pass falls back to the
# coordinator model on its own (run.go's runWithReviewPass), so entrypoint.sh
# must omit --review-model entirely rather than pass it empty.
@test "orchestrator path omits --review-model when no reviewer model is configured" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--review-model' "$ORCHESTRATOR_LOG"
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
  grep -q -- '--review-effort high' "$ORCHESTRATOR_LOG"
}

# Without a reviewer entry in the template, there's no configured effort to
# override with -- the orchestrator's review pass falls back to its own
# default effort on its own, so entrypoint.sh must omit --review-effort
# entirely rather than pass it empty, mirroring the --review-model omit test
# above.
@test "orchestrator path omits --review-effort when no reviewer effort is configured" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--review-effort' "$ORCHESTRATOR_LOG"
}

# Non-vacuous despite Handoff.ReviewEffort also staying empty on this path
# for a second, independent reason (assemble.go only extracts .reviewer.effort
# when ORCHESTRATOR_ENABLED is set, so a driver-exec run never even populates
# the field) -- this test pins the separate bash-level gate at
# run_driver_in_env's tail (entrypoint.sh, `[ "$_driver_invoker" = orchestrator
# ]`) directly, on the live shell code, not just the Go-level extraction one
# layer up. If that bash gate were ever deleted, or a future change fed
# review_effort into this path from some other source entirely (bypassing
# assemble.go's extraction), this is the test that would catch a
# --review-effort leak on the driver-exec path specifically -- the Go-only
# coverage above cannot.
@test "direct driver-exec path omits --review-effort even with a reviewer effort configured" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","effort":"high","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--review-effort' "$DRIVER_LOG"
}

# The parallel worker dispatch (issue #2059, #2058): entrypoint.sh renders
# worker-prompt.md and threads it to the orchestrator's own
# --worker-prompt-file, mirroring --review-prompt-file above -- same gate
# (fresh-issue work-dispatch path), same Handoff descriptor mechanism.
@test "orchestrator path forwards --worker-prompt-file carrying a real path" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--worker-prompt-file' "$ORCHESTRATOR_LOG"
  local worker_prompt_file
  worker_prompt_file="$(grep -oE -- '--worker-prompt-file [^ ]+' "$ORCHESTRATOR_LOG" | awk '{print $2}')"
  # run_driver_in_env removes its own temp files once the pass returns, so the
  # path itself no longer exists by the time bats inspects it here -- assert
  # it was a real, non-empty flag value (not an omitted/empty flag), which is
  # what proves entrypoint.sh actually rendered and threaded worker-prompt.md
  # through, rather than skipping the flag or passing it empty.
  [ -n "$worker_prompt_file" ]
}

# The --worker-prompt-file gate is on _driver_invoker = orchestrator -- the
# direct driver-exec path declares no such flag and would hard-fail on it,
# mirroring the --review-effort omit test above.
@test "direct driver-exec path omits --worker-prompt-file" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--worker-prompt-file' "$DRIVER_LOG"
}

# Issue #2059, #2058: WORKER_WORK_DIR/WORKER_TIMEOUT thread through to the
# orchestrator's own --worker-work-dir/--worker-timeout flags. Unlike
# review_effort (now Handoff-sourced, issue #2512), neither has a Handoff
# descriptor field -- both come straight off the environment.
@test "orchestrator path forwards WORKER_WORK_DIR/WORKER_TIMEOUT to the orchestrator" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export WORKER_WORK_DIR="/tmp/spindrift-workers-test"
  export WORKER_TIMEOUT="15m"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--worker-work-dir /tmp/spindrift-workers-test' "$ORCHESTRATOR_LOG"
  grep -q -- '--worker-timeout 15m' "$ORCHESTRATOR_LOG"
}

# Without WORKER_WORK_DIR/WORKER_TIMEOUT set, there's nothing to override the
# orchestrator's own defaults with -- entrypoint.sh must omit both flags
# entirely rather than pass them empty, mirroring the --review-effort omit
# test above.
@test "orchestrator path omits --worker-work-dir/--worker-timeout when unset" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--worker-work-dir' "$ORCHESTRATOR_LOG"
  ! grep -q -- '--worker-timeout' "$ORCHESTRATOR_LOG"
}

# Issue #2059, #2495: MAX_PARALLEL_WORKERS threads through to the
# orchestrator's own --max-parallel-workers flag, mirroring REVIEW_EFFORT ->
# --review-effort above. Like REVIEW_EFFORT, it has no Handoff descriptor
# field -- it comes straight off the environment.
@test "orchestrator path forwards MAX_PARALLEL_WORKERS to the orchestrator as --max-parallel-workers" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export MAX_PARALLEL_WORKERS=4
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--max-parallel-workers 4' "$ORCHESTRATOR_LOG"
}

# Without MAX_PARALLEL_WORKERS set, there's nothing to override the
# orchestrator's own default with -- entrypoint.sh must omit
# --max-parallel-workers entirely rather than pass it empty, mirroring the
# --review-effort omit test above.
@test "orchestrator path omits --max-parallel-workers when unset" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  unset MAX_PARALLEL_WORKERS
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--max-parallel-workers' "$ORCHESTRATOR_LOG"
}

# The --max-parallel-workers gate is on _driver_invoker = orchestrator -- the
# direct driver-exec path declares no such flag and would hard-fail on it,
# mirroring the --review-effort omit test above.
@test "direct driver-exec path omits --max-parallel-workers even with MAX_PARALLEL_WORKERS set" {
  export MAX_PARALLEL_WORKERS=4
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--max-parallel-workers' "$DRIVER_LOG"
}

# Issue #2694: MAX_BUDGET_TOKENS/MAX_BUDGET_USD thread through to the
# orchestrator's own --max-budget-tokens/--max-budget-usd flags, mirroring
# WORKER_WORK_DIR/WORKER_TIMEOUT above -- neither has a Handoff descriptor
# field, both come straight off the environment (now boxEnv, lib/env-schema.nix).
@test "orchestrator path forwards MAX_BUDGET_TOKENS/MAX_BUDGET_USD to the orchestrator" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export MAX_BUDGET_TOKENS="500000"
  export MAX_BUDGET_USD="4.44"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--max-budget-tokens 500000' "$ORCHESTRATOR_LOG"
  grep -q -- '--max-budget-usd 4.44' "$ORCHESTRATOR_LOG"
}

# Without MAX_BUDGET_TOKENS/MAX_BUDGET_USD set, there's nothing to override the
# orchestrator's own defaults (both disabled/0) with -- entrypoint.sh must omit
# both flags entirely rather than pass them empty, mirroring the
# --worker-work-dir/--worker-timeout omit test above.
@test "orchestrator path omits --max-budget-tokens/--max-budget-usd when unset" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--max-budget-tokens' "$ORCHESTRATOR_LOG"
  ! grep -q -- '--max-budget-usd' "$ORCHESTRATOR_LOG"
}

# The --max-budget-tokens/--max-budget-usd gate is on _driver_invoker =
# orchestrator -- the direct driver-exec path declares no such flags and would
# hard-fail on them, mirroring the --max-parallel-workers omit test above.
@test "direct driver-exec path omits --max-budget-tokens/--max-budget-usd even when set" {
  export MAX_BUDGET_TOKENS="500000"
  export MAX_BUDGET_USD="4.44"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
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
@test "orchestrator path forwards claude's argv shape as --argv-* flags" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--argv-prompt-style flag' "$ORCHESTRATOR_LOG"
  grep -q -- '--argv-prompt-flag -p' "$ORCHESTRATOR_LOG"
  grep -q -- '--argv-model-flag --model' "$ORCHESTRATOR_LOG"
  grep -q -- '--argv-agents-flag --agents' "$ORCHESTRATOR_LOG"
  grep -q -- '--argv-effort-flag --effort' "$ORCHESTRATOR_LOG"
  # The fake orchestrator logs argv via `echo "$@"` (no field-boundary
  # markers), and DRIVER_ARGV_ORDER is itself a space-joined multi-word
  # string (lib/drivers/default.nix's renderArgvShape) -- so a `[^ ]+`
  # capture would grab only its first word ("prompt") and silently pass on a
  # truncated or wrong order. Every flag entrypoint.sh threads after
  # --argv-order is a bare/valued "--"-prefixed flag (or absent), so the
  # order value is exactly everything up to the next " --" (or end of
  # line); pin the full 6-slot value claude.nix's argvShape.order declares.
  local argv_order
  argv_order="$(grep -oE -- '--argv-order .*' "$ORCHESTRATOR_LOG" | sed -E 's/^--argv-order //; s/ --.*$//')"
  [ "$argv_order" = "prompt model agents session driverFlags effort" ]
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
@test "orchestrator path omits --argv-model-omit-empty for claude (modelOmitEmpty=false)" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--argv-model-omit-empty' "$ORCHESTRATOR_LOG"
}
