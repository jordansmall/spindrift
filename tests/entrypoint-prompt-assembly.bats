#!/usr/bin/env bats
# Prompt rendering, FIX_PASS routing, and CI_FAILURE_SUMMARY (issues #425, #426).

load helper

setup() {
  setup_entrypoint_env
}

@test "entrypoint renders the prompt with issue placeholders substituted" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Implement GitHub issue #7: Do the thing" "$DRIVER_PROMPT_FILE"
  grep -q "agent/issue-7" "$DRIVER_PROMPT_FILE"
  grep -q "cut from" "$DRIVER_PROMPT_FILE"
}

# entrypoint.sh has no hardcoded fallback for PROMPTS_DIR (or the other baked
# /agent/* path vars): lib/preambles.nix's renderAgentPathsPreamble emits
# `PROMPTS_DIR=${PROMPTS_DIR:-/agent/prompts}` ahead of the entrypoint body,
# both in the real image and in this suite (helper.bash prepends
# AGENT_PATHS_PREAMBLE_FILE). Without that wiring, an unset PROMPTS_DIR would
# hit a bare `"$PROMPTS_DIR"` under `set -u` and die with "unbound variable".
#
# Whether the run then *succeeds* depends on an environment this test doesn't
# control: a real image has /agent/prompts baked in, while a nix-sandboxed check
# build has no /agent at all. Assert only what must hold in both: never "unbound
# variable", then branch on $status. Scoped to the full
# "/agent/prompts/issue-prompt.md" path, not the bare directory -- an unrelated
# failure merely echoing the --prompts-dir value could satisfy that without
# proving the read resolved through the preamble default.
@test "PROMPTS_DIR unset still resolves via the preamble default (not unbound)" {
  unset PROMPTS_DIR
  run bash "$ENTRYPOINT"
  [[ "$output" != *"unbound variable"* ]]
  if [ "$status" -eq 0 ]; then
    grep -q "Implement GitHub issue #7: Do the thing" "$DRIVER_PROMPT_FILE"
  else
    [[ "$output" == *"/agent/prompts/issue-prompt.md"* ]]
  fi
}

# RUN_NONCE (issue #1937): the launcher mints a per-run nonce and forwards it
# as RUN_NONCE so control-signal prompt fragments can reference it; nothing
# gates on it yet, but it must reach the rendered prompt the Box sees.
@test "RUN_NONCE is rendered into the prompt" {
  export RUN_NONCE="deadbeefcafe1234"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF "deadbeefcafe1234" "$DRIVER_PROMPT_FILE"
}

@test "the configured mkHarness prompt is what reaches claude" {
  : "${PROMPT_HARNESS_DIR:?PROMPT_HARNESS_DIR must be set by the check}"
  export PROMPTS_DIR="$PROMPT_HARNESS_DIR"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "CONFIGURED-PROMPT-MARKER" "$DRIVER_PROMPT_FILE"
  grep -q "Implement issue #7: Do the thing on agent/issue-7" "$DRIVER_PROMPT_FILE"
}

# FIX_PASS (issue #425): the launcher sets FIX_PASS on a fix box (dispatched
# when CI comes back red) so the entrypoint drives a dedicated warm fix-prompt
# instead of the cold issue-prompt a fresh run uses.
@test "FIX_PASS unset drives issue-prompt.md, not fix-prompt.md" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$DRIVER_PROMPT_FILE"
  ! grep -q "already checked out" "$DRIVER_PROMPT_FILE"
}

@test "FIX_PASS=0 still drives issue-prompt.md (byte-identical to unset)" {
  export FIX_PASS="0"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$DRIVER_PROMPT_FILE"
  ! grep -q "already checked out" "$DRIVER_PROMPT_FILE"
}

@test "FIX_PASS>0 drives fix-prompt.md instead of issue-prompt.md" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "already checked out" "$DRIVER_PROMPT_FILE"
  ! grep -q "Fresh clone, new branch" "$DRIVER_PROMPT_FILE"
}

# fix-prompt.md's amend-vs-new-commit bullet used to assert the branch "already
# force-pushes" — true under BOX_ACCESS_READ_WRITE, but a read-only Box never
# pushes at all; the launcher's BundleRelay force-relays host-side instead. The
# wording must hold under either access mode (issue #2462).
@test "fix-prompt.md's history-rewrite line doesn't presuppose the Box pushed" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q "The branch already force-pushes" "$DRIVER_PROMPT_FILE"
  grep -q "force-relayed by the launcher's BundleRelay" "$DRIVER_PROMPT_FILE"
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
  grep -q "lint: FAILURE" "$DRIVER_PROMPT_FILE"
  grep -q "2 errors in main.go" "$DRIVER_PROMPT_FILE"
}

@test "CI_FAILURE_SUMMARY unset on a fix pass falls back with no error" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "already checked out" "$DRIVER_PROMPT_FILE"
  ! grep -q '\${CI_FAILURE_SUMMARY}' "$DRIVER_PROMPT_FILE"
}

@test "CI_FAILURE_SUMMARY is ignored on a fresh (non-fix) run" {
  export CI_FAILURE_SUMMARY="lint: FAILURE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$DRIVER_PROMPT_FILE"
  ! grep -q "lint: FAILURE" "$DRIVER_PROMPT_FILE"
}

# The fix-pass CONTEXT CI-read step forks on CODE_FORGE (issue #1963): `gh pr
# view`/`gh run list`/`gh run view` don't exist against a Forgejo remote, so a
# forgejo fix pass reads CI via `fj pr status` instead.
@test "fix pass on CODE_FORGE=forgejo reads CI via fj pr status, never gh pr view" {
  export FIX_PASS="2"
  export CODE_FORGE=forgejo
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  # clone_repo requires FORGEJO_TOKEN and builds the clone URL as
  # https://<token>@<host>/<slug>.git; redirect that exact URL to the bare repo
  # setup_bare_repo already seeded so the clone stays offline.
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Scoped to the fix pass's own CONTEXT section: the shared contract appended
  # below it keeps its own unconditional `gh pr view` regardless of CODE_FORGE,
  # so a whole-file grep would false-positive.
  local context_section
  context_section="$(awk '/^# CONTEXT/,/^# FIX/' "$DRIVER_PROMPT_FILE")"
  grep -qF 'fj pr status' <<<"$context_section"
  ! grep -qF 'gh pr view' <<<"$context_section"
}

@test "fix pass on default (github) CODE_FORGE reads CI via gh pr view, never fj pr status" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local context_section
  context_section="$(awk '/^# CONTEXT/,/^# FIX/' "$DRIVER_PROMPT_FILE")"
  grep -qF 'gh pr view' <<<"$context_section"
  ! grep -qF 'fj pr status' <<<"$context_section"
}

# Handoff.ReviewPromptFile is only ever populated when Assemble actually renders
# a review prompt (orchestrator on, fresh-work dispatch, FixPass == 0). Every
# other cell -- like this suite's default -- must not leak that mktemp'd file
# for the life of the Box. DRIVER_REVIEW_PROMPT_TMP_FILE is the test-only hook
# entrypoint.sh writes the real mktemp path to, mirroring DRIVER_HANDOFF_FILE.
@test "the review-prompt temp file is removed on a cell that renders no review prompt" {
  export DRIVER_REVIEW_PROMPT_TMP_FILE="$BATS_TEST_TMPDIR/review-prompt-tmp-path"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$(cat "$DRIVER_REVIEW_PROMPT_TMP_FILE")" ]
}

# Contrast the removal test above: the orchestrator-on cell does render a review
# prompt, so the same temp file must survive, non-empty, at the path
# Handoff.ReviewPromptFile names -- the later review pass still reads it.
@test "the review-prompt temp file survives, non-empty, on a cell that renders one" {
  export DRIVER_REVIEW_PROMPT_TMP_FILE="$BATS_TEST_TMPDIR/review-prompt-tmp-path"
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local review_prompt_tmp_path
  review_prompt_tmp_path="$(cat "$DRIVER_REVIEW_PROMPT_TMP_FILE")"
  [ -s "$review_prompt_tmp_path" ]
  [ "$(jq -r .ReviewPromptFile "$(handoff_path_from_log "$ORCHESTRATOR_LOG")")" = "$review_prompt_tmp_path" ]
}

