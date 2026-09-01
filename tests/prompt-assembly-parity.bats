#!/usr/bin/env bats
# Production-path golden harness (ADR 0036): agent/entrypoint.sh's
# phase_prompt_assembly shells out to the real `driver-exec assemble-prompt`
# verb, so there is no separate bash-side rendering path left to diff against.
# This suite instead runs $ENTRYPOINT (via the fake driver/driver-exec chain,
# whose own `assemble-prompt` branch execs the real Go binary, not a bash
# reimplementation) and diffs its captured production artifacts
# ($DRIVER_PROMPT_FILE, $DRIVER_AGENTS_FILE) against checked-in golden fixtures
# under tests/testdata/prompt-assembly-golden/<cell-name>.{prompt.txt,
# agents.json}, git-blame-friendly per cell. Session mode and the four
# orchestrator-only Handoff facts (Invoker, ReviewPromptFile, ReviewModel,
# ReviewEffort) are asserted against the real Handoff JSON itself
# ($DRIVER_HANDOFF_FILE, helper.bash's test-only capture hook).
#
# Every fixture was captured from verified entrypoint output, not hand-authored
# -- a full-text diff is a strictly stronger regression net than hand-picked
# marker strings, catching any unintended byte change to the rendered prompt or
# roster.
#
# The orchestrator-off cells 1-13 share the orchestrator off and every skill
# baked -- box_env_gen.bash's schema-default cell plus setup_entrypoint_env's
# BOX_WRITE_ENABLED=1 -- and differ on four axes:
#
#   DISPATCH_KIND/SELF_CONTAINED/FIX_PASS/RESUME_AFTER_HOLD, with
#   ISSUE_TRACKER/CODE_FORGE/BOX_WRITE_ENABLED held at their defaults:
#     1. plain work (DISPATCH_KIND unset, FIX_PASS 0) -- the original
#        covered cell, exercised with both a populated and an empty agent
#        roster. Also the github read-write cell on the access/forge axis
#        (the schema default).
#     2. research (DISPATCH_KIND=research)
#     3. research, filer-on (DISPATCH_KIND=research, roster/filer axis on
#        below -- issue #2786)
#     4. self-contained research (DISPATCH_KIND=research, SELF_CONTAINED=1)
#     5. self-contained research, filer-on (DISPATCH_KIND=research,
#        SELF_CONTAINED=1, roster/filer axis on below -- issue #2786)
#     6. fix-pass (FIX_PASS>0)
#
#   CODE_FORGE/BOX_WRITE_ENABLED access/forge axis, with dispatch
#   kind/fix-pass/ISSUE_TRACKER untouched:
#     7. github read-only (CODE_FORGE=github, BOX_WRITE_ENABLED unset)
#     8. forgejo read-write (CODE_FORGE=forgejo, BOX_WRITE_ENABLED=1)
#     9. forgejo read-only (CODE_FORGE=forgejo, BOX_WRITE_ENABLED unset)
#
#   ISSUE_TRACKER, with SessionMode held at "initial" (dispatch kind/fix-pass
#   untouched):
#     10. local, no issue reference (ISSUE_TRACKER=local)
#     11. local, issue-reference knob on (ISSUE_TRACKER=local,
#         LOCAL_ISSUE_REFERENCE=1)
#     12. forgejo, read-write (ISSUE_TRACKER=forgejo)
#     13. jira, which rides the github prompt-selection arms
#         (ISSUE_TRACKER=jira)
#
#   Roster/filer axis, added by cells 3 and 5 above -- unlike the axes above,
#   not isolated to a single knob: cells 2 and 4 leave AGENTS_JSON_TEMPLATE
#   unset entirely, so cell 3 against cell 2 flips four knobs at once (roster
#   present at all, the filer key, BOX_FILER_ENABLED, BOX_WORKER_PROVISIONED).
#   A filer in the roster with BOX_FILER_ENABLED=1 pins gates_tracker.go's
#   researchForceRelay, which forces the verdict-comment step onto the
#   SPINDRIFT_COMMENT relay arm even though this suite's default box is
#   read-write -- otherwise only covered at the Go unit level (issue #2786).
#
# The orchestrator-on cells 14-17 (issue #2353, cell 17 added by issue
# #2512) all share dispatch kind "work" (default) with FIX_PASS unset -- the
# only path checkCoveredCell covers combined with the orchestrator on -- and
# differ only on the roster/skills axes:
#   14. orchestrator on, filer-on -- roster carries a "filer" key alongside
#       reviewer and scout (FILER_ENABLED on).
#   15. orchestrator on, filer-off -- roster carries reviewer and scout but
#       no "filer" key (FILER_ENABLED off).
#   16. orchestrator on, skills-absent -- no skill baked at all, contrasting
#       setup()'s unconditional 4-skill baking every other cell relies on.
#   17. orchestrator on, review-effort-set -- roster's reviewer entry
#       carries an explicit "effort" key, proving ReviewEffort's
#       non-empty-overrides case (the filer-on/filer-off cells above only
#       cover the empty-follows-roster case).
#
# Every cell test funnels through the shared assert_cell_golden helper below.
# This suite is not a source of truth for either representation's correctness
# -- that's tests/entrypoint-*.bats and
# cmd/launcher/internal/promptassembly/*_test.go -- it is a regression net
# pinning the production path's byte-exact output per cell.

load helper

setup() {
  setup_entrypoint_env


  # phase_prompt_assembly copies HARNESS_SKILLS_DIR (default /agent/skills) and
  # OPERATOR_SKILLS_DIR (default /operator-skills) into DRIVER_SKILLS_DIR before
  # the SKILLS_FOUND scan -- real, absolute host paths outside this test's
  # sandbox. Left unset, a Box with its own skills baked at /agent/skills
  # silently widens every cell's roster, producing a golden fixture that only
  # matches that one contaminated environment (issue #2059 CI regression). Point
  # both at guaranteed-empty directories.
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/no-harness-skills"
  export OPERATOR_SKILLS_DIR="$BATS_TEST_TMPDIR/no-operator-skills"

  # Bake all four skills: the covered cell requires every per-skill gate on and
  # a non-empty SKILLS_FOUND (assemble.go's checkCoveredCell).
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

# assert_golden_text_or_update: the shared diff-vs-copy primitive UPDATE_GOLDENS
# gates (issue #2951) for plain-text goldens -- diffs produced_file against
# golden_file as today, or, with UPDATE_GOLDENS set, overwrites golden_file
# with produced_file's content instead.
assert_golden_text_or_update() {
  local golden_file="$1" produced_file="$2"
  if [ -n "${UPDATE_GOLDENS:-}" ]; then
    cp "$produced_file" "$golden_file"
  else
    diff "$golden_file" "$produced_file"
  fi
}

# assert_golden_json_or_update: same as assert_golden_text_or_update but for
# JSON goldens -- both sides are canonicalized (and optionally projected)
# through jq_filter (default ".") first, so key order never causes a spurious
# diff and a golden written in update mode is always canonical.
assert_golden_json_or_update() {
  local golden_file="$1" produced_file="$2" jq_filter="${3:-.}"
  if [ -n "${UPDATE_GOLDENS:-}" ]; then
    local tmp
    tmp="$(mktemp)"
    jq -S "$jq_filter" "$produced_file" > "$tmp"
    mv "$tmp" "$golden_file"
  else
    diff <(jq -S "$jq_filter" "$golden_file") <(jq -S "$jq_filter" "$produced_file")
  fi
}

# assert_cell_golden: runs the real bash entrypoint over whatever env the
# calling test exported, then asserts its captured production artifacts against
# a checked-in golden fixture: byte-identical prompt, byte-identical agents JSON
# when a roster was rendered (both canonicalized via `jq -S` first), and a
# session mode matching the cell's expected value, read directly from the real
# Handoff JSON's SessionMode field. expected_session_mode is reused as-is
# rather than round-tripped through a separate golden: it's already the
# deterministic value the calling test asserts.
assert_cell_golden() {
  local golden_name="$1" expected_session_mode="$2"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  assert_golden_text_or_update "$GOLDEN_DIR/${golden_name}.prompt.txt" "$DRIVER_PROMPT_FILE"

  if [ -s "$DRIVER_AGENTS_FILE" ]; then
    assert_golden_json_or_update "$GOLDEN_DIR/${golden_name}.agents.json" "$DRIVER_AGENTS_FILE"
  elif [ -n "${UPDATE_GOLDENS:-}" ]; then
    rm -f "$GOLDEN_DIR/${golden_name}.agents.json"
  else
    [ ! -s "$DRIVER_AGENTS_FILE" ]
  fi

  [ "$(jq -r .SessionMode "$DRIVER_HANDOFF_FILE")" = "$expected_session_mode" ]
}

# assert_review_handoff_golden: for the orchestrator-on cells, asserts the
# Handoff facts assert_cell_golden doesn't cover -- Invoker, ReviewPromptFile,
# ReviewModel, ReviewEffort, all only ever populated with the orchestrator on --
# via a `jq -S` diff against a checked-in <golden_name>.handoff.json fixture.
# A non-empty $ORCHESTRATOR_LOG is kept as a separate cheap proof that the
# orchestrator ran at all: Invoker is asserted byte-exact by the diff, but this
# catches a run that skipped the orchestrator entirely (a caller forgetting to
# export ORCHESTRATOR_ENABLED) with a clearer failure than a JSON diff would.
assert_review_handoff_golden() {
  local golden_name="$1"

  [ -s "$ORCHESTRATOR_LOG" ]

  # Since issue #2975 the Handoff carries the full driver-invocation fact set
  # plus per-run mktemp paths that can't be pinned byte-exact, so diff only the
  # orchestrator-only facts this harness exists to pin: Invoker, ReviewModel,
  # ReviewEffort. ReviewPromptFile is now a path rather than the text itself, so
  # assert it names a non-empty file that actually got written.
  assert_golden_json_or_update "$GOLDEN_DIR/${golden_name}.handoff.json" "$DRIVER_HANDOFF_FILE" \
    '{Invoker, ReviewModel, ReviewEffort}'

  local review_prompt_file
  review_prompt_file="$(jq -r '.ReviewPromptFile' "$DRIVER_HANDOFF_FILE")"
  [ -n "$review_prompt_file" ]
  [ -s "$review_prompt_file" ]
}

@test "assert_golden_text_or_update diffs and fails when golden and produced differ, UPDATE_GOLDENS unset" {
  local golden="$BATS_TEST_TMPDIR/golden.txt" produced="$BATS_TEST_TMPDIR/produced.txt"
  echo "golden content" >"$golden"
  echo "produced content" >"$produced"
  unset UPDATE_GOLDENS

  run assert_golden_text_or_update "$golden" "$produced"

  [ "$status" -eq 1 ]
  [[ "$output" == *"produced content"* ]]
  [ "$(cat "$golden")" = "golden content" ]
}

@test "assert_golden_text_or_update passes silently when golden and produced already match, UPDATE_GOLDENS unset" {
  local golden="$BATS_TEST_TMPDIR/golden.txt" produced="$BATS_TEST_TMPDIR/produced.txt"
  echo "same content" >"$golden"
  echo "same content" >"$produced"
  unset UPDATE_GOLDENS

  run assert_golden_text_or_update "$golden" "$produced"

  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "assert_golden_text_or_update overwrites golden with produced content when UPDATE_GOLDENS is set" {
  local golden="$BATS_TEST_TMPDIR/golden.txt" produced="$BATS_TEST_TMPDIR/produced.txt"
  echo "stale golden content" >"$golden"
  echo "fresh produced content" >"$produced"
  export UPDATE_GOLDENS=1

  run assert_golden_text_or_update "$golden" "$produced"

  [ "$status" -eq 0 ]
  [ "$(cat "$golden")" = "fresh produced content" ]
}

@test "assert_golden_json_or_update diffs and fails when canonicalized JSON differs, UPDATE_GOLDENS unset" {
  local golden="$BATS_TEST_TMPDIR/golden.json" produced="$BATS_TEST_TMPDIR/produced.json"
  echo '{"b": 1, "a": 2}' >"$golden"
  echo '{"b": 1, "a": 3}' >"$produced"
  unset UPDATE_GOLDENS

  run assert_golden_json_or_update "$golden" "$produced"

  [ "$status" -eq 1 ]
  [ "$(jq -S . "$golden")" = "$(jq -S . <<<'{"b": 1, "a": 2}')" ]
}

@test "assert_golden_json_or_update overwrites golden with canonicalized, projected JSON when UPDATE_GOLDENS is set" {
  local golden="$BATS_TEST_TMPDIR/golden.json" produced="$BATS_TEST_TMPDIR/produced.json"
  echo '{"Invoker": "stale", "ReviewModel": "stale-model", "ReviewEffort": "low", "PromptFile": "/tmp/stale"}' >"$golden"
  echo '{"Invoker": "orchestrator", "ReviewModel": "opus", "ReviewEffort": "high", "PromptFile": "/tmp/fresh"}' >"$produced"
  export UPDATE_GOLDENS=1

  run assert_golden_json_or_update "$golden" "$produced" '{Invoker, ReviewModel, ReviewEffort}'

  [ "$status" -eq 0 ]
  [ "$(jq -S . "$golden")" = "$(jq -S . <<<'{"Invoker": "orchestrator", "ReviewModel": "opus", "ReviewEffort": "high"}')" ]
}

# A realistic multi-agent roster -- scout, reviewer (present, not dropped: the
# orchestrator is off in the covered cell), and worker (the WORKER_PROVISIONED
# gate's partner axis to "skills baked").
AGENTS_ROSTER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep","WebFetch"]}}'

# AGENTS_ROSTER plus a "filer" entry, so the orchestrator-on filer-on cell and
# the two research filer-on cells below actually flip the FILER_ENABLED gate on,
# unlike AGENTS_ROSTER alone (the filer-off cells' roster).
AGENTS_ROSTER_WITH_FILER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep","WebFetch"]},"filer":{"description":"File issues from a review'"'"'s non-blocking findings, best-effort","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'

# AGENTS_ROSTER's reviewer entry plus an explicit "effort" key ("xhigh", a value
# distinct from every other effort/model literal in this file, so a golden diff
# fails loudly on an accidental drop or truncation rather than silently matching
# some other cell's default). Scout, reviewer, worker -- no "filer" key, so this
# cell isolates the reviewer-effort-override axis from the filer axis
# AGENTS_ROSTER_WITH_FILER already covers.
AGENTS_ROSTER_WITH_REVIEW_EFFORT='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","effort":"xhigh","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep","WebFetch"]}}'

@test "production path matches the golden fixture for the covered cell, with a populated roster" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  # AGENTS_ROSTER carries a "worker" key but no "filer" key (issue #2533).
  export BOX_WORKER_PROVISIONED=1

  assert_cell_golden "covered-cell-populated-roster" initial

  # Structurally guaranteed by the covered cell, not reverse-engineered from the
  # fake's capture files: the orchestrator gate is off, so the invoker is always
  # "driver-exec" and the orchestrator is never invoked at all.
  [ ! -s "$ORCHESTRATOR_LOG" ]
}

@test "production path matches the golden fixture for omitting the agents flag entirely with no roster" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: proves the "omit the --agents
  # flag/output stays empty" branch, not just the populated-roster branch above.
  unset AGENTS_JSON_TEMPLATE

  assert_cell_golden "no-roster" initial
}

@test "production path matches the golden fixture for the research cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: this cell is about the
  # prompt-selection/session-mode axis, not roster interaction.
  export DISPATCH_KIND="research"

  assert_cell_golden "research" initial
}

@test "production path matches the golden fixture for the research filer-on cell" {
  export DISPATCH_KIND="research"
  # AGENTS_ROSTER_WITH_FILER (not plain AGENTS_ROSTER, unlike the research cell
  # above): the roster/filer axis this cell isolates. No handoff fixture --
  # research never turns the orchestrator on.
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_FILER"
  export BOX_FILER_ENABLED=1
  # AGENTS_ROSTER_WITH_FILER carries a "worker" key too, same as every other
  # roster-exporting cell in this file.
  export BOX_WORKER_PROVISIONED=1

  assert_cell_golden "research-filer-on" initial
}

@test "production path matches the golden fixture for the self-contained research cell" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"

  assert_cell_golden "self-contained-research" initial
}

@test "production path matches the golden fixture for the self-contained research filer-on cell" {
  export DISPATCH_KIND="research"
  export SELF_CONTAINED="1"
  # Same roster/filer axis as the research filer-on cell above, isolated
  # against the self-contained knob instead (issue #2786).
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_FILER"
  export BOX_FILER_ENABLED=1
  export BOX_WORKER_PROVISIONED=1

  assert_cell_golden "self-contained-research-filer-on" initial
}

@test "production path matches the golden fixture for the fix-pass cell" {
  export FIX_PASS="1"

  assert_cell_golden "fix-pass" resume
}

@test "production path matches the golden fixture for the github read-only cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: this cell is about the
  # access/forge axis, not roster interaction.
  unset BOX_WRITE_ENABLED

  assert_cell_golden "github-read-only" initial
}

@test "production path matches the golden fixture for the forgejo read-write cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset, same reasoning as above.
  # BOX_WRITE_ENABLED stays at setup_entrypoint_env's read-write default, so
  # no extra flag override is needed here.
  export CODE_FORGE="forgejo"
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  # clone_repo requires FORGEJO_TOKEN and builds the clone URL as
  # https://<token>@<host>/<slug>.git; redirect that exact URL to the bare repo
  # setup_bare_repo already seeded so the clone stays offline.
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"

  assert_cell_golden "forgejo-read-write" initial
}

@test "production path matches the golden fixture for the forgejo read-only cell" {
  # AGENTS_JSON_TEMPLATE deliberately left unset, same reasoning as above.
  export CODE_FORGE="forgejo"
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  unset BOX_WRITE_ENABLED
  # clone_repo requires FORGEJO_TOKEN and builds the clone URL as
  # https://<token>@<host>/<slug>.git; redirect that exact URL to the bare repo
  # setup_bare_repo already seeded so the clone stays offline.
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"

  assert_cell_golden "forgejo-read-only" initial
}

@test "production path matches the golden fixture for the local tracker cell, no issue reference" {
  # AGENTS_JSON_TEMPLATE deliberately left unset: this cell is about the tracker
  # axis. SessionMode stays "initial" -- ISSUE_TRACKER is orthogonal to dispatch
  # kind/fix-pass.
  export ISSUE_TRACKER="local"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE

  assert_cell_golden "local-tracker-no-issue-ref" initial
}

@test "production path matches the golden fixture for the local tracker cell, issue reference on" {
  export ISSUE_TRACKER="local"
  export LOCAL_ISSUE_REFERENCE="1"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE

  assert_cell_golden "local-tracker-issue-ref-on" initial
}

@test "production path matches the golden fixture for the forgejo tracker cell" {
  export ISSUE_TRACKER="forgejo"
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO

  assert_cell_golden "forgejo-tracker" initial
}

@test "production path matches the golden fixture for the jira tracker cell" {
  # jira rides the same prompt-selection arms as github. The Go side's
  # byte-identity between jira and github is already pinned by a Go unit test;
  # this cell additionally proves the production BASH path renders jira through
  # that same arm.
  export ISSUE_TRACKER="jira"

  assert_cell_golden "jira-tracker" initial
}

# Four orchestrator-on cells (dispatch kind "work", FIX_PASS unset -- the only
# orchestrator-on path checkCoveredCell covers). Unlike the cells above,
# ORCHESTRATOR_ENABLED must be exported so the bash side takes
# run_driver_in_env's orchestrator invocation path.

@test "production path matches the golden fixture for the orchestrator-on filer-on cell" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_FILER"
  # AGENTS_ROSTER_WITH_FILER carries both a "worker" key and a "filer" key.
  export BOX_FILER_ENABLED=1
  export BOX_WORKER_PROVISIONED=1

  assert_cell_golden "orchestrator-filer-on" initial

  assert_review_handoff_golden "orchestrator-filer-on"
}

@test "production path matches the golden fixture for the orchestrator-on filer-off cell" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # AGENTS_ROSTER (not the _WITH_FILER variant): scout+reviewer, no "filer" key
  # -- the FILER_ENABLED-off half of the roster axis. Reviewer is present in
  # both halves, so both cells assert ReviewModel the same way.
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  # AGENTS_ROSTER carries a "worker" key but no "filer" key.
  export BOX_WORKER_PROVISIONED=1

  assert_cell_golden "orchestrator-filer-off" initial

  assert_review_handoff_golden "orchestrator-filer-off"
}

@test "production path matches the golden fixture for the orchestrator-on review-effort-set cell" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # AGENTS_ROSTER_WITH_REVIEW_EFFORT: the reviewer entry's "effort":"xhigh" is
  # the whole point of this cell -- the non-empty-overrides case, contrasting
  # the filer-on/filer-off cells whose reviewer carries no "effort" key at all.
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_REVIEW_EFFORT"
  # AGENTS_ROSTER_WITH_REVIEW_EFFORT carries a "worker" key but no "filer" key.
  export BOX_WORKER_PROVISIONED=1

  assert_cell_golden "orchestrator-review-effort-set" initial

  assert_review_handoff_golden "orchestrator-review-effort-set"
}

@test "production path matches the golden fixture for the orchestrator-on skills-absent cell" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  # AGENTS_ROSTER carries a "worker" key but no "filer" key.
  export BOX_WORKER_PROVISIONED=1

  # Contrast setup()'s unconditional 4-skill baking: this cell is the
  # `SkillsFound == "" and every *SkillBaked flag false` branch checkCoveredCell
  # also covers. Remove the skill dirs setup() just baked, rather than
  # restructuring setup() itself, which every other cell relies on.
  rm -rf "$HOME/.claude/skills"

  assert_cell_golden "orchestrator-skills-absent" initial

  assert_review_handoff_golden "orchestrator-skills-absent"
}
