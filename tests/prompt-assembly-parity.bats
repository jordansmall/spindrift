#!/usr/bin/env bats
# Byte-parity harness (issue #2349 slice 6, extended by issue #2350 and
# #2351): runs the SAME env through both agent/entrypoint.sh's real bash
# phase_prompt_assembly (via $ENTRYPOINT and the fake driver/driver-exec
# chain, tests/helper.bash's setup_entrypoint_env) and the new Go
# `driver-exec assemble-prompt` verb (cmd/launcher/internal/promptassembly +
# driver-exec/assembleprompt_cmd.go), and asserts the two produce equivalent
# output across every Env cell promptassembly.Assemble covers. All cells
# share ISSUE_TRACKER=github, the orchestrator off, and every skill baked --
# exactly tests/box_env_gen.bash's set_box_env schema-default cell, plus
# setup_entrypoint_env's own BOX_WRITE_ENABLED=1 default -- and differ only
# on the DISPATCH_KIND/SELF_CONTAINED/FIX_PASS/RESUME_AFTER_HOLD axes and the
# CODE_FORGE/BOX_WRITE_ENABLED access/forge axis that select among the cells
# below:
#   1. plain work (DISPATCH_KIND unset, FIX_PASS 0) -- the original covered
#      cell, exercised with both a populated and an empty agent roster. Also
#      the github read-write cell on the access/forge axis (the schema
#      default).
#   2. research (DISPATCH_KIND=research)
#   3. self-contained research (DISPATCH_KIND=research, SELF_CONTAINED=1)
#   4. fix-pass (FIX_PASS>0)
#   5. github read-only (CODE_FORGE=github, BOX_WRITE_ENABLED unset)
#   6. forgejo read-write (CODE_FORGE=forgejo, BOX_WRITE_ENABLED=1)
#   7. forgejo read-only (CODE_FORGE=forgejo, BOX_WRITE_ENABLED unset)
# Every cell test funnels through the shared assert_cell_parity helper below,
# so the prompt/agents/handoff comparison logic lives in exactly one place.
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

# assert_cell_parity: runs the bash entrypoint and the Go assemble-prompt
# verb over whatever env the calling test has already exported, then asserts
# three-way parity: byte-identical prompt, byte-identical agents JSON (or
# both sides agreeing the roster is empty), and a handoff whose SessionMode
# matches the cell's expected value. Every extra arg after the expected
# session mode is forwarded to assemble_go (e.g. --dispatch-kind research).
assert_cell_parity() {
  local expected_session_mode="$1"
  shift

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  assemble_go "$@"
  [ "$status" -eq 0 ]

  diff "$DRIVER_PROMPT_FILE" "$BATS_TEST_TMPDIR/go-prompt.txt"

  if [ -s "$DRIVER_AGENTS_FILE" ]; then
    [ -s "$BATS_TEST_TMPDIR/go-agents.json" ]
    diff <(jq -S . "$DRIVER_AGENTS_FILE") <(jq -S . "$BATS_TEST_TMPDIR/go-agents.json")
  else
    [ ! -s "$BATS_TEST_TMPDIR/go-agents.json" ]
  fi

  jq -e --arg m "$expected_session_mode" '.SessionMode == $m' "$BATS_TEST_TMPDIR/go-handoff.json"
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

  assert_cell_parity initial --agents-json-template "$AGENTS_JSON_TEMPLATE"

  # Structurally guaranteed by the covered cell, not reverse-engineered from
  # the bash fake's capture files (entrypoint.sh:1282-1310): the orchestrator
  # gate is off, so the invoker is always "driver-exec" and both
  # orchestrator-only review fields always stay empty.
  jq -e '.Invoker == "driver-exec"' "$BATS_TEST_TMPDIR/go-handoff.json"
  jq -e '.ReviewPromptFile == ""' "$BATS_TEST_TMPDIR/go-handoff.json"
  jq -e '.ReviewModel == ""' "$BATS_TEST_TMPDIR/go-handoff.json"
}

@test "bash and Go agree on omitting the agents flag entirely with no roster" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: proves parity on the
  # "omit the --agents flag/output stays empty" branch, not just the
  # populated-roster branch above.
  unset AGENTS_JSON_TEMPLATE

  assert_cell_parity initial
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the research cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: this cell is about the
  # prompt-selection/session-mode axis, not roster interaction, which the
  # two tests above already cover independently.
  export DISPATCH_KIND="research"

  assert_cell_parity initial --dispatch-kind research
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the self-contained research cell" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"

  assert_cell_parity initial --dispatch-kind research --self-contained
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the fix-pass cell" {
  export FIX_PASS="1"

  assert_cell_parity resume --fix-pass 1
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the github read-only cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: this cell is about the
  # access/forge axis, not roster interaction, which the two tests at the
  # top of this file already cover independently.
  unset BOX_WRITE_ENABLED

  assert_cell_parity initial --box-write-enabled=false
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the forgejo read-write cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset, same reasoning as above.
  # BOX_WRITE_ENABLED stays at setup_entrypoint_env's read-write default, so
  # no extra flag override is needed here.
  export CODE_FORGE="forgejo"
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  # clone_repo requires FORGEJO_TOKEN and builds the clone URL as
  # https://<token>@<host>/<slug>.git; redirect that exact URL to the bare
  # repo setup_bare_repo already seeded so the clone stays offline (mirrors
  # tests/entrypoint-prompt-assembly.bats's CODE_FORGE=forgejo fix-pass test).
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"

  assert_cell_parity initial
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the forgejo read-only cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset, same reasoning as above.
  export CODE_FORGE="forgejo"
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  unset BOX_WRITE_ENABLED
  # clone_repo requires FORGEJO_TOKEN and builds the clone URL as
  # https://<token>@<host>/<slug>.git; redirect that exact URL to the bare
  # repo setup_bare_repo already seeded so the clone stays offline (mirrors
  # tests/entrypoint-prompt-assembly.bats's CODE_FORGE=forgejo fix-pass test).
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"

  assert_cell_parity initial --box-write-enabled=false
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
