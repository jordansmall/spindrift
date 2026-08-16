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
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q '^env: CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1$' "$DRIVER_LOG"
}

# The code-owned review pass (issue #2037): entrypoint.sh renders
# review-prompt.md and threads it to the orchestrator's own
# --review-prompt-file, only on this fresh-issue work-dispatch path.
@test "orchestrator path forwards --review-prompt-file carrying a real path" {
  export ORCHESTRATOR_ENABLED=1
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
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--review-model' "$ORCHESTRATOR_LOG"
}

# Issue #2390: REVIEW_EFFORT threads through to the orchestrator's own
# --review-effort flag, mirroring EFFORT -> --effort above. Unlike
# review_model/review_prompt, REVIEW_EFFORT has no Handoff descriptor field --
# it comes straight off the environment, so it's set here as a plain exported
# var rather than via AGENTS_JSON_TEMPLATE/handoff_json.
@test "orchestrator path forwards REVIEW_EFFORT to the orchestrator as --review-effort" {
  export ORCHESTRATOR_ENABLED=1
  export REVIEW_EFFORT="high"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--review-effort high' "$ORCHESTRATOR_LOG"
}

# Without REVIEW_EFFORT set, there's nothing to override the orchestrator's
# own default review effort with -- entrypoint.sh must omit --review-effort
# entirely rather than pass it empty, mirroring the --review-model omit test
# above.
@test "orchestrator path omits --review-effort when REVIEW_EFFORT is not set" {
  export ORCHESTRATOR_ENABLED=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--review-effort' "$ORCHESTRATOR_LOG"
}

# The --review-effort gate is on _driver_invoker = orchestrator, not on
# REVIEW_EFFORT alone -- the direct driver-exec path has no review pass of
# its own to configure, so it must omit --review-effort even with
# REVIEW_EFFORT set, mirroring the orchestrator-path omit test above.
@test "direct driver-exec path omits --review-effort even with REVIEW_EFFORT set" {
  export REVIEW_EFFORT="high"
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
# orchestrator's own --worker-work-dir/--worker-timeout flags, mirroring
# REVIEW_EFFORT -> --review-effort above. Like REVIEW_EFFORT, neither has a
# Handoff descriptor field -- both come straight off the environment.
@test "orchestrator path forwards WORKER_WORK_DIR/WORKER_TIMEOUT to the orchestrator" {
  export ORCHESTRATOR_ENABLED=1
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
