#!/usr/bin/env bats
# Byte-parity harness (issue #2349 slice 6, extended by issue #2350, #2351,
# #2352, and #2353): runs the SAME env through both agent/entrypoint.sh's
# real bash phase_prompt_assembly (via $ENTRYPOINT and the fake driver/
# driver-exec chain, tests/helper.bash's setup_entrypoint_env) and the new Go
# `driver-exec assemble-prompt` verb (cmd/launcher/internal/promptassembly +
# driver-exec/assembleprompt_cmd.go), and asserts the two produce equivalent
# output across every Env cell promptassembly.Assemble covers. The
# orchestrator-off cells 1-11 share the orchestrator off and every skill
# baked -- exactly tests/box_env_gen.bash's set_box_env schema-default cell,
# plus setup_entrypoint_env's own BOX_WRITE_ENABLED=1 default -- and differ
# only on three orthogonal axes:
#
#   DISPATCH_KIND/SELF_CONTAINED/FIX_PASS/RESUME_AFTER_HOLD, with
#   ISSUE_TRACKER/CODE_FORGE/BOX_WRITE_ENABLED held at their defaults:
#     1. plain work (DISPATCH_KIND unset, FIX_PASS 0) -- the original
#        covered cell, exercised with both a populated and an empty agent
#        roster. Also the github read-write cell on the access/forge axis
#        (the schema default).
#     2. research (DISPATCH_KIND=research)
#     3. self-contained research (DISPATCH_KIND=research, SELF_CONTAINED=1)
#     4. fix-pass (FIX_PASS>0)
#
#   CODE_FORGE/BOX_WRITE_ENABLED access/forge axis, with dispatch
#   kind/fix-pass/ISSUE_TRACKER untouched:
#     5. github read-only (CODE_FORGE=github, BOX_WRITE_ENABLED unset)
#     6. forgejo read-write (CODE_FORGE=forgejo, BOX_WRITE_ENABLED=1)
#     7. forgejo read-only (CODE_FORGE=forgejo, BOX_WRITE_ENABLED unset)
#
#   ISSUE_TRACKER, with SessionMode held at "initial" (dispatch kind/fix-pass
#   untouched):
#     8. local, no issue reference (ISSUE_TRACKER=local)
#     9. local, issue-reference knob on (ISSUE_TRACKER=local,
#        LOCAL_ISSUE_REFERENCE=1)
#     10. forgejo, read-write (ISSUE_TRACKER=forgejo)
#     11. jira, which rides the github prompt-selection arms
#         (ISSUE_TRACKER=jira)
#
# The orchestrator-on cells 12-14 (issue #2353) all share dispatch kind
# "work" (default) with FIX_PASS unset -- the only path checkCoveredCell
# covers combined with the orchestrator on -- and differ only on the
# roster/skills axes:
#   12. orchestrator on, filer-on -- roster carries a "filer" key alongside
#       reviewer and scout (FILER_ENABLED on).
#   13. orchestrator on, filer-off -- roster carries reviewer and scout but
#       no "filer" key (FILER_ENABLED off).
#   14. orchestrator on, skills-absent -- no skill baked at all, contrasting
#       setup()'s unconditional 4-skill baking every other cell relies on.
#
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
#
# PARITY_SKILLS_BAKED gates the four --*-skill-baked bool flags and the
# non-empty --skills-found value below. Left unset (or "1"), every existing
# cell gets today's unchanged behavior: all four skills baked. Go's flag
# package has no "--no-caveman-skill-baked" form to un-set a bool flag once
# passed (unlike a string flag's value, which "$@" can override last-wins),
# so the skills-absent cell can't turn these off by appending an override --
# instead, a caller exports PARITY_SKILLS_BAKED=0 before calling, and this
# function omits the flags entirely, leaving every one at the CLI's own
# false/"" default.
assemble_go() {
  local skill_flags=()
  if [ "${PARITY_SKILLS_BAKED:-1}" = "1" ]; then
    skill_flags=(
      --caveman-skill-baked
      --tdd-skill-baked
      --commit-skill-baked
      --code-review-skill-baked
      --skills-found "caveman, code-review, commit, tdd"
    )
  fi

  run "$DRIVER_EXEC_BIN" assemble-prompt \
    --registry "$PROMPTASSEMBLY_REGISTRY_FILE" \
    --prompt-output "$BATS_TEST_TMPDIR/go-prompt.txt" \
    --agents-json-output "$BATS_TEST_TMPDIR/go-agents.json" \
    --handoff-output "$BATS_TEST_TMPDIR/go-handoff.json" \
    "${skill_flags[@]}" \
    --issue-tracker "$ISSUE_TRACKER" \
    --box-write-enabled \
    --code-forge "$CODE_FORGE" \
    --prompts-dir "$PROMPTS_DIR" \
    --agents-prompt-files "$AGENTS_PROMPT_FILES" \
    --comms-contract-file "$COMMS_CONTRACT_FILE" \
    --check-contract-file "$CHECK_CONTRACT_FILE" \
    --outcome-contract-file "$OUTCOME_CONTRACT_FILE" \
    --research-outcome-contract-file "$RESEARCH_OUTCOME_CONTRACT_FILE" \
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

# assert_review_handoff_parity: for the orchestrator-on cells (issue #2353),
# asserts the Handoff facts assert_cell_parity's own prompt/agents-JSON/
# SessionMode checks don't cover -- Invoker, ReviewPromptFile, and
# ReviewModel, all only ever populated with the orchestrator on. ReviewModel
# is a genuine byte-exact parity assertion: both sides recover the same
# short literal model name (bash from $ORCHESTRATOR_LOG's recorded argv, Go
# from its own handoff JSON). ReviewPromptFile can only be asserted non-empty
# on both sides -- run_driver_in_env writes and deletes its temp file
# entirely inside a single phase downstream of phase_prompt_assembly, so its
# content is already gone by the time bats inspects anything (the same
# constraint entrypoint-orchestrator-handoff.bats's own
# "--review-prompt-file carrying a real path" test documents).
assert_review_handoff_parity() {
  jq -e '.Invoker == "orchestrator"' "$BATS_TEST_TMPDIR/go-handoff.json"
  jq -e '.ReviewPromptFile != ""' "$BATS_TEST_TMPDIR/go-handoff.json"

  grep -q -- '--review-prompt-file' "$ORCHESTRATOR_LOG"
  local bash_review_prompt_file
  bash_review_prompt_file="$(grep -oE -- '--review-prompt-file [^ ]+' "$ORCHESTRATOR_LOG" | awk '{print $2}')"
  [ -n "$bash_review_prompt_file" ]

  local bash_review_model
  bash_review_model="$(grep -oE -- '--review-model [^ ]+' "$ORCHESTRATOR_LOG" | awk '{print $2}')"
  [ "$bash_review_model" = "$(jq -r '.ReviewModel' "$BATS_TEST_TMPDIR/go-handoff.json")" ]
}

# issue #2349: a realistic multi-agent roster -- scout, reviewer (present, not
# dropped: the orchestrator is off in the covered cell), and worker (the
# WORKER_PROVISIONED gate's partner axis to "skills baked" in the covered
# cell) -- mirroring the shape at tests/entrypoint-prompt-fragments.bats's
# WORKER_AGENTS_JSON_TEMPLATE/"entrypoint includes a read-only tools
# whitelist" fixtures.
AGENTS_ROSTER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep","WebFetch"]}}'

# issue #2353: AGENTS_ROSTER plus a "filer" entry (the shape
# tests/entrypoint-agents-json.bats:64 already exercises -- "File issues from
# a review's non-blocking findings, best-effort"), so the orchestrator-on
# filer-on cell below actually flips the FILER_ENABLED gate on, unlike
# AGENTS_ROSTER alone (the filer-off cell's roster).
AGENTS_ROSTER_WITH_FILER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep","WebFetch"]},"filer":{"description":"File issues from a review'"'"'s non-blocking findings, best-effort","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'

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

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the local tracker cell, no issue reference" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: this cell is about the
  # tracker axis, not roster interaction, which the two dispatch-kind cells
  # above already cover independently. SessionMode stays "initial" --
  # ISSUE_TRACKER is orthogonal to dispatch kind/fix-pass.
  export ISSUE_TRACKER="local"

  assert_cell_parity initial
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the local tracker cell, issue reference on" {
  export ISSUE_TRACKER="local"
  export LOCAL_ISSUE_REFERENCE="1"

  assert_cell_parity initial --local-issue-reference
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the forgejo tracker cell" {
  export ISSUE_TRACKER="forgejo"

  assert_cell_parity initial
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the jira tracker cell" {
  # jira rides the same prompt-selection arms as github (assemble.go's
  # checkCoveredCell). The Go side's byte-identity between jira and github
  # is already pinned by a Go unit test in
  # cmd/launcher/internal/promptassembly; this cell additionally proves the
  # BASH side renders jira through that same github arm, so parity holds at
  # the bash/Go boundary too, not just within the Go package.
  export ISSUE_TRACKER="jira"

  assert_cell_parity initial
}

# issue #2353: three orchestrator-on cells (dispatch kind "work", FIX_PASS
# unset -- the only orchestrator-on path checkCoveredCell covers). Unlike
# cells 1-4 above, ORCHESTRATOR_ENABLED must be exported so the bash side
# takes run_driver_in_env's orchestrator invocation path (entrypoint.sh:
# 1282-1286, tests/entrypoint-orchestrator-handoff.bats), and
# --orchestrator-enabled forwarded to assemble_go so the Go side matches.

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the orchestrator-on filer-on cell" {
  export ORCHESTRATOR_ENABLED=1
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_FILER"

  assert_cell_parity initial --orchestrator-enabled --agents-json-template "$AGENTS_JSON_TEMPLATE"

  assert_review_handoff_parity
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the orchestrator-on filer-off cell" {
  export ORCHESTRATOR_ENABLED=1
  # AGENTS_ROSTER (not the _WITH_FILER variant above): scout+reviewer, no
  # "filer" key -- the FILER_ENABLED-off half of the roster axis. Reviewer is
  # present in both filer-on and filer-off (filer-on/off forks only on
  # whether "filer" itself is in the roster), so both cells assert
  # ReviewModel parity the same way via assert_review_handoff_parity.
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"

  assert_cell_parity initial --orchestrator-enabled --agents-json-template "$AGENTS_JSON_TEMPLATE"

  assert_review_handoff_parity
}

@test "bash and Go agree byte-for-byte on prompt/agents/handoff for the orchestrator-on skills-absent cell" {
  export ORCHESTRATOR_ENABLED=1
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"

  # Contrast setup()'s unconditional 4-skill baking: this cell is the
  # "SkillsFound == "" and every *SkillBaked flag false" branch
  # checkCoveredCell also covers. Remove the skill dirs setup() just baked
  # under $HOME (rather than restructuring setup() itself, which every other
  # cell in this suite still relies on baking unconditionally), and tell
  # assemble_go to omit its own skill-baked flags/value the same way.
  rm -rf "$HOME/.claude/skills"
  export PARITY_SKILLS_BAKED=0

  assert_cell_parity initial --orchestrator-enabled --agents-json-template "$AGENTS_JSON_TEMPLATE"

  assert_review_handoff_parity
}
