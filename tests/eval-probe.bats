#!/usr/bin/env bats
# Issue #5860: eval-probe.sh reports the second harness instance's marginal
# function calls. A fake nix stands in for the two real evals, since
# NIX_SHOW_STATS is what the runner reads back.

setup() {
  EVAL_PROBE_SH="${EVAL_PROBE_SH:-$BATS_TEST_DIRNAME/../.github/scripts/eval-probe.sh}"
  FAKE_BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$FAKE_BIN"
  cat >"$FAKE_BIN/nix" <<'FAKE'
#!/bin/sh
echo "$*" >>"$NIX_LOG"
case "$*" in
  *builtins.currentSystem*) printf 'x86_64-linux' ;;
  *) [ -z "${FAKE_NIX_FAIL:-}" ] || { echo "error: eval failed" >&2; exit 1; }
     case "$*" in
       *evalProbe.one*) if [ -n "${FAKE_NIX_NO_CALLS:-}" ]; then printf '{}'; else printf '{"nrFunctionCalls": 1000}'; fi >"$NIX_SHOW_STATS_PATH" ;;
       *evalProbe.two*) printf '{"nrFunctionCalls": 1750}' >"$NIX_SHOW_STATS_PATH" ;;
     esac
     echo '[]' ;;
esac
FAKE
  chmod +x "$FAKE_BIN/nix"
  export NIX_LOG="$BATS_TEST_TMPDIR/nix.log"
  PATH="$FAKE_BIN:$PATH"
}

@test "reports both counts and the marginal as JSON" {
  run bash "$EVAL_PROBE_SH" .
  [ "$status" -eq 0 ]
  [ "$(jq -r .oneInstanceFunctionCalls <<<"$output")" = 1000 ]
  [ "$(jq -r .twoInstancesFunctionCalls <<<"$output")" = 1750 ]
  [ "$(jq -r .marginalFunctionCalls <<<"$output")" = 750 ]
}

@test "evaluates evalProbe.one and evalProbe.two of the given flake ref on the current system" {
  run bash "$EVAL_PROBE_SH" github:o/r
  [ "$status" -eq 0 ]
  grep -q 'github:o/r#legacyPackages.x86_64-linux.evalProbe.one' "$NIX_LOG"
  grep -q 'github:o/r#legacyPackages.x86_64-linux.evalProbe.two' "$NIX_LOG"
  [ "$(grep -c -- '--no-eval-cache.*evalProbe' "$NIX_LOG")" -eq 2 ]
}

@test "a failing eval exits non-zero" {
  FAKE_NIX_FAIL=1 run bash "$EVAL_PROBE_SH" .
  [ "$status" -ne 0 ]
}

@test "a stats file without nrFunctionCalls exits non-zero" {
  FAKE_NIX_NO_CALLS=1 run bash "$EVAL_PROBE_SH" .
  [ "$status" -ne 0 ]
}
