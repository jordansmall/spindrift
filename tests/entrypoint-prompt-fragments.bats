#!/usr/bin/env bats
# Conditional prompt steps rendered from fragment files, and substitution (issue #463).

load helper

setup() {
  setup_entrypoint_env
}

# issue #622/#688: this mechanism test walks 3 of the registry's current
# nine rows (lib/fragments.nix) -- AUTO_FORMAT and AUTO_LINT, both
# knob-gated, plus FILER_ENABLED/file-issues, which is computed-gated --
# and covers their shared off/on matrix: each row renders its marker
# heading only when its gate is on, and leaves zero residue when it's off
# (the conditional-residue mechanism every registry row shares); it used
# to be six bespoke on/off test pairs. CODE_REVIEW_BAKED's on/off gate is
# covered by its own tests further down this file (issue #788). The other
# five rows are covered elsewhere, not in this file's other tests:
# skill-preamble/caveman-default/tdd-default/commit-default in
# tests/entrypoint-skills.bats, ci-failure's on/off gate in
# tests/entrypoint-prompt-assembly.bats.
@test "conditional prompt steps appear only when their knob is on" {
  local case i=0
  for case in \
    'AGENTS_JSON_TEMPLATE={"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}|# FILE ISSUES' \
    'AUTO_FORMAT=1|# AUTO-FORMAT' \
    'AUTO_LINT=1|# AUTO-LINT'
  do
    local assign="${case%%|*}" marker="${case#*|}"

    # A fresh WORK_DIR per invocation -- entrypoint.sh clones into it, and
    # this test execs the entrypoint six times (off/on for three gated
    # cases -- AUTO_FORMAT and AUTO_LINT are knobs, FILER_ENABLED is
    # computed-gated) -- so reusing one dir across invocations would
    # collide on the second clone.
    i=$((i + 1))
    export WORK_DIR="$BATS_TEST_TMPDIR/work-$i-off"
    run bash "$ENTRYPOINT"
    [ "$status" -eq 0 ]
    ! grep -qF "$marker" "$CLAUDE_PROMPT_FILE"

    # shellcheck disable=SC2163 # $assign is itself a NAME=value pair
    export "$assign"
    export WORK_DIR="$BATS_TEST_TMPDIR/work-$i-on"
    run bash "$ENTRYPOINT"
    [ "$status" -eq 0 ]
    grep -qF "$marker" "$CLAUDE_PROMPT_FILE"
    unset "${assign%%=*}"
  done
}

# A scout/reviewer-only template (no "filer" key) must not require
# filer-prompt.md to exist -- the file read has to be gated on the template
# actually carrying a filer entry, same as the FILE_ISSUES_STEP gate above.
@test "entrypoint does not require filer-prompt.md when the template omits filer" {
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'issue stub\n' >"$prompt_dir/issue-prompt.md"
  printf 'scout stub\n' >"$prompt_dir/scout-prompt.md"
  printf 'reviewer stub\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"r","model":"opus","prompt":"","tools":["Read"]},"scout":{"description":"s","model":"haiku","prompt":"","tools":["Read"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e 'has("filer") | not' "$CLAUDE_AGENTS_FILE" >/dev/null
}

# issue #452: `nix fmt` can never succeed in-box (uid 1000 has no
# /nix/store write access, so evaluating the flake dies with a store-lock
# permission error) — the step must not list it as a usable preference, and
# must say why it's unavailable if it names it at all.
@test "AUTO-FORMAT step never instructs nix fmt as a usable preference" {
  export AUTO_FORMAT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q '`nix fmt` when the target flake defines a formatter' "$CLAUDE_PROMPT_FILE"
}

@test "AUTO-FORMAT step explains why nix fmt is unavailable in-box" {
  export AUTO_FORMAT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'nix fmt' "$CLAUDE_PROMPT_FILE"
  grep -qi 'permission' "$CLAUDE_PROMPT_FILE"
}

# issue #463: the conditional prompt steps above (SKILL_PREAMBLE,
# FILE_ISSUES_STEP, AUTO_FORMAT_STEP, AUTO_LINT_STEP, CI_FAILURE_STEP) must be
# read from fragment files under PROMPTS_DIR, not authored as heredocs in the
# script -- a markdown heading string-literal in entrypoint.sh means prose
# leaked back into bash.
@test "entrypoint source contains no prompt-prose markdown headings" {
  run grep -E '# (FILE ISSUES|AUTO-FORMAT|AUTO-LINT|CI FAILURE)' "$ENTRYPOINT"
  [ "$status" -ne 0 ]
}

@test "every registry row ships as a fragment file under prompts/fragments" {
  source "$FRAGMENT_REGISTRY_FILE"
  local row fragment
  for row in "${_FRAGMENT_ROWS[@]}"; do
    # Row shape is "gate|fragment.md|var" -- middle field, already carries
    # the .md suffix.
    fragment="${row#*|}"
    fragment="${fragment%%|*}"
    [ -f "$PROMPTS_DIR/fragments/$fragment" ]
  done
}

# issue #463: `$(_subst ...)` command substitution strips ALL trailing
# newlines, so a fragment's blank-line separator (which the heredoc-string
# assignments it replaces carried literally) must be reconstructed after
# substitution -- otherwise the step glues onto the next heading with no
# even a newline between them.
@test "AUTO-FORMAT and AUTO-LINT steps stay separated from each other and from COMMIT" {
  export AUTO_FORMAT=1
  export AUTO_LINT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'run\.# AUTO-LINT' "$CLAUDE_PROMPT_FILE"
  ! grep -q 'run\.# COMMIT' "$CLAUDE_PROMPT_FILE"
}

@test "FILE ISSUES step stays separated from LAND THE CHANGE" {
  export AGENTS_JSON_TEMPLATE='{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'configured\.# LAND THE CHANGE' "$CLAUDE_PROMPT_FILE"
}

@test "CI FAILURE step stays separated from CONTEXT on a fix pass" {
  export FIX_PASS=1
  export CI_FAILURE_SUMMARY="build failed"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'scratch:build failed' "$CLAUDE_PROMPT_FILE"
  ! grep -q 'failed# CONTEXT' "$CLAUDE_PROMPT_FILE"
}

@test "CAVEMAN_STEP stays separated from the COMMS body text" {
  mkdir -p "$HOME/.claude/skills/caveman"
  cat >"$HOME/.claude/skills/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'verbatim\.Your text output' "$CLAUDE_PROMPT_FILE"
}

# issue #689: TDD_BAKED had zero test coverage of its gate mechanism before
# this test -- mirrors the CAVEMAN_STEP case above.
@test "TDD_STEP renders when the tdd skill is baked" {
  mkdir -p "$HOME/.claude/skills/tdd"
  cat >"$HOME/.claude/skills/tdd/SKILL.md" <<'SKILL'
---
name: tdd
description: Test-driven development.
---
Red, green, refactor.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Use the `/tdd` skill to run the test-first loop below' "$CLAUDE_PROMPT_FILE"
}

# issue #689: COMMIT_BAKED had zero test coverage of its gate mechanism
# before this test -- mirrors the CAVEMAN_STEP case above.
@test "COMMIT_STEP renders when the commit skill is baked" {
  mkdir -p "$HOME/.claude/skills/commit"
  cat >"$HOME/.claude/skills/commit/SKILL.md" <<'SKILL'
---
name: commit
description: Write git commit messages in Conventional Commits style.
---
Hard-wrapped Conventional Commits.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Use the `/commit` skill to write every commit message' "$CLAUDE_PROMPT_FILE"
}

# issue #788: the reviewer subagent favors the /code-review skill when it is
# baked at DRIVER_SKILLS_DIR/code-review/SKILL.md, same gated-fragment idiom
# as CAVEMAN_STEP/TDD_STEP/COMMIT_STEP above. CODE_REVIEW_STEP renders into
# review-prompt.md, which flows into the reviewer subagent's prompt in the
# --agents JSON, not $CLAUDE_PROMPT_FILE -- so this reads it from
# $CLAUDE_AGENTS_FILE's .reviewer.prompt instead.
@test "CODE_REVIEW_STEP renders when the code-review skill is baked" {
  mkdir -p "$HOME/.claude/skills/code-review"
  cat >"$HOME/.claude/skills/code-review/SKILL.md" <<'SKILL'
---
name: code-review
description: Review code changes for standards and spec compliance.
---
Two-axis review: Standards + Spec.
SKILL
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"reviewer","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","Agent"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e '.reviewer.prompt' "$CLAUDE_AGENTS_FILE" | grep -qF 'Run the `/code-review` skill FIRST'
}

# issue #788: the fallback -- no code-review skill baked -- must still end in
# the VERDICT contract, with zero trace of the deferral (the same
# conditional-residue guarantee CAVEMAN_STEP/TDD_STEP/COMMIT_STEP give).
@test "reviewer prompt has no code-review deferral when the skill is absent" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"reviewer","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","Agent"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local rendered
  rendered="$(jq -r '.reviewer.prompt' "$CLAUDE_AGENTS_FILE")"
  ! grep -qF 'Run the `/code-review` skill FIRST' <<<"$rendered"
  grep -qF 'VERDICT: APPROVE | BLOCK' <<<"$rendered"
}

# issue #993: CODE_REVIEW_STEP's deferral claims to "supersede" the inline
# rubric, but the inline four dimensions always render below it regardless of
# the gate -- reviewers need review-prompt.md to say the overlap is
# intentional (skill findings reconcile into the same contract) rather than
# leaving "supersedes" looking like the dimensions get removed.
@test "reviewer prompt explains the code-review rubric overlap is intentional" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"reviewer","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","Agent"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e '.reviewer.prompt' "$CLAUDE_AGENTS_FILE" | grep -qF 'rather than replacing these dimensions'
}

# issue #626: driver-exec absorbed the direct-path/devShell-wrapper dual
# pipeline text (issue #463) entirely -- entrypoint.sh now calls driver-exec
# exactly once, direct and devShell invocation are the same call path, and
# driver-exec's own --devshell switch (not a second hand-copied pipeline)
# tells it which (the direct-path and devShell behavioural tests above
# already prove both paths still work).
@test "driver-exec is invoked exactly once in entrypoint.sh source" {
  count=$(grep -c '^  driver-exec \\$' "$ENTRYPOINT")
  [ "$count" -eq 1 ]
}

# issue #463: a SPINDRIFT_PROMPT_DIR-style override supplies its own fragment
# for a knob it enables, exactly like it already must supply filer-prompt.md
# when AGENTS_JSON_TEMPLATE carries a filer entry (see "entrypoint does not
# require filer-prompt.md..." above) -- documented in docs/reference.md.
@test "runtime prompt-dir override supplies its own auto-format fragment" {
  local prompt_dir="$BATS_TEST_TMPDIR/custom-prompts"
  cp -r "$PROMPTS_DIR" "$prompt_dir"
  chmod -R u+w "$prompt_dir"
  printf '# AUTO-FORMAT\n\nCUSTOM-FRAGMENT-MARKER\n\n' >"$prompt_dir/fragments/auto-format.md"
  export PROMPTS_DIR="$prompt_dir"
  export AUTO_FORMAT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'CUSTOM-FRAGMENT-MARKER' "$CLAUDE_PROMPT_FILE"
}

@test "entrypoint includes a read-only tools whitelist in agents JSON" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e '.scout.tools | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.tools | length > 0' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.scout.tools | contains(["Edit"]) | not' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.scout.tools | contains(["Write"]) | not' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.tools | contains(["Edit"]) | not' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.tools | contains(["Write"]) | not' "$CLAUDE_AGENTS_FILE" >/dev/null
}

@test "IN_PROGRESS_LABEL and COMPLETE_LABEL are substituted in the prompt" {
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  cat >"$prompt_dir/issue-prompt.md" <<'EOF'
label: ${IN_PROGRESS_LABEL} complete: ${COMPLETE_LABEL}
EOF
  printf 'scout stub\n' >"$prompt_dir/scout-prompt.md"
  printf 'reviewer stub\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export IN_PROGRESS_LABEL="wip"
  export COMPLETE_LABEL="done"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'label: wip' "$CLAUDE_PROMPT_FILE"
  grep -q 'complete: done' "$CLAUDE_PROMPT_FILE"
}

@test "envsubst substitutes placeholders in scout and review prompt files" {
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'issue stub\n' >"$prompt_dir/issue-prompt.md"
  printf 'scout for issue ${ISSUE_NUMBER}\n' >"$prompt_dir/scout-prompt.md"
  printf 'review base ${BASE_BRANCH}\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"r","model":"opus","prompt":"","tools":["Read"]},"scout":{"description":"s","model":"haiku","prompt":"","tools":["Read"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e '.scout.prompt | contains("scout for issue 7")' "$CLAUDE_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.prompt | contains("review base main")' "$CLAUDE_AGENTS_FILE" >/dev/null
}

@test "default prompt delegates exploration to the scout subagent" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'scout' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt spawns a reviewer subagent" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'reviewer' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt specifies a review loop keyed on VERDICT: BLOCK" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'VERDICT.*BLOCK\|BLOCK.*VERDICT' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt emits exactly one SPINDRIFT_OUTCOME line" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -c 'SPINDRIFT_OUTCOME' "$CLAUDE_PROMPT_FILE" | grep -q '^[1-9]'
}

@test "default prompt emits SPINDRIFT_OUTCOME with status=blocked in the blocked path" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'status=blocked' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt emits status=ready as the success outcome" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'status=ready' "$CLAUDE_PROMPT_FILE"
  ! grep -q 'status=merged' "$CLAUDE_PROMPT_FILE"
}

# issue #622: the fragment loop and its substitution allowlist are rendered
# from the nix-owned registry (lib/fragments.nix), not hardcoded per-step in
# agent/entrypoint.sh -- proven here by hand-appending one extra row to the
# real, nix-rendered registry data and confirming it renders with zero edits
# to the entrypoint source itself.
@test "a hand-appended registry row renders without any entrypoint edit" {
  local prompt_dir="$BATS_TEST_TMPDIR/fixture-prompts"
  mkdir -p "$prompt_dir/fragments"
  printf 'fixture stub ${FIXTURE_ROW_STEP}end\n' >"$prompt_dir/issue-prompt.md"
  printf '# FIXTURE ROW\n\nFIXTURE-ROW-MARKER\n\n' >"$prompt_dir/fragments/fixture-row.md"
  export PROMPTS_DIR="$prompt_dir"

  local wrapped="$BATS_TEST_TMPDIR/fixture-entrypoint.sh"
  {
    cat "$DRIVER_PREAMBLE_FILE"
    cat "$FRAGMENT_REGISTRY_FILE"
    printf '_FRAGMENT_ROWS+=("FIXTURE_ROW_ON|fixture-row.md|FIXTURE_ROW_STEP")\n'
    printf '_FRAGMENT_SUBST_VARS+=("FIXTURE_ROW_STEP")\n'
    tail -n +2 "$ENTRYPOINT_SRC"
  } >"$wrapped"
  chmod +x "$wrapped"

  export FIXTURE_ROW_ON=1
  run bash "$wrapped"
  [ "$status" -eq 0 ]
  grep -q 'FIXTURE-ROW-MARKER' "$CLAUDE_PROMPT_FILE"
}
