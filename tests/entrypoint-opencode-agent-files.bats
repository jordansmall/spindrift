#!/usr/bin/env bats
# On-disk opencode subagent agent-file rewrite (issue #2153): the file-rewrite
# twin of entrypoint-agents-json.bats's --agents JSON injection loop, for a
# Driver (opencode) whose subagents ride on-disk markdown files instead of a
# JSON flag.

load helper

setup() {
  setup_entrypoint_env
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
  export WORK_DIR="$BATS_TEST_TMPDIR/work-agent-files-orch-on"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ ! -f "$dir/reviewer.md" ]
  local scout_body
  scout_body="$(agent_file_body "$dir/scout.md")"
  [ "$scout_body" != "placeholder body for scout" ]
  [[ "$scout_body" == *"Return only the brief"* ]]
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
# so claude's $CLAUDE_AGENTS_FILE .scout.prompt and opencode's rewritten
# scout.md body must match byte-for-byte, modulo the single trailing newline
# the file body carries (printf '%s\n%s\n' ...) that the JSON string strips
# (command substitution trims trailing newlines).
@test "the same roster yields the same effective scout prompt under claude and opencode" {
  export AGENTS_JSON_TEMPLATE='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$CLAUDE_AGENTS_FILE" ]
  local claude_prompt
  claude_prompt="$(jq -r '.scout.prompt' "$CLAUDE_AGENTS_FILE")"
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
