#!/usr/bin/env bats
# Prompt rendering, FIX_PASS routing, and CI_FAILURE_SUMMARY (issues #425, #426).

load helper

setup() {
  setup_entrypoint_env
}

@test "entrypoint renders the prompt with issue placeholders substituted" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Implement GitHub issue #7: Do the thing" "$CLAUDE_PROMPT_FILE"
  grep -q "agent/issue-7" "$CLAUDE_PROMPT_FILE"
  grep -q "cut from" "$CLAUDE_PROMPT_FILE"
}

# RUN_NONCE (issue #1937): the launcher mints a per-run nonce and forwards it
# as RUN_NONCE so control-signal prompt fragments can reference it; nothing
# gates on it yet, but it must reach the rendered prompt the Box sees.
@test "RUN_NONCE is rendered into the prompt" {
  export RUN_NONCE="deadbeefcafe1234"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF "deadbeefcafe1234" "$CLAUDE_PROMPT_FILE"
}

@test "the configured mkHarness prompt is what reaches claude" {
  : "${PROMPT_HARNESS_DIR:?PROMPT_HARNESS_DIR must be set by the check}"
  export PROMPTS_DIR="$PROMPT_HARNESS_DIR"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "CONFIGURED-PROMPT-MARKER" "$CLAUDE_PROMPT_FILE"
  grep -q "Implement issue #7: Do the thing on agent/issue-7" "$CLAUDE_PROMPT_FILE"
}

# FIX_PASS (issue #425): the launcher sets FIX_PASS on a fix box (dispatched
# when CI comes back red) so the entrypoint drives a dedicated warm fix-prompt
# instead of the cold issue-prompt a fresh run uses.
@test "FIX_PASS unset drives issue-prompt.md, not fix-prompt.md" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
  ! grep -q "already checked out" "$CLAUDE_PROMPT_FILE"
}

@test "FIX_PASS=0 still drives issue-prompt.md (byte-identical to unset)" {
  export FIX_PASS="0"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
  ! grep -q "already checked out" "$CLAUDE_PROMPT_FILE"
}

@test "FIX_PASS>0 drives fix-prompt.md instead of issue-prompt.md" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "already checked out" "$CLAUDE_PROMPT_FILE"
  ! grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
}

# CI_FAILURE_SUMMARY (issue #426): the launcher captures the concrete CI
# failure on genuine-red and forwards it to the fix box so the fix agent goes
# straight to the failing check instead of re-discovering it from scratch.
@test "CI_FAILURE_SUMMARY set on a fix pass is rendered into the prompt" {
  export FIX_PASS="2"
  export CI_FAILURE_SUMMARY="lint: FAILURE
2 errors in main.go"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "lint: FAILURE" "$CLAUDE_PROMPT_FILE"
  grep -q "2 errors in main.go" "$CLAUDE_PROMPT_FILE"
}

@test "CI_FAILURE_SUMMARY unset on a fix pass falls back with no error" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "already checked out" "$CLAUDE_PROMPT_FILE"
  ! grep -q '\${CI_FAILURE_SUMMARY}' "$CLAUDE_PROMPT_FILE"
}

@test "CI_FAILURE_SUMMARY is ignored on a fresh (non-fix) run" {
  export CI_FAILURE_SUMMARY="lint: FAILURE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
  ! grep -q "lint: FAILURE" "$CLAUDE_PROMPT_FILE"
}

# The fix-pass CONTEXT CI-read step forks on CODE_FORGE (issue #1963): `gh pr
# view`/`gh run list`/`gh run view` don't exist against a Forgejo remote, so a
# forgejo fix pass reads CI via `fj pr status` instead.
@test "fix pass on CODE_FORGE=forgejo reads CI via fj pr status, never gh pr view" {
  export FIX_PASS="2"
  export CODE_FORGE=forgejo
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  # clone_repo requires FORGEJO_TOKEN and builds the clone URL as
  # https://<token>@<host>/<slug>.git; redirect that exact URL to the bare
  # repo setup_bare_repo already seeded so the clone stays offline (mirrors
  # tests/entrypoint-clone.bats's CODE_FORGE=forgejo clone test).
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Scoped to the fix pass's own CONTEXT section: the shared contract
  # appended below it (IF BLOCKED's own PR step, out of this issue's scope)
  # legitimately keeps its own unconditional `gh pr view` regardless of
  # CODE_FORGE, so a whole-file grep would false-positive on it.
  local context_section
  context_section="$(awk '/^# CONTEXT/,/^# FIX/' "$CLAUDE_PROMPT_FILE")"
  grep -qF 'fj pr status' <<<"$context_section"
  ! grep -qF 'gh pr view' <<<"$context_section"
}

@test "fix pass on default (github) CODE_FORGE reads CI via gh pr view, never fj pr status" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local context_section
  context_section="$(awk '/^# CONTEXT/,/^# FIX/' "$CLAUDE_PROMPT_FILE")"
  grep -qF 'gh pr view' <<<"$context_section"
  ! grep -qF 'fj pr status' <<<"$context_section"
}

