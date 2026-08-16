#!/usr/bin/env bats
# On-disk opencode subagent agent-file rewrite (issue #2153): the file-rewrite
# twin of entrypoint-agents-json.bats's --agents JSON injection loop, for a
# Driver (opencode) whose subagents ride on-disk markdown files instead of a
# JSON flag.

load helper

setup() {
  setup_entrypoint_env
  : "${DRIVER_EXEC_BIN:?DRIVER_EXEC_BIN must be set (the real driver-exec Go binary, nix/checks/promptassembly.nix)}"
  : "${PROMPTASSEMBLY_REGISTRY_FILE:?PROMPTASSEMBLY_REGISTRY_FILE must be set (lib/fragments.nix rendered to JSON, nix/checks/promptassembly.nix)}"
  : "${PROMPT_CONTRACT_REGISTRY_FILE:?PROMPT_CONTRACT_REGISTRY_FILE must be set (lib/prompt-contract.nix validateMarkers rendered to JSON, nix/checks/promptassembly.nix)}"

  # BRANCH is computed inside entrypoint.sh's main (BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}",
  # entrypoint.sh:55), not exported by set_box_env/setup_entrypoint_env --
  # reproduce the same computation here so the Go side's --branch flag
  # matches what the bash side derives at runtime (tests/prompt-assembly-parity.bats's setup()).
  BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"

  # Bake all four skills (tests/prompt-assembly-parity.bats's setup()
  # pattern): the covered cell requires every per-skill gate on and a
  # non-empty SKILLS_FOUND (assemble.go's checkCoveredCell), for both the
  # orchestrator-off and orchestrator-on cells this file's Go-side byte-parity
  # tests exercise below.
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

# Extracts the body of a baked opencode agent file: every line after the
# second "---" frontmatter delimiter.
agent_file_body() {
  awk '/^---$/ { c++; next } c >= 2 { print }' "$1"
}

# Extracts the frontmatter of a baked opencode agent file: every line up to
# and including the second "---" frontmatter delimiter.
agent_file_frontmatter() {
  awk '{ print } /^---$/ { if (++c == 2) exit }' "$1"
}

# Counts frontmatter fence lines ("---") in a baked opencode agent file --
# used to assert the rewrite preserves the two-fence shape (one open, one
# close) rather than e.g. leaving a stray third fence behind.
agent_file_fence_count() {
  grep -c '^---$' "$1"
}

# Writes a baked opencode agent file fixture with real frontmatter shape and a
# placeholder body distinguishable from any real rendered prompt.
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

# assemble_go_agent_files: invokes the real driver-exec assemble-prompt verb
# with the fixed flag set the covered cell needs (mirrors
# tests/prompt-assembly-parity.bats's assemble_go(), simplified for this
# file's job -- proving the ON-DISK agent-file rewrite is byte-identical, not
# the prompt/agents-JSON parity that file already covers). --prompt-output/
# --agents-json-output/--handoff-output all land on throwaway paths under
# $BATS_TEST_TMPDIR (the CLI still requires all four output flags, but
# nothing here diffs the prompt/agents-JSON bytes against the bash side).
# $1 is the on-disk agent-files dir this invocation rewrites in place
# (--driver-agent-files-dir); --agents-prompt-files "$AGENTS_PROMPT_FILES"
# feeds the same roster-keyed rewrite loop the bash side reads. Every skill
# is baked (setup() above), matching the covered cell's skills-fully-baked
# rule. Extra args (e.g. --orchestrator-enabled, --agents-json-template ...)
# are appended after the fixed flag set.
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
    --issue-tracker "$ISSUE_TRACKER" \
    --box-write-enabled \
    --code-forge "$CODE_FORGE" \
    --prompts-dir "$PROMPTS_DIR" \
    --agents-prompt-files "$AGENTS_PROMPT_FILES" \
    --driver-agent-files-dir "$dir" \
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
  [[ "$body" == *"Return only the brief"* ]]
  [ "$(agent_file_frontmatter "$dir/scout.md")" = "$frontmatter_before" ]
}

# Byte-parity twin of the test just above (issue #2353): the SAME fixture
# rewritten independently by both the bash entrypoint and the Go
# `driver-exec assemble-prompt` verb must land on byte-identical output, not
# just "changed from the placeholder" like the bash-only test above already
# checks.
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
  [[ "$scout_body" == *"Return only the brief"* ]]
  [ "$worker_body" != "placeholder body for worker" ]
  [[ "$worker_body" == *"Stay inside the slice you were handed"* ]]
}

# A custom Nth agent (issue #264, roster) must get its baked file rewritten
# the same generic way as the built-in names -- no per-name branch in the
# entrypoint. AGENTS_PROMPT_FILES (nix-baked from the roster) maps each agent
# name to its prompt file under PROMPTS_DIR; here it names a custom
# "auditor-prompt.md" that lives only in this test's own prompt dir. Copied
# from the real PROMPTS_DIR (rather than a bare empty dir) so every other
# fragment/prompt file phase_prompt_assembly reads along the way still
# resolves -- only the extra auditor-prompt.md is genuinely new. Its content
# references ISSUE_NUMBER, so this test also verifies runtime substitution
# actually ran (setup_entrypoint_env sets ISSUE_NUMBER=7).
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
  [[ "$scout_body" == *"Return only the brief"* ]]
}

# Byte-parity twin of the reviewer-drop test just above plus the
# --review-model forwarding test below (issue #2353): both bash and Go must
# drop reviewer.md, rewrite the remaining roster file byte-identically, and
# recover the SAME review model -- bash from $ORCHESTRATOR_LOG's recorded
# argv, Go from its own handoff JSON's .ReviewModel.
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

  assemble_go_agent_files "$dir_go" --orchestrator-enabled
  [ "$status" -eq 0 ]

  [ ! -f "$dir_bash/reviewer.md" ]
  [ ! -f "$dir_go/reviewer.md" ]
  diff "$dir_bash/scout.md" "$dir_go/scout.md"

  grep -q -- '--review-model opus' "$ORCHESTRATOR_LOG"
  jq -e '.ReviewModel == "opus"' "$BATS_TEST_TMPDIR/go-handoff.json"
}

# Issue #2278: file-based twin of entrypoint-orchestrator-handoff.bats's
# "orchestrator path forwards --review-model from the reviewer's configured
# model" (issue #2277, JSON path). Here the reviewer's configured model rides
# the baked reviewer.md's `model:` frontmatter scalar instead of
# AGENTS_JSON_TEMPLATE's .reviewer.model, but it must reach the orchestrator's
# --review-model flag the same way, extracted before the reviewer.md removal
# just above drops it.
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

  grep -q -- '--review-model opus' "$ORCHESTRATOR_LOG"
}

# Mirrors entrypoint-orchestrator-handoff.bats's "orchestrator path omits
# --review-model when no reviewer model is configured": on the opencode
# file-based path, no reviewer.md at all (the #392 empty-model-drops-the-file
# semantics) means no configured model to extract -- entrypoint.sh must omit
# --review-model entirely rather than pass it empty.
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

  ! grep -q -- '--review-model' "$ORCHESTRATOR_LOG"
}

@test "entrypoint rewrites the reviewer's baked opencode agent file when the orchestrator is off" {
  # Parity with the JSON loop's off-row: with the orchestrator off, reviewer
  # is not dropped -- its baked file is rewritten like any other roster entry.
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

# Byte-parity twin of the test just above (issue #2353): with the
# orchestrator off, neither side drops reviewer.md -- both must rewrite its
# body byte-identically, like any other roster entry.
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
  # Only scout has a baked file -- reviewer/filer/worker (also in
  # AGENTS_PROMPT_FILES via setup_entrypoint_env) do not, mirroring the
  # opencode-side empty-model case where no file gets baked at all.
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
# effective subagent prompt content under either Driver. Both mechanisms
# derive scout's prompt from the identical _subst "$PROMPTS_DIR/scout-prompt.md"
# call (entrypoint.sh's --agents JSON injection loop and its
# DRIVER_AGENT_FILES_DIR-gated file-rewrite twin, agent/entrypoint.sh:784+),
# so claude's $DRIVER_AGENTS_FILE .scout.prompt and opencode's rewritten
# scout.md body must match byte-for-byte, modulo the single trailing newline
# the file body carries (printf '%s\n%s\n' ...) that the JSON string strips
# (command substitution trims trailing newlines).
@test "the same roster yields the same effective scout prompt under claude and opencode" {
  export AGENTS_JSON_TEMPLATE='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$DRIVER_AGENTS_FILE" ]
  local claude_prompt
  claude_prompt="$(jq -r '.scout.prompt' "$DRIVER_AGENTS_FILE")"
  [ -n "$claude_prompt" ]

  # Fresh state for the opencode-side run: no --agents JSON flag (opencode
  # composes subagents from on-disk files, not this flag), a distinct
  # WORK_DIR (the first run's checkout is non-empty, same reasoning as
  # entrypoint-driver-session.bats's independent-cold-runs test), and a baked
  # scout.md this run's DRIVER_AGENT_FILES_DIR-gated rewrite loop rewrites in
  # place.
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
  # Trim exactly one trailing newline from the file body so it compares
  # equal to the JSON string, which command substitution already stripped of
  # its own trailing newline.
  opencode_body="${opencode_body%$'\n'}"

  [ "$opencode_body" = "$claude_prompt" ]
}

# Cross-half integration case (issue #2262): renders agent files through the
# REAL baked agentFilesTemplate (lib/drivers/opencode.nix:132-152) instead of
# write_agent_file's hand-written fixture, and derives DRIVER_AGENT_FILES_DIR
# from the REAL rendered preamble (lib/drivers/default.nix's renderPreamble)
# instead of retyping the relative path -- so if agentFilesTemplate's
# on-disk path (opencode.nix:139) ever drifts from agentFilesDirRelative
# (opencode.nix:40), the two Nix-rendered artifacts land at different
# relative paths and this test fails instead of staying silently pinned.
@test "entrypoint rewrites the real baked opencode agent-files template output, preserving frontmatter and the two-fence shape" {
  eval "$(grep '^DRIVER_AGENT_FILES_DIR=' "$OPENCODE_DRIVER_PREAMBLE_FILE")"
  local relative="${DRIVER_AGENT_FILES_DIR#/home/agent/}"
  local dir="$BATS_TEST_TMPDIR/agent-files-real/$relative"
  mkdir -p "$dir"
  cp "$OPENCODE_AGENT_FILES/home/agent/$relative/"*.md "$dir/"
  # The store path is read-only; the entrypoint rewrites these files in
  # place, so give the copies write permission (the store's own bits don't
  # carry over usefully here since cp preserves them).
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
  [[ "$body" == *"Return only the brief"* ]]

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
