#!/usr/bin/env bats
# bench.sh analyze: pools per-task metrics and judge scores by factor level.
# Fixture numbers are chosen so every expected value below is hand-checkable:
#
#   alpha (judged)              cost  min  resolved  overall
#     coh-shm-wsh-roh r1         4    10   1         4.5
#     coh-shm-wsh-roh r2         6    20   1         3.5
#     coh-shm-whl-roh r1         2    10   1         4.0
#     coh-shm-whl-roh r2         2    10   0         (judge error)
#     csm-shm-whl-rsm r1         1     5   0         2.0
#     coh-shh-wsm-roh r1         3    11   1         3.2
#     control r1 (unparseable) 100   100   1         5.0
#   → task means over parseable rows: cost 3.00, minutes 11.0
#
#   beta (no SCORES.tsv)        cost  min  resolved
#     coh-shm-wsh-roh r1         6    20   1
#     coh-shm-whl-roh r1         3    10   1
#     csm-shm-whl-rsm r1         3    10   0
#   → task means: cost 4.00, minutes 13.3

bench() { bash "${BENCH_SH:-$BATS_TEST_DIRNAME/../bench.sh}" "$@"; }

HEADER=$'task\tarm\trep\tblind\toutcome\tresolved\tgrade\tchecks\tcost_usd\tin_tok\tout_tok\tcache_read\tcache_create\tturns\tduration_ms\tpasses\tverdicts\tdecision\tharness\tspec'
SCORES_HEADER=$'blind\tcorrectness\ttests\tscope\tquality\toverall\tjudge_model\tjudge_effort\trationale'

# metric TASK ARM REP BLIND RESOLVED COST MINUTES
metric() {
  local grade=fail
  [ "$5" = 1 ] && grade=pass
  printf '%s\t%s\t%s\t%s\tready\t%s\t%s\tskip\t%s\t1\t1\t1\t1\t1\t%s\t1\t\t\tdeadbeef\tspec\n' \
    "$1" "$2" "$3" "$4" "$5" "$grade" "$6" "$(($7 * 60000))"
}

setup() {
  T="$BATS_TEST_TMPDIR"
  export BENCH_OUT_ROOT="$T/results" BENCH_ANALYSIS_OUT="$T/analysis.md"
  mkdir -p "$T/results/alpha/judging" "$T/results/beta"
  {
    echo "$HEADER"
    metric alpha coh-shm-wsh-roh 1 a1 1 4 10
    metric alpha coh-shm-wsh-roh 2 a2 1 6 20
    metric alpha coh-shm-whl-roh 1 a3 1 2 10
    metric alpha coh-shm-whl-roh 2 a4 0 2 10
    metric alpha csm-shm-whl-rsm 1 a5 0 1 5
    metric alpha coh-shh-wsm-roh 1 a7 1 3 11
    metric alpha control 1 a6 1 100 100
  } >"$T/results/alpha/metrics.tsv"
  {
    echo "$SCORES_HEADER"
    printf 'a1\t5\t4\t5\t4\t4.5\topus\thigh\tgood\n'
    printf 'a2\t4\t3\t4\t3\t3.5\topus\thigh\tok\n'
    printf 'a3\t4\t4\t4\t4\t4.0\topus\thigh\tok\n'
    printf 'a4\t\t\t\t\t\topus\thigh\tjudge-error: timed out\n'
    printf 'a5\t2\t2\t2\t2\t2.0\topus\thigh\tweak\n'
    printf 'a6\t5\t5\t5\t5\t5.0\topus\thigh\tperfect\n'
    printf 'a7\t3\t3\t4\t3\t3.2\topus\thigh\tfine\n'
  } >"$T/results/alpha/judging/SCORES.tsv"
  {
    echo "$HEADER"
    metric beta coh-shm-wsh-roh 1 b1 1 6 20
    metric beta coh-shm-whl-roh 1 b2 1 3 10
    metric beta csm-shm-whl-rsm 1 b3 0 3 10
  } >"$T/results/beta/metrics.tsv"
}

analyze() {
  run bench analyze alpha beta
  [ "$status" -eq 0 ] || { echo "$output"; return 1; }
}

has() { grep -qxF -- "$1" "$T/analysis.md" || { echo "missing line: $1"; cat "$T/analysis.md"; return 1; }; }

@test "analyze: writes the report and prints its path" {
  analyze
  [ "$output" = "$T/analysis.md" ]
  [ -s "$T/analysis.md" ]
}

@test "analyze: with no task it prints usage and fails" {
  run bench analyze
  [ "$status" -ne 0 ]
  [[ "$output" == *"usage: bench.sh analyze TASK..."* ]]
}

@test "analyze: a task with no metrics.tsv is an error" {
  run bench analyze alpha gamma
  [ "$status" -ne 0 ]
  [[ "$output" == *"$T/results/gamma/metrics.tsv"* ]]
}

@test "analyze: per-task overview excludes unparseable arms and blanks a task with no scores" {
  analyze
  has '| task | runs | resolved | judge mean | judged | cost $ mean | minutes mean |'
  has '| alpha | 6 | 67% | 3.4 | 5 | 3.00 | 11.0 |'
  has '| beta | 3 | 67%† | – | 0 | 4.00† | 13.3† |'
}

@test "analyze: notes the excluded unparseable runs and the task without scores" {
  analyze
  # shellcheck disable=SC2016 # the backticks are literal markdown
  grep -qF 'Excluded 1 run(s) whose arm does not parse as `c<M><E>-s<M><E>-w<M><E>-r<M><E>`: control.' "$T/analysis.md"
  grep -qF 'beta has no judging/SCORES.tsv' "$T/analysis.md"
}

@test "analyze: explains the † mark and the ranking formula" {
  analyze
  grep -qF '† marks a statistic resting on fewer than 4 runs' "$T/analysis.md"
  grep -qF 'value = mean judge overall ÷ mean cost ratio' "$T/analysis.md"
}

@test "analyze: main effects pool task-normalised ratios and cost per resolved run" {
  analyze
  has '| factor | level | n | resolved | judge mean ± sd | judged | cost ratio | minutes ratio | cost $ mean | cost $ / resolved |'
  has '| coordinator model | opus-5-5 | 7 | 86% | 3.8 ± 0.6 | 4 | 1.13 | 1.11 | 3.71 | 4.33 |'
  has '| coordinator model | sonnet-5-5 | 2 | 0%† | 2.0 ± –† | 1 | 0.54† | 0.60† | 2.00† | – |'
}

@test "analyze: a judge-error row counts as unjudged, not as a zero score" {
  analyze
  has '| worker model | sonnet-5-5 | 4 | 100% | 3.7 ± 0.7† | 3 | 1.46 | 1.31 | 4.75 | 4.75 |'
  has '| worker model | haiku-5-5 | 5 | 40% | 3.0 ± 1.4† | 2 | 0.63 | 0.75 | 2.20 | 5.50 |'
}

@test "analyze: every factor has its own rows, levels in canonical order" {
  analyze
  local factors
  factors="$(grep -oE '^\| (coordinator|scout|worker|reviewer) (model|effort) \| [a-z0-9-]+ ' "$T/analysis.md" | sed 's/^| //; s/ *$//')"
  [ "$factors" = "$(printf '%s\n' \
    'coordinator model | opus-5-5' 'coordinator model | sonnet-5-5' \
    'coordinator effort | medium' 'coordinator effort | high' \
    'scout model | haiku-5-5' \
    'scout effort | medium' 'scout effort | high' \
    'worker model | sonnet-5-5' 'worker model | haiku-5-5' \
    'worker effort | low' 'worker effort | medium' 'worker effort | high' \
    'reviewer model | opus-5-5' 'reviewer model | sonnet-5-5' \
    'reviewer effort | medium' 'reviewer effort | high')" ]
}

@test "analyze: a model x effort grid per role" {
  analyze
  grep -qxF '### worker' "$T/analysis.md"
  has '| model | low | medium | high |'
  has '| sonnet-5-5 |  | 3.2† / 1.00† (1) | 4.0† / 1.61† (3) |'
  has '| haiku-5-5 | 3.0† / 0.63 (5) |  |  |'
}

@test "analyze: arm ranking drops arms too far below the best resolved rate" {
  analyze
  has '| rank | arm | coordinator | scout | worker | reviewer | runs | resolved | judge | cost ratio | value |'
  local ranked
  ranked="$(sed -n '/^## Arm ranking/,/^## /p' "$T/analysis.md" | grep -E '^\| [0-9]+ \|')"
  [ "$ranked" = "$(printf '%s\n' \
    '| 1 | coh-shh-wsm-roh | opus-5-5/high | haiku-5-5/high | sonnet-5-5/medium | opus-5-5/high | 1 | 100%† | 3.2† | 1.00† | 3.20† |' \
    '| 2 | coh-shm-wsh-roh | opus-5-5/high | haiku-5-5/medium | sonnet-5-5/high | opus-5-5/high | 3 | 100%† | 4.0† | 1.61† | 2.48† |')" ]
  grep -qF 'Left out (resolved below 75%): coh-shm-whl-roh (67%), csm-shm-whl-rsm (0%).' "$T/analysis.md"
}

@test "analyze: the bottom of the ranking never repeats an arm from the top 10" {
  analyze
  sed -n '/^### Bottom 5/,/^## /p' "$T/analysis.md" | grep -qF 'No arms beyond the top 10.'
}

@test "analyze: the Pareto frontier keeps only undominated ranked arms, cheapest first" {
  analyze
  has '| arm | resolved | cost ratio | judge | runs |'
  local front
  front="$(sed -n '/^## Pareto frontier/,$p' "$T/analysis.md" | grep -E '^\| c[a-z]{2}-')"
  # Cheaper and better-judged arms that resolve too rarely to rank are left
  # out, so the frontier never offers an arm that fails its tests.
  [ "$front" = "$(printf '%s\n' \
    '| coh-shh-wsm-roh | 100%† | 1.00† | 3.2† | 1 |' \
    '| coh-shm-wsh-roh | 100%† | 1.61† | 4.0† | 3 |')" ]
}

@test "analyze: with 13 eligible arms the bottom 5 shows only ranks 11 to 13" {
  # Equal costs make every cost ratio 1.00, so value is the judge score: arm i
  # scores i/10 and ranks 14 - i.
  local -a arms=(col com coh cox csl csm csh csx chl chm chh chx)
  local i arm top bottom
  mkdir -p "$T/results/wide/judging"
  echo "$HEADER" >"$T/results/wide/metrics.tsv"
  echo "$SCORES_HEADER" >"$T/results/wide/judging/SCORES.tsv"
  for i in $(seq 1 13); do
    arm="${arms[i - 1]:-cox}-som-wsm-rom"
    [ "$i" -eq 13 ] && arm=cox-shh-wsm-rom
    metric wide "$arm" 1 "w$i" 1 1 1 >>"$T/results/wide/metrics.tsv"
    printf 'w%s\t3\t3\t3\t3\t%s.%s\topus\thigh\tok\n' "$i" "$((i / 10))" "$((i % 10))" \
      >>"$T/results/wide/judging/SCORES.tsv"
  done
  run bench analyze wide
  [ "$status" -eq 0 ]
  top="$(sed -n '/^### Top 10/,/^### Bottom 5/p' "$T/analysis.md" | grep -oE '^\| [0-9]+ \| [a-z-]+' | tr -d '|' | xargs)"
  bottom="$(sed -n '/^### Bottom 5/,/^## /p' "$T/analysis.md" | grep -oE '^\| [0-9]+ \| [a-z-]+' | tr -d '|' | xargs)"
  [ "$top" = "1 cox-shh-wsm-rom 2 chx-som-wsm-rom 3 chh-som-wsm-rom 4 chm-som-wsm-rom 5 chl-som-wsm-rom 6 csx-som-wsm-rom 7 csh-som-wsm-rom 8 csm-som-wsm-rom 9 csl-som-wsm-rom 10 cox-som-wsm-rom" ]
  [ "$bottom" = "11 coh-som-wsm-rom 12 com-som-wsm-rom 13 col-som-wsm-rom" ]
}

@test "analyze: leaves out runs the peek audit flagged, and says which" {
  awk -F'\t' -v OFS='\t' 'NR == 1 { print $0, "peek"; next } { print $0, 0 }' \
    "$T/results/alpha/metrics.tsv" >"$T/m"
  printf '%s\t1\n' "$(metric alpha coh-shm-wsh-roh 3 a8 1 90 90)" >>"$T/m"
  mv "$T/m" "$T/results/alpha/metrics.tsv"
  analyze
  has '| alpha | 6 | 67% | 3.4 | 5 | 3.00 | 11.0 |'
  grep -qF "Excluded 1 run(s) the peek audit flagged as reaching for another run's work: alpha coh-shm-wsh-roh r3." "$T/analysis.md"
}
