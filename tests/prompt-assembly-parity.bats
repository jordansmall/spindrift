#!/usr/bin/env bats
# Production-path golden harness (issue #2349 slice 6, extended by issue
# #2350, #2351, #2352, #2353, and re-pointed at the production path by issue
# #2354 slice 4): agent/entrypoint.sh's phase_prompt_assembly now shells out
# to the real `driver-exec assemble-prompt` verb directly (ADR 0036) --
# there is no separate bash-side rendering path left to diff against a
# second, independently-built Go invocation. This suite instead runs
# $ENTRYPOINT (via the fake driver/driver-exec chain,
# tests/helper.bash's setup_entrypoint_env -- tests/fakes/driver-exec's own
# `assemble-prompt` branch execs the real Go binary, not a bash
# reimplementation) and diffs its own captured production artifacts
# ($DRIVER_PROMPT_FILE, $DRIVER_AGENTS_FILE) against a checked-in golden
# fixture under tests/testdata/prompt-assembly-golden/<cell-name>.{prompt.txt,
# agents.json}, git-blame-friendly per cell. Session mode (initial vs resume)
# and the three orchestrator-only Handoff facts (Invoker, ReviewPromptFile,
# ReviewModel) are asserted against the real Handoff JSON itself
# ($DRIVER_HANDOFF_FILE, tests/helper.bash's test-only capture hook, issue
# #2395 slice 2) -- SessionMode directly against the cell's own expected
# value, the orchestrator-only facts via a `jq -S` diff against a checked-in
# <cell-name>.handoff.json golden fixture, the same pattern as the prompt/
# agents fixtures above.
#
# Each golden fixture was captured from this branch's own already-verified
# bash entrypoint output (every other slice of issue #2354 is green), not
# hand-authored -- a full-text diff is a strictly stronger regression net
# than hand-picked marker strings, catching any unintended byte change to the
# rendered prompt or roster, not just the specific facts someone thought to
# check.
#
# The orchestrator-off cells 1-11 share the orchestrator off and every skill
# baked -- exactly tests/box_env_gen.bash's set_box_env schema-default cell,
# plus setup_entrypoint_env's own BOX_WRITE_ENABLED=1 default -- and differ
# only on three orthogonal axes:
#
#   DISPATCH_KIND/SELF_CONTAINED/FIX_PASS/RESUME_AFTER_HOLD, with
#   ISSUE_TRACKER/CODE_FORGE/BOX_WRITE_ENABLED held at their defaults:
#     1. plain work (DISPATCH_KIND unset, FIX_PASS 0) -- the original
#        covered cell, exercised with both a populated and an empty agent
#        roster. Also the github read-write cell on the access/forge axis
#        (the schema default).
#     2. research (DISPATCH_KIND=research)
#     3. self-contained research (DISPATCH_KIND=research, SELF_CONTAINED=1)
#     4. fix-pass (FIX_PASS>0)
#
#   CODE_FORGE/BOX_WRITE_ENABLED access/forge axis, with dispatch
#   kind/fix-pass/ISSUE_TRACKER untouched:
#     5. github read-only (CODE_FORGE=github, BOX_WRITE_ENABLED unset)
#     6. forgejo read-write (CODE_FORGE=forgejo, BOX_WRITE_ENABLED=1)
#     7. forgejo read-only (CODE_FORGE=forgejo, BOX_WRITE_ENABLED unset)
#
#   ISSUE_TRACKER, with SessionMode held at "initial" (dispatch kind/fix-pass
#   untouched):
#     8. local, no issue reference (ISSUE_TRACKER=local)
#     9. local, issue-reference knob on (ISSUE_TRACKER=local,
#        LOCAL_ISSUE_REFERENCE=1)
#     10. forgejo, read-write (ISSUE_TRACKER=forgejo)
#     11. jira, which rides the github prompt-selection arms
#         (ISSUE_TRACKER=jira)
#
# The orchestrator-on cells 12-14 (issue #2353) all share dispatch kind
# "work" (default) with FIX_PASS unset -- the only path checkCoveredCell
# covers combined with the orchestrator on -- and differ only on the
# roster/skills axes:
#   12. orchestrator on, filer-on -- roster carries a "filer" key alongside
#       reviewer and scout (FILER_ENABLED on).
#   13. orchestrator on, filer-off -- roster carries reviewer and scout but
#       no "filer" key (FILER_ENABLED off).
#   14. orchestrator on, skills-absent -- no skill baked at all, contrasting
#       setup()'s unconditional 4-skill baking every other cell relies on.
#
# Every cell test funnels through the shared assert_cell_golden helper below,
# so the prompt/agents/session-mode comparison logic lives in exactly one
# place. This suite is not a source of truth for either representation's own
# correctness -- that's tests/entrypoint-*.bats for the bash/production path
# and cmd/launcher/internal/promptassembly/*_test.go for the Go package's own
# unit coverage -- it is a regression net pinning the production path's own
# byte-exact output per cell.

load helper

setup() {
  setup_entrypoint_env

  # BRANCH is computed inside entrypoint.sh's main (BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}",
  # entrypoint.sh:55), not exported by set_box_env/setup_entrypoint_env, but
  # nothing here needs to reproduce that computation anymore -- $ENTRYPOINT
  # derives it itself now that there's no second, independently-built Go
  # invocation to hand a --branch flag to.

  # Bake all four skills (entrypoint-prompt-fragments.bats:660-730's pattern):
  # the covered cell requires every per-skill gate on and a non-empty
  # SKILLS_FOUND (assemble.go's checkCoveredCell).
  mkdir -p "$HOME/.claude/skills/caveman"
  cat >"$HOME/.claude/skills/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL

  mkdir -p "$HOME/.claude/skills/tdd"
  cat >"$HOME/.claude/skills/tdd/SKILL.md" <<'SKILL'
---
name: tdd
description: Test-driven development.
---
Red, green, refactor.
SKILL

  mkdir -p "$HOME/.claude/skills/commit"
  cat >"$HOME/.claude/skills/commit/SKILL.md" <<'SKILL'
---
name: commit
description: Write git commit messages in Conventional Commits style.
---
Hard-wrapped Conventional Commits.
SKILL

  mkdir -p "$HOME/.claude/skills/code-review"
  cat >"$HOME/.claude/skills/code-review/SKILL.md" <<'SKILL'
---
name: code-review
description: Review code changes for standards and spec compliance.
---
Two-axis review: Standards + Spec.
SKILL
}

GOLDEN_DIR="${BATS_TEST_DIRNAME}/testdata/prompt-assembly-golden"

# assert_cell_golden: runs the real bash entrypoint over whatever env the
# calling test has already exported, then asserts its own captured
# production artifacts against a checked-in golden fixture: byte-identical
# prompt ($DRIVER_PROMPT_FILE vs <golden_name>.prompt.txt), byte-identical
# agents JSON when a roster was rendered ($DRIVER_AGENTS_FILE vs
# <golden_name>.agents.json, both canonicalized via `jq -S` first), and a
# session mode that matches the cell's expected value -- read directly from
# the real Handoff JSON's own SessionMode field ($DRIVER_HANDOFF_FILE,
# tests/helper.bash's test-only DRIVER_HANDOFF_FILE hook, issue #2395 slice
# 1), which entrypoint.sh's phase_prompt_assembly sets to exactly "initial"
# or "resume" (assemble.go's Handoff.SessionMode). expected_session_mode is
# reused as-is rather than round-tripped through a separate golden fixture:
# it's already the deterministic value the calling test itself asserts,
# there's no independent fact left to pin.
assert_cell_golden() {
  local golden_name="$1" expected_session_mode="$2"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  diff "$GOLDEN_DIR/${golden_name}.prompt.txt" "$DRIVER_PROMPT_FILE"

  if [ -s "$DRIVER_AGENTS_FILE" ]; then
    diff <(jq -S . "$GOLDEN_DIR/${golden_name}.agents.json") <(jq -S . "$DRIVER_AGENTS_FILE")
  else
    [ ! -s "$DRIVER_AGENTS_FILE" ]
  fi

  case "$expected_session_mode" in
    initial | resume) ;;
    *)
      echo "assert_cell_golden: unknown expected_session_mode '$expected_session_mode'" >&2
      return 1
      ;;
  esac

  [ "$(jq -r .SessionMode "$DRIVER_HANDOFF_FILE")" = "$expected_session_mode" ]
}

# assert_review_handoff_golden: for the orchestrator-on cells (issue #2353),
# asserts the Handoff facts assert_cell_golden's own prompt/agents-JSON/
# session-mode checks don't cover -- Invoker, ReviewPromptFile, and
# ReviewModel, all only ever populated with the orchestrator on -- via a
# `jq -S` diff of the real Handoff JSON ($DRIVER_HANDOFF_FILE) against a
# checked-in <golden_name>.handoff.json fixture, the same
# canonicalize-then-diff pattern assert_cell_golden already uses for
# <golden_name>.agents.json. A non-empty $ORCHESTRATOR_LOG is separately kept
# as a cheap, independent proof the orchestrator (not driver-exec) was the
# invoker for this pass -- Invoker itself is asserted byte-exact by the diff
# below, but this catches a run that skipped the orchestrator entirely
# (e.g. a caller forgetting to export ORCHESTRATOR_ENABLED) with a clearer
# failure than a JSON diff would.
assert_review_handoff_golden() {
  local golden_name="$1"

  [ -s "$ORCHESTRATOR_LOG" ]

  diff <(jq -S . "$GOLDEN_DIR/${golden_name}.handoff.json") <(jq -S . "$DRIVER_HANDOFF_FILE")
}

# issue #2349: a realistic multi-agent roster -- scout, reviewer (present, not
# dropped: the orchestrator is off in the covered cell), and worker (the
# WORKER_PROVISIONED gate's partner axis to "skills baked" in the covered
# cell) -- mirroring the shape at tests/entrypoint-prompt-fragments.bats's
# WORKER_AGENTS_JSON_TEMPLATE/"entrypoint includes a read-only tools
# whitelist" fixtures.
AGENTS_ROSTER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep","WebFetch"]}}'

# issue #2353: AGENTS_ROSTER plus a "filer" entry (the shape
# tests/entrypoint-agents-json.bats:64 already exercises -- "File issues from
# a review's non-blocking findings, best-effort"), so the orchestrator-on
# filer-on cell below actually flips the FILER_ENABLED gate on, unlike
# AGENTS_ROSTER alone (the filer-off cell's roster).
AGENTS_ROSTER_WITH_FILER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep","WebFetch"]},"filer":{"description":"File issues from a review'"'"'s non-blocking findings, best-effort","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'

@test "production path matches the golden fixture for the covered cell, with a populated roster" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"

  assert_cell_golden "covered-cell-populated-roster" initial

  # Structurally guaranteed by the covered cell, not reverse-engineered from
  # the bash fake's capture files (entrypoint.sh:1282-1310): the orchestrator
  # gate is off, so the invoker is always "driver-exec" and the orchestrator
  # is never invoked at all.
  [ ! -s "$ORCHESTRATOR_LOG" ]
}

@test "production path matches the golden fixture for omitting the agents flag entirely with no roster" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: proves the "omit the
  # --agents flag/output stays empty" branch, not just the populated-roster
  # branch above.
  unset AGENTS_JSON_TEMPLATE

  assert_cell_golden "no-roster" initial
}

@test "production path matches the golden fixture for the research cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: this cell is about the
  # prompt-selection/session-mode axis, not roster interaction, which the
  # two tests above already cover independently.
  export DISPATCH_KIND="research"

  assert_cell_golden "research" initial
}

@test "production path matches the golden fixture for the self-contained research cell" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"

  assert_cell_golden "self-contained-research" initial
}

@test "production path matches the golden fixture for the fix-pass cell" {
  export FIX_PASS="1"

  assert_cell_golden "fix-pass" resume
}

@test "production path matches the golden fixture for the github read-only cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: this cell is about the
  # access/forge axis, not roster interaction, which the two tests at the
  # top of this file already cover independently.
  unset BOX_WRITE_ENABLED

  assert_cell_golden "github-read-only" initial
}

@test "production path matches the golden fixture for the forgejo read-write cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset, same reasoning as above.
  # BOX_WRITE_ENABLED stays at setup_entrypoint_env's read-write default, so
  # no extra flag override is needed here.
  export CODE_FORGE="forgejo"
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  # clone_repo requires FORGEJO_TOKEN and builds the clone URL as
  # https://<token>@<host>/<slug>.git; redirect that exact URL to the bare
  # repo setup_bare_repo already seeded so the clone stays offline (mirrors
  # tests/entrypoint-prompt-assembly.bats's CODE_FORGE=forgejo fix-pass test).
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"

  assert_cell_golden "forgejo-read-write" initial
}

@test "production path matches the golden fixture for the forgejo read-only cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset, same reasoning as above.
  export CODE_FORGE="forgejo"
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  unset BOX_WRITE_ENABLED
  # clone_repo requires FORGEJO_TOKEN and builds the clone URL as
  # https://<token>@<host>/<slug>.git; redirect that exact URL to the bare
  # repo setup_bare_repo already seeded so the clone stays offline (mirrors
  # tests/entrypoint-prompt-assembly.bats's CODE_FORGE=forgejo fix-pass test).
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"

  assert_cell_golden "forgejo-read-only" initial
}

@test "production path matches the golden fixture for the local tracker cell, no issue reference" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: this cell is about the
  # tracker axis, not roster interaction, which the two dispatch-kind cells
  # above already cover independently. SessionMode stays "initial" --
  # ISSUE_TRACKER is orthogonal to dispatch kind/fix-pass.
  export ISSUE_TRACKER="local"

  assert_cell_golden "local-tracker-no-issue-ref" initial
}

@test "production path matches the golden fixture for the local tracker cell, issue reference on" {
  export ISSUE_TRACKER="local"
  export LOCAL_ISSUE_REFERENCE="1"

  assert_cell_golden "local-tracker-issue-ref-on" initial
}

@test "production path matches the golden fixture for the forgejo tracker cell" {
  export ISSUE_TRACKER="forgejo"

  assert_cell_golden "forgejo-tracker" initial
}

@test "production path matches the golden fixture for the jira tracker cell" {
  # jira rides the same prompt-selection arms as github (assemble.go's
  # checkCoveredCell). The Go side's byte-identity between jira and github
  # is already pinned by a Go unit test in
  # cmd/launcher/internal/promptassembly; this cell additionally proves the
  # production BASH path renders jira through that same github arm, not just
  # within the Go package.
  export ISSUE_TRACKER="jira"

  assert_cell_golden "jira-tracker" initial
}

# issue #2353: three orchestrator-on cells (dispatch kind "work", FIX_PASS
# unset -- the only orchestrator-on path checkCoveredCell covers). Unlike
# cells 1-4 above, ORCHESTRATOR_ENABLED must be exported so the bash side
# takes run_driver_in_env's orchestrator invocation path (entrypoint.sh:
# 1282-1286, tests/entrypoint-orchestrator-handoff.bats).

@test "production path matches the golden fixture for the orchestrator-on filer-on cell" {
  export ORCHESTRATOR_ENABLED=1
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_FILER"

  assert_cell_golden "orchestrator-filer-on" initial

  assert_review_handoff_golden "orchestrator-filer-on"
}

@test "production path matches the golden fixture for the orchestrator-on filer-off cell" {
  export ORCHESTRATOR_ENABLED=1
  # AGENTS_ROSTER (not the _WITH_FILER variant above): scout+reviewer, no
  # "filer" key -- the FILER_ENABLED-off half of the roster axis. Reviewer is
  # present in both filer-on and filer-off (filer-on/off forks only on
  # whether "filer" itself is in the roster), so both cells assert
  # ReviewModel the same way via assert_review_handoff_golden.
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"

  assert_cell_golden "orchestrator-filer-off" initial

  assert_review_handoff_golden "orchestrator-filer-off"
}

@test "production path matches the golden fixture for the orchestrator-on skills-absent cell" {
  export ORCHESTRATOR_ENABLED=1
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"

  # Contrast setup()'s unconditional 4-skill baking: this cell is the
  # "SkillsFound == "" and every *SkillBaked flag false" branch
  # checkCoveredCell also covers. Remove the skill dirs setup() just baked
  # under $HOME (rather than restructuring setup() itself, which every other
  # cell in this suite still relies on baking unconditionally).
  rm -rf "$HOME/.claude/skills"

  assert_cell_golden "orchestrator-skills-absent" initial

  assert_review_handoff_golden "orchestrator-skills-absent"
}
