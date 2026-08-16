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

# entrypoint.sh's own hardcoded fallback default for PROMPTS_DIR (and the
# other 7 baked /agent/* path vars) was removed in favor of the nix-rendered
# agent-paths preamble (issue #2531): lib/preambles.nix's
# renderAgentPathsPreamble emits `PROMPTS_DIR=${PROMPTS_DIR:-/agent/prompts}`
# -- unquoted, since its escapeShellArg only single-quote-wraps a value that
# contains characters outside `[[:alnum:],._+:@%/-]`, and `/agent/prompts` has
# none -- ahead of the entrypoint body, both in the real image (lib/image.nix)
# and in this bats suite (tests/helper.bash prepends AGENT_PATHS_PREAMBLE_FILE
# the same way it already does for DRIVER_PREAMBLE_FILE/FRAGMENT_REGISTRY_FILE,
# issue #433/#622). Without that wiring, an unset PROMPTS_DIR would hit
# entrypoint.sh's bare `"$PROMPTS_DIR"` reference under `set -u` and die with
# "unbound variable" partway through, instead of resolving to the real baked
# default -- a weaker entrypoint than production ever runs.
#
# Whether the resulting run then *succeeds* depends on an environment this
# test doesn't control: a real production image (or this dogfood Box, built
# from one) genuinely has /agent/prompts baked in, so the run completes; a
# real nix-sandboxed check build has no /agent directory at all (only nix
# store paths and the build dir), so `driver-exec assemble-prompt` fails
# trying to read /agent/prompts/issue-prompt.md. Assert only what must hold
# in both: never "unbound variable", then branch on $status -- a successful
# run must have actually rendered the prompt (same as the sibling test
# above); a failing one must still show the resolved preamble default
# (/agent/prompts) in its output, proving PROMPTS_DIR got there via the
# preamble rather than by crashing unbound or resolving to something else.
@test "PROMPTS_DIR unset still resolves via the preamble default (not unbound)" {
  unset PROMPTS_DIR
  run bash "$ENTRYPOINT"
  [[ "$output" != *"unbound variable"* ]]
  if [ "$status" -eq 0 ]; then
    grep -q "Implement GitHub issue #7: Do the thing" "$DRIVER_PROMPT_FILE"
  else
    [[ "$output" == *"/agent/prompts"* ]]
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

# fix-prompt.md's amend-vs-new-commit bullet used to assert the branch
# "already force-pushes" — true under BOX_ACCESS_READ_WRITE, but a read-only
# Box never pushes at all; the launcher's BundleRelay force-relays the branch
# host-side instead. The wording must hold under either access mode (issue
# #2462).
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

