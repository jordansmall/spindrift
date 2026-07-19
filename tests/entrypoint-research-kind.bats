#!/usr/bin/env bats
# Research dispatch kind (ADR 0022, issue #640): DISPATCH_KIND=research selects
# the research prompt instead of the work issue-prompt.md.

load helper

setup() {
  setup_entrypoint_env
}

@test "DISPATCH_KIND=research drives research-prompt.md, not issue-prompt.md" {
  export DISPATCH_KIND="research"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Research GitHub issue #7" "$CLAUDE_PROMPT_FILE"
  ! grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
}

@test "DISPATCH_KIND unset still drives issue-prompt.md" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
}

# --- research kind skips the work-only branch/rebase phases (ADR 0022) ------
# A research dispatch clones fresh but never cuts, checks out, or pushes an
# agent branch -- there is no code to land, so there is nothing to rebase.

@test "research kind never checks out or pushes an agent branch" {
  export DISPATCH_KIND="research"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$output" = "main" ]
  run git -C "$BATS_TEST_TMPDIR" ls-remote "https://github.com/owner/repo.git" "agent/issue-7"
  [ -z "$output" ]
}

@test "DISPATCH_KIND unset (work) still checks out the agent branch" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$output" = "agent/issue-7" ]
}

# --- research kind's log line does not claim to implement (issue #734) -----
# A research dispatch never cuts, checks out, or pushes a branch, so its log
# line must not say "implementing ... on $BRANCH".

@test "research kind logs researching, not implementing, and names no branch" {
  export DISPATCH_KIND="research"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "==> claude researching issue #7" <<<"$output"
  ! grep -q "claude implementing issue" <<<"$output"
  ! grep -q "on agent/issue-7" <<<"$output"
}

@test "DISPATCH_KIND unset (work) still logs implementing on the branch" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "==> claude implementing issue #7 on agent/issue-7" <<<"$output"
}

# --- research outcome-contract injection/idempotency (issue #640) ----------
# Mirrors tests/entrypoint-outcome-contract.bats, for the research kind's own
# outcome contract instead of the work "# LAND THE CHANGE" one.

@test "runtime prompt-dir override of research-prompt.md lacking the outcome contract gets it appended" {
  export DISPATCH_KIND="research"
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'research stub, no contract here\n' >"$prompt_dir/research-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export RESEARCH_OUTCOME_CONTRACT_FILE="$BATS_TEST_TMPDIR/research-outcome-contract.md"
  printf '# POST THE VERDICT\n\ncanonical research contract for issue %s\n' '${ISSUE_NUMBER}' \
    >"$RESEARCH_OUTCOME_CONTRACT_FILE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '# POST THE VERDICT' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  grep -q 'canonical research contract for issue 7' "$CLAUDE_PROMPT_FILE"
}

@test "runtime prompt-dir override of research-prompt.md already containing the outcome contract is unchanged" {
  export DISPATCH_KIND="research"
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'research stub\n\n# POST THE VERDICT\n\nalready has its own contract\n' \
    >"$prompt_dir/research-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export RESEARCH_OUTCOME_CONTRACT_FILE="$BATS_TEST_TMPDIR/research-outcome-contract.md"
  printf '# POST THE VERDICT\n\nshould not appear\n' >"$RESEARCH_OUTCOME_CONTRACT_FILE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '# POST THE VERDICT' "$CLAUDE_PROMPT_FILE")" -eq 1 ]
  grep -q 'already has its own contract' "$CLAUDE_PROMPT_FILE"
  ! grep -q 'should not appear' "$CLAUDE_PROMPT_FILE"
}

@test "research kind fails loudly when RESEARCH_OUTCOME_CONTRACT_FILE is missing" {
  export DISPATCH_KIND="research"
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'research stub, no contract here\n' >"$prompt_dir/research-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export RESEARCH_OUTCOME_CONTRACT_FILE="$BATS_TEST_TMPDIR/does-not-exist.md"
  run bash "$ENTRYPOINT"
  [ "$status" -ne 0 ]
}

# --- research kind's own outcome backstop (issue #640) ----------------------
# A research driver that exits with no outcome line has no branch to push
# best-effort (there is none) -- the backstop must not attempt one, and must
# emit the research-appropriate blocked line instead.

@test "research kind backstop: no outcome line emits blocked with no branch push" {
  export DISPATCH_KIND="research"
  export FAKE_CLAUDE_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=none status=blocked note=.*driver exited without emitting an outcome' <<<"$output"
  # A research dispatch pins no session worth resuming (issue #1607) --
  # exactly one Driver invocation, no resume pass, and its note carries no
  # mention of a recovery attempt.
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 1 ]
  ! grep -q 'resume attempt' <<<"$output"
}
