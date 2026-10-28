#!/usr/bin/env bats
# Issue #5862: nix/gate.sh streams nix-eval-jobs output into builds. The
# nix-eval-jobs, nix and nix-store binaries are PATH stubs; only the script's
# own decisions (worker count, failure attribution, exit code) are under test.

bats_require_minimum_version 1.5.0

setup() {
  GATE_SH="${GATE_SH:-$BATS_TEST_DIRNAME/../nix/gate.sh}"
  STUBS="$BATS_TEST_TMPDIR/stubs"
  mkdir -p "$STUBS"
  export NEJ_ARGV="$BATS_TEST_TMPDIR/nej.argv"
  export NIX_ARGV="$BATS_TEST_TMPDIR/nix.argv"
  export NEJ_JOBS="$BATS_TEST_TMPDIR/jobs.jsonl"
  export STUB_NIX_STATUS=0
  export STUB_INVALID=""
  : >"$NEJ_JOBS"

  cat >"$STUBS/nix-eval-jobs" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$NEJ_ARGV"
if [ -z "${STUB_NEJ_HANDSHAKE:-}" ]; then
  cat "$NEJ_JOBS"
else
  # Hold back each line until the gate has built the previous one, so eval can
  # only finish if builds overlap it. A regression times out instead of hanging.
  mapfile -t lines <"$NEJ_JOBS"
  for i in "${!lines[@]}"; do
    printf '%s\n' "${lines[i]}"
    [ "$i" -lt $((${#lines[@]} - 1)) ] || break
    drv=$(grep -o '/nix/store/[^"]*\.drv' <<<"${lines[i]}")
    for _ in $(seq 200); do
      grep -qsF -- "$drv" "$NIX_ARGV" && continue 2
      sleep 0.05
    done
    exit 1
  done
fi
exit "${STUB_NEJ_STATUS:-0}"
STUB
  cat >"$STUBS/nix" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$NIX_ARGV"
[ -z "${STUB_NIX_STDERR:-}" ] || printf '%s\n' "$STUB_NIX_STDERR" >&2
# With STUB_FAIL_DRVS set, a lone-drv rebuild (the gate's per-job fallback)
# fails only for the drvs listed; every other call exits STUB_NIX_STATUS. A
# rebuild is a call with one `^*` whose drv an earlier call already named (each
# drv is in exactly one batch), so a single-job batch is not mistaken for one.
if [ -n "${STUB_FAIL_DRVS+x}" ] && [ "$(grep -o -- '\^\*' <<<"$*" | wc -l)" -eq 1 ]; then
  drv=${*: -1}
  if head -n -1 "$NIX_ARGV" | grep -qF -- "$drv"; then
    for d in $STUB_FAIL_DRVS; do
      case "$*" in *"$d^*"*) exit 1 ;; esac
    done
    exit 0
  fi
fi
exit "$STUB_NIX_STATUS"
STUB
  cat >"$STUBS/nix-store" <<'STUB'
#!/usr/bin/env bash
[ -z "$STUB_INVALID" ] || printf '%s\n' "$STUB_INVALID"
exit 0
STUB
  # A sandboxed build host has no /usr/bin/env.
  sed -i "1s|.*|#!$BASH|" "$STUBS"/*
  chmod +x "$STUBS"/*
  export PATH="$STUBS:$PATH"

  export GATE_INSTALLABLE=".#set"
  export GATE_POLL_SECONDS=0.05
  export GATE_MEMORY_MAX_FILE="$BATS_TEST_TMPDIR/memory.max"
  echo max >"$GATE_MEMORY_MAX_FILE"
}

job() {
  printf '{"attr":"%s","attrPath":["%s"],"drvPath":"/nix/store/%s.drv","outputs":{"out":"/nix/store/%s-out"}}\n' \
    "$1" "$1" "$1" "$1" >>"$NEJ_JOBS"
}

workers_for() {
  printf '%s\n' "$1" >"$GATE_MEMORY_MAX_FILE"
  job a
  bash "$GATE_SH" 2>"$BATS_TEST_TMPDIR/gate.err" || return 1
  grep -o -- '--workers [0-9]*' "$NEJ_ARGV"
}

@test "an uncapped cgroup (max) runs 4 workers" {
  run workers_for max
  [ "$output" = "--workers 4" ]
}

@test "a missing memory.max file runs 4 workers" {
  job a
  rm -f "$GATE_MEMORY_MAX_FILE"
  run bash "$GATE_SH"
  [ "$status" -eq 0 ]
  grep -q -- '--workers 4' "$NEJ_ARGV"
}

@test "5 GiB runs 1 worker" {
  run workers_for 5368709120
  [ "$output" = "--workers 1" ]
}

@test "12 GiB runs 2 workers" {
  run workers_for 12884901888
  [ "$output" = "--workers 2" ]
}

@test "a limit below one worker's share still runs 1 worker" {
  run workers_for 1073741824
  [ "$output" = "--workers 1" ]
}

@test "a memory.max with no trailing newline is still read" {
  job a
  printf '%s' 12884901888 >"$GATE_MEMORY_MAX_FILE"
  run bash "$GATE_SH"
  [ "$status" -eq 0 ]
  grep -q -- '--workers 2' "$NEJ_ARGV"
}

@test "an empty memory.max runs 4 workers" {
  job a
  : >"$GATE_MEMORY_MAX_FILE"
  run bash "$GATE_SH"
  [ "$status" -eq 0 ]
  grep -q -- '--workers 4' "$NEJ_ARGV"
}

@test "64 GiB clamps to 4 workers" {
  run workers_for 68719476736
  [ "$output" = "--workers 4" ]
}

@test "workers get a memory ceiling above the single-worker peak" {
  job a
  bash "$GATE_SH" 2>"$BATS_TEST_TMPDIR/gate.err"
  mib=$(grep -o -- '--max-memory-size [0-9]*' "$NEJ_ARGV" | cut -d' ' -f2)
  [ "$mib" -ge 10240 ]
}

@test "all jobs passing exits 0 and builds each drv's outputs" {
  job a
  job b
  run bash "$GATE_SH"
  [ "$status" -eq 0 ]
  [[ "$output" == *"all 2 jobs passed"* ]]
  grep -q -- '/nix/store/a.drv^\*' "$NIX_ARGV"
  grep -q -- '/nix/store/b.drv^\*' "$NIX_ARGV"
  grep -q -- '--keep-going' "$NIX_ARGV"
}

@test "a job that fails to evaluate fails the gate and names the job" {
  job good
  printf '{"attr":"broken","attrPath":["broken"],"error":"boom: cannot evaluate"}\n' >>"$NEJ_JOBS"
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"eval failed: broken"* ]]
  [[ "$output" == *"boom: cannot evaluate"* ]]
  [[ "$output" != *"failed: good"* ]]
}

@test "a job that fails to build fails the gate and names only that job" {
  job good
  job bad
  STUB_NIX_STATUS=1
  STUB_INVALID=/nix/store/bad-out
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"build failed: bad"* ]]
  [[ "$output" != *"failed: good"* ]]
}

@test "a build failure with no identifiable invalid output still fails the gate" {
  job a
  STUB_NIX_STATUS=1
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"build failed: a"* ]]
}

@test "a batch abort re-builds each invalid job alone and names only the real failure" {
  job a
  job b
  job c
  STUB_NIX_STATUS=1
  STUB_INVALID=$'/nix/store/a-out\n/nix/store/b-out\n/nix/store/c-out'
  STUB_FAIL_DRVS=/nix/store/b.drv
  STUB_NIX_STDERR="error: fchmodat2 \"/nix/store/x-go-modules\": Operation not permitted"
  export STUB_FAIL_DRVS STUB_NIX_STDERR
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"1 job(s) failed: b"* ]]
  [[ "$output" != *"failed: a"* ]]
  [[ "$output" != *"failed: c"* ]]
  [[ "$output" == *"fchmodat2 \"/nix/store/x-go-modules\""* ]]
  for j in a b c; do
    grep -qxF -- "build --no-link --keep-going -L /nix/store/$j.drv^*" "$NIX_ARGV"
  done
}

@test "a builder failure named in the batch log is reported without a second build" {
  job bad
  job other
  job fine
  STUB_NIX_STATUS=1
  STUB_INVALID=$'/nix/store/bad-out\n/nix/store/other-out'
  STUB_FAIL_DRVS=/nix/store/other.drv
  STUB_NIX_STDERR="error: builder for '/nix/store/bad.drv' failed with exit code 1"
  export STUB_FAIL_DRVS STUB_NIX_STDERR
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"2 job(s) failed: bad other"* ]]
  [[ "$output" == *"build failed: bad: error: builder for '/nix/store/bad.drv' failed"* ]]
  [ "$(grep -c -- '/nix/store/bad.drv^\*' "$NIX_ARGV")" -eq 1 ]
  [ "$(grep -c -- '/nix/store/other.drv^\*' "$NIX_ARGV")" -eq 2 ]
  [ "$(grep -c -- '/nix/store/fine.drv^\*' "$NIX_ARGV")" -eq 1 ]
}

@test "the newer Cannot build form also counts as already failed" {
  job bad
  job other
  STUB_NIX_STATUS=1
  STUB_INVALID=$'/nix/store/bad-out\n/nix/store/other-out'
  STUB_FAIL_DRVS=""
  STUB_NIX_STDERR="error: Cannot build '/nix/store/bad.drv'."
  export STUB_FAIL_DRVS STUB_NIX_STDERR
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"1 job(s) failed: bad"* ]]
  [ "$(grep -c -- '/nix/store/bad.drv^\*' "$NIX_ARGV")" -eq 1 ]
}

@test "a failed batch whose lone re-builds all succeed still fails the gate" {
  job a
  job b
  STUB_NIX_STATUS=1
  STUB_INVALID=/nix/store/a-out
  STUB_FAIL_DRVS=""
  export STUB_FAIL_DRVS
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"1 job(s) failed: nix build (batch)"* ]]
  [[ "$output" != *"build failed: a"* ]]
  [[ "$output" != *"build failed: b"* ]]
}

@test "a single-job batch is not mistaken for a lone rebuild" {
  job a
  STUB_NIX_STATUS=1
  STUB_INVALID=/nix/store/a-out
  STUB_FAIL_DRVS=/nix/store/other.drv
  export STUB_FAIL_DRVS
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"1 job(s) failed: nix build (batch)"* ]]
  [ "$(grep -c -- '/nix/store/a.drv^\*' "$NIX_ARGV")" -eq 2 ]
}

@test "GATE_INSTALLABLE is required" {
  unset GATE_INSTALLABLE
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *GATE_INSTALLABLE* ]]
}

@test "a job with several invalid outputs is reported once" {
  printf '{"attr":"multi","attrPath":["multi"],"drvPath":"/nix/store/multi.drv","outputs":{"out":"/nix/store/multi-out","dev":"/nix/store/multi-dev"}}\n' >>"$NEJ_JOBS"
  STUB_NIX_STATUS=1
  STUB_INVALID=$'/nix/store/multi-out\n/nix/store/multi-dev'
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"1 job(s) failed: multi"* ]]
}

@test "nix-eval-jobs exiting non-zero fails the gate even after emitting jobs" {
  job a
  export STUB_NEJ_STATUS=137
  run bash "$GATE_SH"
  [ "$status" -ne 0 ]
  [[ "$output" == *"nix-eval-jobs exited 137"* ]]
  [[ "$output" != *"all 1 jobs passed"* ]]
}

@test "jobs are built as they are evaluated, each exactly once" {
  job a
  job b
  job c
  export STUB_NEJ_HANDSHAKE=1
  run bash "$GATE_SH"
  [ "$status" -eq 0 ]
  [[ "$output" == *"all 3 jobs passed"* ]]
  [ "$(wc -l <"$NIX_ARGV")" -gt 1 ]
  for j in a b c; do
    [ "$(grep -o -- "/nix/store/$j.drv^\*" "$NIX_ARGV" | wc -l)" -eq 1 ]
  done
}

# The liveness probe must be sampled before the line count. Reversed, an eval
# that writes its last line and exits between the two reads reports "dead" with
# that line unread, and the gate drops the job. A wc shim lands exactly that
# write+exit between the probe and the count.
@test "a job written just before eval exits is still built" {
  job a
  job b
  local fifo="$BATS_TEST_TMPDIR/release" realwc
  realwc=$(command -v wc)
  mkfifo "$fifo"
  export FIFO="$fifo" EVAL_PID_FILE="$BATS_TEST_TMPDIR/eval.pid" \
    WC_MARK="$BATS_TEST_TMPDIR/wc.done" REAL_WC="$realwc"

  cat >"$STUBS/nix-eval-jobs" <<'STUB'
#!/usr/bin/env bash
echo $$ >"$EVAL_PID_FILE"
sed -n 1p "$NEJ_JOBS"
read -r <"$FIFO"
sed -n 2p "$NEJ_JOBS"
STUB
  mkdir "$BATS_TEST_TMPDIR/wcdir"
  cat >"$BATS_TEST_TMPDIR/wcdir/wc" <<'STUB'
#!/usr/bin/env bash
if [ ! -e "$WC_MARK" ] && grep -qsF -- '/nix/store/a.drv' "$NIX_ARGV"; then
  : >"$WC_MARK"
  stale=$("$REAL_WC" "$@")
  echo >"$FIFO"
  for _ in $(seq 200); do
    if ! kill -0 "$(cat "$EVAL_PID_FILE")" 2>/dev/null; then
      printf '%s\n' "$stale"
      exit 0
    fi
    sleep 0.05
  done
  exit 1
fi
exec "$REAL_WC" "$@"
STUB
  sed -i "1s|.*|#!$BASH|" "$STUBS/nix-eval-jobs" "$BATS_TEST_TMPDIR/wcdir/wc"
  chmod +x "$STUBS/nix-eval-jobs" "$BATS_TEST_TMPDIR/wcdir/wc"

  PATH="$BATS_TEST_TMPDIR/wcdir:$PATH" run timeout 30 bash "$GATE_SH"
  [ "$status" -eq 0 ]
  [ -e "$WC_MARK" ]
  [[ "$output" == *"all 2 jobs passed"* ]]
  grep -qF -- '/nix/store/b.drv^*' "$NIX_ARGV"
}
