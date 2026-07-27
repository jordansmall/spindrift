#!/usr/bin/env bats
# The PR-intent required-marker gate row (issue #2045, the #2036 fix): a
# read-only github Box that reaches status=ready but never printed a
# SPINDRIFT_PR_INTENT line leaves the launcher's hostMediateDraftPR with
# nothing to relay -- it posts "merge blocked" and strands the finished
# branch. Registering a second row on required_marker_gate (issue #2044)
# resumes the same pinned session once with a corrective nudge before that
# happens.

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

# --- _emit_pr_intent_giveup_op ---------------------------------------------
# When the PR-intent nudge is exhausted (issue #2046) the gate records the
# give-up as a heartbeat "decision" op (issue #2027's spindrift_op stream) --
# an ordinary stream-json line on the Box's own stdout, parsed by the host
# heartbeat Writer exactly like the orchestrator's own ops -- so an operator
# sees why the run ended blocked rather than an unexplained state.

@test "_emit_pr_intent_giveup_op: prints one well-formed spindrift_op decision line" {
  local harness="$BATS_TEST_TMPDIR/op_harness.sh"
  sed '$d' "$ENTRYPOINT" >"$harness"
  cat >>"$harness" <<'EOF'
_emit_pr_intent_giveup_op 1
EOF
  run bash "$harness"
  [ "$status" -eq 0 ]

  # Exactly one line, and valid JSON the Go heartbeat Writer's Event/
  # SpindriftOp shape (cmd/launcher/internal/driver/claude) unmarshals.
  [ "$(printf '%s' "$output" | grep -c .)" -eq 1 ]
  echo "$output" | jq -e '.type == "spindrift_op"' >/dev/null
  echo "$output" | jq -e '.spindrift_op.op == "decision"' >/dev/null
  echo "$output" | jq -e '.spindrift_op.decision == "stop"' >/dev/null

  # The give-up reason names the single nudge attempt behind it.
  echo "$output" | jq -e '.spindrift_op.reason | test("nudge exhausted after 1 attempt")' >/dev/null

  # Never carries the literal marker token -- it must not be mistaken for a
  # genuine SPINDRIFT_PR_INTENT attempt by any downstream scan.
  ! [[ "$output" == *"SPINDRIFT_PR_INTENT"* ]]
}

# --- end-to-end: the required-marker gate's PR-intent row ------------------
# The #2036 dogfood failure reproduced: a read-only github run reaches
# status=ready but the Driver never printed SPINDRIFT_PR_INTENT, so the
# launcher's hostMediateDraftPR has nothing to relay.

@test "PR-intent gate: missing PR-intent on a status=ready read-only run resumes once and the resumed pass supplies it" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  export FAKE_CLAUDE_NO_PR_INTENT_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # Exactly two Driver invocations: the initial pass and the one resume pass
  # the gate's required_marker_gate row fires.
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 2 ]

  # The resumed pass is the one whose prompt was actually rendered last
  # (fakes/claude overwrites $CLAUDE_PROMPT_FILE per call) -- it must carry
  # the canonical grammar and this run's own nonce, not a fresh one.
  grep -qF 'SPINDRIFT_PR_INTENT deadbeefcafe1234 <base64-encoded title' "$CLAUDE_PROMPT_FILE"
  grep -qF 'status=ready' "$CLAUDE_PROMPT_FILE"

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
  export FAKE_CLAUDE_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # One resume attempt only -- never a second.
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 2 ]

  # required_marker_gate never rewrites the outcome itself on a miss -- the
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
  export FAKE_CLAUDE_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # A well-formed spindrift_op decision line (issue #2027's Event shape),
  # recording the give-up and the single nudge attempt behind it.
  grep -q '"type":"spindrift_op"' <<<"$output"
  grep -q '"op":"decision"' <<<"$output"
  grep -q '"decision":"stop"' <<<"$output"
  grep -q 'nudge exhausted after 1 attempt' <<<"$output"
}

@test "PR-intent gate: never fires on a read-write run" {
  # setup_entrypoint_env's default BOX_WRITE_ENABLED=1 stands -- a read-write
  # Box opens its own PR in-box (gh pr create) and never prints PR-intent at
  # all, so a missing one here is expected, not a bug.
  export RUN_NONCE="deadbeefcafe1234"
  export FAKE_CLAUDE_NO_PR_INTENT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 1 ]
}

@test "PR-intent gate: never fires under CODE_FORGE=git" {
  # A push-only Code Forge never reaches OPEN A PULL REQUEST at all (ADR
  # 0034) -- there is no PR-intent contract to nudge.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="git"
  export CODE_FORGE_REMOTE_URL="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  export FAKE_CLAUDE_NO_PR_INTENT=1
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 1 ]
}

@test "PR-intent gate: never fires under CODE_FORGE=local" {
  # The harness-mediated Code Forge (ADR 0033) never reaches OPEN A PULL
  # REQUEST either -- same exclusion as CODE_FORGE=git above, the other
  # non-github value the gate's equality check must reject.
  export RUN_NONCE="deadbeefcafe1234"
  export CODE_FORGE="local"
  export REPO_MOUNT_DIR="$REMOTE_ROOT/owner/repo.git"
  unset BOX_WRITE_ENABLED
  export FAKE_CLAUDE_NO_PR_INTENT=1
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 1 ]
}

@test "PR-intent gate: never fires on a status=blocked run" {
  export RUN_NONCE="deadbeefcafe1234"
  unset BOX_WRITE_ENABLED
  export FAKE_CLAUDE_NO_PR_INTENT=1
  export FAKE_CLAUDE_OUTCOME_STATUS="blocked"
  export OUTBOX_DIR="$BATS_TEST_TMPDIR/outbox"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^claude invoked for issue' "$CLAUDE_LOG")" -eq 1 ]
}
