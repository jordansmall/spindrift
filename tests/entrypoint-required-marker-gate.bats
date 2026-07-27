#!/usr/bin/env bats
# Reusable required-marker gate (issue #2044): the resume-once-then-fall-
# through shape issue #1607 hardwired to SPINDRIFT_OUTCOME is extracted into
# required_marker_gate(scanner, corrective_prompt, predicate) so a future
# marker (e.g. SPINDRIFT_PR_INTENT) can register its own row without
# touching the outcome path. These tests exercise the gate directly with a
# synthetic marker/predicate, decoupled from SPINDRIFT_OUTCOME semantics --
# the outcome-specific behavior itself stays covered unchanged by
# entrypoint-outcome-recovery.bats and entrypoint-outcome-backstop.bats.

load helper

setup() {
  setup_entrypoint_env
  # Every entrypoint.sh function definition, minus the script's own
  # unconditional trailing `main "$@"` -- required_marker_gate is a plain
  # function, and main's container-only phases aren't needed to exercise it
  # in isolation. Each test appends its own doubles/assertions below this.
  GATE_HARNESS="$BATS_TEST_TMPDIR/gate_harness.sh"
  sed '$d' "$ENTRYPOINT" >"$GATE_HARNESS"
}

@test "required_marker_gate: marker present -> no resume" {
  cat >>"$GATE_HARNESS" <<'EOF'
run_driver_in_env() { echo "run_driver_in_env called"; return 0; }
_is_research_kind() { return 1; }
_scan_present() { printf 'present'; }
_require_nonempty() { [ -n "$1" ]; }
claude_rc=0
agents_json=""
_recovery_attempted=""
required_marker_gate _scan_present "corrective prompt" _require_nonempty
echo "recovery_attempted=[$_recovery_attempted]"
echo "claude_rc=$claude_rc"
EOF
  run bash "$GATE_HARNESS"
  [ "$status" -eq 0 ]
  [ "$(grep -c 'run_driver_in_env called' <<<"$output")" -eq 0 ]
  grep -q 'recovery_attempted=\[\]' <<<"$output"
  grep -q 'claude_rc=0' <<<"$output"
}

@test "required_marker_gate: marker absent -> resumes once and re-scans" {
  cat >>"$GATE_HARNESS" <<'EOF'
_driver_calls=0
run_driver_in_env() {
  _driver_calls=$((_driver_calls + 1))
  echo "run_driver_in_env called with prompt=[$1] mode=[$3]"
  return 0
}
_is_research_kind() { return 1; }
_scan_absent() { printf ''; }
_require_nonempty() { [ -n "$1" ]; }
claude_rc=0
agents_json="{}"
_recovery_attempted=""
required_marker_gate _scan_absent "corrective prompt text" _require_nonempty
echo "recovery_attempted=[$_recovery_attempted]"
echo "driver_calls=$_driver_calls"
EOF
  run bash "$GATE_HARNESS"
  [ "$status" -eq 0 ]
  [ "$(grep -c 'run_driver_in_env called' <<<"$output")" -eq 1 ]
  grep -q 'prompt=\[corrective prompt text\] mode=\[resume\]' <<<"$output"
  grep -q 'recovery_attempted=\[1\]' <<<"$output"
  grep -q 'driver_calls=1' <<<"$output"
}

@test "required_marker_gate: research kind never resumes even when marker absent" {
  cat >>"$GATE_HARNESS" <<'EOF'
run_driver_in_env() { echo "run_driver_in_env called"; return 0; }
_is_research_kind() { return 0; }
_scan_absent() { printf ''; }
_require_nonempty() { [ -n "$1" ]; }
claude_rc=0
agents_json=""
_recovery_attempted=""
required_marker_gate _scan_absent "corrective prompt" _require_nonempty
echo "recovery_attempted=[$_recovery_attempted]"
EOF
  run bash "$GATE_HARNESS"
  [ "$status" -eq 0 ]
  [ "$(grep -c 'run_driver_in_env called' <<<"$output")" -eq 0 ]
  grep -q 'recovery_attempted=\[\]' <<<"$output"
}

@test "required_marker_gate: non-zero claude_rc never resumes" {
  cat >>"$GATE_HARNESS" <<'EOF'
run_driver_in_env() { echo "run_driver_in_env called"; return 0; }
_is_research_kind() { return 1; }
_scan_absent() { printf ''; }
_require_nonempty() { [ -n "$1" ]; }
claude_rc=17
agents_json=""
_recovery_attempted=""
required_marker_gate _scan_absent "corrective prompt" _require_nonempty
echo "recovery_attempted=[$_recovery_attempted]"
EOF
  run bash "$GATE_HARNESS"
  [ "$status" -eq 0 ]
  [ "$(grep -c 'run_driver_in_env called' <<<"$output")" -eq 0 ]
  grep -q 'recovery_attempted=\[\]' <<<"$output"
}
