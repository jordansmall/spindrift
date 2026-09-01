#!/usr/bin/env bats
# The PR-intent required-marker gate row (issue #2045, the #2036 fix): a
# read-only github Box that reaches status=ready but never printed a
# SPINDRIFT_PR_INTENT line leaves the launcher's hostMediateDraftPR with nothing
# to relay -- it posts "merge blocked" and strands the finished branch. The gate
# resumes the same pinned session once with a corrective nudge before that.

load helper

setup() {
  setup_entrypoint_env
}

# --- end-to-end: the required-marker gate's PR-intent row ------------------
# The #2036 dogfood failure reproduced: a read-only github run reaches
# status=ready but the Driver never printed SPINDRIFT_PR_INTENT, so the
# launcher's hostMediateDraftPR has nothing to relay.

@test "PR-intent gate: default fake output already supplies a genuine PR-intent line on a status=ready read-only run, no spurious resume" {
  # None of the FAKE_DRIVER_NO_PR_INTENT* knobs are set here, unlike every other
  # test in this file -- fakes/claude emits a genuine SPINDRIFT_PR_INTENT line by
  # default. Also a mutation guard for the gate's --log-path wiring: a wrong
  # value would leave the verb unable to find a marker that is actually there,
  # wrongly firing a spurious second Driver invocation.
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Exactly one Driver invocation -- no spurious resume fired.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]

  # The genuine marker really is present -- so this isn't vacuously passing on a
  # marker that was absent for some other reason.
  grep -q 'SPINDRIFT_PR_INTENT deadbeefcafe1234' <<<"$output"

  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
}

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

  # The nudge succeeded, so no exhausted-nudge give-up op is emitted -- that op
  # fires only on the genuinely-blocked, nudge-exhausted path.
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

  # The gate never rewrites the outcome itself on a miss -- the status=ready line
  # the Driver committed to survives, for hostMediateDraftPR to classify.
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
  ! grep -q 'SPINDRIFT_PR_INTENT' <<<"$output"
}

# When the nudge is exhausted (issue #2046), the gate emits a heartbeat op
# recording the attempt count and the give-up decision, so an operator can see
# *why* the run ended blocked. The op is a plain stream-json line on the box's
# stdout, parsed by the host heartbeat Writer like the orchestrator's own ops.
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

  # The op line never carries the literal marker token: a real downstream scan
  # (outcome.LastPRIntentInLog) runs over this whole stream, so the give-up
  # reason must not be mistaken for a genuine PR-intent attempt.
  ! grep -q 'SPINDRIFT_PR_INTENT' <<<"$output"
}

# Issue #2448: the synthetic outcome backstop only ever printed its
# SPINDRIFT_OUTCOME line -- it never assigned $_last_outcome_line -- so a
# status=ready reached only via the backstop left this gate reading an empty
# variable and the nudge silently never fired.
@test "PR-intent gate: fires on a status=ready reached only via the synthetic backstop" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1   # every call (initial + the outcome gate's own resume) produces no outcome line, forcing the synthetic backstop to fire
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Exactly three Driver invocations: calls 1 and 2 (initial + the
  # SPINDRIFT_OUTCOME gate's own resume) are both no-outcome under
  # FAKE_DRIVER_NO_OUTCOME, so the backstop fires a synthetic status=ready line;
  # call 3 is the PR-intent nudge's resume, reachable thanks to the #2448 fix.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 3 ]

  # FAKE_DRIVER_NO_OUTCOME is unconditional (not _FIRST_CALL_ONLY), so the nudge
  # call produces no PR-intent marker either -- the nudge is exhausted. That
  # resumed pass's result text carries no SPINDRIFT_OUTCOME token at all, so
  # nothing shadowed the original line: the gate's fallback must not reprint it,
  # and the line still appears exactly once.
  [ "$(grep -c '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*relayed via outbox bundle' <<<"$output")" -eq 1 ]

  # The give-up heartbeat op fires (same assertions as "an exhausted nudge
  # emits a heartbeat give-up op" above).
  grep -q '"type":"spindrift_op"' <<<"$output"
  grep -q '"op":"decision"' <<<"$output"
  grep -q '"decision":"stop"' <<<"$output"
  grep -q 'nudge exhausted after 1 attempt' <<<"$output"

  [ -f "$OUTBOX_DIR/seam.bundle" ]
}

# Issue #2448 AC2: a nudged pass that supplies a usable PR-intent line hands off
# a draft PR exactly as a genuinely-ready run does. The test above exercises only
# the *exhausted* nudge; this one lets the nudge's own resume succeed.
@test "PR-intent gate: a backstop-derived status=ready run's nudge succeeds when the resumed pass supplies PR-intent" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  # call_index 0 and 1 are both no-outcome, forcing the synthetic backstop --
  # same setup as the test above. Unlike it, call_index 2 (the PR-intent nudge's
  # resume) falls through to the fake's normal scripted output, supplying a
  # genuine SPINDRIFT_PR_INTENT line plus a fresh status=ready outcome.
  export FAKE_DRIVER_NO_OUTCOME_BEFORE_CALL=2
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Three Driver invocations: initial, the SPINDRIFT_OUTCOME gate's resume,
  # and the PR-intent nudge's own (this time successful) resume.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 3 ]

  # The resumed pass's own canned output actually supplies the marker --
  # visible in the raw teed Driver stream, not just believed.
  grep -q 'SPINDRIFT_PR_INTENT deadbeefcafe1234' <<<"$output"

  # The nudge succeeded, so no exhausted-nudge give-up op is emitted.
  ! grep -q '"op":"decision"' <<<"$output"
}

# Issue #2448 finding 3: now that the fix lets the nudge reach a
# backstop-derived run, a new hazard opens up. If the nudge's own corrective
# resume crashes or exits non-zero, that failure lands in main()'s $claude_rc,
# and the unconditional `exit "$claude_rc"` would send this already-backstopped,
# already-relayed run to the launcher's ClassifyTransient/retry path -- exactly
# what the backstop's terminal exit 0 exists to prevent (issue #593). The
# backstop already committed to a terminal status=ready verdict; a transient
# failure in the nudge that follows must not retroactively undo it.
@test "PR-intent gate: a crashed resume after a backstop-declared ready outcome stays terminal (exit 0)" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  # call_index 0 and 1 produce no outcome line, forcing the synthetic backstop;
  # call_index 2 (the PR-intent nudge's resume) instead crashes with a
  # transient-looking non-zero exit, simulating an infrastructure failure
  # mid-nudge.
  export FAKE_DRIVER_NO_OUTCOME=1
  export FAKE_DRIVER_CRASH_EXIT=17
  export FAKE_DRIVER_CRASH_EXIT_FROM_CALL=2
  run bash "$ENTRYPOINT"

  # The crash in the nudge's resume must not flip the entrypoint's exit status
  # away from the terminal 0 the backstop already committed to -- neither to the
  # crash's own code (17) nor any other non-zero value.
  [ "$status" -eq 0 ]

  # All three calls happened: initial, the SPINDRIFT_OUTCOME gate's resume,
  # and the PR-intent nudge's own (crashing) resume.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 3 ]

  # The backstop's status=ready line was still emitted exactly once -- the crash
  # happened after it, and must not have erased or duplicated it.
  [ "$(grep -c '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*relayed via outbox bundle' <<<"$output")" -eq 1 ]

  [ -f "$OUTBOX_DIR/seam.bundle" ]
}

# Issue #2448 finding 2: the gate's "restoring it" fallback used to restore
# unconditionally whenever the resumed line differed from the original ready
# line -- including when the resumed pass supplied its own genuine,
# differently-shaped verdict (deciding mid-nudge that the run is actually
# status=blocked). Restoring there clobbered the driver's honest final word with
# the earlier ready line, which the launcher's last-line-wins scan would then
# see, hiding a genuinely blocked run behind a manufactured status=ready.
@test "PR-intent gate: a genuine status=blocked verdict from the resumed pass is never clobbered back to ready" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  # Unconditional (not _FIRST_CALL_ONLY): neither call supplies a PR-intent
  # marker, so the nudge fires and is exhausted from the marker's perspective,
  # even though the resumed pass's own outcome verdict changes.
  export FAKE_DRIVER_NO_PR_INTENT=1
  # Only the resume call reports status=blocked -- the initial call stays at the
  # default status=ready, so the gate's trigger condition still fires.
  export FAKE_DRIVER_OUTCOME_STATUS_ON_RESUME=blocked
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # The final (last-line-wins) SPINDRIFT_OUTCOME line is the resumed pass's own
  # status=blocked verdict, not the original status=ready line.
  last_outcome_line="$(grep '^SPINDRIFT_OUTCOME ' <<<"$output" | tail -n1)"
  [[ "$last_outcome_line" == *"status=blocked"* ]]

  # Nothing after that final blocked line resurrects the original ready
  # line's text -- the fallback must not have reprinted it.
  after_last_blocked="$(awk '/^SPINDRIFT_OUTCOME .*status=blocked/{found=1; next} found' <<<"$output")"
  ! grep -q 'status=ready' <<<"$after_last_blocked"
}

@test "PR-intent gate: never fires on a read-write run" {
  # setup_entrypoint_env's default BOX_WRITE_ENABLED=1 stands -- a read-write Box
  # opens its own PR in-box and never prints PR-intent at all.
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
  # A real CODE_FORGE=git Box never gets this signal forwarded (neither the "git"
  # nor "local" backend row carries outboxRelayCapable=true), so unset
  # setup_entrypoint_env's github-mirroring default to reproduce the git case the
  # gate (keyed on _is_readonly_outbox_relay, not a raw CODE_FORGE check) sees.
  unset BOX_OUTBOX_RELAY_CAPABLE
  export FAKE_DRIVER_NO_PR_INTENT=1
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]
}

@test "PR-intent gate: never fires under CODE_FORGE=local" {
  # The harness-mediated Code Forge (ADR 0033) never reaches OPEN A PULL REQUEST
  # either -- same exclusion as CODE_FORGE=git above.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  # A real CODE_FORGE=local Box never gets this signal forwarded either (the
  # "local" row has hostMediatedRemote=true, not outboxRelayCapable=true) --
  # unset the helper's github-mirroring default, same as the git test above.
  unset BOX_OUTBOX_RELAY_CAPABLE
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

# Issue #2448 AC4: the nudge's scoping must hold on the backstop path too. The
# four scoping tests above only exercise a genuine, driver-supplied outcome
# line; the four companion tests below force every run through the backstop
# (two calls, via FAKE_DRIVER_NO_OUTCOME) and assert the same scoping.

@test "PR-intent gate: never fires on a read-write run reached via the synthetic backstop" {
  # setup_entrypoint_env's default BOX_WRITE_ENABLED=1 stands -- a read-write Box
  # never prints PR-intent, regardless of how status=ready was reached.
  export RUN_NONCE="deadbeefcafe1234"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Exactly two Driver invocations: initial + the SPINDRIFT_OUTCOME gate's resume
  # force the backstop to fire; never a third for the PR-intent nudge.
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
  # See the non-backstop CODE_FORGE=git test above: a real git Box never gets
  # this signal forwarded, so unset the helper's default here too.
  unset BOX_OUTBOX_RELAY_CAPABLE
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
  # See the non-backstop CODE_FORGE=local test above: a real local Box never gets
  # this signal forwarded either, so unset the helper's default here too.
  unset BOX_OUTBOX_RELAY_CAPABLE
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]
}

@test "PR-intent gate: never fires on a status=blocked run reached via the synthetic backstop" {
  # Deliberately no FAKE_DRIVER_COMMIT: with nothing on BRANCH to preserve, the
  # backstop's own commit-count check lands on status=blocked mechanically from
  # git state rather than a driver-supplied field -- mirroring the non-backstop
  # FAKE_DRIVER_OUTCOME_STATUS=blocked scoping test without that knob.
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
  # proving this reached that path, not some other skip reason.
  grep -q '^SPINDRIFT_OUTCOME .*status=blocked' <<<"$output"
}
