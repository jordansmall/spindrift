#!/usr/bin/env bats
# On-disk opencode subagent agent-file rewrite (issue #2153): the file-rewrite
# twin of entrypoint-agents-json.bats's --agents JSON injection loop, for a
# Driver whose subagents live in on-disk markdown files instead of a JSON flag.

load helper

setup() {
  setup_entrypoint_env
  : "${DRIVER_EXEC_BIN:?DRIVER_EXEC_BIN must be set (the real driver-exec Go binary, nix/checks/promptassembly.nix)}"
  : "${PROMPTASSEMBLY_REGISTRY_FILE:?PROMPTASSEMBLY_REGISTRY_FILE must be set (lib/fragments.nix rendered to JSON, nix/checks/promptassembly.nix)}"
  : "${PROMPT_CONTRACT_REGISTRY_FILE:?PROMPT_CONTRACT_REGISTRY_FILE must be set (lib/prompt-contract.nix validateMarkers rendered to JSON, nix/checks/promptassembly.nix)}"

  # entrypoint.sh's main computes BRANCH itself and never exports it, so repeat
  # the computation here for the Go side's EnvFromEnviron().
  export BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"

  # Point the skills dirs at empty paths before the SKILLS_FOUND scan (issue
  # #2059). Left unset they default to /agent/skills and /operator-skills, and
  # a Box with its own baked skills widens the bash side's roster past the four
  # baked below, so the byte-parity tests diff a fixed --skills-found against a
  # bash side that discovered extra skills.
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/no-harness-skills"
  export OPERATOR_SKILLS_DIR="$BATS_TEST_TMPDIR/no-operator-skills"

  # The covered cell requires every per-skill gate on and a non-empty
  # SKILLS_FOUND (assemble.go's checkCoveredCell), so bake all four.
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

agent_file_body() {
  awk '/^---$/ { c++; next } c >= 2 { print }' "$1"
}

agent_file_frontmatter() {
  awk '{ print } /^---$/ { if (++c == 2) exit }' "$1"
}

# Callers assert the rewrite kept the two-fence shape rather than leaving a
# stray third fence behind.
agent_file_fence_count() {
  grep -c '^---$' "$1"
}

# The placeholder body must stay distinguishable from any real rendered prompt.
write_agent_file() {
  local path="$1" desc="$2"
  cat >"$path" <<EOF
---
description: "$desc"
mode: "subagent"
model: "opus"
---
placeholder body for $desc
EOF
}

# Rewrites the agent files in dir $1 with the real driver-exec verb. The tests
# diff only that dir, so the prompt, agents-JSON and handoff outputs land on
# throwaway paths just to satisfy the CLI's four required output flags.
# EnvFromEnviron() reads the rest from the process environment, which `run`
# inherits into the subprocess (issue #2979).
assemble_go_agent_files() {
  local dir="$1"
  shift

  run "$DRIVER_EXEC_BIN" assemble-prompt \
    --registry "$PROMPTASSEMBLY_REGISTRY_FILE" \
    --validate-markers-registry "$PROMPT_CONTRACT_REGISTRY_FILE" \
    --prompt-output "$BATS_TEST_TMPDIR/go-prompt.txt" \
    --agents-json-output "$BATS_TEST_TMPDIR/go-agents.json" \
    --handoff-output "$BATS_TEST_TMPDIR/go-handoff.json" \
    --caveman-skill-baked \
    --tdd-skill-baked \
    --commit-skill-baked \
    --code-review-skill-baked \
    --skills-found "caveman, code-review, commit, tdd" \
    --prompts-dir "$PROMPTS_DIR" \
    --agents-prompt-files "$AGENTS_PROMPT_FILES" \
    --driver-agent-files-dir "$dir" \
    --comms-contract-file "$COMMS_CONTRACT_FILE" \
    --check-contract-file "$CHECK_CONTRACT_FILE" \
    --outcome-contract-file "$OUTCOME_CONTRACT_FILE" \
    --research-outcome-contract-file "$RESEARCH_OUTCOME_CONTRACT_FILE" \
    "$@"
}

@test "entrypoint rewrites a single baked opencode agent file's body with the rendered prompt" {
  local dir="$BATS_TEST_TMPDIR/agent-files"
  mkdir -p "$dir"
  write_agent_file "$dir/scout.md" "scout"
  local frontmatter_before
  frontmatter_before="$(agent_file_frontmatter "$dir/scout.md")"
  export DRIVER_AGENT_FILES_DIR="$dir"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local body
  body="$(agent_file_body "$dir/scout.md")"
  [ -n "$body" ]
  [ "$body" != "placeholder body for scout" ]
  [[ "$body" == *"Return only the brief's path"* ]]
  [ "$(agent_file_frontmatter "$dir/scout.md")" = "$frontmatter_before" ]
}

# Byte-parity twin of the test above (issue #2353): one fixture rewritten
# independently by the bash entrypoint and the Go verb must land on identical
# bytes, not just "changed from the placeholder".
@test "bash and Go rewrite a single baked opencode agent file byte-identically" {
  local dir_bash="$BATS_TEST_TMPDIR/agent-files-bash"
  local dir_go="$BATS_TEST_TMPDIR/agent-files-go"
  mkdir -p "$dir_bash" "$dir_go"
  write_agent_file "$dir_bash/scout.md" "scout"
  write_agent_file "$dir_go/scout.md" "scout"

  export DRIVER_AGENT_FILES_DIR="$dir_bash"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  assemble_go_agent_files "$dir_go"
  [ "$status" -eq 0 ]

  diff "$dir_bash/scout.md" "$dir_go/scout.md"
}

@test "entrypoint rewrites multiple baked opencode agent files generically" {
  local dir="$BATS_TEST_TMPDIR/agent-files"
  mkdir -p "$dir"
  write_agent_file "$dir/scout.md" "scout"
  write_agent_file "$dir/worker.md" "worker"
  export DRIVER_AGENT_FILES_DIR="$dir"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local scout_body worker_body
  scout_body="$(agent_file_body "$dir/scout.md")"
  worker_body="$(agent_file_body "$dir/worker.md")"
  [ "$scout_body" != "placeholder body for scout" ]
  [[ "$scout_body" == *"Return only the brief's path"* ]]
  [ "$worker_body" != "placeholder body for worker" ]
  [[ "$worker_body" == *"Stay inside the slice you were handed"* ]]
}

# A custom Nth agent (issue #264, roster) must get its baked file rewritten the
# same generic way as the built-in names, with no per-name branch in the
# entrypoint. This copies the real PROMPTS_DIR rather than starting from an
# empty dir so every other file phase_prompt_assembly reads still resolves. The
# auditor prompt names ISSUE_NUMBER, so this also proves substitution ran.
@test "entrypoint rewrites a custom Nth agent's baked file generically via AGENTS_PROMPT_FILES" {
  local prompt_dir="$BATS_TEST_TMPDIR/custom-prompts"
  cp -r "$PROMPTS_DIR" "$prompt_dir"
  chmod -R u+w "$prompt_dir"
  printf 'issue ${ISSUE_NUMBER} body\n' >"$prompt_dir/auditor-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export AGENTS_PROMPT_FILES='{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md","auditor":"auditor-prompt.md"}'

  local dir="$BATS_TEST_TMPDIR/agent-files"
  mkdir -p "$dir"
  write_agent_file "$dir/auditor.md" "audit"
  export DRIVER_AGENT_FILES_DIR="$dir"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local body
  body="$(agent_file_body "$dir/auditor.md")"
  [ "$body" = "issue 7 body" ]
}

@test "entrypoint drops the reviewer's baked opencode agent file when the orchestrator is on" {
  local dir="$BATS_TEST_TMPDIR/agent-files"
  mkdir -p "$dir"
  write_agent_file "$dir/scout.md" "scout"
  write_agent_file "$dir/reviewer.md" "reviewer"
  export DRIVER_AGENT_FILES_DIR="$dir"
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-agent-files-orch-on"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$dir/reviewer.md" ]
  local scout_body
  scout_body="$(agent_file_body "$dir/scout.md")"
  [ "$scout_body" != "placeholder body for scout" ]
  [[ "$scout_body" == *"Return only the brief's path"* ]]
}

# Byte-parity twin of the reviewer-drop test above and the --review-model test
# below (issue #2353). Bash reports its review model through the Handoff
# descriptor it hands the orchestrator via --handoff-file (issue #2975), whose
# path this recovers from the argv $ORCHESTRATOR_LOG recorded; Go writes its
# own handoff JSON.
@test "bash and Go drop reviewer.md and recover the same --review-model when the orchestrator is on" {
  local dir_bash="$BATS_TEST_TMPDIR/agent-files-bash"
  local dir_go="$BATS_TEST_TMPDIR/agent-files-go"
  mkdir -p "$dir_bash" "$dir_go"
  write_agent_file "$dir_bash/scout.md" "scout"
  write_agent_file "$dir_bash/reviewer.md" "reviewer"
  write_agent_file "$dir_go/scout.md" "scout"
  write_agent_file "$dir_go/reviewer.md" "reviewer"

  export DRIVER_AGENT_FILES_DIR="$dir_bash"
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-agent-files-orch-on-parity"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  assemble_go_agent_files "$dir_go"
  [ "$status" -eq 0 ]

  [ ! -f "$dir_bash/reviewer.md" ]
  [ ! -f "$dir_go/reviewer.md" ]
  diff "$dir_bash/scout.md" "$dir_go/scout.md"

  jq -e '.ReviewModel == "opus"' "$(handoff_path_from_log "$ORCHESTRATOR_LOG")"
  jq -e '.ReviewModel == "opus"' "$BATS_TEST_TMPDIR/go-handoff.json"
}

# Issue #2278: file-based twin of the JSON path's --review-model forwarding
# (issue #2277). The configured model rides reviewer.md's `model:` frontmatter
# scalar instead of AGENTS_JSON_TEMPLATE's .reviewer.model, and must be
# extracted before the reviewer.md removal above drops it.
@test "entrypoint forwards --review-model from the reviewer's baked opencode agent file when the orchestrator is on" {
  local dir="$BATS_TEST_TMPDIR/agent-files"
  mkdir -p "$dir"
  write_agent_file "$dir/reviewer.md" "reviewer"
  export DRIVER_AGENT_FILES_DIR="$dir"
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-agent-files-review-model"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ "$(jq -r .ReviewModel "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "opus" ]
}

# No reviewer.md at all (issue #392's empty-model-drops-the-file rule) means no
# configured model to extract, so entrypoint.sh must omit --review-model
# entirely rather than pass it empty.
@test "entrypoint omits --review-model when no reviewer baked opencode agent file exists" {
  local dir="$BATS_TEST_TMPDIR/agent-files"
  mkdir -p "$dir"
  write_agent_file "$dir/scout.md" "scout"
  export DRIVER_AGENT_FILES_DIR="$dir"
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-agent-files-no-review-model"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ "$(jq -r .ReviewModel "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "" ]
}

@test "entrypoint rewrites the reviewer's baked opencode agent file when the orchestrator is off" {
  local dir="$BATS_TEST_TMPDIR/agent-files"
  mkdir -p "$dir"
  write_agent_file "$dir/reviewer.md" "reviewer"
  export DRIVER_AGENT_FILES_DIR="$dir"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -f "$dir/reviewer.md" ]
  local reviewer_body
  reviewer_body="$(agent_file_body "$dir/reviewer.md")"
  [ -n "$reviewer_body" ]
  [ "$reviewer_body" != "placeholder body for reviewer" ]
}

# Byte-parity twin of the test above (issue #2353).
@test "bash and Go rewrite the reviewer's baked opencode agent file byte-identically when the orchestrator is off" {
  local dir_bash="$BATS_TEST_TMPDIR/agent-files-bash"
  local dir_go="$BATS_TEST_TMPDIR/agent-files-go"
  mkdir -p "$dir_bash" "$dir_go"
  write_agent_file "$dir_bash/reviewer.md" "reviewer"
  write_agent_file "$dir_go/reviewer.md" "reviewer"

  export DRIVER_AGENT_FILES_DIR="$dir_bash"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  assemble_go_agent_files "$dir_go"
  [ "$status" -eq 0 ]

  [ -f "$dir_bash/reviewer.md" ]
  [ -f "$dir_go/reviewer.md" ]
  diff "$dir_bash/reviewer.md" "$dir_go/reviewer.md"
}

@test "entrypoint skips a roster agent with no baked opencode agent file without error" {
  local dir="$BATS_TEST_TMPDIR/agent-files"
  mkdir -p "$dir"
  # Reviewer, filer and worker are in AGENTS_PROMPT_FILES but have no baked
  # file, mirroring the opencode empty-model case where nothing gets baked.
  write_agent_file "$dir/scout.md" "scout"
  export DRIVER_AGENT_FILES_DIR="$dir"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$dir/reviewer.md" ]
  [ ! -f "$dir/filer.md" ]
  [ ! -f "$dir/worker.md" ]
  local scout_body
  scout_body="$(agent_file_body "$dir/scout.md")"
  [ "$scout_body" != "placeholder body for scout" ]
}

@test "entrypoint leaves opencode agent files untouched when DRIVER_AGENT_FILES_DIR is unset" {
  local dir="$BATS_TEST_TMPDIR/agent-files-untouched"
  mkdir -p "$dir"
  write_agent_file "$dir/scout.md" "scout"
  local before
  before="$(cat "$dir/scout.md")"
  unset DRIVER_AGENT_FILES_DIR

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ "$(cat "$dir/scout.md")" = "$before" ]
}

# Cross-Driver parity (issue #2153, AC3): the same roster must yield the same
# subagent prompt under either Driver. Claude's .scout.prompt and opencode's
# rewritten scout.md body must match byte-for-byte, except for the single
# trailing newline the file body carries and the JSON string does not, because
# command substitution trims it.
@test "the same roster yields the same effective scout prompt under claude and opencode" {
  export AGENTS_JSON_TEMPLATE='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_AGENTS_FILE" ]
  local claude_prompt
  claude_prompt="$(jq -r '.scout.prompt' "$DRIVER_AGENTS_FILE")"
  [ -n "$claude_prompt" ]

  # Fresh state for the opencode-side run: no --agents JSON flag, since opencode
  # composes subagents from on-disk files, and a distinct WORK_DIR, because the
  # first run left a non-empty checkout behind.
  unset AGENTS_JSON_TEMPLATE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-opencode"
  local dir="$BATS_TEST_TMPDIR/agent-files-parity"
  mkdir -p "$dir"
  write_agent_file "$dir/scout.md" "scout"
  export DRIVER_AGENT_FILES_DIR="$dir"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local opencode_body
  opencode_body="$(agent_file_body "$dir/scout.md")"
  [ -n "$opencode_body" ]
  # Trim one trailing newline so the body compares equal to the JSON string,
  # which command substitution already stripped.
  opencode_body="${opencode_body%$'\n'}"

  [ "$opencode_body" = "$claude_prompt" ]
}

# Cross-half integration case (issue #2262). Deriving DRIVER_AGENT_FILES_DIR
# from the real rendered preamble instead of retyping the relative path means
# a drift between agentFilesTemplate's on-disk path and agentFilesDirRelative
# fails this test instead of leaving it silently pinned.
@test "entrypoint rewrites the real baked opencode agent-files template output, preserving frontmatter and the two-fence shape" {
  eval "$(grep '^DRIVER_AGENT_FILES_DIR=' "$OPENCODE_DRIVER_PREAMBLE_FILE")"
  local relative="${DRIVER_AGENT_FILES_DIR#/home/agent/}"
  local dir="$BATS_TEST_TMPDIR/agent-files-real/$relative"
  mkdir -p "$dir"
  cp "$OPENCODE_AGENT_FILES/home/agent/$relative/"*.md "$dir/"
  # cp preserves the store's read-only bits, and the entrypoint rewrites these
  # files in place.
  chmod u+w "$dir"/*.md

  local scout="$dir/scout.md"
  [ -f "$scout" ]
  local frontmatter_before
  frontmatter_before="$(agent_file_frontmatter "$scout")"
  [ "$(agent_file_fence_count "$scout")" -eq 2 ]

  export DRIVER_AGENT_FILES_DIR="$dir"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ "$(agent_file_fence_count "$scout")" -eq 2 ]
  [ "$(agent_file_frontmatter "$scout")" = "$frontmatter_before" ]
  local body
  body="$(agent_file_body "$scout")"
  [ -n "$body" ]
  [ "$body" != "Map relevant files, seams, and tests; return a structured brief" ]
  [[ "$body" == *"Return only the brief's path"* ]]

  local reviewer="$dir/reviewer.md"
  local reviewer_body
  reviewer_body="$(agent_file_body "$reviewer")"
  [ -n "$reviewer_body" ]
  [ "$reviewer_body" != "Review the branch diff for spec compliance and coding standards" ]
  [[ "$reviewer_body" == *"adversarially review a branch diff"* ]]

  local worker="$dir/worker.md"
  local worker_body
  worker_body="$(agent_file_body "$worker")"
  [ -n "$worker_body" ]
  [ "$worker_body" != "Implement a scoped slice of work delegated to it, with full implement-capable tools" ]
  [[ "$worker_body" == *"Stay inside the slice you were handed"* ]]
}
