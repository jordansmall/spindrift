#!/usr/bin/env bats
# Conditional prompt steps rendered from fragment files, and substitution (issue #463).

load helper

setup() {
  setup_entrypoint_env
}

# issue #622/#688: this mechanism test walks 3 of the registry's nine rows
# (lib/fragments.nix) -- AUTO_FORMAT and AUTO_LINT, both knob-gated, plus
# FILER_ENABLED/file-issues, which is computed-gated -- across their shared
# off/on matrix: each row renders its marker heading only when its gate is on,
# and leaves zero residue when off. CODE_REVIEW_BAKED has its own tests further
# down; the other five rows are covered in tests/entrypoint-skills.bats and
# tests/entrypoint-prompt-assembly.bats.
@test "conditional prompt steps appear only when their knob is on" {
  local case i=0
  for case in \
    'AGENTS_JSON_TEMPLATE={"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}|# FILE ISSUES' \
    'AUTO_FORMAT=1|# AUTO-FORMAT' \
    'AUTO_LINT=1|# AUTO-LINT'
  do
    local assign="${case%%|*}" marker="${case#*|}"

    # A fresh WORK_DIR per invocation -- entrypoint.sh clones into it and this
    # test execs the entrypoint six times, so reusing one dir would collide on
    # the second clone.
    i=$((i + 1))
    export WORK_DIR="$BATS_TEST_TMPDIR/work-$i-off"
    run bash "$ENTRYPOINT"
    [ "$status" -eq 0 ]
    ! grep -qF "$marker" "$DRIVER_PROMPT_FILE"

    # shellcheck disable=SC2163 # $assign is itself a NAME=value pair
    export "$assign"
    # BOX_FILER_ENABLED is not a schema knob (issue #2533): FILER_ENABLED is
    # nix-computed roster-presence forwarded verbatim, so this loop's
    # AGENTS_JSON_TEMPLATE case must flip the BOX_* var too to trip the gate.
    if [[ "$assign" == AGENTS_JSON_TEMPLATE=* ]]; then
      export BOX_FILER_ENABLED=1
    fi
    export WORK_DIR="$BATS_TEST_TMPDIR/work-$i-on"
    run bash "$ENTRYPOINT"
    [ "$status" -eq 0 ]
    grep -qF "$marker" "$DRIVER_PROMPT_FILE"
    unset "${assign%%=*}"
    if [[ "$assign" == AGENTS_JSON_TEMPLATE=* ]]; then
      unset BOX_FILER_ENABLED
    fi
  done
}

# issue #1429/ADR 0029: the PR-body ticket-reference step is the one registry
# row with three mutually exclusive fragments instead of an on/off pair --
# ISSUE_TRACKER x LOCAL_ISSUE_REFERENCE pick exactly one of
# PR_BODY_CLOSES/PR_BODY_LOCAL_REF/PR_BODY_LOCAL_NOREF. box_env_gen.bash
# already exports ISSUE_TRACKER=github, so the first case needs no override.
@test "PR-body reference: github tracker keeps Closes unchanged" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-pr-body-github"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Closes #7' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'Local-issue:' "$DRIVER_PROMPT_FILE"
}

@test "PR-body reference: local tracker defaults to no reference at all" {
  export ISSUE_TRACKER=local
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-pr-body-local-off"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qF 'Closes #7' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'Local-issue:' "$DRIVER_PROMPT_FILE"
}

@test "PR-body reference: local tracker opt-in emits a Local-issue breadcrumb, never Closes" {
  export ISSUE_TRACKER=local
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  export LOCAL_ISSUE_REFERENCE=1
  export WORK_DIR="$BATS_TEST_TMPDIR/work-pr-body-local-on"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Local-issue: 7' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'Closes #7' "$DRIVER_PROMPT_FILE"
}

# Same conditional-residue guarantee as the AUTO-FORMAT/AUTO-LINT pair above,
# but this step abuts the next paragraph on the same template line rather than
# a following heading, so the failure mode is the two gluing together with no
# blank line, not a missing heading.
@test "PR-body reference step stays separated from the following paragraph" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-pr-body-sep"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'know\.The PR opens' "$DRIVER_PROMPT_FILE"
}

# issue #1691/ADR 0032: the issue-read step's ISSUE_TRACKER_GITHUB/
# ISSUE_TRACKER_LOCAL gates drive four row pairs -- this exercises
# issue-prompt.md's, the one DRIVER_PROMPT_FILE captures directly; the other
# three are covered at the fragment-content level by nix/checks/prompts.nix.
@test "issue-read step: github tracker reads the issue with bounded comments" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-issue-read-github"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue view 7 --json body,comments --jq' "$DRIVER_PROMPT_FILE"
  grep -qF 'comments[-10:]' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'gh issue view 7 --comments`' "$DRIVER_PROMPT_FILE"
  ! grep -qF '/issues/7.md' "$DRIVER_PROMPT_FILE"
}

@test "issue-read step: local tracker reads the /issues mount, never gh issue view" {
  export ISSUE_TRACKER=local
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-issue-read-local"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF '/issues/7.md' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'gh issue view' "$DRIVER_PROMPT_FILE"
}

# issue #1963: the forgejo tracker's third issue-read gate cell
# (ISSUE_TRACKER_FORGEJO) speaks fj instead of gh.
@test "issue-read step: forgejo tracker reads the issue with fj, never gh issue view" {
  export ISSUE_TRACKER=forgejo
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  export WORK_DIR="$BATS_TEST_TMPDIR/work-issue-read-forgejo"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'fj issue view 7' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'gh issue view' "$DRIVER_PROMPT_FILE"
}

# jira maps to the same gh-flavored path as github (ISSUE_TRACKER_GITHUB=1 ->
# gh issue view) -- this guards that mapping so a future refactor collapsing
# the per-tracker gates into a per-axis case (whose `*)` arm covers github
# AND jira) can't silently regress jira onto fj or the local mount.
@test "issue-read step: jira tracker reads the issue with gh, never fj or the local mount" {
  export ISSUE_TRACKER=jira
  export WORK_DIR="$BATS_TEST_TMPDIR/work-issue-read-jira"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue view' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'fj issue view' "$DRIVER_PROMPT_FILE"
  ! grep -qF '/issues/7.md' "$DRIVER_PROMPT_FILE"
}

# issue #1692/ADR 0032: the local content-plane write step. A local Dispatch's
# Box has no in-box tracker client, so the research verdict travels as a single
# nonce-guarded SPINDRIFT_COMMENT line on stdout instead of a direct gh issue
# comment, and the work blocked-note step is a no-op in-box (settle posts it
# host-side).
@test "research verdict step: github tracker keeps gh issue comment unchanged" {
  export DISPATCH_KIND="research"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-research-verdict-github"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue comment 7' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'SPINDRIFT_COMMENT_BEGIN' "$DRIVER_PROMPT_FILE"
}

@test "research verdict step: local tracker emits a nonce-guarded SPINDRIFT_COMMENT line, never gh issue comment" {
  export DISPATCH_KIND="research"
  export ISSUE_TRACKER=local
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-research-verdict-local"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'SPINDRIFT_COMMENT deadbeefcafe1234' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'SPINDRIFT_COMMENT_BEGIN' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'SPINDRIFT_COMMENT_END' "$DRIVER_PROMPT_FILE"
  # Not the bare substring: the unconditional OUTCOME section still explains
  # the github-side `gh issue comment` URL source for contrast. It's the
  # invocation shape (issue number immediately after) that must be absent.
  ! grep -qF 'gh issue comment 7' "$DRIVER_PROMPT_FILE"
}

@test "issue blocked-comment step: github tracker keeps gh issue comment unchanged" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-blocked-comment-github"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue comment 7' "$DRIVER_PROMPT_FILE"
}

@test "issue blocked-comment step: local tracker never runs gh issue comment" {
  export ISSUE_TRACKER=local
  export BOX_TRACKER_AXIS_READ=LOCAL
  unset BOX_TRACKER_AXIS_WRITE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-blocked-comment-local"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qF 'gh issue comment' "$DRIVER_PROMPT_FILE"
  grep -qF 'the launcher posts the SPINDRIFT_OUTCOME' "$DRIVER_PROMPT_FILE"
}

# issue #1917: read-only (BOX_WRITE_ENABLED absent) strips the Box's write
# token, so a github tracker's write-step gate (ISSUE_TRACKER_GITHUB_READONLY,
# distinct from the issue-read gates above) must render the same host-mediated
# relay form local always gets, never the in-box gh issue comment invocation.
@test "research verdict step: github tracker under read-only relays via a nonce-guarded SPINDRIFT_COMMENT line, never gh issue comment" {
  export DISPATCH_KIND="research"
  unset BOX_WRITE_ENABLED
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-research-verdict-github-readonly"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'SPINDRIFT_COMMENT deadbeefcafe1234' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'SPINDRIFT_COMMENT_BEGIN' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'SPINDRIFT_COMMENT_END' "$DRIVER_PROMPT_FILE"
  # Not the bare substring: research-prompt.md's unconditional OUTCOME section
  # names `gh issue comment` (with no issue number) for contrast, same reason
  # the local variant's test above pins the invocation shape.
  ! grep -qF 'gh issue comment 7' "$DRIVER_PROMPT_FILE"
}

@test "issue blocked-comment step: github tracker under read-only never runs gh issue comment" {
  unset BOX_WRITE_ENABLED
  export WORK_DIR="$BATS_TEST_TMPDIR/work-blocked-comment-github-readonly"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qF 'gh issue comment' "$DRIVER_PROMPT_FILE"
  grep -qF 'the launcher posts it as the issue comment' "$DRIVER_PROMPT_FILE"
}

# jira rides github's write-step arm too: the consolidated `_it_write` case
# maps jira through its `*)` catch-all onto ISSUE_TRACKER_GITHUB_READWRITE/
# _READONLY (issue #2214). Guard both halves so a future forge added to that
# arm can't silently regress jira's blocked-note off the gh-flavored path.
@test "issue blocked-comment step: jira tracker under read-write keeps gh issue comment unchanged" {
  export ISSUE_TRACKER=jira
  export WORK_DIR="$BATS_TEST_TMPDIR/work-blocked-comment-jira-readwrite"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue comment 7' "$DRIVER_PROMPT_FILE"
}

@test "issue blocked-comment step: jira tracker under read-only never runs gh issue comment" {
  export ISSUE_TRACKER=jira
  unset BOX_WRITE_ENABLED
  export WORK_DIR="$BATS_TEST_TMPDIR/work-blocked-comment-jira-readonly"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qF 'gh issue comment' "$DRIVER_PROMPT_FILE"
  grep -qF 'the launcher posts it as the issue comment' "$DRIVER_PROMPT_FILE"
}

@test "research verdict step: github tracker under read-write is unaffected by the new gate" {
  export DISPATCH_KIND="research"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-research-verdict-github-readwrite-explicit"
  # setup_entrypoint_env already exports BOX_WRITE_ENABLED=1, so this
  # read-write case needs no override.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue comment 7' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'SPINDRIFT_COMMENT_BEGIN' "$DRIVER_PROMPT_FILE"
}

# issue #1963: the forgejo-side counterpart of the github write-step gates
# above (ISSUE_TRACKER_FORGEJO_READWRITE/_READONLY).
@test "research verdict step: forgejo tracker under read-write keeps fj issue comment unchanged" {
  export DISPATCH_KIND="research"
  export ISSUE_TRACKER=forgejo
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  export WORK_DIR="$BATS_TEST_TMPDIR/work-research-verdict-forgejo-readwrite"
  # setup_entrypoint_env already exports BOX_WRITE_ENABLED=1, so this
  # read-write case needs no override.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'fj issue comment 7' "$DRIVER_PROMPT_FILE"
}

@test "research verdict step: forgejo tracker under read-only relays via a nonce-guarded SPINDRIFT_COMMENT line, never fj issue comment" {
  export DISPATCH_KIND="research"
  export ISSUE_TRACKER=forgejo
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  unset BOX_WRITE_ENABLED
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-research-verdict-forgejo-readonly"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'SPINDRIFT_COMMENT deadbeefcafe1234' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'fj issue comment 7' "$DRIVER_PROMPT_FILE"
}

@test "issue blocked-comment step: forgejo tracker under read-write keeps fj issue comment unchanged" {
  export ISSUE_TRACKER=forgejo
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  export WORK_DIR="$BATS_TEST_TMPDIR/work-blocked-comment-forgejo-readwrite"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'fj issue comment 7' "$DRIVER_PROMPT_FILE"
}

@test "issue blocked-comment step: forgejo tracker under read-only never runs fj issue comment" {
  export ISSUE_TRACKER=forgejo
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  unset BOX_WRITE_ENABLED
  export WORK_DIR="$BATS_TEST_TMPDIR/work-blocked-comment-forgejo-readonly"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qF 'fj issue comment' "$DRIVER_PROMPT_FILE"
  grep -qF 'the launcher posts it as the issue comment' "$DRIVER_PROMPT_FILE"
}

# issue #1918: the OPEN A PULL REQUEST push step's BOX_ACCESS_READ_WRITE/
# BOX_ACCESS_READ_ONLY gates, derived from BOX_WRITE_ENABLED.
# setup_entrypoint_env already exports BOX_WRITE_ENABLED=1, so the first case
# needs no override.
@test "OPEN A PULL REQUEST push step: read-write keeps git push unchanged" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-open-pr-push-read-write"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'git push --force-with-lease -u origin' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'seam.bundle' "$DRIVER_PROMPT_FILE"
}

@test "OPEN A PULL REQUEST push step: read-only takes no bundle/push action -- harness lands the committed branch" {
  unset BOX_WRITE_ENABLED
  export WORK_DIR="$BATS_TEST_TMPDIR/work-open-pr-push-read-only"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qF '/outbox/seam.bundle' "$DRIVER_PROMPT_FILE"

  # Scoped to the OPEN A PULL REQUEST section -- the earlier COMMIT section's
  # generic rebase-then-push guidance also contains the literal 'git push
  # --force-with-lease -u origin', and the IF BLOCKED read-only fragment shares
  # the 'harness relays' phrase, so a whole-file grep would false-pass.
  local open_pr_section
  open_pr_section="$(awk '/^# OPEN A PULL REQUEST/,/^# OUTCOME/' "$DRIVER_PROMPT_FILE")"
  grep -qF 'harness relays your committed branch out' <<<"$open_pr_section"
  ! grep -qF 'git push --force-with-lease -u origin' <<<"$open_pr_section"
}

# Same conditional-residue separation guarantee as the PR-body reference step's
# own test above, this time against the following `2. gh pr create` line.
@test "OPEN A PULL REQUEST push step stays separated from the gh pr create step" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-open-pr-push-sep-rw"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q '`2\. `gh pr create' "$DRIVER_PROMPT_FILE"

  unset BOX_WRITE_ENABLED
  export WORK_DIR="$BATS_TEST_TMPDIR/work-open-pr-push-sep-ro"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'attempt\.2\. `gh pr create' "$DRIVER_PROMPT_FILE"
}

# issue #1919: the OPEN A PULL REQUEST create step's BOX_ACCESS_READ_WRITE/
# BOX_ACCESS_READ_ONLY gates -- the counterpart to #1918's push-step gates
# above, this time for `gh pr create` itself. setup_entrypoint_env already
# exports BOX_WRITE_ENABLED=1, so the first case needs no override.
@test "OPEN A PULL REQUEST create step: read-write keeps gh pr create unchanged" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-open-pr-create-read-write"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh pr create --draft' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'fj pr create' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'SPINDRIFT_PR_INTENT_BEGIN' "$DRIVER_PROMPT_FILE"
}

# issue #1963: the create step's read-write case further forks on CODE_FORGE
# (OPEN_PR_CREATE_RW_GH/OPEN_PR_CREATE_RW_FORGEJO) -- a forgejo Box opens its
# draft PR with `fj pr create`, never `gh pr create`.
@test "OPEN A PULL REQUEST create step: forgejo read-write uses fj pr create, never gh pr create" {
  export ISSUE_TRACKER=forgejo
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  export CODE_FORGE=forgejo
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  # clone_repo requires FORGEJO_TOKEN and builds the clone URL as
  # https://<token>@<host>/<slug>.git; redirect that exact URL to the bare repo
  # setup_bare_repo already seeded so the clone stays offline.
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-open-pr-create-forgejo-read-write"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Scoped to the OPEN A PULL REQUEST section: LAND THE CHANGE's own
  # CODE_FORGE=forgejo branch mentions `fj pr create` in prose regardless of
  # read/write mode, so a whole-file grep would false-positive.
  local open_pr_section
  open_pr_section="$(awk '/^# OPEN A PULL REQUEST/,/^# OUTCOME/' "$DRIVER_PROMPT_FILE")"
  # Anchored to the step-2 invocation, not a bare substring: the forgejo
  # fragment's step 2 also carries a "Do NOT run `gh pr create`" reminder.
  grep -qE '^2\. `fj pr create' <<<"$open_pr_section"
  ! grep -qE '^2\. `gh pr create' <<<"$open_pr_section"
}

# Read-only stays forge-agnostic (SPINDRIFT_PR_INTENT relay, issue #1963) --
# a read-only forgejo Box must never render `fj pr create` either.
@test "OPEN A PULL REQUEST create step: forgejo read-only stays forge-agnostic via SPINDRIFT_PR_INTENT, never fj pr create" {
  export ISSUE_TRACKER=forgejo
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  export CODE_FORGE=forgejo
  export BOX_FORGE_BACKEND=FORGEJO
  export FORGEJO_BASE_URL="https://forge.test"
  export FORGEJO_TOKEN="fjtok"
  git config --global "url.file://$REMOTE_ROOT/.insteadOf" "https://fjtok@forge.test/"
  unset BOX_WRITE_ENABLED
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-open-pr-create-forgejo-read-only"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'SPINDRIFT_PR_INTENT deadbeefcafe1234' "$DRIVER_PROMPT_FILE"
  # Scoped to the OPEN A PULL REQUEST section, same reasoning as the
  # read-write case above.
  local open_pr_section
  open_pr_section="$(awk '/^# OPEN A PULL REQUEST/,/^# OUTCOME/' "$DRIVER_PROMPT_FILE")"
  ! grep -qF 'fj pr create' <<<"$open_pr_section"
}

@test "OPEN A PULL REQUEST create step: read-only emits a nonce-guarded SPINDRIFT_PR_INTENT line, never gh pr create" {
  unset BOX_WRITE_ENABLED
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-open-pr-create-read-only"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'SPINDRIFT_PR_INTENT deadbeefcafe1234' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'SPINDRIFT_PR_INTENT_BEGIN' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'SPINDRIFT_PR_INTENT_END' "$DRIVER_PROMPT_FILE"

  # Not the bare substring: the read-only fragment itself explains "do NOT `gh
  # pr create`" -- pin the concrete invocation form instead, which only the
  # read-write fragment ever renders.
  ! grep -qF 'gh pr create --draft --base' "$DRIVER_PROMPT_FILE"
}

# issue #2462: the COMMIT section's push step has the same
# BOX_ACCESS_READ_WRITE/BOX_ACCESS_READ_ONLY gate need as the OPEN A PULL
# REQUEST push step above -- a read-only Box holds no push-capable token at
# commit time either. setup_entrypoint_env already exports BOX_WRITE_ENABLED=1,
# so the first case needs no override.
@test "COMMIT push step: read-write keeps git push and the retry loop unchanged" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-commit-push-read-write"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local commit_section
  commit_section="$(awk '/^# COMMIT/,/^# REVIEW/' "$DRIVER_PROMPT_FILE")"
  grep -qF 'git push --force-with-lease -u origin' <<<"$commit_section"
  grep -qF 'one retry only' <<<"$commit_section"
}

@test "COMMIT push step: read-only takes no push/retry action -- harness lands the committed branch" {
  unset BOX_WRITE_ENABLED
  export WORK_DIR="$BATS_TEST_TMPDIR/work-commit-push-read-only"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Scoped to the COMMIT section -- the later OPEN A PULL REQUEST and IF
  # BLOCKED sections share both the 'git push --force-with-lease -u origin'
  # string and the 'harness relays your committed branch out' phrase.
  local commit_section
  commit_section="$(awk '/^# COMMIT/,/^# REVIEW/' "$DRIVER_PROMPT_FILE")"
  grep -qF 'harness relays your committed branch out' <<<"$commit_section"
  ! grep -qF 'git push --force-with-lease -u origin' <<<"$commit_section"
  grep -qF 'git rebase origin/' <<<"$commit_section"
}

# issue #2462: the IF BLOCKED section's push-failure triage block has the same
# read-write/read-only gate need as the COMMIT push step above -- a read-only
# Box never attempts a push in the failure path either, so a denied push is
# expected rather than evidence of an under-scoped token, and the
# `.github/workflows/` diff triage must not render for it.
@test "IF BLOCKED triage step: read-write keeps the push-failure triage block unchanged" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-if-blocked-triage-read-write"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local if_blocked_section
  if_blocked_section="$(awk '/^# IF BLOCKED/,/^# OUTCOME/' "$DRIVER_PROMPT_FILE")"
  grep -qF 'Push failure — check the actual cause before reporting it' <<<"$if_blocked_section"
  grep -qF ".github/workflows/" <<<"$if_blocked_section"
}

@test "IF BLOCKED triage step: read-only treats a denied push as expected, never a token problem" {
  unset BOX_WRITE_ENABLED
  export WORK_DIR="$BATS_TEST_TMPDIR/work-if-blocked-triage-read-only"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local if_blocked_section
  if_blocked_section="$(awk '/^# IF BLOCKED/,/^# OUTCOME/' "$DRIVER_PROMPT_FILE")"
  grep -qF 'denied' <<<"$if_blocked_section"
  grep -qF '`git push` here is expected' <<<"$if_blocked_section"
  ! grep -qF 'Push failure — check the actual cause before reporting it' <<<"$if_blocked_section"
  # Not a bare no-`.github/workflows/`-anywhere check: the read-only fragment
  # itself names that path in passing, explaining that the diff triage is
  # skipped. Pin the two read-write-only artifacts instead: the actual diff
  # command and the "Genuine ... change" bullet.
  ! grep -qF "git diff origin/" <<<"$if_blocked_section"
  ! grep -qF '**Genuine `.github/workflows/` change:**' <<<"$if_blocked_section"
}

# issue #1933: the IF BLOCKED section's push step (step 1) has the same
# read-write/read-only gate need -- a read-only Box holds no push-capable token
# on either path, so "Push what you have" must not render unconditionally.
@test "IF BLOCKED push step: read-write keeps push-what-you-have unchanged" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-if-blocked-push-read-write"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Push what you have (or note if even that is impossible)' "$DRIVER_PROMPT_FILE"
  local if_blocked_section
  if_blocked_section="$(awk '/^# IF BLOCKED/,/^# OUTCOME/' "$DRIVER_PROMPT_FILE")"
  ! grep -qF 'seam.bundle' <<<"$if_blocked_section"
}

@test "IF BLOCKED push step: read-only takes no bundle/push action -- harness lands the committed branch" {
  unset BOX_WRITE_ENABLED
  export WORK_DIR="$BATS_TEST_TMPDIR/work-if-blocked-push-read-only"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local if_blocked_section
  if_blocked_section="$(awk '/^# IF BLOCKED/,/^# OUTCOME/' "$DRIVER_PROMPT_FILE")"
  ! grep -qF '/outbox/seam.bundle' <<<"$if_blocked_section"
  grep -qF 'harness relays your committed branch out' <<<"$if_blocked_section"
  ! grep -qF 'Push what you have (or note if even that is impossible)' <<<"$if_blocked_section"
}

# issue #1933: the IF BLOCKED section's PR check/create step (step 2) has the
# same gate need as the OPEN A PULL REQUEST create step above.
@test "IF BLOCKED PR step: read-write keeps gh pr view/create unchanged" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-if-blocked-pr-read-write"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local if_blocked_section
  if_blocked_section="$(awk '/^# IF BLOCKED/,/^# OUTCOME/' "$DRIVER_PROMPT_FILE")"
  grep -qF 'gh pr view --json url' <<<"$if_blocked_section"
  ! grep -qF 'SPINDRIFT_PR_INTENT' <<<"$if_blocked_section"
}

@test "IF BLOCKED PR step: read-only emits a nonce-guarded SPINDRIFT_PR_INTENT line, never gh pr view or gh pr create" {
  unset BOX_WRITE_ENABLED
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-if-blocked-pr-read-only"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local if_blocked_section
  if_blocked_section="$(awk '/^# IF BLOCKED/,/^# OUTCOME/' "$DRIVER_PROMPT_FILE")"
  grep -qF 'SPINDRIFT_PR_INTENT deadbeefcafe1234' <<<"$if_blocked_section"
  ! grep -qF 'gh pr view --json url' <<<"$if_blocked_section"

  # Not the bare substring: the read-only fragment itself explains "do NOT `gh
  # pr create`" -- pin the concrete invocation form instead.
  ! grep -qF 'gh pr create --draft' <<<"$if_blocked_section"
}

# issue #1933: the IF BLOCKED section's own final SPINDRIFT_OUTCOME line
# carries the same landing=<pr-url> placeholder the ready path did before #1919
# gated it -- a read-only Box never opens a PR in-box on the blocked path
# either, so it must print the branch name instead.
@test "IF BLOCKED outcome line: read-write keeps the pr-url placeholder unchanged" {
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-if-blocked-outcome-read-write"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'landing=<pr-url> status=blocked' "$DRIVER_PROMPT_FILE"
}

@test "IF BLOCKED outcome line: read-only reports the branch, never a pr-url placeholder" {
  unset BOX_WRITE_ENABLED
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-if-blocked-outcome-read-only"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'landing=agent/issue-7 status=blocked' "$DRIVER_PROMPT_FILE"

  local if_blocked_section
  if_blocked_section="$(awk '/^# IF BLOCKED/,0' "$DRIVER_PROMPT_FILE")"
  ! grep -qF 'landing=<pr-url>' <<<"$if_blocked_section"
}

# issue #1919: the OUTCOME section's landing= value under read-only carries the
# branch name, not a PR URL -- the Box never opens the PR itself, so it never
# learns a URL to report. ISSUE_NUMBER=7/BRANCH_PREFIX=agent/issue- together fix
# BRANCH at agent/issue-7.
#
# ADR 0039 retired the nonce gate for SPINDRIFT_OUTCOME specifically:
# structural scoping already gives every genuine outcome line the freshness
# guarantee the nonce provided. RUN_NONCE is still set here because the same
# rendered prompt also carries SPINDRIFT_PR_INTENT, which keeps its nonce gate.
@test "OUTCOME landing step: read-write keeps the pr-url placeholder unchanged" {
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-outcome-landing-read-write"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'landing=<pr-url> status=ready' "$DRIVER_PROMPT_FILE"
  # End-anchored: the OUTCOME line must terminate at note=, with no trailing
  # nonce= field (ADR 0039). A bare -F substring match would still pass if the
  # retired nonce crept back onto the line.
  grep -qE 'status=ready note=<short reason>$' "$DRIVER_PROMPT_FILE"
}

@test "OUTCOME landing step: read-only reports the branch, never a pr-url placeholder" {
  unset BOX_WRITE_ENABLED
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-outcome-landing-read-only"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'landing=agent/issue-7 status=ready' "$DRIVER_PROMPT_FILE"
  # End-anchored for the same reason as the read-write case above.
  grep -qE 'status=ready note=<short reason>$' "$DRIVER_PROMPT_FILE"

  # Scoped to the OUTCOME section -- OPEN A PULL REQUEST's own PR-intent
  # fragment legitimately mentions "the launcher opens the draft PR" in prose.
  local outcome_section
  outcome_section="$(awk '/^# OUTCOME/,/^# IF BLOCKED/' "$DRIVER_PROMPT_FILE")"
  ! grep -qF 'landing=<pr-url>' <<<"$outcome_section"
}

# A scout/reviewer-only template (no "filer" key) must not require
# filer-prompt.md to exist -- the file read has to be gated on the template
# actually carrying a filer entry.
@test "entrypoint does not require filer-prompt.md when the template omits filer" {
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  printf 'issue stub\n' >"$prompt_dir/issue-prompt.md"
  printf 'scout stub\n' >"$prompt_dir/scout-prompt.md"
  printf 'reviewer stub\n' >"$prompt_dir/review-prompt.md"
  export PROMPTS_DIR="$prompt_dir"
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"r","model":"opus","prompt":"","tools":["Read"]},"scout":{"description":"s","model":"haiku","prompt":"","tools":["Read"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e 'has("filer") | not' "$DRIVER_AGENTS_FILE" >/dev/null
}

# issue #452: `nix fmt` can never succeed in-box (uid 1000 has no /nix/store
# write access, so evaluating the flake dies with a store-lock permission
# error) — the step must not list it as a usable preference, and must say why
# it's unavailable if it names it at all.
@test "AUTO-FORMAT step never instructs nix fmt as a usable preference" {
  export AUTO_FORMAT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q '`nix fmt` when the target flake defines a formatter' "$DRIVER_PROMPT_FILE"
}

# issue #2489: the nix-fmt rationale lives in the harness-owned /auto-format
# skill's SKILL.md; the rendered prompt just points at the skill by name.
@test "AUTO-FORMAT step points to the skill instead of explaining nix fmt inline" {
  export AUTO_FORMAT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF '`/auto-format`' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'store-lock permission error' "$DRIVER_PROMPT_FILE"
}

# issue #2490: the inline linting procedure lives in the harness-owned
# /auto-lint skill's SKILL.md; the rendered prompt just points at it by name.
@test "AUTO-LINT step points to the skill instead of explaining the linter procedure inline" {
  export AUTO_LINT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF '`/auto-lint`' "$DRIVER_PROMPT_FILE"
  ! grep -qF "Apply the linter's safe auto-fix mode" "$DRIVER_PROMPT_FILE"
}

# issue #463: the conditional prompt steps above must be read from fragment
# files under PROMPTS_DIR, not authored as heredocs in the script -- a markdown
# heading string-literal in entrypoint.sh means prose leaked back into bash.
@test "entrypoint source contains no prompt-prose markdown headings" {
  run grep -E '# (FILE ISSUES|AUTO-FORMAT|AUTO-LINT|CI FAILURE)' "$ENTRYPOINT"
  [ "$status" -ne 0 ]
}

@test "every registry row ships as a fragment file under prompts/fragments" {
  source "$FRAGMENT_REGISTRY_FILE"
  local row fragment
  for row in "${_FRAGMENT_ROWS[@]}"; do
    # Row shape is "gate|fragment.md|var" -- middle field, already carries
    # the .md suffix.
    fragment="${row#*|}"
    fragment="${fragment%%|*}"
    [ -f "$PROMPTS_DIR/fragments/$fragment" ]
  done
}

# issue #463: `$(_subst ...)` strips ALL trailing newlines, so a fragment's
# blank-line separator must be reconstructed after substitution -- otherwise
# the step glues onto the next heading with not even a newline between them.
@test "AUTO-FORMAT and AUTO-LINT steps stay separated from each other and from COMMIT" {
  export AUTO_FORMAT=1
  export AUTO_LINT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'changed\.# AUTO-LINT' "$DRIVER_PROMPT_FILE"
  ! grep -q 'changed\.# COMMIT' "$DRIVER_PROMPT_FILE"
}

@test "FILE ISSUES step stays separated from LAND THE CHANGE" {
  export AGENTS_JSON_TEMPLATE='{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'
  export BOX_FILER_ENABLED=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'configured\.# LAND THE CHANGE' "$DRIVER_PROMPT_FILE"
}

@test "CI FAILURE step stays separated from CONTEXT on a fix pass" {
  export FIX_PASS=1
  export CI_FAILURE_SUMMARY="build failed"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'scratch:build failed' "$DRIVER_PROMPT_FILE"
  ! grep -q 'failed# CONTEXT' "$DRIVER_PROMPT_FILE"
}

@test "CAVEMAN_STEP stays separated from the COMMS body text" {
  mkdir -p "$HOME/.claude/skills/caveman"
  cat >"$HOME/.claude/skills/caveman/SKILL.md" <<'SKILL'
---
name: caveman
description: Ultra-compressed communication mode.
---
Respond terse like smart caveman.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'message\.Your text output' "$DRIVER_PROMPT_FILE"
}

# TDD_BAKED's gate mechanism -- mirrors the CAVEMAN_STEP case above.
@test "TDD_STEP renders when the tdd skill is baked" {
  mkdir -p "$HOME/.claude/skills/tdd"
  cat >"$HOME/.claude/skills/tdd/SKILL.md" <<'SKILL'
---
name: tdd
description: Test-driven development.
---
Red, green, refactor.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Use the `/tdd` skill to run the test-first loop below' "$DRIVER_PROMPT_FILE"
}

# COMMIT_BAKED's gate mechanism -- mirrors the CAVEMAN_STEP case above.
@test "COMMIT_STEP renders when the commit skill is baked" {
  mkdir -p "$HOME/.claude/skills/commit"
  cat >"$HOME/.claude/skills/commit/SKILL.md" <<'SKILL'
---
name: commit
description: Write git commit messages in Conventional Commits style.
---
Hard-wrapped Conventional Commits.
SKILL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Use the `/commit` skill to write every commit message' "$DRIVER_PROMPT_FILE"
}

# issue #788: the reviewer subagent favors the /code-review skill when it is
# baked at DRIVER_SKILLS_DIR/code-review/SKILL.md. CODE_REVIEW_STEP renders
# into review-prompt.md, which flows into the reviewer's prompt in the --agents
# JSON -- so this reads $DRIVER_AGENTS_FILE's .reviewer.prompt, not
# $DRIVER_PROMPT_FILE.
@test "CODE_REVIEW_STEP renders when the code-review skill is baked" {
  mkdir -p "$HOME/.claude/skills/code-review"
  cat >"$HOME/.claude/skills/code-review/SKILL.md" <<'SKILL'
---
name: code-review
description: Review code changes for standards and spec compliance.
---
Two-axis review: Standards + Spec.
SKILL
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"reviewer","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","Agent"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e '.reviewer.prompt' "$DRIVER_AGENTS_FILE" | grep -qF 'Run the `/code-review` skill FIRST'
}

# The fallback -- no code-review skill baked -- must still end in the VERDICT
# contract, with zero trace of the deferral.
@test "reviewer prompt has no code-review deferral when the skill is absent" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"reviewer","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","Agent"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  local rendered
  rendered="$(jq -r '.reviewer.prompt' "$DRIVER_AGENTS_FILE")"
  ! grep -qF 'Run the `/code-review` skill FIRST' <<<"$rendered"
  grep -qF 'VERDICT: APPROVE | BLOCK' <<<"$rendered"
}

# issue #993: CODE_REVIEW_STEP's deferral claims to "supersede" the inline
# rubric, but the inline four dimensions always render below it -- so
# review-prompt.md must say the overlap is intentional (skill findings
# reconcile into the same contract), not that the dimensions get removed.
@test "reviewer prompt explains the code-review rubric overlap is intentional" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"reviewer","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","Agent"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e '.reviewer.prompt' "$DRIVER_AGENTS_FILE" | grep -qF 'rather than replacing these dimensions'
}

# entrypoint.sh invokes exactly one binary exactly once: direct and devShell
# invocation are the same call path (driver-exec's own --devshell switch picks
# which), and since issue #1996 the call site names no binary literally --
# $_driver_invoker picks driver-exec or orchestrator at runtime.
@test "the driver invocation is called exactly once in entrypoint.sh source" {
  count=$(grep -c '^  "\$_driver_invoker" \\$' "$ENTRYPOINT")
  [ "$count" -eq 1 ]
}

# issue #463: a SPINDRIFT_PROMPT_DIR-style override supplies its own fragment
# for a knob it enables, exactly as it already must supply filer-prompt.md when
# AGENTS_JSON_TEMPLATE carries a filer entry. Documented in docs/reference.md.
@test "runtime prompt-dir override supplies its own auto-format fragment" {
  local prompt_dir="$BATS_TEST_TMPDIR/custom-prompts"
  cp -r "$PROMPTS_DIR" "$prompt_dir"
  chmod -R u+w "$prompt_dir"
  printf '# AUTO-FORMAT\n\nCUSTOM-FRAGMENT-MARKER\n\n' >"$prompt_dir/fragments/auto-format.md"
  export PROMPTS_DIR="$prompt_dir"
  export AUTO_FORMAT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'CUSTOM-FRAGMENT-MARKER' "$DRIVER_PROMPT_FILE"
}

# Parallels the auto-format override test above, now that AUTO-LINT is a shrunk
# skill-invocation fragment too.
@test "runtime prompt-dir override supplies its own auto-lint fragment" {
  local prompt_dir="$BATS_TEST_TMPDIR/custom-prompts"
  cp -r "$PROMPTS_DIR" "$prompt_dir"
  chmod -R u+w "$prompt_dir"
  printf '# AUTO-LINT\n\nCUSTOM-FRAGMENT-MARKER\n\n' >"$prompt_dir/fragments/auto-lint.md"
  export PROMPTS_DIR="$prompt_dir"
  export AUTO_LINT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'CUSTOM-FRAGMENT-MARKER' "$DRIVER_PROMPT_FILE"
}

@test "entrypoint includes a read-only tools whitelist in agents JSON" {
  export AGENTS_JSON_TEMPLATE='{"reviewer":{"description":"Review the branch diff for spec compliance and coding standards","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]},"scout":{"description":"Map relevant files, seams, and tests; return a structured brief","model":"opus","prompt":"","tools":["Read","Bash","WebFetch","WebSearch","Glob","Grep"]}}'
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  jq -e '.scout.tools | length > 0' "$DRIVER_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.tools | length > 0' "$DRIVER_AGENTS_FILE" >/dev/null
  jq -e '.scout.tools | contains(["Edit"]) | not' "$DRIVER_AGENTS_FILE" >/dev/null
  jq -e '.scout.tools | contains(["Write"]) | not' "$DRIVER_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.tools | contains(["Edit"]) | not' "$DRIVER_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.tools | contains(["Write"]) | not' "$DRIVER_AGENTS_FILE" >/dev/null
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
  grep -q 'label: wip' "$DRIVER_PROMPT_FILE"
  grep -q 'complete: done' "$DRIVER_PROMPT_FILE"
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
  jq -e '.scout.prompt | contains("scout for issue 7")' "$DRIVER_AGENTS_FILE" >/dev/null
  jq -e '.reviewer.prompt | contains("review base main")' "$DRIVER_AGENTS_FILE" >/dev/null
}

@test "default prompt delegates exploration to the scout subagent" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'scout' "$DRIVER_PROMPT_FILE"
}

@test "default prompt spawns a reviewer subagent" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'reviewer' "$DRIVER_PROMPT_FILE"
}

@test "default prompt specifies a review loop keyed on VERDICT: BLOCK" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'VERDICT.*BLOCK\|BLOCK.*VERDICT' "$DRIVER_PROMPT_FILE"
}

@test "default prompt emits exactly one SPINDRIFT_OUTCOME line" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -c 'SPINDRIFT_OUTCOME' "$DRIVER_PROMPT_FILE" | grep -q '^[1-9]'
}

@test "default prompt emits SPINDRIFT_OUTCOME with status=blocked in the blocked path" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'status=blocked' "$DRIVER_PROMPT_FILE"
}

@test "default prompt emits status=ready as the success outcome" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'status=ready' "$DRIVER_PROMPT_FILE"
  ! grep -q 'status=merged' "$DRIVER_PROMPT_FILE"
}

# Fragment-loop genericity over the registry is no longer proven by a test
# here: rendering lives entirely in the Go assemble-prompt verb, which loops
# over `reg.Rows` from a nix-baked JSON registry, and the
# promptassembly-registry-ownership/-drift nix checks guard it.

# issue #2019: the filer's write-mechanism gates (FILER_FILE_DIRECT/
# FILER_FILE_RELAY) pick the host-mediated SPINDRIFT_ISSUE_INTENT relay only on
# read-only (BOX_WRITE_ENABLED absent) + ORCHESTRATOR_ENABLED; every other
# combination keeps the direct `gh issue create`/`gh label create` path. Tests
# below unset BOX_WRITE_ENABLED for the read-only cases. The filer's prompt
# text lands in DRIVER_AGENTS_FILE, not DRIVER_PROMPT_FILE.
FILER_AGENTS_JSON_TEMPLATE='{"filer":{"description":"filer","model":"haiku","prompt":"","tools":["Read","Bash","WebFetch"]}}'

@test "filer write step: read-write keeps gh issue create unchanged regardless of ORCHESTRATOR_ENABLED" {
  export AGENTS_JSON_TEMPLATE="$FILER_AGENTS_JSON_TEMPLATE"
  export BOX_FILER_ENABLED=1
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-filer-readwrite-orch-on"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue create' "$DRIVER_AGENTS_FILE"
  grep -qF 'gh label create' "$DRIVER_AGENTS_FILE"
  ! grep -qF 'SPINDRIFT_ISSUE_INTENT' "$DRIVER_AGENTS_FILE"
  grep -qF "the filer's returned issue URLs" "$DRIVER_PROMPT_FILE"
}

# AC2 (issue #2019): read-write with the orchestrator OFF must emit no
# SPINDRIFT_ISSUE_INTENT line either -- distinct from the orchestrator-on case
# above, which proves ORCHESTRATOR_ENABLED alone can't flip the gate.
@test "filer write step: read-write with orchestrator off emits no SPINDRIFT_ISSUE_INTENT" {
  export AGENTS_JSON_TEMPLATE="$FILER_AGENTS_JSON_TEMPLATE"
  export BOX_FILER_ENABLED=1
  export WORK_DIR="$BATS_TEST_TMPDIR/work-filer-readwrite-orch-off"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue create' "$DRIVER_AGENTS_FILE"
  ! grep -qF 'SPINDRIFT_ISSUE_INTENT' "$DRIVER_AGENTS_FILE"
  grep -qF "the filer's returned issue URLs" "$DRIVER_PROMPT_FILE"
}

@test "filer write step: read-only with orchestrator off keeps today's degraded direct-file path unchanged" {
  export AGENTS_JSON_TEMPLATE="$FILER_AGENTS_JSON_TEMPLATE"
  export BOX_FILER_ENABLED=1
  unset BOX_WRITE_ENABLED
  export WORK_DIR="$BATS_TEST_TMPDIR/work-filer-readonly-orch-off"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue create' "$DRIVER_AGENTS_FILE"
  ! grep -qF 'SPINDRIFT_ISSUE_INTENT' "$DRIVER_AGENTS_FILE"
  grep -qF "the filer's returned issue URLs" "$DRIVER_PROMPT_FILE"
}

@test "filer write step: read-only with orchestrator on emits SPINDRIFT_ISSUE_INTENT, never gh issue create" {
  export AGENTS_JSON_TEMPLATE="$FILER_AGENTS_JSON_TEMPLATE"
  export BOX_FILER_ENABLED=1
  unset BOX_WRITE_ENABLED
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export RUN_NONCE="deadbeefcafe1234"
  export WORK_DIR="$BATS_TEST_TMPDIR/work-filer-readonly-orch-on"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'SPINDRIFT_ISSUE_INTENT deadbeefcafe1234' "$DRIVER_AGENTS_FILE"
  ! grep -qF 'gh issue create' "$DRIVER_AGENTS_FILE"
  ! grep -qF 'gh label create' "$DRIVER_AGENTS_FILE"
  grep -qF 'queued for filing' "$DRIVER_PROMPT_FILE"
  ! grep -qF "the filer's returned issue URLs" "$DRIVER_PROMPT_FILE"
}

# The direct case forks further on ISSUE_TRACKER (issue #1963): fj has no label
# verb and `fj issue create` has no --label flag, so a forgejo tracker's direct
# filer writes go through the *-forgejo fragments instead.
@test "filer write step: forgejo direct filer speaks fj issue create, never gh issue create" {
  export AGENTS_JSON_TEMPLATE="$FILER_AGENTS_JSON_TEMPLATE"
  export BOX_FILER_ENABLED=1
  export ISSUE_TRACKER=forgejo
  export BOX_TRACKER_AXIS_READ=FORGEJO
  export BOX_TRACKER_AXIS_WRITE=FORGEJO
  export BOX_TRACKER_AXIS_FILER=FORGEJO
  export WORK_DIR="$BATS_TEST_TMPDIR/work-filer-forgejo-direct"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'fj issue create' "$DRIVER_AGENTS_FILE"
  ! grep -qF 'gh issue create' "$DRIVER_AGENTS_FILE"
  ! grep -qF 'gh label create' "$DRIVER_AGENTS_FILE"
}

@test "filer write step: github direct filer still speaks gh issue create, never fj issue create" {
  export AGENTS_JSON_TEMPLATE="$FILER_AGENTS_JSON_TEMPLATE"
  export BOX_FILER_ENABLED=1
  export WORK_DIR="$BATS_TEST_TMPDIR/work-filer-github-direct"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'gh issue create' "$DRIVER_AGENTS_FILE"
  ! grep -qF 'fj issue create' "$DRIVER_AGENTS_FILE"
}

# The REVIEW section fork (issue #2037, ADR 0035): orchestrator off keeps the
# implementor's inline "spawn a reviewer subagent, loop until no blocking
# findings" prose; orchestrator on replaces it with a deferral to the
# orchestrator's code-owned review pass -- never both, never neither.
# The coordinator fork (issue #2056): a `worker` subagent in
# AGENTS_JSON_TEMPLATE turns the main session's IMPLEMENT section into a
# coordinator that delegates each slice; with no worker the section is
# byte-identical to the single-implementor prompt. Gated on worker presence
# alone, orthogonal to ORCHESTRATOR.
WORKER_AGENTS_JSON_TEMPLATE='{"worker":{"description":"Implement a scoped slice of work delegated to it","model":"sonnet","prompt":"","tools":["Read","Bash","Edit","Write","Glob","Grep","WebFetch"]}}'

@test "IMPLEMENT section: a provisioned worker turns the section into a coordinator that delegates slices" {
  export AGENTS_JSON_TEMPLATE="$WORKER_AGENTS_JSON_TEMPLATE"
  export BOX_WORKER_PROVISIONED=1
  export WORK_DIR="$BATS_TEST_TMPDIR/work-coordinator-on"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'coordinator' "$DRIVER_PROMPT_FILE"
  grep -qF 'delegate each slice' "$DRIVER_PROMPT_FILE"
}

@test "IMPLEMENT section: coordinator does not touch the consumer repo's .gitignore" {
  export AGENTS_JSON_TEMPLATE="$WORKER_AGENTS_JSON_TEMPLATE"
  export BOX_WORKER_PROVISIONED=1
  export WORK_DIR="$BATS_TEST_TMPDIR/work-coordinator-no-gitignore"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # The .gitignore/$WORK_DIR paragraph was removed (issue #2058 review):
  # $WORK_DIR is never exported by entrypoint.sh nor in the envsubst allowlist,
  # so it rendered as a literal, broken reference. Scoped to the IMPLEMENT
  # section, so an unrelated future fragment elsewhere is free to mention
  # either word without tripping this test.
  implement_section="$(awk '/^# IMPLEMENT/,/^# CHECK/' "$DRIVER_PROMPT_FILE")"
  [[ "$implement_section" != *'gitignore'* ]]
  [[ "$implement_section" != *'WORK_DIR'* ]]
}

@test "IMPLEMENT section: no worker leaves the single-implementor prompt unchanged" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-coordinator-off"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -qF 'delegate each slice' "$DRIVER_PROMPT_FILE"
  # The single-implementor test-first rule still leads the section verbatim.
  grep -qF 'Work test-first, one slice at a time. Hard rule:' "$DRIVER_PROMPT_FILE"
}

@test "coordinator step stays separated from the test-first rule below it" {
  export AGENTS_JSON_TEMPLATE="$WORKER_AGENTS_JSON_TEMPLATE"
  export BOX_WORKER_PROVISIONED=1
  export WORK_DIR="$BATS_TEST_TMPDIR/work-coordinator-sep"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q 'commits\.Work test-first' "$DRIVER_PROMPT_FILE"
  # The coordinator step still renders ahead of the retained Hard rule prose.
  coord_line=$(grep -nF 'coordinator' "$DRIVER_PROMPT_FILE" | head -1 | cut -d: -f1)
  rule_line=$(grep -nF 'Work test-first, one slice' "$DRIVER_PROMPT_FILE" | head -1 | cut -d: -f1)
  [ "$coord_line" -lt "$rule_line" ]
}

@test "REVIEW section: orchestrator off keeps the inline reviewer-subagent loop" {
  export WORK_DIR="$BATS_TEST_TMPDIR/work-review-loop-off"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'spawn a fresh `reviewer` subagent' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'Review is handled by the orchestrator as a separate' "$DRIVER_PROMPT_FILE"
}

@test "REVIEW section: orchestrator on defers to the code-owned review pass" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  export WORK_DIR="$BATS_TEST_TMPDIR/work-review-loop-on"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qF 'Review is handled by the orchestrator as a separate' "$DRIVER_PROMPT_FILE"
  ! grep -qF 'spawn a fresh `reviewer` subagent' "$DRIVER_PROMPT_FILE"
}
