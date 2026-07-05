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

# CLAUDE_LOG records the whole argv, including the -p prompt text — and the
# prompt itself mentions the word "--agents". So match the flag's JSON payload
# ("name":"scout"/"reviewer"), which the prompt prose never contains, not the
# bare flag string.
@test "entrypoint passes --agents to claude when SCOUT_MODEL and REVIEW_MODEL are set" {
  export SCOUT_MODEL="claude-haiku-3-5"
  export REVIEW_MODEL="claude-opus-4-5"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF '"name":"scout"' "$CLAUDE_LOG"
  grep -qF '"name":"reviewer"' "$CLAUDE_LOG"
}

@test "entrypoint omits --agents when SCOUT_MODEL is unset" {
  unset SCOUT_MODEL
  export REVIEW_MODEL="claude-opus-4-5"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qF '"name":"reviewer"' "$CLAUDE_LOG"
}

@test "entrypoint omits --agents when REVIEW_MODEL is unset" {
  export SCOUT_MODEL="claude-haiku-3-5"
  unset REVIEW_MODEL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qF '"name":"scout"' "$CLAUDE_LOG"
}

@test "IN_PROGRESS_LABEL and COMPLETE_LABEL are substituted in the prompt" {
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  cat >"$prompt_dir/issue-prompt.md" <<'EOF'
label: ${IN_PROGRESS_LABEL} complete: ${COMPLETE_LABEL}
EOF
  export PROMPTS_DIR="$prompt_dir"
  export IN_PROGRESS_LABEL="wip"
  export COMPLETE_LABEL="done"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'label: wip' "$CLAUDE_PROMPT_FILE"
  grep -q 'complete: done' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt delegates exploration to the scout subagent" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'scout' "$CLAUDE_PROMPT_FILE"
}

# The container has none of the operator's user/project skills, so the prompt
# must be self-contained: spell the process out, never reference a skill.
@test "default prompt makes TDD a hard rule, not a preference" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'hard rule' "$CLAUDE_PROMPT_FILE"
  grep -qi 'never write implementation code before' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt forbids batching tests and implementation" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'one failing test, one change' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt is self-contained: commit convention stated directly, no skill references" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'Conventional Commits' "$CLAUDE_PROMPT_FILE"
  ! grep -qi 'skill' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt spawns a reviewer subagent with SPEC and STANDARDS rubric" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'reviewer' "$CLAUDE_PROMPT_FILE"
  grep -q 'SPEC' "$CLAUDE_PROMPT_FILE"
  grep -q 'STANDARDS' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt explains why inline review risks a premature stop" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'turn.ending\|halfway gate\|finish.line' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt requires the reviewer subagent and restricts inline to a fallback" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'do not review inline\|not inline\|inline.*only.*when\|only.*inline.*when' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt specifies a review-build loop that never advances with a blocking finding" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'BLOCKING\|blocking' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt degrades gracefully when tier models are unavailable" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'if available\|if it.*available\|when.*available' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt blocks on CI and never merges on red" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'pr checks\|watch.*ci\|ci.*watch\|watch.*check\|check.*watch' "$CLAUDE_PROMPT_FILE"
  grep -qi 'never.*merg.*red\|red.*never.*merg\|do not.*merg.*red\|merg.*only.*green\|green.*merg' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt waits for CI to register before trusting the watch" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # "no checks" right after opening the PR must not read as green — the prompt
  # tells the agent to wait for a check to register before merging.
  grep -qi 'register' "$CLAUDE_PROMPT_FILE"
  grep -qi 'no checks' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt requires the CI-registration wait to run in the foreground" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # A backgrounded wait ends the agent's turn before CI registers, so the final
  # outcome line is never printed and the launcher can never learn the PR to merge.
  grep -qi 'foreground' "$CLAUDE_PROMPT_FILE"
  grep -qi 'background' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt states the launcher owns the merge" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'launcher.*owns\|launcher.*rebase\|rebase.*launcher\|launcher.*merge\|merge.*launcher' "$CLAUDE_PROMPT_FILE"
  grep -qi 'do not run.*gh pr merge\|do not.*merge\|not.*run.*pr merge' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt instructs agent the launcher owns the complete-label swap" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'launcher.*complete\|complete.*launcher\|launcher.*owns\|owns.*complete' "$CLAUDE_PROMPT_FILE"
  grep -qi 'do not.*add-label\|do not run.*issue edit' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt requires the outcome line be the literal final output" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # The launcher only learns the PR from this line; the agent must not end its
  # turn with prose or defer the line to a background task.
  grep -qi 'do not end your turn\|must be the last\|literal.*final\|final.*message\|nothing after' "$CLAUDE_PROMPT_FILE"
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

@test "default prompt states the launcher owns the CI-green decision" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'launcher.*ci\|launcher.*green\|launcher.*owns\|ci.*launcher\|green.*launcher' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt takes the blocked path when CI fails to register" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'no check.*register\|if no check\|never.*register\|not.*register' "$CLAUDE_PROMPT_FILE"
  grep -q 'status=blocked' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt opens a draft PR and comments on the issue when blocked" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--draft' "$CLAUDE_PROMPT_FILE"
  grep -q 'issue comment\|pr comment\|comment.*issue\|comment.*blocked' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt instructs agent to push the branch after every substantive commit" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'push.*after.*commit\|push.*every.*commit\|push.*each.*commit\|after.*commit.*push' "$CLAUDE_PROMPT_FILE"
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

@test "entrypoint skips devShell probe when repo has no flake.nix" {
  # standard setup_bare_repo has no flake.nix
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "devShell"
}

@test "default prompt mentions nix develop and nix flake check for flake projects" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'nix develop' "$CLAUDE_PROMPT_FILE"
  grep -q 'nix flake check' "$CLAUDE_PROMPT_FILE"
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
