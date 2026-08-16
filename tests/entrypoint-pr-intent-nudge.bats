#!/usr/bin/env bats
# The PR-intent required-marker gate row (issue #2045, the #2036 fix): a
# read-only github Box that reaches status=ready but never printed a
# SPINDRIFT_PR_INTENT line leaves the launcher's hostMediateDraftPR with
# nothing to relay -- it posts "merge blocked" and strands the finished
# branch. A second required-marker gate row (issue #2044, verb-owned
# decision issue #2511) resumes the same pinned session once with a
# corrective nudge before that happens.

load helper

setup() {
  setup_entrypoint_env
}

# --- _scan_pr_intent_in_log -------------------------------------------------
# Matching is anchored on this run's own $RUN_NONCE, not just the bare token
# (issue #1937's reasoning, applied in-box): a bash regex has no reliable way
# to tell a genuine base64 payload from ordinary prose by character class
# alone -- both are letters -- but an untrusted mid-conversation mention of
# the token essentially never also carries this run's own nonce verbatim.

@test "_scan_pr_intent_in_log: finds a well-formed marker line carrying this run's nonce" {
  export RUN_NONCE="deadbeefcafe1234"
  local harness="$BATS_TEST_TMPDIR/scan_harness.sh"
  sed '$d' "$ENTRYPOINT" >"$harness"
  local log="$BATS_TEST_TMPDIR/stream.log"
  printf '{"type":"assistant","message":{"content":[{"type":"text","text":"working..."}]}}\n' >"$log"
  printf '{"type":"result","subtype":"success","is_error":false,"result":"SPINDRIFT_PR_INTENT deadbeefcafe1234 Zm9vCgpiYXI="}\n' >>"$log"
  cat >>"$harness" <<EOF
_scan_pr_intent_in_log "$log"
EOF
  run bash "$harness"
  [ "$status" -eq 0 ]
  [ "$output" = "SPINDRIFT_PR_INTENT deadbeefcafe1234 Zm9vCgpiYXI=" ]
}

@test "_scan_pr_intent_in_log: empty when no marker line is present" {
  export RUN_NONCE="deadbeefcafe1234"
  local harness="$BATS_TEST_TMPDIR/scan_harness.sh"
  sed '$d' "$ENTRYPOINT" >"$harness"
  local log="$BATS_TEST_TMPDIR/stream.log"
  printf '{"type":"result","subtype":"success","is_error":false,"result":"SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=fake"}\n' >"$log"
  cat >>"$harness" <<EOF
printf '[%s]' "\$(_scan_pr_intent_in_log "$log")"
EOF
  run bash "$harness"
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
}

@test "_scan_pr_intent_in_log: a mid-sentence mention of the token with no matching nonce is not a match" {
  export RUN_NONCE="deadbeefcafe1234"
  local harness="$BATS_TEST_TMPDIR/scan_harness.sh"
  sed '$d' "$ENTRYPOINT" >"$harness"
  local log="$BATS_TEST_TMPDIR/stream.log"
  printf '{"type":"assistant","message":{"content":[{"type":"text","text":"I still need to print a SPINDRIFT_PR_INTENT line later."}]}}\n' >"$log"
  cat >>"$harness" <<EOF
printf '[%s]' "\$(_scan_pr_intent_in_log "$log")"
EOF
  run bash "$harness"
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
}

@test "_scan_pr_intent_in_log: a line carrying a stale/foreign nonce is not a match" {
  export RUN_NONCE="deadbeefcafe1234"
  local harness="$BATS_TEST_TMPDIR/scan_harness.sh"
  sed '$d' "$ENTRYPOINT" >"$harness"
  local log="$BATS_TEST_TMPDIR/stream.log"
  printf '{"type":"result","subtype":"success","is_error":false,"result":"SPINDRIFT_PR_INTENT some-other-nonce Zm9vCgpiYXI="}\n' >"$log"
  cat >>"$harness" <<EOF
printf '[%s]' "\$(_scan_pr_intent_in_log "$log")"
EOF
  run bash "$harness"
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
}

@test "_scan_pr_intent_in_log: empty RUN_NONCE never matches" {
  unset RUN_NONCE
  local harness="$BATS_TEST_TMPDIR/scan_harness.sh"
  sed '$d' "$ENTRYPOINT" >"$harness"
  local log="$BATS_TEST_TMPDIR/stream.log"
  printf '{"type":"result","subtype":"success","is_error":false,"result":"SPINDRIFT_PR_INTENT deadbeefcafe1234 Zm9vCgpiYXI="}\n' >"$log"
  cat >>"$harness" <<EOF
printf '[%s]' "\$(_scan_pr_intent_in_log "$log")"
EOF
  run bash "$harness"
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
}

# --- end-to-end: the required-marker gate's PR-intent row ------------------
# The #2036 dogfood failure reproduced: a read-only github run reaches
# status=ready but the Driver never printed SPINDRIFT_PR_INTENT, so the
# launcher's hostMediateDraftPR has nothing to relay.

@test "PR-intent gate: missing PR-intent on a status=ready read-only run resumes once and the resumed pass supplies it" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_NO_PR_INTENT_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Exactly two Driver invocations: the initial pass and the one resume pass
  # the PR-intent required-marker gate fires.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # The resumed pass is the one whose prompt was actually rendered last
  # (fakes/claude overwrites $DRIVER_PROMPT_FILE per call) -- it must carry
  # the canonical grammar and this run's own nonce, not a fresh one.
  grep -qF 'SPINDRIFT_PR_INTENT deadbeefcafe1234 <base64-encoded title' "$DRIVER_PROMPT_FILE"
  grep -qF 'status=ready' "$DRIVER_PROMPT_FILE"

  # The resumed pass's own canned output actually supplies the marker this
  # time -- visible in the raw teed Driver stream, not just believed.
  [[ "$output" == *"SPINDRIFT_PR_INTENT deadbeefcafe1234"* ]]

  # The original status=ready outcome survives untouched -- no synthetic
  # blocked backstop fired despite the extra resume pass.
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
  ! grep -q 'driver produced no SPINDRIFT_OUTCOME line' <<<"$output"

  # The nudge succeeded, so no exhausted-nudge give-up op is emitted (issue
  # #2046) -- that op fires only on the genuinely-blocked, nudge-exhausted
  # path, never when the resumed pass supplies the marker.
  ! grep -q '"op":"decision"' <<<"$output"
}

@test "PR-intent gate: a second consecutive miss falls through unchanged, no loop" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # One resume attempt only -- never a second.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # The PR-intent gate never rewrites the outcome itself on a miss -- the
  # status=ready line the Driver already committed to survives, for the
  # launcher's own hostMediateDraftPR/blockHandoff to classify.
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
  ! grep -q 'SPINDRIFT_PR_INTENT' <<<"$output"
}

# When the nudge is exhausted (issue #2046), the gate emits a heartbeat op
# (issue #2027's spindrift_op stream) recording the attempt count and the
# give-up decision, so an operator can see *why* the run ended blocked rather
# than an unexplained state. The op is a plain stream-json line on the box's
# own stdout, parsed by the host heartbeat Writer exactly like the
# orchestrator's own ops.
@test "PR-intent gate: an exhausted nudge emits a heartbeat give-up op" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # A well-formed spindrift_op decision line (issue #2027's Event shape),
  # recording the give-up and the single nudge attempt behind it.
  grep -q '"type":"spindrift_op"' <<<"$output"
  grep -q '"op":"decision"' <<<"$output"
  grep -q '"decision":"stop"' <<<"$output"
  grep -q 'nudge exhausted after 1 attempt' <<<"$output"

  # The op line never carries the literal marker token in the full-run
  # context either -- a real downstream scan (_scan_pr_intent_in_log, the
  # launcher's outcome.LastPRIntentInLog) runs over this whole stream, so
  # the give-up reason must not be mistaken for a genuine PR-intent attempt.
  ! grep -q 'SPINDRIFT_PR_INTENT' <<<"$output"
}

# Issue #2448: the synthetic outcome backstop (entrypoint-outcome-backstop.bats'
# "read-only github + no outcome line -> branch relayed via outbox bundle, no
# force-push" test's read-only + no-outcome-line setup) only ever printed its
# SPINDRIFT_OUTCOME line -- it never assigned $_last_outcome_line -- so a
# status=ready reached only via that backstop left this gate's own read below
# looking at an empty variable and the nudge silently never fired.
@test "PR-intent gate: fires on a status=ready reached only via the synthetic backstop" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1   # every call (initial + the outcome gate's own resume) produces no outcome line, forcing the synthetic backstop to fire
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Exactly three Driver invocations: call 1 (initial) and call 2 (the
  # SPINDRIFT_OUTCOME required-marker gate's own one resume) are both still
  # no-outcome under FAKE_DRIVER_NO_OUTCOME, so the backstop fires a
  # synthetic status=ready line; call 3 is the PR-intent nudge gate's own
  # resume attempt, now reachable thanks to the #2448 fix that carries the
  # backstop's synthetic line into $_last_outcome_line.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 3 ]

  # FAKE_DRIVER_NO_OUTCOME is unconditional (not _FIRST_CALL_ONLY), so the
  # third (nudge) call also produces no PR-intent marker -- the nudge is
  # exhausted. But that resumed pass's result text carries no
  # SPINDRIFT_OUTCOME token at all (not even a garbled one), so nothing
  # shadowed the original line in the container log -- the gate's own
  # fallback must not reprint it, and the line still appears exactly once
  # (same shape as entrypoint-outcome-backstop.bats' line-332 fixture, fixed
  # alongside this test for the same reason).
  [ "$(grep -c '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*relayed via outbox bundle' <<<"$output")" -eq 1 ]

  # The give-up heartbeat op fires (same assertions as "an exhausted nudge
  # emits a heartbeat give-up op" above).
  grep -q '"type":"spindrift_op"' <<<"$output"
  grep -q '"op":"decision"' <<<"$output"
  grep -q '"decision":"stop"' <<<"$output"
  grep -q 'nudge exhausted after 1 attempt' <<<"$output"

  # The bundle relay still happened -- unaffected by the fix.
  [ -f "$OUTBOX_DIR/seam.bundle" ]
}

# Issue #2448 AC2: "When the nudged pass supplies a usable PR-intent line,
# that run hands off a draft PR exactly as a genuinely-ready run does." The
# test above only exercises the *exhausted* nudge (FAKE_DRIVER_NO_OUTCOME is
# unconditional there, so the nudge's own resume also produces nothing) --
# this test instead lets the nudge's own resume succeed.
@test "PR-intent gate: a backstop-derived status=ready run's nudge succeeds when the resumed pass supplies PR-intent" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  # call_index 0 (initial) and call_index 1 (the SPINDRIFT_OUTCOME
  # required-marker gate's own one resume) are both no-outcome, forcing the
  # synthetic backstop to fire after those two calls -- same setup as "fires
  # on a status=ready reached only via the synthetic backstop" above. Unlike
  # that test, call_index 2 (the PR-intent nudge gate's own resume) falls
  # through to this fake's normal scripted output, which supplies a genuine
  # SPINDRIFT_PR_INTENT line plus a fresh status=ready outcome line.
  export FAKE_DRIVER_NO_OUTCOME_BEFORE_CALL=2
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Three Driver invocations: initial, the SPINDRIFT_OUTCOME gate's resume,
  # and the PR-intent nudge's own (this time successful) resume.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 3 ]

  # The resumed pass's own canned output actually supplies the marker --
  # visible in the raw teed Driver stream, not just believed.
  grep -q 'SPINDRIFT_PR_INTENT deadbeefcafe1234' <<<"$output"

  # The nudge succeeded, so no exhausted-nudge give-up op is emitted (mirrors
  # the non-backstop success test's own assertion above) -- that op fires
  # only on the genuinely-exhausted path.
  ! grep -q '"op":"decision"' <<<"$output"
}

# Issue #2448 finding 3: before the #2448 fix above, a backstop-derived
# status=ready run's $_last_outcome_line was left empty, so the
# _is_readonly_outbox_relay + status=ready guard around the PR-intent nudge was
# always false on that path -- the nudge never got a chance to run at all.
# Now that the fix makes the nudge reach a backstop-derived run (the whole
# point of #2448), a new hazard opens up: if the nudge's own corrective
# resume (the PR-intent gate's one resume attempt) itself crashes or
# otherwise exits non-zero, that failure lands in main()'s own $claude_rc,
# and the unconditional `exit "$claude_rc"` at the bottom of main() would
# send this already-backstopped, already-relayed run to the launcher's
# ClassifyTransient/retry path -- exactly the outcome the backstop's own
# comment (issue #593) says forcing a terminal exit 0 must prevent, except
# here the failure is in a later, best-effort nudge, not the driver's actual
# work. The backstop already committed to a terminal status=ready verdict
# earlier in this same run; a transient failure in the nudge that follows it
# must not retroactively undo that.
@test "PR-intent gate: a crashed resume after a backstop-declared ready outcome stays terminal (exit 0)" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  # call_index 0 (initial) and call_index 1 (the SPINDRIFT_OUTCOME
  # required-marker gate's own one resume) both produce no outcome line,
  # forcing the synthetic backstop to fire after those two calls -- same
  # setup as "fires on a status=ready reached only via the synthetic
  # backstop" above. call_index 2 (the PR-intent nudge gate's own resume)
  # instead crashes with a transient-looking non-zero exit, simulating an
  # infrastructure failure mid-nudge.
  export FAKE_DRIVER_NO_OUTCOME=1
  export FAKE_DRIVER_CRASH_EXIT=17
  export FAKE_DRIVER_CRASH_EXIT_FROM_CALL=2
  run bash "$ENTRYPOINT"

  # The crash in the PR-intent nudge's own resume must not flip the
  # entrypoint's own exit status away from the terminal 0 the backstop
  # already committed to -- not the crash's exit code (17), and not any
  # other non-zero value.
  [ "$status" -eq 0 ]

  # All three calls happened: initial, the SPINDRIFT_OUTCOME gate's resume,
  # and the PR-intent nudge's own (crashing) resume.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 3 ]

  # The synthetic backstop's status=ready line was still emitted exactly
  # once -- the crash happened after it, and must not have erased or
  # duplicated it.
  [ "$(grep -c '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*relayed via outbox bundle' <<<"$output")" -eq 1 ]

  # The bundle relay still happened -- unaffected by the crash.
  [ -f "$OUTBOX_DIR/seam.bundle" ]
}

# Issue #2448 finding 2: the gate's "resumed pass did not repeat the original
# SPINDRIFT_OUTCOME line — restoring it" fallback used to restore
# unconditionally whenever the resumed line differed from the original ready
# line -- including when the resumed pass supplied its own genuine, valid,
# differently-shaped verdict (e.g. deciding mid-nudge that the run is
# actually status=blocked). Restoring in that case clobbered the driver's
# own honest final word with the earlier synthetic/original ready line, both
# in $_last_outcome_line bookkeeping and reprinted into the container log --
# exactly the line the launcher's own last-line-wins outcome.LastInLog scan
# would then see, hiding a genuinely blocked run behind a manufactured
# status=ready. A non-empty, differently-shaped resumed outcome line must be
# left completely alone.
@test "PR-intent gate: a genuine status=blocked verdict from the resumed pass is never clobbered back to ready" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  # Unconditional (not _FIRST_CALL_ONLY): neither call ever supplies a
  # PR-intent marker, so the gate's nudge fires on the initial genuine
  # status=ready pass and is exhausted from the PR-intent marker's own
  # perspective, even though the resumed pass's own outcome verdict changes.
  export FAKE_DRIVER_NO_PR_INTENT=1
  # Only the resume call (call_index >= 1) reports status=blocked -- the
  # initial call stays at the default status=ready, so the gate's own
  # status=ready trigger condition still fires in the first place.
  export FAKE_DRIVER_OUTCOME_STATUS_ON_RESUME=blocked
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # The final (last-line-wins) SPINDRIFT_OUTCOME line in the container log is
  # the resumed pass's own genuine status=blocked verdict, not the original
  # status=ready line the gate captured before nudging.
  last_outcome_line="$(grep '^SPINDRIFT_OUTCOME ' <<<"$output" | tail -n1)"
  [[ "$last_outcome_line" == *"status=blocked"* ]]

  # Nothing after that final blocked line resurrects the original ready
  # line's text -- the fallback must not have reprinted it.
  after_last_blocked="$(awk '/^SPINDRIFT_OUTCOME .*status=blocked/{found=1; next} found' <<<"$output")"
  ! grep -q 'status=ready' <<<"$after_last_blocked"
}

@test "PR-intent gate: never fires on a read-write run" {
  # setup_entrypoint_env's default BOX_WRITE_ENABLED=1 stands -- a read-write
  # Box opens its own PR in-box (gh pr create) and never prints PR-intent at
  # all, so a missing one here is expected, not a bug.
  export RUN_NONCE="deadbeefcafe1234"
  export FAKE_DRIVER_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]
}

@test "PR-intent gate: never fires under CODE_FORGE=git" {
  # A push-only Code Forge never reaches OPEN A PULL REQUEST at all (ADR
  # 0034) -- there is no PR-intent contract to nudge.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="git"
  export CODE_FORGE_REMOTE_URL="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_NO_PR_INTENT=1
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]
}

@test "PR-intent gate: never fires under CODE_FORGE=local" {
  # The harness-mediated Code Forge (ADR 0033) never reaches OPEN A PULL
  # REQUEST either -- same exclusion as CODE_FORGE=git above, the other
  # non-github value the gate's equality check must reject.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_NO_PR_INTENT=1
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]
}

@test "PR-intent gate: never fires on a status=blocked run" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_NO_PR_INTENT=1
  export FAKE_DRIVER_OUTCOME_STATUS="blocked"
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]
}

# Issue #2448 AC4: "The nudge's existing scoping is unchanged... including
# when those runs reach their status via the backstop rather than a real
# outcome line." The four scoping tests above only ever exercise a genuine,
# driver-supplied outcome line ($_last_outcome_line valid straight from the
# first call); none of them route status through the synthetic backstop,
# which #2448 made a newly-reachable input to this same gate. The four
# companion tests below force every run through the backstop (two calls, via
# FAKE_DRIVER_NO_OUTCOME) and assert the same scoping still holds on that
# path.

@test "PR-intent gate: never fires on a read-write run reached via the synthetic backstop" {
  # setup_entrypoint_env's default BOX_WRITE_ENABLED=1 stands -- a read-write
  # Box opens its own PR in-box and never prints PR-intent, so a missing one
  # here is expected regardless of how status=ready was reached.
  export RUN_NONCE="deadbeefcafe1234"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Exactly two Driver invocations: initial + the SPINDRIFT_OUTCOME
  # required-marker gate's own resume force the backstop to fire; never a
  # third for the PR-intent nudge.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # The backstop path was actually exercised, not skipped for some other
  # reason -- a synthetic status=ready line is present.
  grep -q '^SPINDRIFT_OUTCOME .*status=ready' <<<"$output"
}

@test "PR-intent gate: never fires under CODE_FORGE=git reached via the synthetic backstop" {
  # A push-only Code Forge never reaches OPEN A PULL REQUEST at all (ADR
  # 0034) -- there is no PR-intent contract to nudge, backstop or not.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="git"
  export CODE_FORGE_REMOTE_URL="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
}

@test "PR-intent gate: never fires under CODE_FORGE=local reached via the synthetic backstop" {
  # The harness-mediated Code Forge (ADR 0033) never reaches OPEN A PULL
  # REQUEST either -- same exclusion as CODE_FORGE=git above, backstop or not.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
}

@test "PR-intent gate: never fires on a status=blocked run reached via the synthetic backstop" {
  # Deliberately no FAKE_DRIVER_COMMIT: with nothing on BRANCH to preserve,
  # the backstop's own commit-count check (cmd/launcher/internal/outcomebackstop
  # Run(): count == 0 -> note gets "; no work to preserve", status stays
  # "blocked") lands on status=blocked mechanically from git state, not from
  # a driver-supplied field -- mirroring the non-backstop
  # FAKE_DRIVER_OUTCOME_STATUS=blocked scoping test above without that knob.
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Exactly two Driver invocations: never a third for the PR-intent nudge,
  # which only ever fires on status=ready.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # The backstop path was actually exercised and landed on status=blocked --
  # proving this test reached the blocked-via-backstop path, not some other
  # skip reason.
  grep -q '^SPINDRIFT_OUTCOME .*status=blocked' <<<"$output"
}
