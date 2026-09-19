#!/usr/bin/env bats
# The PR-intent required-marker gate (issue #2045, the #2036 fix): a read-only
# github Box reaching status=ready without printing SPINDRIFT_PR_INTENT leaves
# hostMediateDraftPR nothing to relay, so the launcher posts "merge blocked" and
# strands the branch. A second row (issue #2044, decision #2511) nudges it once.

load helper

setup() {
  setup_entrypoint_env
}

@test "PR-intent gate: default fake output already supplies a genuine PR-intent line on a status=ready read-only run, no spurious resume" {
  # None of the FAKE_DRIVER_NO_PR_INTENT* knobs are set here: fakes/claude by
  # default emits a genuine SPINDRIFT_PR_INTENT line on every call. This also
  # guards the gate's --log-path wiring, since an empty or wrong value would
  # hide the marker that is really there and fire a spurious second Driver
  # invocation.
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # No spurious resume fired.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]

  # Not vacuously passing: the genuine marker really is in the output.
  grep -q 'SPINDRIFT_PR_INTENT deadbeefcafe1234' <<<"$output"

  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
}

@test "PR-intent gate: missing PR-intent on a status=ready read-only run resumes once and the resumed pass supplies it" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_NO_PR_INTENT_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The two calls are the initial pass and the gate's one resume.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # fakes/claude overwrites $DRIVER_PROMPT_FILE per call, so this is the
  # resumed pass's prompt. It must carry the canonical grammar and this run's
  # own nonce, not a fresh one.
  grep -qF 'SPINDRIFT_PR_INTENT deadbeefcafe1234 <base64-encoded title' "$DRIVER_PROMPT_FILE"
  grep -qF 'status=ready' "$DRIVER_PROMPT_FILE"

  # The resumed pass really supplied the marker, visible in the teed stream.
  [[ "$output" == *"SPINDRIFT_PR_INTENT deadbeefcafe1234"* ]]

  # The original status=ready outcome survives: no synthetic blocked backstop
  # fired despite the extra resume pass.
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
  ! grep -q 'driver produced no SPINDRIFT_OUTCOME line' <<<"$output"

  # The nudge succeeded, so no give-up op is emitted (issue #2046): that op
  # fires only on the genuinely-blocked, nudge-exhausted path.
  ! grep -q '"op":"decision"' <<<"$output"
}

@test "PR-intent gate: a second consecutive miss falls through unchanged, no loop" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The gate attempts one resume only, never a second.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # The gate never rewrites the outcome itself on a miss: the status=ready line
  # the Driver committed to survives for the launcher's own
  # hostMediateDraftPR/blockHandoff to classify.
  grep -q '^SPINDRIFT_OUTCOME issue=7 landing=https://github.com/owner/repo/pull/1 status=ready note=fake$' <<<"$output"
  ! grep -q 'SPINDRIFT_PR_INTENT' <<<"$output"
}

# When the nudge is exhausted (issue #2046), the gate emits a heartbeat op
# (issue #2027's spindrift_op stream) recording the attempt count and the
# give-up decision, so an operator can see why the run ended blocked. The op is
# a plain stream-json line on the box's stdout, parsed by the host heartbeat
# Writer like the orchestrator's own ops.
@test "PR-intent gate: an exhausted nudge emits a heartbeat give-up op" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  export FAKE_DRIVER_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # A well-formed spindrift_op decision line (issue #2027's Event shape).
  grep -q '"type":"spindrift_op"' <<<"$output"
  grep -q '"op":"decision"' <<<"$output"
  grep -q '"decision":"stop"' <<<"$output"
  grep -q 'nudge exhausted after 1 attempt' <<<"$output"

  # A real downstream scan (the launcher's outcome.LastPRIntentInLog, via
  # driver-exec marker-gate) runs over this whole stream, so the give-up reason
  # must not carry the literal marker token and be mistaken for a genuine
  # PR-intent attempt.
  ! grep -q 'SPINDRIFT_PR_INTENT' <<<"$output"
}

# Issue #2448: the synthetic outcome backstop only ever printed its
# SPINDRIFT_OUTCOME line and never assigned $_last_outcome_line, so a
# status=ready reached only that way left this gate reading an empty variable
# and the nudge silently never fired.
@test "PR-intent gate: fires on a status=ready reached only via the synthetic backstop" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1   # every call, forcing the synthetic backstop to fire
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Calls 1 and 2 (initial plus the SPINDRIFT_OUTCOME gate's one resume) are
  # both no-outcome, so the backstop fires a synthetic status=ready line. Call
  # 3 is the PR-intent nudge's own resume, reachable thanks to the #2448 fix
  # that carries the backstop's synthetic line into $_last_outcome_line.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 3 ]

  # FAKE_DRIVER_NO_OUTCOME is unconditional, so the third (nudge) call supplies
  # no PR-intent marker either and the nudge is exhausted. That pass carries no
  # SPINDRIFT_OUTCOME token at all, so nothing shadowed the original line and
  # the gate's fallback must not reprint it.
  [ "$(grep -c '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*relayed via outbox bundle' <<<"$output")" -eq 1 ]

  # The give-up heartbeat op fires, as in the exhausted-nudge test above.
  grep -q '"type":"spindrift_op"' <<<"$output"
  grep -q '"op":"decision"' <<<"$output"
  grep -q '"decision":"stop"' <<<"$output"
  grep -q 'nudge exhausted after 1 attempt' <<<"$output"

  # The bundle relay still happened, unaffected by the fix.
  [ -f "$OUTBOX_DIR/seam.bundle" ]
}

# Issue #2448 AC2: a nudged pass that supplies a usable PR-intent line hands
# off a draft PR like a genuinely-ready run. The test above only exercises the
# exhausted nudge, since FAKE_DRIVER_NO_OUTCOME is unconditional there; this
# one lets the nudge's own resume succeed.
@test "PR-intent gate: a backstop-derived status=ready run's nudge succeeds when the resumed pass supplies PR-intent" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  # Calls 0 and 1 are no-outcome, forcing the synthetic backstop to fire. Unlike
  # the test above, call 2 (the nudge's own resume) falls through to the fake's
  # normal output, which supplies a genuine SPINDRIFT_PR_INTENT line plus a
  # fresh status=ready outcome line.
  export FAKE_DRIVER_NO_OUTCOME_BEFORE_CALL=2
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The three calls are the initial pass, the SPINDRIFT_OUTCOME gate's resume,
  # and the nudge's own resume.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 3 ]

  # The resumed pass really supplied the marker, visible in the teed stream.
  grep -q 'SPINDRIFT_PR_INTENT deadbeefcafe1234' <<<"$output"

  # The nudge succeeded, so no give-up op: that fires only when exhausted.
  ! grep -q '"op":"decision"' <<<"$output"
}

# Issue #2448 finding 3: now that the nudge can reach a backstop-derived run, a
# crash in its corrective resume lands in main()'s own $claude_rc, and the
# unconditional `exit "$claude_rc"` would send an already-backstopped,
# already-relayed run to the launcher's ClassifyTransient/retry path. The
# backstop (issue #593) already committed to a terminal status=ready verdict.
@test "PR-intent gate: a crashed resume after a backstop-declared ready outcome stays terminal (exit 0)" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED     # read-only Box: no push token
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_COMMIT=1
  # Calls 0 and 1 produce no outcome line, forcing the synthetic backstop to
  # fire. Call 2 (the nudge's own resume) instead crashes with a
  # transient-looking non-zero exit.
  export FAKE_DRIVER_NO_OUTCOME=1
  export FAKE_DRIVER_CRASH_EXIT=17
  export FAKE_DRIVER_CRASH_EXIT_FROM_CALL=2
  run bash "$ENTRYPOINT"

  # The crash must not flip the exit status away from the terminal 0 the
  # backstop already committed to, neither to the crash's own 17 nor to any
  # other non-zero value.
  [ "$status" -eq 0 ]

  # The three calls are the initial pass, the SPINDRIFT_OUTCOME gate's resume,
  # and the crashing nudge resume.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 3 ]

  # The crash happened after the backstop's status=ready line and must not have
  # erased or duplicated it.
  [ "$(grep -c '^SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=.*relayed via outbox bundle' <<<"$output")" -eq 1 ]

  # The bundle relay still happened, unaffected by the crash.
  [ -f "$OUTBOX_DIR/seam.bundle" ]
}

# Issue #2448 finding 2: the gate's "resumed pass did not repeat the original
# SPINDRIFT_OUTCOME line" fallback used to restore unconditionally whenever the
# resumed line differed, clobbering a genuine differently-shaped verdict (say a
# mid-nudge status=blocked) with the earlier ready line, which the launcher's
# last-line-wins outcome.LastInLog scan would then see.
@test "PR-intent gate: a genuine status=blocked verdict from the resumed pass is never clobbered back to ready" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  # Unconditional (not _FIRST_CALL_ONLY): neither call supplies a PR-intent
  # marker, so the nudge fires on the initial status=ready pass and is
  # exhausted even though the resumed pass's outcome verdict changes.
  export FAKE_DRIVER_NO_PR_INTENT=1
  # Only the resume call reports status=blocked, so the initial call stays
  # status=ready and the gate's trigger condition still fires.
  export FAKE_DRIVER_OUTCOME_STATUS_ON_RESUME=blocked
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # The last-line-wins SPINDRIFT_OUTCOME line is the resumed pass's genuine
  # status=blocked verdict, not the ready line the gate captured before nudging.
  last_outcome_line="$(grep '^SPINDRIFT_OUTCOME ' <<<"$output" | tail -n1)"
  [[ "$last_outcome_line" == *"status=blocked"* ]]

  # Nothing after the blocked line resurrects the ready line: the fallback must
  # not have reprinted it.
  after_last_blocked="$(awk '/^SPINDRIFT_OUTCOME .*status=blocked/{found=1; next} found' <<<"$output")"
  ! grep -q 'status=ready' <<<"$after_last_blocked"
}

@test "PR-intent gate: never fires on a read-write run" {
  # setup_entrypoint_env's default BOX_WRITE_ENABLED=1 stands: a read-write Box
  # opens its own PR in-box (gh pr create) and never prints PR-intent, so a
  # missing one here is expected.
  export RUN_NONCE="deadbeefcafe1234"
  export FAKE_DRIVER_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]
}

@test "PR-intent gate: never fires under CODE_FORGE=git" {
  # A push-only Code Forge never reaches OPEN A PULL REQUEST (ADR 0034), so
  # there is no PR-intent contract to nudge.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="git"
  export CODE_FORGE_REMOTE_URL="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  # A real CODE_FORGE=git Box never gets this signal forwarded (neither the
  # "git" nor "local" row in lib/backends/default.nix carries
  # outboxRelayCapable=true); helper.bash exports it by default to mirror a
  # github Box, so unset it to reproduce the git case the gate sees.
  unset BOX_OUTBOX_RELAY_CAPABLE
  export FAKE_DRIVER_NO_PR_INTENT=1
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 1 ]
}

@test "PR-intent gate: never fires under CODE_FORGE=local" {
  # The harness-mediated Code Forge (ADR 0033) never reaches OPEN A PULL
  # REQUEST either, the same exclusion as CODE_FORGE=git above.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  # A real CODE_FORGE=local Box never gets this signal forwarded either (the
  # "local" row has hostMediatedRemote=true, not outboxRelayCapable=true), so
  # unset the helper's github-mirroring default as above.
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

# Issue #2448 AC4: the four scoping tests above only exercise a genuine,
# driver-supplied outcome line. The four below force every run through the
# synthetic backstop (two calls, via FAKE_DRIVER_NO_OUTCOME), which #2448 made
# a newly-reachable input to this gate, and assert the same scoping holds.

@test "PR-intent gate: never fires on a read-write run reached via the synthetic backstop" {
  # setup_entrypoint_env's default BOX_WRITE_ENABLED=1 stands: a read-write Box
  # never prints PR-intent, so a missing one is expected however status=ready
  # was reached.
  export RUN_NONCE="deadbeefcafe1234"
  export FAKE_DRIVER_COMMIT=1
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Initial plus the SPINDRIFT_OUTCOME gate's resume force the backstop to
  # fire; never a third call for the PR-intent nudge.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # A synthetic status=ready line proves the backstop path ran, rather than the
  # nudge being skipped for some other reason.
  grep -q '^SPINDRIFT_OUTCOME .*status=ready' <<<"$output"
}

@test "PR-intent gate: never fires under CODE_FORGE=git reached via the synthetic backstop" {
  # A push-only Code Forge never reaches OPEN A PULL REQUEST (ADR 0034), so
  # there is no PR-intent contract to nudge, backstop or not.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="git"
  export CODE_FORGE_REMOTE_URL="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  # A real git Box never gets this signal forwarded, so unset the helper's
  # github-mirroring default here too (see the non-backstop test above).
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
  # REQUEST either, the same exclusion as CODE_FORGE=git above.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  # A real local Box never gets this signal forwarded either (hostMediatedRemote,
  # not outboxRelayCapable), so unset the helper's default here too.
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
  # backstop's own commit-count check (cmd/launcher/internal/outcomebackstop
  # Run(): count == 0 keeps status "blocked") lands on status=blocked from git
  # state, not from a driver-supplied field.
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  unset CODE_FORGE            # default github
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  export FAKE_DRIVER_NO_OUTCOME=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # There is never a third call for the nudge, which only fires on status=ready.
  [ "$(grep -c '^driver invoked for issue' "$DRIVER_LOG")" -eq 2 ]

  # This line proves the run reached the blocked-via-backstop path, not some
  # other skip reason.
  grep -q '^SPINDRIFT_OUTCOME .*status=blocked' <<<"$output"
}
