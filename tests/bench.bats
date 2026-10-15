#!/usr/bin/env bats
# bench.sh: frozen tasks, an arm matrix, repeated runs and hidden-test grading.
# `nix` is faked (BENCH_NIX) and every remote is a local bare repo, so these
# tests spend nothing. Model IDs are sample data, not the default model: leave
# them literal when the default model is bumped (issue #5141).

bench() { bash "${BENCH_SH:-$BATS_TEST_DIRNAME/../bench.sh}" "$@"; }

git_q() { git -c user.name=t -c user.email=t@t "$@"; }

setup() {
  T="$BATS_TEST_TMPDIR"
  export FAKE_LOG="$T/fake.log" FAKE_SEEN="$T/seen.log"

  # Source repo: BASE is broken, FIX repairs value.txt and adds the hidden test.
  git init -q -b main "$T/src"
  echo broken >"$T/src/value.txt"
  git_q -C "$T/src" add -A
  git_q -C "$T/src" commit -qm base
  BASE=$(git -C "$T/src" rev-parse HEAD)
  mkdir "$T/src/tests"
  echo 'grep -qx fixed value.txt' >"$T/src/tests/check.sh"
  echo fixed >"$T/src/value.txt"
  git_q -C "$T/src" add -A
  git_q -C "$T/src" commit -qm fix
  FIX=$(git -C "$T/src" rev-parse HEAD)

  # The bench remote starts out holding the answer, as a stale mirror would.
  git init -q --bare "$T/remote.git"
  git -C "$T/src" push -q "$T/remote.git" "$FIX:refs/heads/main" "$FIX:refs/heads/stale"

  mkdir -p "$T/tasks/t1"
  cat >"$T/tasks/t1/task.env" <<EOF
BASE=$BASE
FIX=$FIX
TEST_PATHS="tests/check.sh"
GRADE_CMD="bash tests/check.sh"
CHECK_CMD=""
EOF
  printf -- '---\ntitle: Fix value\nstate: ready-for-agent\n---\nvalue.txt should say fixed.\n' >"$T/tasks/t1/issue.md"

  printf '# name app flags...\ngood .#good\nbad .#bad --effort low\ncrash .#crash\n' >"$T/arms"

  mkdir -p "$T/bin" "$T/cwd"
  touch "$T/cwd/flake.nix"
  # The Nix build sandbox has no /usr/bin/env, so fakes get bash's own path.
  printf '#!%s\n' "$BASH" >"$T/bin/nix"
  cat >>"$T/bin/nix" <<'FAKE'
# Stands in for `nix run <app> -- build|dispatch ...`.
set -euo pipefail
app="$2"; shift 3
if [ "$1" = build ]; then echo "build $app" >>"$FAKE_LOG"; exit 0; fi
shift
echo "$app $*" >>"$FAKE_LOG"
args=("$@"); slug="${args[-1]}"
remote="$FAKE_REMOTE"
for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[i]}" in
    --branch-prefix) prefix="${args[i+1]}" ;;
    --base-branch) base="${args[i+1]}" ;;
    --local-dir) dir="${args[i+1]}" ;;
  esac
done
git ls-remote --heads "$remote" | awk '{printf "%s ", $2} END {print ""}' >>"$FAKE_SEEN"
[ -f "$dir/$slug.md" ] || { echo "issue not staged" >&2; exit 9; }
cwd=$PWD
mkdir -p .spindrift/logs
log=".spindrift/logs/issue-$slug.log"
[ "$app" = ".#crash" ] && { echo '==> box died' >"$log"; exit 1; }
work=$(mktemp -d)
git clone -q "$remote" "$work"
cd "$work"
git checkout -q -b "$prefix$slug" "origin/$base"
case "$app" in .#good) echo fixed >value.txt ;; *) echo wrong >value.txt ;; esac
git -c user.name=t -c user.email=t@t commit -qam work
# A read-only Box always leaves its bundle in the outbox; the host then
# relays it. fail: the relay push failed. fixpass: a CI fix pass repaired the
# branch after the handoff. none: nothing got out at all.
if [ "${FAKE_PUSH:-ok}" != none ]; then
  mkdir -p "$cwd/.spindrift/outbox/$slug"
  git bundle create "$cwd/.spindrift/outbox/$slug/seam.bundle" "origin/$base..$prefix$slug" 2>/dev/null
fi
case "${FAKE_PUSH:-ok}" in
  ok) git push -q origin HEAD ;;
  fixpass)
    echo fixed >value.txt
    git -c user.name=t -c user.email=t@t commit -qam fix-pass
    git push -q origin HEAD ;;
esac
cd "$cwd"
cat >"$log" <<EOF
{"type":"assistant","message":{"model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":1000,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
{"type":"result","total_cost_usd":0.25,"num_turns":3,"duration_ms":60000,"usage":{"input_tokens":1000,"output_tokens":100}}
SPINDRIFT_OUTCOME status=${FAKE_STATUS:-ready} landing=$prefix$slug
EOF
FAKE
  chmod +x "$T/bin/nix"

  export FAKE_REMOTE="file://$T/remote.git"
  export BENCH_NIX="$T/bin/nix" BENCH_SRC="$T/src" BENCH_TASKS_DIR="$T/tasks" \
    BENCH_ARMS_FILE="$T/arms" BENCH_REMOTE="file://$T/remote.git" \
    BENCH_PUSH_URL="file://$T/remote.git" BENCH_REPO_SLUG=acme/bench \
    BENCH_OUT="$T/out" BENCH_CONFIRM=1
}

rows() { tail -n +2 "$T/out/metrics.tsv"; }
col() { head -1 "$T/out/metrics.tsv" | tr '\t' '\n' | grep -nx "$1" | cut -d: -f1; }

@test "run: every arm runs REPS times and is graded by the hidden test" {
  cd "$T/cwd"
  BENCH_REPS=2 run bench run t1 good bad
  [ "$status" -eq 0 ]
  [ "$(rows | wc -l)" -eq 4 ]
  local a r g
  a=$(col arm) r=$(col resolved) g=$(col grade)
  [ "$(rows | awk -F'\t' -v a="$a" -v r="$r" -v g="$g" '$a=="good" && $r==1 && $g=="pass"' | wc -l)" -eq 2 ]
  [ "$(rows | awk -F'\t' -v a="$a" -v r="$r" -v g="$g" '$a=="bad" && $r==0 && $g=="fail"' | wc -l)" -eq 2 ]
  grep -F '| good | 2 | 2/2 |' "$T/out/summary.md"
  grep -F '| bad | 2 | 0/2 |' "$T/out/summary.md"
  awk -F'\t' 'NR == 1 { n = NF } NF != n { exit 1 }' "$T/out/metrics.tsv"
}

@test "run: each dispatch sees only main at BASE, never the fix or another run's branch" {
  cd "$T/cwd"
  BENCH_REPS=2 run bench run t1 good bad
  [ "$status" -eq 0 ]
  [ "$(wc -l <"$FAKE_SEEN")" -eq 4 ]
  [ "$(sort -u "$FAKE_SEEN")" = "refs/heads/main " ]
  [ "$(git ls-remote --heads "$T/remote.git" | awk '{print $2}')" = "refs/heads/main" ]
  [ "$(git ls-remote "$T/remote.git" refs/heads/main | cut -f1)" = "$BASE" ]
}

@test "run: dispatches read-only through the GitHub forge like dogfood, plus the arm's own flags" {
  cd "$T/cwd"
  BENCH_REPS=1 run bench run t1 good bad
  [ "$status" -eq 0 ]
  local good bad
  good=$(grep '^\.#good ' "$FAKE_LOG")
  bad=$(grep '^\.#bad ' "$FAKE_LOG")
  for f in "--no-build" "--yes" "--tracker local" "--forge-backend github" \
    "--base-branch main" "--merge-policy manual" "--repo-slug acme/bench"; do
    [[ "$good" == *" $f "* ]] || { echo "missing $f in: $good"; false; }
  done
  for f in --box-access --remote-url --max-fix-attempts --merge-poll-timeout; do
    [[ "$good" != *"$f"* ]] || { echo "unexpected $f in: $good"; false; }
  done
  [[ "$good" == *"--branch-prefix bench/good/r1/ "* ]]
  [[ "$good" == *" t1" ]]
  [[ "$bad" == *"--effort low"* ]]
  [[ "$good" != *"--effort"* ]]
  [ "$(grep -c '^build ' "$FAKE_LOG")" -eq 2 ]
}

@test "run: a later invocation appends, numbering reps after the existing ones" {
  cd "$T/cwd"
  BENCH_REPS=1 run bench run t1 good bad
  [ "$status" -eq 0 ]
  BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  [ "$(rows | wc -l)" -eq 3 ]
  rows | awk -F'\t' -v a="$(col arm)" -v p="$(col rep)" '$a=="good" {print $p}' | sort | tr '\n' ' ' | grep -x '1 2 '
  grep -F '| good | 2 | 2/2 |' "$T/out/summary.md"
}

@test "summary: the arms file's first arm is the baseline, and a changed arm spec is flagged" {
  cd "$T/cwd"
  BENCH_REPS=1 run bench run t1 bad good
  [ "$status" -eq 0 ]
  grep -F 'delta against good' "$T/out/summary.md"
  sed -i 's/^bad .*/bad .#bad --effort high/' "$T/arms"
  BENCH_REPS=1 run bench run t1 bad
  [ "$status" -eq 0 ]
  grep -F 'arm bad ran under more than one spec' "$T/out/summary.md"
  ! grep -qF 'arm good ran under more than one spec' "$T/out/summary.md" || false
}

@test "run: a crashed Box that pushes nothing grades nobranch, not resolved" {
  cd "$T/cwd"
  BENCH_REPS=1 run bench run t1 crash
  [ "$status" -eq 0 ]
  rows | awk -F'\t' -v g="$(col grade)" -v o="$(col outcome)" -v r="$(col resolved)" '{print $g, $o, $r}' | grep -x 'nobranch none 0'
}

@test "run: a branch the host failed to push is graded from the Box's outbox bundle" {
  cd "$T/cwd"
  FAKE_PUSH=fail BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  rows | awk -F'\t' -v g="$(col grade)" -v r="$(col resolved)" '{print $g, $r}' | grep -x 'pass 1'
  grep -q '^+fixed' "$T/out/runs/good/r1/diff.patch"
  [ -f "$T/out/runs/good/r1/seam.bundle" ]
  [ ! -e .spindrift/outbox/t1 ]
}

@test "run: the pushed branch outranks the outbox bundle, since fix passes land after the handoff" {
  cd "$T/cwd"
  FAKE_PUSH=fixpass BENCH_REPS=1 run bench run t1 bad
  [ "$status" -eq 0 ]
  rows | awk -F'\t' -v g="$(col grade)" -v r="$(col resolved)" '{print $g, $r}' | grep -x 'pass 1'
  [ -f "$T/out/runs/bad/r1/seam.bundle" ]
  [ ! -e .spindrift/outbox/t1 ]
}

@test "run: an outbox bundle left by an interrupted attempt is never graded" {
  cd "$T/cwd"
  FAKE_PUSH=fail BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  head -1 "$T/out/metrics.tsv" >"$T/m" && mv "$T/m" "$T/out/metrics.tsv"
  mkdir -p .spindrift/outbox/t1
  cp "$T/out/runs/good/r1/seam.bundle" .spindrift/outbox/t1/
  FAKE_PUSH=none BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  rows | awk -F'\t' -v p="$(col rep)" -v g="$(col grade)" '{print $p, $g}' | grep -x '1 nobranch'
  [ ! -s "$T/out/runs/good/r1/diff.patch" ]
}

@test "run: diffs are staged under blind ids with a separate key" {
  cd "$T/cwd"
  BENCH_REPS=2 run bench run t1 good bad
  [ "$status" -eq 0 ]
  [ "$(find "$T/out/judging" -name '*.patch' | wc -l)" -eq 4 ]
  [ -z "$(find "$T/out/judging" -name '*good*' -o -name '*bad*')" ]
  [ "$(wc -l <"$T/out/judging/KEY.tsv")" -eq 4 ]
  [ -f "$T/out/judging/ISSUE.md" ]
}

@test "run: an arm the arms file lacks is an error before anything is dispatched" {
  cd "$T/cwd"
  run bench run t1 nosuch
  [ "$status" -ne 0 ]
  [[ "$output" == *"nosuch"* ]]
  [ ! -e "$FAKE_LOG" ]
}

@test "run: an arm named twice is an error before anything is dispatched" {
  cd "$T/cwd"
  run bench run t1 good good
  [ "$status" -ne 0 ]
  [[ "$output" == *"good"*"twice"* ]]
  [ ! -e "$FAKE_LOG" ]
}

@test "run: a rep left half-written by an interrupt is cleared and redone" {
  cd "$T/cwd"
  BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  # Interrupted after the key row but before the metrics row, with the
  # grading worktree still registered.
  git -C "$T/out/work.git" worktree add -q --detach "$T/out/runs/good/r1/tree" HEAD
  head -1 "$T/out/metrics.tsv" >"$T/m" && mv "$T/m" "$T/out/metrics.tsv"
  BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  rows | awk -F'\t' -v p="$(col rep)" -v r="$(col resolved)" '{print $p, $r}' | grep -x '1 1'
  [ "$(wc -l <"$T/out/judging/KEY.tsv")" -eq 1 ]
  [ "$(find "$T/out/judging" -name '*.patch' | wc -l)" -eq 1 ]
}

@test "run: a dry run prints every planned dispatch and writes nothing" {
  cd "$T/cwd"
  BENCH_DRY_RUN=1 BENCH_REPS=2 run bench run t1 good bad
  [ "$status" -eq 0 ]
  [ "$(grep -c 'DRY-RUN:' <<<"$output")" -eq 4 ]
  grep -F -- "--local-dir $T/out/runs/good/r2/issues" <<<"$output"
  [[ "$output" != *summary.md* ]]
  [ ! -e "$T/out" ]
  [ ! -e "$FAKE_LOG" ]
  [ ! -e .spindrift ]
  git ls-remote --heads "$T/remote.git" | grep -q refs/heads/stale
}

@test "run: a dry run leaves an interrupted rep's leftovers for the real run to clear" {
  cd "$T/cwd"
  BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  git -C "$T/out/work.git" worktree add -q --detach "$T/out/runs/good/r1/tree" HEAD
  head -1 "$T/out/metrics.tsv" >"$T/m" && mv "$T/m" "$T/out/metrics.tsv"
  snapshot() { (cd "$T/out" && find . -path ./work.git -prune -o -type f -print0 | sort -z | xargs -0 sha256sum); }
  before="$(snapshot)"
  BENCH_DRY_RUN=1 BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  grep -F -- '--branch-prefix bench/good/r1/' <<<"$output"
  [ "$(snapshot)" = "$before" ]
  [ -d "$T/out/runs/good/r1/tree" ]
}

@test "summary: outcomes beyond ready, blocked, failed and none are counted as other" {
  cd "$T/cwd"
  FAKE_STATUS=ambiguous BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  grep -F '| ready | blocked | failed | none | other |' "$T/out/summary.md"
  grep -E '^\| good \| 1 \| 1/1 \| [0-9]+ \| 0 \| 0 \| 0 \| 0 \| 1 \|' "$T/out/summary.md"
}

@test "validate: a task whose hidden test fails on BASE and passes on FIX is valid" {
  run bench validate t1
  [ "$status" -eq 0 ]
  [[ "$output" == *"valid"* ]]
}

@test "validate: a grader that passes on BASE or fails on FIX is rejected" {
  sed -i 's/^GRADE_CMD=.*/GRADE_CMD="true"/' "$T/tasks/t1/task.env"
  run bench validate t1
  [ "$status" -ne 0 ]
  [[ "$output" == *"passes on BASE"* ]]
  sed -i 's/^GRADE_CMD=.*/GRADE_CMD="false"/' "$T/tasks/t1/task.env"
  run bench validate t1
  [ "$status" -ne 0 ]
  [[ "$output" == *"fails on FIX"* ]]
}

@test "validate: FIX is graded with the task's hidden/ overlay too" {
  mkdir -p "$T/tasks/t1/hidden/tests"
  echo 'grep -qx never value.txt' >"$T/tasks/t1/hidden/tests/check.sh"
  run bench validate t1
  [ "$status" -ne 0 ]
  [[ "$output" == *"fails on FIX"* ]]
}

# An overlay that accepts any edit to value.txt, as a behavioural rewrite of a
# FIX test that pinned one exact answer would.
loosen_hidden_test() {
  mkdir -p "$T/tasks/t1/hidden/tests"
  printf '%s\n' 'git ls-files --error-unmatch tests/extra.sh >/dev/null' \
    'grep -qxE "fixed|wrong" value.txt' >"$T/tasks/t1/hidden/tests/check.sh"
  echo true >"$T/tasks/t1/hidden/tests/extra.sh"
}

@test "run: a task's hidden/ overlay replaces FIX's test at the same path, staged so a flake sees it" {
  loosen_hidden_test
  cd "$T/cwd"
  BENCH_REPS=1 run bench run t1 good bad
  [ "$status" -eq 0 ]
  [ "$(rows | awk -F'\t' -v r="$(col resolved)" '$r == 1' | wc -l)" -eq 2 ]
}

@test "regrade: re-grades every stored run against the task's current grader, dispatching nothing" {
  cd "$T/cwd"
  BENCH_REPS=1 run bench run t1 good bad crash
  [ "$status" -eq 0 ]
  local before
  before="$(cut -f1-5,9- "$T/out/metrics.tsv")"
  : >"$FAKE_LOG"
  loosen_hidden_test
  run bench regrade t1
  [ "$status" -eq 0 ]
  [ ! -s "$FAKE_LOG" ]
  [ "$(rows | awk -F'\t' -v a="$(col arm)" -v g="$(col grade)" -v r="$(col resolved)" '{print $a, $g, $r}' | sort)" \
    = "$(printf 'bad pass 1\ncrash nobranch 0\ngood pass 1')" ]
  [ "$(cut -f1-5,9- "$T/out/metrics.tsv")" = "$before" ]
  grep -F '| bad | 1 | 1/1 |' "$T/out/summary.md"
  [ "$(git -C "$T/out/work.git" worktree list | wc -l)" -eq 1 ]
}

@test "regrade: a CHECK_CMD that now fails leaves the run unresolved" {
  cd "$T/cwd"
  BENCH_REPS=1 run bench run t1 good
  [ "$status" -eq 0 ]
  sed -i 's/^CHECK_CMD=.*/CHECK_CMD="false"/' "$T/tasks/t1/task.env"
  run bench regrade t1
  [ "$status" -eq 0 ]
  rows | awk -F'\t' -v g="$(col grade)" -v c="$(col checks)" -v r="$(col resolved)" '{print $g, $c, $r}' | grep -x 'pass fail 0'
  [ -f "$T/out/runs/good/r1/checks.log" ]
}

@test "new-task: snapshots the issue and takes BASE and hidden tests from the fix" {
  printf '#!%s\n' "$BASH" >"$T/bin/gh"
  cat >>"$T/bin/gh" <<'FAKE'
echo '{"title":"Fix: value \"says\" broken","body":"value.txt should say fixed."}'
FAKE
  chmod +x "$T/bin/gh"
  BENCH_GH="$T/bin/gh" run bench new-task --issue 42 --fix "$FIX"
  [ "$status" -eq 0 ]
  local d="$T/tasks/t42"
  local want_base want_fix="$FIX"
  want_base=$(git -C "$T/src" rev-parse "$FIX^")
  # shellcheck disable=SC1091
  . "$d/task.env"
  [ "$BASE" = "$want_base" ]
  [ "$FIX" = "$want_fix" ]
  [ "$TEST_PATHS" = tests/check.sh ]
  grep -Fx 'title: "Fix: value \"says\" broken"' "$d/issue.md"
  grep -Fx 'state: ready-for-agent' "$d/issue.md"
  grep -Fx 'value.txt should say fixed.' "$d/issue.md"
}

@test "new-task: guesses one go test per module and bats per suite for GRADE_CMD" {
  mkdir -p "$T/src/mod/pkg/inner" "$T/src/tests"
  echo 'module x' >"$T/src/mod/go.mod"
  git_q -C "$T/src" add -A
  git_q -C "$T/src" commit -qm module
  local base
  base=$(git -C "$T/src" rev-parse HEAD)
  touch "$T/src/mod/pkg/a_test.go" "$T/src/mod/pkg/inner/b_test.go" "$T/src/tests/s.bats"
  git_q -C "$T/src" add -A
  git_q -C "$T/src" commit -qm tests
  printf '#!%s\necho %s\n' "$BASH" "'{\"title\":\"t\",\"body\":\"b\"}'" >"$T/bin/gh"
  chmod +x "$T/bin/gh"
  BENCH_GH="$T/bin/gh" run bench new-task --issue 7 --fix HEAD --base "$base"
  [ "$status" -eq 0 ]
  # shellcheck disable=SC1091
  . "$T/tasks/t7/task.env"
  [ "$GRADE_CMD" = 'nix develop -c bash -c "(cd mod && go test ./pkg ./pkg/inner) && bats tests/s.bats"' ]
  # task.env is edited by hand, so values are plain single-quoted strings.
  grep -Fx "GRADE_CMD='nix develop -c bash -c \"(cd mod && go test ./pkg ./pkg/inner) && bats tests/s.bats\"'" "$T/tasks/t7/task.env"
  grep -Fx "CHECK_CMD='nix build .#checks-inbox'" "$T/tasks/t7/task.env"
}

@test "new-task: a numeric task name is refused, since a numeric slug reaches a real issue" {
  BENCH_GH=true run bench new-task --issue 42 --fix "$FIX" --name 42
  [ "$status" -ne 0 ]
  [ ! -e "$T/tasks/42" ]
}

@test "--breakdown attributes tokens per role+model with injected pricing" {
  local log="$T/box.log"
  cat >"$log" <<'JSON'
==> some non-json narration line
{"type":"assistant","message":{"model":"claude-sonnet-5","content":[{"type":"tool_use","id":"toolu_scout","name":"Task","input":{"subagent_type":"scout"}}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}
{"type":"assistant","message":{"model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":200,"output_tokens":80,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}},"parent_tool_use_id":"toolu_scout"}
{"type":"assistant","message":{"model":"claude-sonnet-5","content":[],"usage":{"input_tokens":50,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
{"type":"result","total_cost_usd":0.5,"usage":{"input_tokens":9,"output_tokens":9}}
JSON
  run env BENCH_PRICES='{"claude-sonnet-5":[3,15,0.3,3.75],"claude-haiku":[1,5,0.1,1]}' \
    bash "${BENCH_SH:-$BATS_TEST_DIRNAME/../bench.sh}" --breakdown "$log"
  [ "$status" -eq 0 ]
  # (150*3 + 70*15 + 10*0.3 + 5*3.75)/1e6 = 0.00152175; the largest single
  # prompt is the first turn's 100+10+5.
  echo "$output" | grep -P '^implementor\tclaude-sonnet-5\t10\t5\t150\t70\t0.001522\t115$'
  echo "$output" | grep -P '^scout\tclaude-haiku-5-5\t0\t0\t200\t80\t0.000600\t200$'
}

@test "--breakdown reads a real Box log: deduped ids, Agent spawns, review passes, cache TTLs, session output" {
  local log="$T/real.log"
  cat >"$log" <<'JSON'
{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":1,"role":"implement"}}
{"type":"assistant","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":40,"ephemeral_1h_input_tokens":0}}}}
{"type":"assistant","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_w","name":"Agent","input":{"subagent_type":"worker"}}],"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":40,"ephemeral_1h_input_tokens":0}}}}
{"type":"assistant","message":{"id":"m2","model":"claude-haiku-5-5","content":[{"type":"tool_use","id":"toolu_s","name":"Agent","input":{"subagent_type":"scout"}}],"usage":{"input_tokens":200,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":10}}},"parent_tool_use_id":"toolu_w"}
{"type":"assistant","message":{"id":"m3","model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":300,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}},"parent_tool_use_id":"toolu_s"}
{"type":"result","total_cost_usd":1,"usage":{"output_tokens":7},"modelUsage":{"claude-opus-5-5":{"outputTokens":50},"claude-haiku-5-5":{"outputTokens":400}}}
{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":2,"role":"review"}}
{"type":"assistant","message":{"id":"m4","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":8}}}
{"type":"result","total_cost_usd":1,"usage":{"output_tokens":3},"modelUsage":{"claude-opus-5-5":{"outputTokens":30}}}
JSON
  run env BENCH_PRICES='{"claude-opus":[10,100,1,20],"claude-haiku":[1,10,0.1,2]}' \
    bash "${BENCH_SH:-$BATS_TEST_DIRNAME/../bench.sh}" --breakdown "$log"
  [ "$status" -eq 0 ]
  # m1 once, its 5m write at 1.25x input: (100*10 + 50*100 + 40*12.5)/1e6.
  echo "$output" | grep -P '^implementor\tclaude-opus-5-5\t0\t40\t100\t50\t0.006500\t140$'
  # A session's per-model output splits over that model's messages in it.
  echo "$output" | grep -P '^worker\tclaude-haiku-5-5\t0\t10\t200\t200\t0.002220\t210$'
  echo "$output" | grep -P '^scout\tclaude-haiku-5-5\t0\t0\t300\t200\t0.002300\t300$'
  # An un-split cache write is 5m, as the launcher assumes.
  echo "$output" | grep -P '^reviewer\tclaude-opus-5-5\t0\t8\t10\t30\t0.003200\t18$'
  [ "$(wc -l <<<"$output")" -eq 4 ]
}

@test "--breakdown prices a turn past a model's prompt threshold at the higher tier" {
  local log="$T/tier.log"
  cat >"$log" <<'JSON'
{"type":"assistant","message":{"model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":1000,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
{"type":"assistant","message":{"model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":10000,"output_tokens":1000,"cache_read_input_tokens":110000,"cache_creation_input_tokens":0}}}
JSON
  run env BENCH_PRICES='{"claude-haiku-5-5":[0.1,0.5,0.01,0.2,100000,0.5,2.5,0.05,1]}' \
    bash "${BENCH_SH:-$BATS_TEST_DIRNAME/../bench.sh}" --breakdown "$log"
  [ "$status" -eq 0 ]
  # (1000*0.1 + 100*0.5)/1e6 under the threshold, then the 120000-token turn
  # at (10000*0.5 + 1000*2.5 + 110000*0.05)/1e6 = 0.013.
  echo "$output" | grep -P '^implementor\tclaude-haiku-5-5\t110000\t0\t11000\t1100\t0.013150\t120000$'
}

@test "--breakdown reports zero cost for a model with no pricing-table prefix match" {
  local log="$T/unknown.log"
  echo '{"type":"assistant","message":{"model":"some-other-model","content":[],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":1,"cache_creation_input_tokens":1}}}' >"$log"
  run env BENCH_PRICES='{"claude-sonnet-5":[3,15,0.3,3.75]}' \
    bash "${BENCH_SH:-$BATS_TEST_DIRNAME/../bench.sh}" --breakdown "$log"
  [ "$status" -eq 0 ]
  echo "$output" | grep -P '^implementor\tsome-other-model\t1\t1\t10\t5\t0.000000\t12$'
}

@test "--compare averages repeated labels and deltas every arm against the first" {
  printf 'implementor\tclaude-sonnet-5\t10\t5\t150\t70\t0.002000\t100\n' >"$T/c1.tsv"
  printf 'implementor\tclaude-sonnet-5\t30\t5\t150\t70\t0.004000\t300\n' >"$T/c2.tsv"
  printf 'implementor\tclaude-sonnet-5\t20\t10\t100\t40\t0.001000\t50\n' >"$T/v1.tsv"
  printf 'worker\tclaude-haiku-5-5\t0\t0\t200\t80\t0.000600\t200\n' >>"$T/v1.tsv"
  printf 'implementor\tclaude-sonnet-5\t0\t0\t0\t0\t0.006000\t10\n' >"$T/x1.tsv"
  run bench --compare control="$T/c1.tsv" control="$T/c2.tsv" variant="$T/v1.tsv" extra="$T/x1.tsv"
  [ "$status" -eq 0 ]
  # control is the mean of two runs; max prompt is the max across them.
  echo "$output" | grep -P '^\| control \| implementor \| claude-sonnet-5 \| 20 \| 5 \| 150 \| 70 \| 0.003000 \| 300 \|$'
  echo "$output" | grep -P '^\| variant \| worker \| claude-haiku-5-5 \| 0 \| 0 \| 200 \| 80 \| 0.000600 \| 200 \|$'
  echo "$output" | grep -P '^\| claude-haiku-5-5 \| 0.000000 \| 0.000600 \| 0.000000 \| 0.000600 \| 0.000000 \|$'
  echo "$output" | grep -P '^\| \*\*total\*\* \| 0.003000 \| 0.001600 \| 0.006000 \| -0.001400 \| 0.003000 \|$'
}

@test "--breakdown then --compare uses the default price table end-to-end" {
  local clog="$T/c-box.log" vlog="$T/v-box.log"
  echo '{"type":"assistant","message":{"model":"claude-sonnet-5","content":[],"usage":{"input_tokens":1000,"output_tokens":500,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}' >"$clog"
  cat >"$vlog" <<'JSON'
{"type":"assistant","message":{"model":"claude-sonnet-5","content":[{"type":"tool_use","id":"toolu_w","name":"Task","input":{"subagent_type":"worker"}}],"usage":{"input_tokens":300,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
{"type":"assistant","message":{"model":"claude-sonnet-5","content":[],"usage":{"input_tokens":400,"output_tokens":200,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}},"parent_tool_use_id":"toolu_w"}
JSON
  bench --breakdown "$clog" >"$T/c.tsv"
  bench --breakdown "$vlog" >"$T/v.tsv"
  run bench --compare "$T/c.tsv" "$T/v.tsv"
  [ "$status" -eq 0 ]
  # Default table, claude-sonnet-5 at $2/$10 per MTok; bare files label as
  # control and variant.
  echo "$output" | grep -P '^\| variant \| worker \| claude-sonnet-5 \| 0 \| 0 \| 400 \| 200 \| 0.002800 \| 400 \|$'
  echo "$output" | grep -P '^\| control \| implementor \| claude-sonnet-5 \| 0 \| 0 \| 1000 \| 500 \| 0.007000 \| 1000 \|$'
  echo "$output" | grep -P '^\| \*\*total\*\* \| 0.007000 \| 0.004400 \| -0.002600 \|$'
}
