#!/usr/bin/env bats
# --agents JSON composition (scout/reviewer/filer).

load helper

setup() {
  setup_entrypoint_env
}

# --agents JSON: produced by nix (builtins.toJSON), composing each subagent
# independently by its own model knob; forwarded by the entrypoint as-is after
# prompt injection. The fake claude records the --agents value to
# $CLAUDE_AGENTS_FILE for structural assertions without grepping a log that
# also contains prompt prose.
@test "entrypoint omits --agents when AGENTS_JSON_TEMPLATE is not set" {
  # AGENTS_JSON_TEMPLATE is nix-baked from SCOUT_MODEL/REVIEW_MODEL at
  # image-build time, not derived by the entrypoint at runtime, so it stays
  # unset here regardless of set_box_env's SCOUT_MODEL/REVIEW_MODEL values.
  # The entrypoint must not build JSON itself; with no template, no flag is passed.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$CLAUDE_AGENTS_FILE" ]
}

@test "entrypoint passes --agents with only scout when the template carries scout alone" {
  export AGENTS_JSON_TEMPLATE='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$CLAUDE_AGENTS_FILE" ]
  jq -e 'has("scout") and (has("reviewer") | not)' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.scout.prompt | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
}

@test "entrypoint passes --agents with only reviewer when the template carries reviewer alone" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$CLAUDE_AGENTS_FILE" ]
  jq -e 'has("reviewer") and (has("scout") | not)' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.prompt | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
}

@test "entrypoint passes --agents as a JSON object with scout and reviewer when template is set" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$CLAUDE_AGENTS_FILE" ]
  jq -e 'has("scout") and has("reviewer")' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.scout.prompt | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.prompt | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
}

@test "entrypoint forwards model fields from the nix-baked agents JSON template" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"reviewer","model":"claude-opus-4-5","prompt":"","tools":["Read","Bash","WebFetch"]},"scout":{"description":"scout","model":"claude-haiku-3-5","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e '.scout.model == "claude-haiku-3-5"' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.model == "claude-opus-4-5"' "$CLAUDE_AGENTS_FILE" >/dev/null
}

# The filer (issue #393) is opt-in and composed independently, exactly like
# scout/reviewer (#392) — never bundled with either.
@test "entrypoint passes --agents with only filer when the template carries filer alone" {
  export AGENTS_JSON_TEMPLATE='{"filer":{"description":"File issues from a review'"'"'s non-blocking findings, best-effort","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$CLAUDE_AGENTS_FILE" ]
  jq -e 'has("filer") and (has("scout") | not) and (has("reviewer") | not)' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.filer.prompt | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
}

@test "entrypoint passes --agents with scout, reviewer, and filer all present" {
  export AGENTS_JSON_TEMPLATE='{"scout":{"description":"scout","model":"opus","prompt":"","tools":["Read"]},"reviewer":{"description":"reviewer","model":"opus","prompt":"","tools":["Read"]},"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e 'has("scout") and has("reviewer") and has("filer")' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.scout.prompt | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.prompt | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.filer.prompt | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
}

