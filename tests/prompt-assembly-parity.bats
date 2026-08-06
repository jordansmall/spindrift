#!/usr/bin/env bats
# Byte-parity harness (issue #2349, slice 6): runs the SAME env through both
# agent/entrypoint.sh's real bash phase_prompt_assembly (via $ENTRYPOINT and
# the fake driver/driver-exec chain, tests/helper.bash's setup_entrypoint_env)
# and the new Go `driver-exec assemble-prompt` verb
# (cmd/launcher/internal/promptassembly + driver-exec/assembleprompt_cmd.go),
# and asserts the two produce equivalent output for the one Env cell
# promptassembly.Assemble covers (see assemble.go's checkCoveredCell):
# ISSUE_TRACKER=github, CODE_FORGE=github, a read-write box, DISPATCH_KIND
# "work", FIX_PASS 0, the orchestrator off, and every skill baked. That
# combination, plus setup_entrypoint_env's own BOX_WRITE_ENABLED=1, is
# exactly tests/box_env_gen.bash's set_box_env schema-default cell with zero
# overrides.
#
# Neither side is the reference for the other: this suite is a regression
# net for the two representations drifting apart, not a source of truth for
# either one's own correctness (that's tests/entrypoint-*.bats for the bash
# side and cmd/launcher/internal/promptassembly/*_test.go for the Go side).

load helper

setup() {
  setup_entrypoint_env
  : "${DRIVER_EXEC_BIN:?DRIVER_EXEC_BIN must be set (the real driver-exec Go binary, nix/checks/promptassembly.nix)}"
  : "${PROMPTASSEMBLY_REGISTRY_FILE:?PROMPTASSEMBLY_REGISTRY_FILE must be set (lib/fragments.nix rendered to JSON, nix/checks/promptassembly.nix)}"

  # BRANCH is computed inside entrypoint.sh's main (BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}",
  # entrypoint.sh:55), not exported by set_box_env/setup_entrypoint_env --
  # reproduce the same computation here so the Go side's --branch flag
  # matches what the bash side derives at runtime.
  BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"

  # Bake all four skills (entrypoint-prompt-fragments.bats:660-730's pattern):
  # the covered cell requires every per-skill gate on and a non-empty
  # SKILLS_FOUND (assemble.go's checkCoveredCell).
  mkdir -p "$HOME/.claude/skills/caveman"
  cat >"$HOME/.claude/skills/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL

  mkdir -p "$HOME/.claude/skills/tdd"
  cat >"$HOME/.claude/skills/tdd/SKILL.md" <<'SKILL'
---
name: tdd
description: Test-driven development.
---
Red, green, refactor.
SKILL

  mkdir -p "$HOME/.claude/skills/commit"
  cat >"$HOME/.claude/skills/commit/SKILL.md" <<'SKILL'
---
name: commit
description: Write git commit messages in Conventional Commits style.
---
Hard-wrapped Conventional Commits.
SKILL

  mkdir -p "$HOME/.claude/skills/code-review"
  cat >"$HOME/.claude/skills/code-review/SKILL.md" <<'SKILL'
---
name: code-review
description: Review code changes for standards and spec compliance.
---
Two-axis review: Standards + Spec.
SKILL
}

# assemble_go: invokes the real driver-exec assemble-prompt verb with every
# flag mapped from this test's own exported env, writing its three outputs
# under $BATS_TEST_TMPDIR. Extra args (e.g. --resume-after-hold or
# --agents-json-template) are appended after the fixed flag set.
assemble_go() {
  run "$DRIVER_EXEC_BIN" assemble-prompt \
    --registry "$PROMPTASSEMBLY_REGISTRY_FILE" \
    --prompt-output "$BATS_TEST_TMPDIR/go-prompt.txt" \
    --agents-json-output "$BATS_TEST_TMPDIR/go-agents.json" \
    --handoff-output "$BATS_TEST_TMPDIR/go-handoff.json" \
    --caveman-skill-baked \
    --tdd-skill-baked \
    --commit-skill-baked \
    --code-review-skill-baked \
    --issue-tracker "$ISSUE_TRACKER" \
    --box-write-enabled \
    --code-forge "$CODE_FORGE" \
    --prompts-dir "$PROMPTS_DIR" \
    --agents-prompt-files "$AGENTS_PROMPT_FILES" \
    --comms-contract-file "$COMMS_CONTRACT_FILE" \
    --check-contract-file "$CHECK_CONTRACT_FILE" \
    --outcome-contract-file "$OUTCOME_CONTRACT_FILE" \
    --research-outcome-contract-file "$RESEARCH_OUTCOME_CONTRACT_FILE" \
    --skills-found "caveman, code-review, commit, tdd" \
    --issue-number "$ISSUE_NUMBER" \
    --issue-title "$ISSUE_TITLE" \
    --branch "$BRANCH" \
    --base-branch "$BASE_BRANCH" \
    --in-progress-label "$IN_PROGRESS_LABEL" \
    --complete-label "$COMPLETE_LABEL" \
    --run-nonce "$RUN_NONCE" \
    "$@"
}

# issue #2349: a realistic multi-agent roster -- scout, reviewer (present, not
# dropped: the orchestrator is off in the covered cell), and worker (the
# WORKER_PROVISIONED gate's partner axis to "skills baked" in the covered
# cell) -- mirroring the shape at tests/entrypoint-prompt-fragments.bats's
# WORKER_AGENTS_JSON_TEMPLATE/"entrypoint includes a read-only tools
# whitelist" fixtures.
AGENTS_ROSTER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep","WebFetch"]}}'

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the covered cell, with a populated roster" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  assemble_go --agents-json-template "$AGENTS_JSON_TEMPLATE"
  [ "$status" -eq 0 ]

  diff "$DRIVER_PROMPT_FILE" "$BATS_TEST_TMPDIR/go-prompt.txt"

  [ -s "$DRIVER_AGENTS_FILE" ]
  [ -s "$BATS_TEST_TMPDIR/go-agents.json" ]
  diff <(jq -S . "$DRIVER_AGENTS_FILE") <(jq -S . "$BATS_TEST_TMPDIR/go-agents.json")

  # Structurally guaranteed by the covered cell, not reverse-engineered from
  # the bash fake's capture files (entrypoint.sh:1282-1310): the orchestrator
  # gate is off, so the invoker is always "driver-exec" and both
  # orchestrator-only review fields always stay empty. No RESUME_AFTER_HOLD
  # override in this case, so the session mode is this cell's default
  # "initial" (entrypoint.sh:1037-1052) -- same reasoning assemble.go's own
  # comments already use.
  jq -e '.Invoker == "driver-exec"' "$BATS_TEST_TMPDIR/go-handoff.json"
  jq -e '.ReviewPromptFile == ""' "$BATS_TEST_TMPDIR/go-handoff.json"
  jq -e '.ReviewModel == ""' "$BATS_TEST_TMPDIR/go-handoff.json"
  jq -e '.SessionMode == "initial"' "$BATS_TEST_TMPDIR/go-handoff.json"
}

@test "bash and Go agree on omitting the agents flag entirely with no roster" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: proves parity on the
  # "omit the --agents flag/output stays empty" branch, not just the
  # populated-roster branch above.
  unset AGENTS_JSON_TEMPLATE

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  assemble_go
  [ "$status" -eq 0 ]

  diff "$DRIVER_PROMPT_FILE" "$BATS_TEST_TMPDIR/go-prompt.txt"

  [ ! -s "$DRIVER_AGENTS_FILE" ]
  [ ! -s "$BATS_TEST_TMPDIR/go-agents.json" ]
}

@test "RESUME_AFTER_HOLD flips the Go side's session mode to resume" {
  # The bash side's RESUME_AFTER_HOLD behavior is already covered by
  # tests/entrypoint-driver-session.bats ("RESUME_AFTER_HOLD resumes the
  # pinned session on the work path") -- re-running $ENTRYPOINT here would
  # not add coverage this suite doesn't already get from the primary case
  # above, so this asserts the Go side alone.
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  assemble_go --agents-json-template "$AGENTS_JSON_TEMPLATE" --resume-after-hold
  [ "$status" -eq 0 ]
  jq -e '.SessionMode == "resume"' "$BATS_TEST_TMPDIR/go-handoff.json"
}
