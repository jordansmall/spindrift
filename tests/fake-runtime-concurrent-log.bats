#!/usr/bin/env bats
# Covers the shared-log append in tests/fakes/runtime. A launcher dispatching
# several Boxes at once (MAX_JOBS=0) runs concurrent `podman run` fakes that
# all append to one $PODMAN_LOG, and a real `run` argv is well past bash's
# stdio buffer: the printf builtin flushes such a line as several write()s, so
# concurrent appenders interleave mid-line and the line-count assertions on that
# log (tests/run-image-build.bats) lose a record. Each invocation here carries a
# line far over that buffer and the test asserts none are torn.
# Invoked directly rather than through tests/helper.bash: the contract under
# test is the fake's own log-write behaviour.

setup() {
  RUNTIME_FAKE="${FAKES_DIR:-$BATS_TEST_DIRNAME/fakes}/runtime"
  [ -f "$RUNTIME_FAKE" ]
  mkdir -p "$BATS_TEST_TMPDIR/bin"
  cp "$RUNTIME_FAKE" "$BATS_TEST_TMPDIR/bin/podman"
  export PODMAN_LOG="$BATS_TEST_TMPDIR/podman.log"
}

@test "concurrent podman invocations with oversized argv each land as one intact log line" {
  local pad rounds=40 jobs=16 round k
  pad="$(head -c 100000 /dev/zero | tr '\0' x)"
  for ((round = 0; round < rounds; round++)); do
    : >"$PODMAN_LOG"
    for ((k = 0; k < jobs; k++)); do
      bash "$BATS_TEST_TMPDIR/bin/podman" run "-e" "PAD=$pad" "ARG=$k" &
    done
    wait
    [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq "$jobs" ]
    [ "$(wc -l <"$PODMAN_LOG")" -eq "$jobs" ]
  done
}
