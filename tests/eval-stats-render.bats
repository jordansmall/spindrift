#!/usr/bin/env bats
# Issue #5860: eval-stats-render.sh turns two NIX_SHOW_STATS captures into the
# Markdown table a reviewer reads. It reports eval cost and never gates on it,
# so every content problem must still exit 0.

setup() {
  EVAL_STATS_RENDER_SH="${EVAL_STATS_RENDER_SH:-$BATS_TEST_DIRNAME/../.github/scripts/eval-stats-render.sh}"
  FX="$BATS_TEST_DIRNAME/testdata/eval-stats"
}

@test "a baseline renders the table with deltas and names the baseline commit" {
  run bash "$EVAL_STATS_RENDER_SH" "$FX/current.stats.json" "$FX/current.probe.json" \
    "$FX/baseline-big.stats.json" "$FX/baseline-big.probe.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"### Eval stats"* ]]
  [[ "$output" == *"Baseline: \`abc1234\`"* ]]
  [[ "$output" == *"| Metric | Current | Baseline | Δ |"* ]]
  [[ "$output" == *"| Function calls | 1000000 | 800000 | +25.0% |"* ]]
  [[ "$output" == *"| CPU time | 10.00s | 8.00s | +25.0% |"* ]]
  [[ "$output" == *"| Heap | 1024.0 MiB | 512.0 MiB | +100.0% |"* ]]
  [[ "$output" == *"| Marginal instance cost | 5000 | 4000 | +25.0% |"* ]]
  [[ "$output" != *"noise"* ]]
}

@test "a negative delta is signed" {
  run bash "$EVAL_STATS_RENDER_SH" "$FX/baseline-big.stats.json" "$FX/baseline-big.probe.json" \
    "$FX/current.stats.json" "$FX/current.probe.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"| Function calls | 800000 | 1000000 | -20.0% |"* ]]
}

@test "deltas under 1% are labelled noise" {
  run bash "$EVAL_STATS_RENDER_SH" "$FX/current.stats.json" "$FX/current.probe.json" \
    "$FX/baseline-noise.stats.json" "$FX/baseline-noise.probe.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"| Function calls | 1000000 | 1004000 | -0.4% (noise) |"* ]]
  [[ "$output" == *"| CPU time | 10.00s | 10.05s | -0.5% (noise) |"* ]]
  [[ "$output" == *"| Heap | 1024.0 MiB | 1026.2 MiB | -0.2% (noise) |"* ]]
  [[ "$output" == *"| Marginal instance cost | 5000 | 5010 | -0.2% (noise) |"* ]]
}

@test "no baseline arguments prints the current numbers and a no baseline line" {
  run bash "$EVAL_STATS_RENDER_SH" "$FX/current.stats.json" "$FX/current.probe.json"
  [ "$status" -eq 0 ]
  [[ "$output" == *"no baseline"* ]]
  [[ "$output" == *"| Function calls | 1000000 |"* ]]
  [[ "$output" == *"| Marginal instance cost | 5000 |"* ]]
}

@test "a bad baseline stats file prints no baseline; a bad baseline probe only blanks its marginal row" {
  printf 'not json' >"$BATS_TEST_TMPDIR/garbage.json"
  for bad in "$FX/empty.json" "$BATS_TEST_TMPDIR/absent.json" "$BATS_TEST_TMPDIR/garbage.json"; do
    run bash "$EVAL_STATS_RENDER_SH" "$FX/current.stats.json" "$FX/current.probe.json" \
      "$bad" "$FX/baseline-big.probe.json" abc1234
    [ "$status" -eq 0 ]
    [[ "$output" == *"no baseline"* ]]
    [[ "$output" == *"| Function calls | 1000000 |"* ]]

    run bash "$EVAL_STATS_RENDER_SH" "$FX/current.stats.json" "$FX/current.probe.json" \
      "$FX/baseline-big.stats.json" "$bad" abc1234
    [ "$status" -eq 0 ]
    [[ "$output" == *"Baseline: \`abc1234\`"* ]]
    [[ "$output" == *"| Function calls | 1000000 | 800000 | +25.0% |"* ]]
    [[ "$output" == *"| Marginal instance cost | 5000 | n/a | n/a |"* ]]
  done
}

@test "an empty baseline SHA prints no baseline and exits 0" {
  run bash "$EVAL_STATS_RENDER_SH" "$FX/current.stats.json" "$FX/current.probe.json" \
    "$FX/baseline-big.stats.json" "$FX/baseline-big.probe.json" ""
  [ "$status" -eq 0 ]
  [[ "$output" == *"no baseline"* ]]
}

@test "a missing field renders n/a rather than failing" {
  printf '{"nrFunctionCalls":5}\n' >"$BATS_TEST_TMPDIR/partial.json"
  run bash "$EVAL_STATS_RENDER_SH" "$BATS_TEST_TMPDIR/partial.json" "$FX/current.probe.json" \
    "$FX/baseline-big.stats.json" "$FX/baseline-big.probe.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"| CPU time | n/a | 8.00s | n/a |"* ]]
}

@test "a zero baseline value gives an n/a delta" {
  printf '{"nrFunctionCalls":0,"cpuTime":1.0,"gc":{"heapSize":1048576}}\n' >"$BATS_TEST_TMPDIR/zero.json"
  run bash "$EVAL_STATS_RENDER_SH" "$FX/current.stats.json" "$FX/current.probe.json" \
    "$BATS_TEST_TMPDIR/zero.json" "$FX/baseline-big.probe.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"| Function calls | 1000000 | 0 | n/a |"* ]]
}

@test "a non-object gc renders the Heap row n/a without shifting the other rows" {
  printf '{"nrFunctionCalls":800000,"cpuTime":8.0,"gc":"x"}\n' >"$BATS_TEST_TMPDIR/badgc.json"

  run bash "$EVAL_STATS_RENDER_SH" "$FX/current.stats.json" "$FX/current.probe.json" \
    "$BATS_TEST_TMPDIR/badgc.json" "$FX/baseline-big.probe.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"| Heap | 1024.0 MiB | n/a | n/a |"* ]]
  [[ "$output" == *"| Marginal instance cost | 5000 | 4000 | +25.0% |"* ]]

  run bash "$EVAL_STATS_RENDER_SH" "$BATS_TEST_TMPDIR/badgc.json" "$FX/current.probe.json" \
    "$FX/baseline-big.stats.json" "$FX/baseline-big.probe.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"| Heap | n/a | 512.0 MiB | n/a |"* ]]
  [[ "$output" == *"| Marginal instance cost | 5000 | 4000 | +25.0% |"* ]]
}

@test "missing current stats with a usable probe still render the marginal row" {
  run bash "$EVAL_STATS_RENDER_SH" "$FX/empty.json" "$FX/current.probe.json"
  [ "$status" -eq 0 ]
  [[ "$output" != *"unavailable"* ]]
  [[ "$output" == *"| Function calls | n/a |"* ]]
  [[ "$output" == *"| Marginal instance cost | 5000 |"* ]]

  run bash "$EVAL_STATS_RENDER_SH" "$BATS_TEST_TMPDIR/absent.json" "$FX/current.probe.json" \
    "$FX/baseline-big.stats.json" "$FX/baseline-big.probe.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"| Function calls | n/a | 800000 | n/a |"* ]]
  [[ "$output" == *"| Marginal instance cost | 5000 | 4000 | +25.0% |"* ]]
}

@test "current stats and probe both unusable print an unavailable line and exit 0" {
  run bash "$EVAL_STATS_RENDER_SH" "$FX/empty.json" "$FX/empty.json"
  [ "$status" -eq 0 ]
  [[ "$output" == *"unavailable"* ]]

  run bash "$EVAL_STATS_RENDER_SH" "$BATS_TEST_TMPDIR/absent.json" "$BATS_TEST_TMPDIR/absent.json" \
    "$FX/baseline-big.stats.json" "$FX/baseline-big.probe.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"unavailable"* ]]
}

@test "the noise label follows the rounded delta, so +0.96% is not called noise" {
  printf '{"nrFunctionCalls":1000000}\n' >"$BATS_TEST_TMPDIR/base.json"
  printf '{"nrFunctionCalls":1009600}\n' >"$BATS_TEST_TMPDIR/over.json"
  printf '{"nrFunctionCalls":1009400}\n' >"$BATS_TEST_TMPDIR/under.json"
  run bash "$EVAL_STATS_RENDER_SH" "$BATS_TEST_TMPDIR/over.json" "$FX/empty.json" \
    "$BATS_TEST_TMPDIR/base.json" "$FX/empty.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"| Function calls | 1009600 | 1000000 | +1.0% |"* ]]

  run bash "$EVAL_STATS_RENDER_SH" "$BATS_TEST_TMPDIR/under.json" "$FX/empty.json" \
    "$BATS_TEST_TMPDIR/base.json" "$FX/empty.json" abc1234
  [ "$status" -eq 0 ]
  [[ "$output" == *"| Function calls | 1009400 | 1000000 | +0.9% (noise) |"* ]]
}

@test "a missing current probe still renders the stats, marginal cost n/a" {
  run bash "$EVAL_STATS_RENDER_SH" "$FX/current.stats.json" "$FX/empty.json"
  [ "$status" -eq 0 ]
  [[ "$output" == *"| Function calls | 1000000 |"* ]]
  [[ "$output" == *"| Marginal instance cost | n/a |"* ]]
}

@test "a wrong argument count is a usage error" {
  run bash "$EVAL_STATS_RENDER_SH"
  [ "$status" -eq 2 ]
  [[ "$output" == *usage* ]]

  run bash "$EVAL_STATS_RENDER_SH" a b c
  [ "$status" -eq 2 ]
}
