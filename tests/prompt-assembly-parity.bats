#!/usr/bin/env bats
# Golden harness for the production prompt-assembly path (issues #2349, #2350,
# #2351, #2352, #2353, #2354, #2395): each cell runs $ENTRYPOINT, whose fake
# driver chain execs the real Go `assemble-prompt` (ADR 0036), and diffs the
# captured prompt, agents and handoff artifacts against the checked-in goldens.

load helper

setup() {
  setup_entrypoint_env

  # phase_prompt_assembly copies HARNESS_SKILLS_DIR and OPERATOR_SKILLS_DIR into
  # DRIVER_SKILLS_DIR before the SKILLS_FOUND scan, and both default to real host
  # paths outside this test's sandbox. A Box with its own skills baked there
  # widens every cell's roster, so the goldens then only match that one machine
  # (issue #2059). Point both at guaranteed-empty directories.
  export HARNESS_SKILLS_DIR="$BATS_TEST_TMPDIR/no-harness-skills"
  export OPERATOR_SKILLS_DIR="$BATS_TEST_TMPDIR/no-operator-skills"

  # The covered cell requires every per-skill gate on and a non-empty
  # SKILLS_FOUND (assemble.go's checkCoveredCell), so bake all six.
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

  mkdir -p "$HOME/.claude/skills/check-hygiene"
  cat >"$HOME/.claude/skills/check-hygiene/SKILL.md" <<'SKILL'
---
name: check-hygiene
description: Run a check or build gate and read its output without losing the run.
---
Foreground the gate; grep the log on disk.
SKILL

  mkdir -p "$HOME/.claude/skills/code-comments"
  cat >"$HOME/.claude/skills/code-comments/SKILL.md" <<'SKILL'
---
name: code-comments
description: Judge whether a comment earns its place before writing it.
---
Only the non-obvious why, a constraint, or a gotcha qualifies.
SKILL
}

GOLDEN_DIR="${BATS_TEST_DIRNAME}/testdata/prompt-assembly-golden"

# UPDATE_GOLDENS (issue #2951) overwrites the golden instead of diffing it.
assert_golden_text_or_update() {
  local golden_file="$1" produced_file="$2"
  if [ -n "${UPDATE_GOLDENS:-}" ]; then
    cp "$produced_file" "$golden_file"
  else
    diff "$golden_file" "$produced_file"
  fi
}

# Both sides go through `jq -S` and jq_filter first, so key order never causes a
# spurious diff and a golden written in update mode is always canonical.
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

# Runs the real entrypoint over whatever env the caller exported, then diffs the
# captured artifacts against <golden_name>.{prompt.txt,agents.json} and reads
# SessionMode from the real Handoff JSON (issue #2395). expected_session_mode is
# an argument rather than a golden: the calling test already pins that value, so
# a fixture would add no independent fact.
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

# Pins the Handoff facts only the orchestrator-on cells populate (issues #2353
# and #2512), which assert_cell_golden does not cover. The separate
# $ORCHESTRATOR_LOG check catches a run that skipped the orchestrator entirely,
# say a caller that forgot ORCHESTRATOR_ENABLED, with a clearer failure than the
# JSON diff gives.
assert_review_handoff_golden() {
  local golden_name="$1"

  [ -s "$ORCHESTRATOR_LOG" ]

  # Since issue #2975 the Handoff also carries per-run mktemp paths that no
  # golden can pin byte-exact, so diff only the orchestrator-only facts and
  # assert ReviewPromptFile merely names a file that actually got written.
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

# issue #2349: a realistic multi-agent roster. The reviewer entry stays even
# though the covered cell leaves the orchestrator off.
AGENTS_ROSTER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}'

# issue #3157 (AC5): the roster minus its "scout" key, isolating the
# worker-provisioned and scout-absent combination no other cell here pins.
AGENTS_ROSTER_NO_SCOUT='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}'

# issue #3163: the roster minus its "worker" key, isolating the
# scout-provisioned and worker-absent combination no other cell here pins.
AGENTS_ROSTER_NO_WORKER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'

# issue #2353: AGENTS_ROSTER plus a "filer" entry, so the filer-on cells below
# actually flip the FILER_ENABLED gate that plain AGENTS_ROSTER leaves off.
AGENTS_ROSTER_WITH_FILER='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]},"filer":{"description":"File issues from a review'"'"'s non-blocking findings, best-effort","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'

# issue #2512: AGENTS_ROSTER whose reviewer carries an explicit "effort". The
# value "xhigh" is distinct from every other effort and model literal in this
# file, so a dropped or truncated field fails the diff instead of matching some
# other cell's default. No "filer" key, keeping this cell off the filer axis.
AGENTS_ROSTER_WITH_REVIEW_EFFORT='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","effort":"xhigh","prompt":"","tools":["Read","Bash","WebFetch"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}'

@test "production path matches the golden fixture for the covered cell, with a populated roster" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  # AGENTS_ROSTER has no "filer" key, so BOX_FILER_ENABLED stays off
  # (issue #2533).
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

  assert_cell_golden "covered-cell-populated-roster" initial

  # The covered cell leaves the orchestrator gate off, so the invoker is always
  # "driver-exec" and the orchestrator never runs at all.
  [ ! -s "$ORCHESTRATOR_LOG" ]
}

# issue #3445: one line carries a literal triple-backtick run, the untrusted
# shape promptfence.Block's dynamic fence widening exists for (CLAUDE.md's
# comment-injection trust boundary). Untrusted text must not close its own fence.
ISSUE_TEXT_FIXTURE='Widgets double-count on retry when the frobnicator restarts mid-batch.

Repro:
```
frobnicate --retry --batch=widgets
```

Expected: each widget counted once. Actual: counted twice on retry.'

@test "production path matches the golden fixture for the covered cell, with ISSUE_TEXT set" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1
  export ISSUE_TEXT="$ISSUE_TEXT_FIXTURE"

  assert_cell_golden "covered-cell-issue-text" initial
}

# issue #3447: AGENTS_ROSTER plus the "review-axis" fan-out entry, the one roster
# shape here that flips code-review-baked.md's ${REVIEW_FANOUT_AGENT} from the
# general-purpose fallback to the governed name.
AGENTS_ROSTER_WITH_REVIEW_AXIS='{"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]},"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"review-axis":{"description":"Run one axis (Standards or Spec) of the code-review skill'"'"'s two-axis fan-out","model":"haiku","prompt":"","tools":["Read","Bash","Glob","Grep"]},"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep"]}}'

@test "production path matches the golden fixture for a roster that provisions review-axis" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_REVIEW_AXIS"
  # setup_entrypoint_env's default maps only the historical four names, so the
  # fan-out entry needs its own row for the injection loop to reach
  # templates/default/prompts/review-axis-prompt.md.
  export AGENTS_PROMPT_FILES='{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md","review-axis":"review-axis-prompt.md"}'
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

  assert_cell_golden "covered-cell-review-axis-roster" initial
}

@test "production path matches the golden fixture for a worker-provisioned, scout-absent roster" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_NO_SCOUT"
  export BOX_WORKER_PROVISIONED=1
  # BOX_SCOUT_PROVISIONED deliberately unset (issue #3157 AC5): pins that a
  # no-scout run leaves no dangling reference to a brief nobody wrote, on the
  # worker-on path the no-roster cell does not cover.

  assert_cell_golden "worker-no-scout" initial
}

@test "production path matches the golden fixture for a scout-provisioned, worker-absent roster" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_NO_WORKER"
  export BOX_SCOUT_PROVISIONED=1
  # BOX_WORKER_PROVISIONED deliberately unset (issue #3163): pins the whole
  # scout-solo prompt — the # SCOUT section alongside the absent coordinator
  # and worker blocks — which is the scout-on path the no-scout cell above
  # does not cover.

  assert_cell_golden "scout-no-worker" initial
}

@test "production path matches the golden fixture for omitting the agents flag entirely with no roster" {
  # AGENTS_JSON_TEMPLATE deliberately unset: pins the branch that omits the
  # --agents flag and leaves the output empty.
  unset AGENTS_JSON_TEMPLATE

  assert_cell_golden "no-roster" initial
}

@test "production path matches the golden fixture for the research cell" {
  # AGENTS_JSON_TEMPLATE deliberately unset: this cell is about the
  # prompt-selection and session-mode axis, not roster interaction.
  export DISPATCH_KIND="research"

  assert_cell_golden "research" initial
}

@test "production path matches the golden fixture for the research filer-on cell" {
  export DISPATCH_KIND="research"
  # A filer in the roster with BOX_FILER_ENABLED=1 pins gates_tracker.go's
  # researchForceRelay, which forces the verdict comment onto the
  # SPINDRIFT_COMMENT relay arm even though this suite's box is read-write
  # (issue #2786). No handoff fixture: research never turns the orchestrator on.
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_FILER"
  export BOX_FILER_ENABLED=1
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

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
  # The same filer axis as the research filer-on cell above, isolated against
  # the self-contained knob instead (issue #2786).
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_FILER"
  export BOX_FILER_ENABLED=1
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

  assert_cell_golden "self-contained-research-filer-on" initial
}

@test "production path matches the golden fixture for the fix-pass cell" {
  export FIX_PASS="1"

  assert_cell_golden "fix-pass" resume
}

@test "production path matches the golden fixture for the github read-only cell" {
  # AGENTS_JSON_TEMPLATE deliberately unset: this cell is about the access and
  # forge axis, not roster interaction.
  unset BOX_WRITE_ENABLED

  assert_cell_golden "github-read-only" initial
}

@test "production path matches the golden fixture for the forgejo read-write cell" {
  # BOX_WRITE_ENABLED stays at setup_entrypoint_env's read-write default.
  export CODE_FORGE="forgejo"
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  # clone_repo builds the clone URL as https://<token>@<host>/<slug>.git.
  # Redirect that exact URL to the bare repo setup_bare_repo already seeded,
  # so the clone stays offline.
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"

  assert_cell_golden "forgejo-read-write" initial
}

@test "production path matches the golden fixture for the forgejo read-only cell" {
  export CODE_FORGE="forgejo"
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  unset BOX_WRITE_ENABLED
  # clone_repo builds the clone URL as https://<token>@<host>/<slug>.git.
  # Redirect that exact URL to the bare repo setup_bare_repo already seeded,
  # so the clone stays offline.
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"

  assert_cell_golden "forgejo-read-only" initial
}

@test "production path matches the golden fixture for the local tracker cell, no issue reference" {
  # AGENTS_JSON_TEMPLATE deliberately unset: this cell is about the tracker
  # axis, not roster interaction. SessionMode stays "initial" because
  # ISSUE_TRACKER is orthogonal to dispatch kind and fix-pass.
  export ISSUE_TRACKER="local"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE

  assert_cell_golden "local-tracker-no-issue-ref" initial
}

# issue #3469: a host-rendered forge.IssueText fixture covering both block kinds
# issuetext.go's renderLinkedIssues emits. The local tracker's issue-read
# fragment used to tell the agent to walk the link chain in-box; this pins that
# the chain now arrives pre-rendered through ISSUE_TEXT instead.
ISSUE_TEXT_LOCAL_TRACKER_FIXTURE='Retry math drifts when two frobnicators race the same batch.

## Linked issues

### widget-lock — Add per-batch locking (blocked-by of retry-math)

status: open

Guard the batch counter with a mutex before the retry path lands.

### Unresolved references

- https://github.com/o/r/issues/9 (parent of retry-math): issue not found'

@test "production path matches the golden fixture for the local tracker cell, with ISSUE_TEXT set" {
  export ISSUE_TRACKER="local"
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  export ISSUE_TEXT="$ISSUE_TEXT_LOCAL_TRACKER_FIXTURE"

  assert_cell_golden "local-tracker-issue-text" initial
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
  # jira rides the same prompt-selection arms as github (assemble.go's
  # checkCoveredCell). A Go unit test already pins that byte-identity; this
  # cell proves the bash path reaches the same arm.
  export ISSUE_TRACKER="jira"

  assert_cell_golden "jira-tracker" initial
}

# issue #2353: the orchestrator-on cells run dispatch kind "work" with FIX_PASS
# unset, the only orchestrator-on path checkCoveredCell covers. Each must export
# ORCHESTRATOR_ENABLED so the bash side takes run_driver_in_env's orchestrator
# invocation path.

@test "production path matches the golden fixture for the orchestrator-on filer-on cell" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_FILER"
  export BOX_FILER_ENABLED=1
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

  assert_cell_golden "orchestrator-filer-on" initial

  assert_review_handoff_golden "orchestrator-filer-on"
}

@test "production path matches the golden fixture for the orchestrator-on filer-off cell" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # AGENTS_ROSTER, not the _WITH_FILER variant: the FILER_ENABLED-off half of
  # the roster axis. The reviewer is present either way, so both cells assert
  # ReviewModel the same way through assert_review_handoff_golden.
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

  assert_cell_golden "orchestrator-filer-off" initial

  assert_review_handoff_golden "orchestrator-filer-off"
}

@test "production path matches the golden fixture for the orchestrator-on skills-absent cell" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

  # This cell is checkCoveredCell's "SkillsFound empty, every *SkillBaked flag
  # false" branch. Remove the dirs setup() just baked rather than restructuring
  # setup(), which every other cell relies on baking unconditionally.
  rm -rf "$HOME/.claude/skills"

  assert_cell_golden "orchestrator-skills-absent" initial

  assert_review_handoff_golden "orchestrator-skills-absent"
}

@test "production path matches the golden fixture for the orchestrator-on review-effort-set cell" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # The reviewer's "effort":"xhigh" is the point of this cell: issue #2512's AC2
  # non-empty-overrides case, against the filer-on and filer-off cells above
  # whose reviewer has no "effort" key at all (the empty-follows-roster case).
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER_WITH_REVIEW_EFFORT"
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

  assert_cell_golden "orchestrator-review-effort-set" initial

  assert_review_handoff_golden "orchestrator-review-effort-set"
}

@test "production path matches the golden fixture for the tdd-skill-absent cell" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

  # Remove only tdd's SKILL.md, leaving the rest baked: the realistic shape,
  # since a consumer bakes a subset (issue #3219). The skills-absent cell above
  # flips every per-skill gate at once, so it cannot tell "tdd's unbaked
  # fallback renders" apart from "no skill fragment renders at all".
  rm -rf "$HOME/.claude/skills/tdd"

  assert_cell_golden "tdd-skill-absent" initial
}

@test "production path matches the golden fixture for the commit-skill-absent cell" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1
  # Remove only commit's SKILL.md, leaving the rest baked (issue #3222). The
  # skills-absent cell above flips every per-skill gate at once, so it cannot
  # tell "commit's unbaked fallback renders" apart from "no skill fragment
  # renders at all".
  rm -rf "$HOME/.claude/skills/commit"
  assert_cell_golden "commit-skill-absent" initial
}

@test "production path matches the golden fixture for the code-review-skill-absent cell" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1
  # Remove only code-review's SKILL.md, leaving the rest baked (issue #3222).
  # AGENTS_ROSTER's "reviewer" key is what makes this cell exercise
  # review-prompt.md at all: the gate renders into .agents.json's
  # reviewer.prompt, not $DRIVER_PROMPT_FILE.
  rm -rf "$HOME/.claude/skills/code-review"
  assert_cell_golden "code-review-skill-absent" initial
}

@test "production path matches the golden fixture for the nix-checks-skill-baked cell" {
  export AGENTS_JSON_TEMPLATE="$AGENTS_ROSTER"
  export BOX_WORKER_PROVISIONED=1
  export BOX_SCOUT_PROVISIONED=1

  # Bake nix-checks on top of setup()'s six: it is dogfood-only, so no other
  # cell bakes it, and NIX_CHECKS_BAKED's anchor would otherwise render nowhere
  # in the golden matrix, free to regress to nothing (issue #3223).
  mkdir -p "$HOME/.claude/skills/nix-checks"
  cat >"$HOME/.claude/skills/nix-checks/SKILL.md" <<'SKILL'
---
name: nix-checks
description: Run Nix checks the way a Nix flake repo expects.
---
Scoped check target over full flake check; git add before first build.
SKILL

  assert_cell_golden "nix-checks-skill-baked" initial
}
