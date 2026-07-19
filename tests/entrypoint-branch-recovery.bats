#!/usr/bin/env bats
# Pre-work rebase and conflict resolution, including generated files (issues #215, #216, #403).

load helper

setup() {
  setup_entrypoint_env
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
  # FAKE_CLAUDE_RESOLVE_CONFLICT stays exported for the whole run, so the main
  # agent invocation sees it too, with no rebase left in progress -- it must
  # fall through to a real outcome (issue #1607's resume-once recovery would
  # otherwise kick in on the silent no-op this used to be).
  [ "$(grep -c '^SPINDRIFT_OUTCOME ' <<<"$output")" -eq 1 ]
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=.*status=ready' <<<"$output"
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 2 ]
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

# --- pre-work rebase conflict on a generated file (issue #403) ---------------
# A conflicted file that declares itself generated ("DO NOT EDIT" / "Code
# generated by X from Y") must be resolved by merging in its source of truth
# and regenerating the artifact, never by hand-merging its own conflict
# markers. The fake `claude` stub's FAKE_CLAUDE_RESOLVE_CONFLICT mode encodes
# this: generated files are regenerated from a merged source; ordinary files
# still fall back to accepting the incoming (theirs) side.

seed_generated_file_fixture() {
  # Push a regen.sh + baseline source.txt/generated.txt pair to main so both
  # diverging branches inherit the same generation contract.
  local seed="$BATS_TEST_TMPDIR/seed-generated"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  cat >"$seed/regen.sh" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
printf '<!-- Code generated by regen.sh from source.txt. DO NOT EDIT. -->\nGENERATED: %s' \
  "$(cat source.txt)" >generated.txt
SCRIPT
  chmod +x "$seed/regen.sh"
  printf 'base\n' >"$seed/source.txt"
  ( cd "$seed" && bash regen.sh )
  git -C "$seed" add regen.sh source.txt generated.txt
  git -C "$seed" commit -q -m "chore: add generated-file fixture"
  git -C "$seed" push -q origin HEAD:main
}

setup_rebase_conflict_generated() {
  # Helper: diverge source.txt (and its regenerated artifact) on both the
  # agent branch and main so rebasing conflicts in both files.
  seed_generated_file_fixture

  local prior advance
  prior="$BATS_TEST_TMPDIR/prior-gen"
  advance="$BATS_TEST_TMPDIR/advance-gen"

  git clone -q "https://github.com/owner/repo.git" "$prior"
  git -C "$prior" checkout -q -b "agent/issue-7" "origin/main"
  printf "branch source\n" >"$prior/source.txt"
  ( cd "$prior" && bash regen.sh )
  git -C "$prior" add source.txt generated.txt
  git -C "$prior" commit -q -m "feat: branch modifies source"
  git -C "$prior" push -q origin "agent/issue-7"

  git clone -q "https://github.com/owner/repo.git" "$advance"
  printf "main source\n" >"$advance/source.txt"
  ( cd "$advance" && bash regen.sh )
  git -C "$advance" add source.txt generated.txt
  git -C "$advance" commit -q -m "chore: main modifies source (conflicts)"
  git -C "$advance" push -q origin HEAD:main

  export FAKE_GH_PR_LIST_7="https://github.com/owner/repo/pull/7"
}

@test "pre-work rebase conflict on generated file: regenerates instead of hand-merging" {
  setup_rebase_conflict_generated
  export FAKE_CLAUDE_RESOLVE_CONFLICT=1

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]

  # No conflict markers left in either file.
  ! grep -q '^<<<<<<<' "$WORK_DIR/source.txt"
  ! grep -q '^<<<<<<<' "$WORK_DIR/generated.txt"

  # The source of truth carries both sides' intent — a real merge, not a
  # one-sided pick.
  grep -q 'branch source' "$WORK_DIR/source.txt"
  grep -q 'main source' "$WORK_DIR/source.txt"

  # The generated artifact matches regenerating fresh from the resolved
  # source — proof it was regenerated, not hand-merged in place.
  local before after
  before="$(cat "$WORK_DIR/generated.txt")"
  ( cd "$WORK_DIR" && bash regen.sh )
  after="$(cat "$WORK_DIR/generated.txt")"
  [ "$before" = "$after" ]
}

