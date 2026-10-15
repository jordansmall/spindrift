#!/usr/bin/env bats
# bench.sh judge: blind LLM scoring of judging/*.patch. `claude` is faked
# (BENCH_CLAUDE), so these tests spend nothing. Model IDs are sample data, not
# the default model: leave them literal when the default model is bumped.

bench() { bash "${BENCH_SH:-$BATS_TEST_DIRNAME/../bench.sh}" "$@"; }

git_q() { git -c user.name=t -c user.email=t@t "$@"; }

setup() {
  T="$BATS_TEST_TMPDIR"

  git init -q -b main "$T/src"
  echo broken >"$T/src/value.txt"
  git_q -C "$T/src" add -A
  git_q -C "$T/src" commit -qm base
  BASE=$(git -C "$T/src" rev-parse HEAD)
  echo REFERENCE_FIX_LINE >"$T/src/value.txt"
  git_q -C "$T/src" commit -qam fix
  FIX=$(git -C "$T/src" rev-parse HEAD)

  mkdir -p "$T/tasks/t1"
  cat >"$T/tasks/t1/task.env" <<EOF
BASE=$BASE
FIX=$FIX
TEST_PATHS=""
GRADE_CMD="true"
CHECK_CMD=""
EOF
  printf -- '---\ntitle: Fix value\n---\nISSUE_BODY_MARKER value.txt should say fixed.\n' >"$T/tasks/t1/issue.md"

  J="$T/out/judging"
  mkdir -p "$J"
  cp "$T/tasks/t1/issue.md" "$J/ISSUE.md"
  printf 'aaaaaa\tcontrol-arm\t1\nbbbbbb\tvariant-arm\t1\n' >"$J/KEY.tsv"
  printf -- '--- a/value.txt\n+++ b/value.txt\n-broken\n+CANDIDATE_A_MARKER\n' >"$J/aaaaaa.patch"
  printf -- '--- a/value.txt\n+++ b/value.txt\n-broken\n+CANDIDATE_B_MARKER\n' >"$J/bbbbbb.patch"

  # Each call leaves its stdin as calls/<n>.prompt and its argv as
  # calls/<n>.args. FAKE_JUDGE picks the reply: ok, garbage, range, or flaky
  # (garbage on the first call, ok after).
  mkdir -p "$T/bin" "$T/calls"
  printf '#!%s\n' "$BASH" >"$T/bin/claude"
  cat >>"$T/bin/claude" <<'FAKE'
set -euo pipefail
f=$(mktemp "$FAKE_CALLS/call.XXXXXX")
cat >"$f.prompt"
printf '%s\n' "$@" >"$f.args"
rm -f "$f"
n=$(find "$FAKE_CALLS" -name '*.prompt' | wc -l)
mode="${FAKE_JUDGE:-ok}"
[ "$mode" = flaky ] && { if [ "$n" -eq 1 ]; then mode=garbage; else mode=ok; fi; }
case "$mode" in
  garbage) echo 'I think this patch is pretty good!'; exit 0 ;;
  range) inner='{"correctness":4,"tests":3,"scope":5,"quality":2,"overall":11,"rationale":"too high"}' ;;
  *) inner=$(printf '```json\n{"correctness":4,"tests":3,"scope":5,"quality":2,"overall":7,"rationale":"solid\\tfix\\nwith tests"}\n```') ;;
esac
jq -cn --arg r "$inner" '{type:"result",subtype:"success",is_error:false,result:$r}'
FAKE
  chmod +x "$T/bin/claude"

  export FAKE_CALLS="$T/calls"
  export BENCH_CLAUDE="$T/bin/claude" BENCH_SRC="$T/src" BENCH_TASKS_DIR="$T/tasks" \
    BENCH_OUT="$T/out"
}

calls() { find "$T/calls" -name '*.prompt' | wc -l; }
scores() { tail -n +2 "$J/SCORES.tsv"; }
row() { awk -F'\t' -v id="$1" '$1 == id' "$J/SCORES.tsv"; }

@test "judge: the prompt carries the issue, the candidate and the labelled reference fix" {
  run bench judge t1
  [ "$status" -eq 0 ]
  [ "$(calls)" -eq 2 ]
  local p
  p=$(grep -l CANDIDATE_A_MARKER "$T/calls"/*.prompt)
  grep -q ISSUE_BODY_MARKER "$p"
  grep -q REFERENCE_FIX_LINE "$p"
  grep -qi 'reference fix that was merged' "$p"
  grep -qi 'must not be penalised' "$p"
  [ "$(grep -c CANDIDATE_B_MARKER "$p")" -eq 0 ]
  grep -q '"correctness"' "$p"
}

@test "judge: the prompt never names an arm from KEY.tsv" {
  run bench judge t1
  [ "$status" -eq 0 ]
  [ "$(calls)" -eq 2 ]
  [ "$(cat "$T/calls"/*.prompt "$T/calls"/*.args | grep -c -e control-arm -e variant-arm)" -eq 0 ]
}

@test "judge: scores land in SCORES.tsv under the exact header" {
  run bench judge t1
  [ "$status" -eq 0 ]
  [ "$(head -1 "$J/SCORES.tsv")" = $'blind\tcorrectness\ttests\tscope\tquality\toverall\tjudge_model\tjudge_effort\trationale' ]
  [ "$(scores | wc -l)" -eq 2 ]
  [ "$(row aaaaaa)" = $'aaaaaa\t4\t3\t5\t2\t7\tclaude-opus-5-5\thigh\tsolid fix with tests' ]
  [ -n "$(row bbbbbb)" ]
  awk -F'\t' 'NF != 9 { exit 1 }' "$J/SCORES.tsv"
  [[ "$output" == *"scored 2, skipped 0, errors 0"* ]]
}

@test "judge: a rerun scores only ids with no row yet" {
  printf 'blind\tcorrectness\ttests\tscope\tquality\toverall\tjudge_model\tjudge_effort\trationale\naaaaaa\t5\t5\t5\t5\t10\tm\te\tkept\n' >"$J/SCORES.tsv"
  run bench judge t1
  [ "$status" -eq 0 ]
  [ "$(calls)" -eq 1 ]
  grep -q CANDIDATE_B_MARKER "$T/calls"/*.prompt
  [ "$(row aaaaaa)" = $'aaaaaa\t5\t5\t5\t5\t10\tm\te\tkept' ]
  [[ "$output" == *"scored 1, skipped 1, errors 0"* ]]
  run bench judge t1
  [ "$(calls)" -eq 1 ]
  [ "$(scores | wc -l)" -eq 2 ]
}

@test "judge: an empty patch scores zero without calling the model" {
  : >"$J/cccccc.patch"
  run bench judge t1
  [ "$status" -eq 0 ]
  [ "$(calls)" -eq 2 ]
  [ "$(row cccccc | cut -f2-6)" = $'0\t0\t0\t0\t0' ]
  [ "$(row cccccc | cut -f9)" = "empty diff" ]
}

@test "judge: garbage output is retried once, then recorded as judge-error" {
  rm "$J/bbbbbb.patch"
  FAKE_JUDGE=garbage run bench judge t1
  [ "$status" -eq 0 ]
  [ "$(calls)" -eq 2 ]
  [ "$(row aaaaaa | cut -f2-6)" = $'\t\t\t\t' ]
  [ "$(row aaaaaa | cut -f9)" = judge-error ]
  [[ "$output" == *aaaaaa* ]]
  [[ "$output" == *"errors 1"* ]]
}

@test "judge: a garbage first reply recovers on the retry" {
  rm "$J/bbbbbb.patch"
  FAKE_JUDGE=flaky run bench judge t1
  [ "$status" -eq 0 ]
  [ "$(calls)" -eq 2 ]
  [ "$(row aaaaaa | cut -f2-6)" = $'4\t3\t5\t2\t7' ]
}

@test "judge: an out-of-range score is rejected like garbage" {
  rm "$J/bbbbbb.patch"
  FAKE_JUDGE=range run bench judge t1
  [ "$status" -eq 0 ]
  [ "$(calls)" -eq 2 ]
  [ "$(row aaaaaa | cut -f9)" = judge-error ]
}

@test "judge: runs claude in print mode with no tools, JSON output and the model and effort overrides" {
  BENCH_JUDGE_MODEL=claude-sonnet-5 BENCH_JUDGE_EFFORT=max run bench judge t1
  [ "$status" -eq 0 ]
  local a
  for a in "$T/calls"/*.args; do
    grep -qx -- -p "$a"
    grep -A1 -x -- --model "$a" | grep -qx claude-sonnet-5
    grep -A1 -x -- --effort "$a" | grep -qx max
    grep -A1 -x -- --output-format "$a" | grep -qx json
    [ "$(awk 'p { print "[" $0 "]"; exit } $0 == "--tools" { p = 1 }' "$a")" = "[]" ]
  done
  [ "$(row aaaaaa | cut -f7-8)" = $'claude-sonnet-5\tmax' ]
}

@test "judge: BENCH_JUDGE_JOBS calls run side by side without tearing rows" {
  local i
  for i in 1 2 3 4 5 6 7 8; do
    printf -- '+CANDIDATE_%s\n' "$i" >"$J/00000$i.patch"
  done
  BENCH_JUDGE_JOBS=3 run bench judge t1
  [ "$status" -eq 0 ]
  [ "$(calls)" -eq 10 ]
  [ "$(scores | wc -l)" -eq 10 ]
  awk -F'\t' 'NF != 9 { exit 1 }' "$J/SCORES.tsv"
  [ "$(scores | cut -f1 | sort -u | wc -l)" -eq 10 ]
}
