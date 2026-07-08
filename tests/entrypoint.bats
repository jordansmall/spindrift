#!/usr/bin/env bats
# Behaviour of the in-container entrypoint: clone, branch, render prompt, and
# hand off to the (stubbed) agent. No network, no real LLM.

load helper

setup() {
  setup_fakes
  setup_bare_repo
  export REPO_SLUG="owner/repo"
  export GH_TOKEN="fake-token"
  export GIT_USER_NAME="Bot"
  export GIT_USER_EMAIL="bot@example.com"
  export BASE_BRANCH="main"
  export BRANCH_PREFIX="agent/issue-"
  # These are baked by the nix preamble at image-build time (env-schema.nix defaults);
  # set explicitly here so the raw script runs correctly in the bats suite.
  export MODEL="claude-opus-4-8"
  export SCOUT_MODEL=""
  export REVIEW_MODEL=""
  export IN_PROGRESS_LABEL="agent-in-progress"
  export COMPLETE_LABEL="agent-complete"
  export DEV_SHELL_PROBE_TIMEOUT=300
  export DEV_SHELL_NAME=default
  export ISSUE_NUMBER="7"
  export ISSUE_TITLE="Do the thing"
  export WORK_DIR="$BATS_TEST_TMPDIR/work"
}

@test "entrypoint clones the target repo and cuts the issue branch" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "agent/issue-7" ]
}

@test "entrypoint renders the prompt with issue placeholders substituted" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Implement GitHub issue #7: Do the thing" "$CLAUDE_PROMPT_FILE"
  grep -q "agent/issue-7" "$CLAUDE_PROMPT_FILE"
  grep -q "cut from" "$CLAUDE_PROMPT_FILE"
}

@test "the configured mkHarness prompt is what reaches claude" {
  : "${PROMPT_HARNESS_DIR:?PROMPT_HARNESS_DIR must be set by the check}"
  export PROMPTS_DIR="$PROMPT_HARNESS_DIR"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "CONFIGURED-PROMPT-MARKER" "$CLAUDE_PROMPT_FILE"
  grep -q "Implement issue #7: Do the thing on agent/issue-7" "$CLAUDE_PROMPT_FILE"
}

@test "entrypoint invokes claude headlessly with skip-permissions" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "claude invoked for issue #7" "$CLAUDE_LOG"
  grep -q -- "--dangerously-skip-permissions" "$CLAUDE_LOG"
}

@test "entrypoint passes MODEL env var to claude" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--model claude-opus-4-8" "$CLAUDE_LOG"
}

@test "MODEL env overrides the baked default model at runtime" {
  export MODEL="claude-sonnet-4-6"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--model claude-sonnet-4-6" "$CLAUDE_LOG"
  ! grep -q -- "--model claude-opus-4-8" "$CLAUDE_LOG"
}

# Observability (#113): text --print emits nothing until the end, so the box
# looks dead under `podman logs -f`. stream-json is the only --print mode that
# emits events in realtime.
@test "entrypoint runs claude in stream-json mode so activity streams live" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--output-format stream-json" "$CLAUDE_LOG"
  grep -q -- "--verbose" "$CLAUDE_LOG"
}

# In-box heartbeat view (#183): the entrypoint pipes claude's output through
# spindrift-heartbeat-filter so a human can `tail -f /tmp/heartbeat.log` inside
# the box and see coarse status lines instead of raw NDJSON. Raw stream-json
# still reaches stdout unchanged for the launcher's byte-exact capture.

@test "entrypoint writes coarse heartbeat log at /tmp/heartbeat.log" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -f /tmp/heartbeat.log ]
}

@test "heartbeat log contains status lines, not raw NDJSON" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Heartbeat lines look like "#7 · …", not raw JSON objects.
  grep -q '^#' /tmp/heartbeat.log
  ! grep -q '"type":' /tmp/heartbeat.log
}

# Regression (#123): logs/issue-<n>.log is the sole input to outcome.Classify
# (transient-vs-terminal retry) and outcome.LastInLog. #123 routed the console
# through a lossy formatter that collapsed each event to a summary, stripping the
# raw JSON — including rate_limit_error / resetsAt markers — so retryable
# rate-limit exits were misread as terminal. The raw stream-json must reach
# stdout verbatim; human-readable rendering is a host-side viewer over the log.
@test "entrypoint streams the raw stream-json to stdout for failure classification" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" | grep -q '"type":"result"'
  printf '%s\n' "$output" | grep -q '"type":"assistant"'
}

# The launcher greps '^SPINDRIFT_OUTCOME ' from the container log. Under
# stream-json the outcome is buried in a JSON result event, so the entrypoint
# must surface it as a bare line to keep that contract.
@test "entrypoint re-emits the agent's SPINDRIFT_OUTCOME as a bare line" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}

@test "entrypoint runs the configured prefetch hook inside the work tree" {
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'FAKE'
echo "warmed $PWD for #${ISSUE_NUMBER:-?}" >>"$PREFETCH_LOG"
FAKE
  } >"$FAKE_BIN/warm-cache"
  chmod +x "$FAKE_BIN/warm-cache"
  export PREFETCH="warm-cache"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "warmed" "$PREFETCH_LOG"
  grep -q "$WORK_DIR" "$PREFETCH_LOG"
}

# --agents JSON: produced by nix (builtins.toJSON) when both subagent models are
# configured; forwarded by the entrypoint as-is after prompt injection.
# The fake claude records the --agents value to $CLAUDE_AGENTS_FILE for
# structural assertions without grepping a log that also contains prompt prose.
@test "entrypoint omits --agents when AGENTS_JSON_TEMPLATE is not set" {
  # Default setup: SCOUT_MODEL="" REVIEW_MODEL="" → AGENTS_JSON_TEMPLATE not set
  # The entrypoint must not build JSON itself; with no template, no flag is passed.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$CLAUDE_AGENTS_FILE" ]
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

# --- re-dispatch idempotency (issue #217) ------------------------------------
# The in-box push must use --force-with-lease so a retry from a different base
# replaces the prior run's branch state rather than colliding non-fast-forward.

@test "default prompt instructs agent to push with --force-with-lease" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--force-with-lease' "$CLAUDE_PROMPT_FILE"
}

@test "re-dispatched box force-resets a stale remote branch (no open PR)" {
  # Simulate a prior run that pushed agent/issue-7 with a commit, then died
  # before opening a PR.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "stale content from prior run" > "$prior/stale.txt"
  git -C "$prior" add -A
  git -C "$prior" commit -q -m "feat: prior run commit"
  git -C "$prior" push -q origin "agent/issue-7"
  # No FAKE_GH_PR_LIST_7 → gh pr list returns empty → no open PR

  # A re-dispatch should succeed and start clean from main.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Entrypoint logged the force-reset.
  [[ "$output" == *"force-resetting"* ]]

  # The remote branch was force-reset, so a plain push from the clean
  # work-tree succeeds without a non-fast-forward rejection.
  echo "new work" > "$WORK_DIR/new.txt"
  git -C "$WORK_DIR" add -A
  git -C "$WORK_DIR" commit -q -m "feat: new work"
  run git -C "$WORK_DIR" push origin "agent/issue-7"
  [ "$status" -eq 0 ]
}

@test "re-dispatched box skips force-reset when an open PR exists on the stale branch" {
  # Simulate a prior run that pushed commits AND opened a PR, then died before
  # printing SPINDRIFT_OUTCOME.  The entrypoint must not destroy the branch so
  # the #122 adoption path can still recover the run.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "prior run work" > "$prior/prior.txt"
  git -C "$prior" add -A
  git -C "$prior" commit -q -m "feat: prior run commit"
  git -C "$prior" push -q origin "agent/issue-7"
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Entrypoint logged that it skipped the force-reset.
  [[ "$output" == *"skipping force-reset"* ]]

  # The stale commit is still on the remote branch (not force-reset).
  stale_sha="$(git -C "$BATS_TEST_TMPDIR/prior" rev-parse HEAD)"
  run git -C "$WORK_DIR" ls-remote origin "refs/heads/agent/issue-7"
  [ "$status" -eq 0 ]
  [[ "$output" == "$stale_sha"* ]]
}

@test "entrypoint detects devShell and logs when flake.nix has a devShell" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "devShell"
  echo "$output" | grep -q "nix develop"
}

@test "entrypoint logs fallback when flake.nix has no devShell" {
  seed_flake_repo
  # FAKE_NIX_DEV_SHELL_OK defaults to 0 — nix develop will fail
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "flake.nix"
  echo "$output" | grep -q "baked toolchain"
}

@test "entrypoint times out the devShell probe and falls back to baked toolchain" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_HANG=1
  export DEV_SHELL_PROBE_TIMEOUT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "timed out"
  echo "$output" | grep -q "baked toolchain"
}

@test "entrypoint skips devShell probe when repo has no flake.nix" {
  # standard setup_bare_repo has no flake.nix
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "devShell"
}

@test "entrypoint skips the prefetch hook when it is empty" {
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'FAKE'
echo ran >>"$PREFETCH_LOG"
FAKE
  } >"$FAKE_BIN/warm-cache"
  chmod +x "$FAKE_BIN/warm-cache"
  export PREFETCH=""
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -f "$PREFETCH_LOG" ]
}

# --- skills dir discovery path (issue #118) -----------------------------------
# Claude Code discovers skills from $HOME/.claude/skills/. In the box HOME is
# /home/agent (mkHarness.nix sets HOME=/home/agent for OCI; bwrap.go passes
# --setenv HOME /home/agent). The entrypoint invokes `claude -p` which
# discovers skills from HOME. The fake claude stub mirrors real discovery:
# it scans $HOME/.claude/skills/*.md and logs each file found. The test
# seeds a skill there and asserts the fake claude discovers it, proving the
# full discovery path without requiring a live LLM.
@test "headless agent discovers a skill seeded at HOME/.claude/skills" {
  mkdir -p "$HOME/.claude/skills"
  cat >"$HOME/.claude/skills/test-skill.md" <<'SKILL'
---
name: test-skill
description: A stub skill used only by this test.
---
Do the test thing.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # The fake claude reports each discovered skill; assert this one was found.
  grep -q "skill discovered: test-skill.md" "$CLAUDE_LOG"
}

# --- prompt skill preference (issue #120) -------------------------------------
# When a skill is present at HOME/.claude/skills/, the rendered prompt must
# direct the agent to use it. When absent, the inline guidance stands alone
# with no skill reference — the inline path is the floor, the skill the upgrade.

@test "prompt references available skill when present at HOME/.claude/skills" {
  mkdir -p "$HOME/.claude/skills"
  cat >"$HOME/.claude/skills/tdd.md" <<'SKILL'
---
name: tdd
description: Test-driven development skill.
---
Use TDD.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'tdd' "$CLAUDE_PROMPT_FILE"
}

@test "prompt contains no skill reference when HOME/.claude/skills is empty" {
  # No skills seeded — inline guidance must stand alone; the word "skill"
  # must not appear so agents on skill-free boxes get only the inline path.
  mkdir -p "$HOME/.claude/skills"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qi '\bskill\b' "$CLAUDE_PROMPT_FILE"
}

# --- pre-work rebase (issue #215) -------------------------------------------
# Before the agent starts, the box must rebase the working branch onto the
# latest origin/BASE_BRANCH so the agent works against current main rather
# than the state of origin at clone time.

@test "entrypoint rebases prior work onto latest origin/BASE_BRANCH before agent starts" {
  # Simulate a prior run: agent/issue-7 was pushed with a commit, then main
  # advanced with a non-conflicting change while the branch was in flight.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  echo "branch work" > "$prior/branch.txt"
  git -C "$prior" add branch.txt
  git -C "$prior" commit -q -m "feat: prior run work"
  git -C "$prior" push -q origin "agent/issue-7"

  # Advance main with a non-conflicting commit (simulates a refactor landing
  # on main while the branch was in flight).
  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  echo "main advance" > "$advance/main_advance.txt"
  git -C "$advance" add main_advance.txt
  git -C "$advance" commit -q -m "chore: advance main"
  git -C "$advance" push -q origin HEAD:main

  # Open PR so the adoption path is taken (no force-reset).
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # After the pre-work rebase the working branch must be on top of the latest
  # main: it should have both the prior branch work and the main advance.
  [ -f "$WORK_DIR/branch.txt" ]
  [ -f "$WORK_DIR/main_advance.txt" ]

  # The rebased branch must have been force-pushed so the agent's first
  # incremental push is a fast-forward, not a non-fast-forward rejection.
  echo "agent work" > "$WORK_DIR/agent.txt"
  git -C "$WORK_DIR" add agent.txt
  git -C "$WORK_DIR" commit -q -m "feat: agent work on rebased branch"
  run git -C "$WORK_DIR" push origin "agent/issue-7"
  [ "$status" -eq 0 ]
}

@test "entrypoint fails fast when pre-work rebase conflicts with latest main" {
  # Simulate a prior run that modified README.md on the branch, then main
  # landed a conflicting change to the same file.
  local prior="$BATS_TEST_TMPDIR/prior"
  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  printf "branch version\n" > "$prior/README.md"
  git -C "$prior" add README.md
  git -C "$prior" commit -q -m "feat: branch modifies README"
  git -C "$prior" push -q origin "agent/issue-7"

  local advance="$BATS_TEST_TMPDIR/advance"
  git clone -q "https://github.com/owner/repo.git" "$advance"
  printf "main version\n" > "$advance/README.md"
  git -C "$advance" add README.md
  git -C "$advance" commit -q -m "chore: main modifies README (conflicts)"
  git -C "$advance" push -q origin HEAD:main

  # Open PR so the adoption path is taken (where the rebase is attempted).
  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pre-work rebase"* ]]
}

# --- pre-work rebase conflict resolution (issue #216) -------------------------
# When a pre-work rebase conflict occurs, an agent is spawned to resolve it.
# Only genuinely unresolvable conflicts fail the box.

setup_rebase_conflict() {
  # Helper: push a conflicting README.md change from a prior run, then advance
  # main with a different conflicting change, and open a fake PR.
  local prior advance
  prior="$BATS_TEST_TMPDIR/prior"
  advance="$BATS_TEST_TMPDIR/advance"

  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -b "agent/issue-7" "origin/main"
  printf "branch version\n" > "$prior/README.md"
  git -C "$prior" add README.md
  git -C "$prior" commit -q -m "feat: branch modifies README"
  git -C "$prior" push -q origin "agent/issue-7"

  git clone -q "https://github.com/owner/repo.git" "$advance"
  printf "main version\n" > "$advance/README.md"
  git -C "$advance" add README.md
  git -C "$advance" commit -q -m "chore: main modifies README (conflicts)"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
}

@test "pre-work rebase conflict: agent resolves and entrypoint continues" {
  setup_rebase_conflict
  # FAKE_CLAUDE_RESOLVE_CONFLICT=1 makes the stub agent run git rebase --continue.
  export FAKE_CLAUDE_RESOLVE_CONFLICT=1

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Working dir must exist (clone succeeded and rebase completed).
  [ -d "$WORK_DIR/.git" ]
  # The main agent prompt must have been passed to claude.
  grep -q "Implement GitHub issue #7" "$CLAUDE_PROMPT_FILE"
}

@test "pre-work rebase conflict: unresolvable conflict exits non-zero" {
  setup_rebase_conflict
  # No FAKE_CLAUDE_RESOLVE_CONFLICT — stub does not complete the rebase.

  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"pre-work rebase"* ]]
}

@test "CONFLICT_RESOLVE_PR_URL: exits after resolving without running main agent" {
  setup_rebase_conflict
  export FAKE_CLAUDE_RESOLVE_CONFLICT=1
  export CONFLICT_RESOLVE_PR_URL="https://github.com/owner/repo/pull/7"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Main agent must NOT have been invoked — the issue prompt should be absent.
  ! grep -q "Implement GitHub issue #7" "$CLAUDE_PROMPT_FILE"
}

# --- rebase-before-push and push-failure handling (issue #345) ---------------
# The prompt must instruct the agent to rebase onto the latest base before
# pushing, retry exactly once on rejection, and surface the real error
# (including the .github/workflows/ hard stop) rather than stranding commits.

@test "prompt instructs agent to rebase onto base before pushing" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'git rebase' "$CLAUDE_PROMPT_FILE"
  grep -q 'git fetch' "$CLAUDE_PROMPT_FILE"
}

@test "prompt instructs agent to retry push exactly once on rejection" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'rejected' "$CLAUDE_PROMPT_FILE"
  grep -q 'retry' "$CLAUDE_PROMPT_FILE"
}

@test "prompt instructs agent to emit status=blocked on persistent push failure" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'status=blocked' "$CLAUDE_PROMPT_FILE"
  grep -q 'gh issue comment' "$CLAUDE_PROMPT_FILE"
}

@test "prompt treats genuine .github/workflows/ change as a hard stop" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q '\.github/workflows' "$CLAUDE_PROMPT_FILE"
  grep -q 'workflow' "$CLAUDE_PROMPT_FILE"
}

# --- devShell lifecycle wrapping (issue #341) ----------------------------------
# When the Target repo has a usable devShell, the prefetch hook and Driver
# (claude invocation) must run inside `nix develop` so the agent operates in
# the Target's exact pinned environment — not just the baked toolchain.

@test "devShell-present Driver: claude is launched inside nix develop when devShell is found" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # nix develop --command bash <wrapper> must appear in NIX_LOG (beyond the probe)
  grep -q 'develop.*--command bash' "$NIX_LOG"
}

@test "DEV_SHELL_NAME default: nix develop targets .#default when name is default" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  # DEV_SHELL_NAME=default is set in setup(); probe and wrappers must target .#default
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'develop .#default' "$NIX_LOG"
}

@test "DEV_SHELL_NAME selector: nix develop uses the configured devShell name" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  export DEV_SHELL_NAME=ci
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'develop .#ci' "$NIX_LOG"
}

@test "launch-failure relaunch: entrypoint relaunches in baked env when nix develop cannot exec Driver" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  export FAKE_NIX_DEV_SHELL_LAUNCH_FAIL=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # nix develop was attempted for the Driver
  grep -q 'develop.*--command bash' "$NIX_LOG"
  # Claude was still invoked (in baked env as fallback)
  grep -q "claude invoked for issue" "$CLAUDE_LOG"
}

@test "devShell-present prefetch: prefetch runs inside nix develop when devShell is found" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'FAKE'
echo "warmed $PWD for #${ISSUE_NUMBER:-?}" >>"$PREFETCH_LOG"
FAKE
  } >"$FAKE_BIN/warm-cache"
  chmod +x "$FAKE_BIN/warm-cache"
  # Override the inherited PREFETCH so the prefetch test uses our command.
  export PREFETCH="warm-cache"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "warmed" "$PREFETCH_LOG"
  # Both prefetch and Driver wrappers use --command bash (probe uses --command true).
  [ "$(grep -c 'develop.*--command bash' "$NIX_LOG")" -ge 2 ]
}

@test "devShell-present Driver: MODEL is forwarded into nix develop wrapper" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  export MODEL=claude-test-model
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # fake claude logs "model=<value>" — verify MODEL reached the wrapper
  grep -q 'model=claude-test-model' "$CLAUDE_LOG"
}

# --- cold-run toolchain nudge (issue #343) ------------------------------------

@test "nudge: hint emitted when no prefetch configured and go.sum present" {
  seed_lockfile "go.sum"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "go mod"
  echo "$output" | grep -q "prefetch"
  echo "$output" | grep -q "packages"
}

@test "nudge: hint suppressed when prefetch is configured" {
  seed_lockfile "go.sum"
  export PREFETCH="true"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "hint:"
}

@test "nudge: hint suppressed when no recognized lockfile present" {
  # Default setup_bare_repo seeds only README.md — no lockfile.
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "hint:"
}
