# shellcheck shell=bash
# bench.sh judge TASK: blind LLM scoring of judging/*.patch.
#
# Every run passes the hidden tests on most tasks, so pass/fail cannot rank
# arms. The judge scores each staged patch against the issue and the merged
# reference fix, by rubric (bench/judge-rubric.md), knowing only the patch's
# blind id: the prompt never carries an arm, rep, cost or model name.

BENCH_JUDGE_RUBRIC="${BENCH_JUDGE_RUBRIC:-$(dirname "${BASH_SOURCE[0]}")/../judge-rubric.md}"

SCORES_HEADER=$'blind\tcorrectness\ttests\tscope\tquality\toverall\tjudge_model\tjudge_effort\trationale'

# Prints "correctness tests scope quality overall rationale" as one TSV line
# from a `claude --output-format json` reply, or fails when the reply holds no
# object with every score an in-range integer. The model may wrap the object
# in prose or a code fence, so the outermost {...} is tried when the whole
# text is not JSON.
parse_judgment() {
  jq -er '
    def obj:
      if type == "object" then .
      else tostring as $s
        | try ($s | fromjson)
          catch (($s | index("{")) as $a | ($s | rindex("}")) as $b
            | if $a != null and $b != null then $s[$a:$b + 1] | fromjson
              else error("no JSON object") end)
      end;
    def ok($hi): type == "number" and . == floor and . >= 1 and . <= $hi;
    select(.is_error != true) | .result | obj
    | if (.correctness | ok(5)) and (.tests | ok(5)) and (.scope | ok(5))
         and (.quality | ok(5)) and (.overall | ok(10)) and (.rationale | type == "string")
      then [.correctness, .tests, .scope, .quality, .overall,
            (.rationale | gsub("[\\t\\r\\n]+"; " ") | .[0:1000])]
        | map(tostring) | join("\t")
      else error("score out of range") end'
}

# One printf per row, so each lands as a single O_APPEND write and concurrent
# workers' rows never interleave; the rationale cap keeps a row inside one
# write.
append_score() {
  printf '%s\n' "$(IFS=$'\t'; echo "$*")" >>"$SCORES"
}

judge_prompt() {
  local issue="$1" reference="$2" patch="$3"
  cat "$BENCH_JUDGE_RUBRIC"
  printf '\n<issue>\n'
  cat "$issue"
  printf '</issue>\n\n'
  printf '<reference_diff>\nA reference fix that was merged for this issue. Other approaches may be equally or more correct and must not be penalised for differing from it.\n\n'
  cat "$reference"
  printf '</reference_diff>\n\n<candidate_patch>\n'
  cat "$patch"
  printf '</candidate_patch>\n'
}

# The call runs from an empty directory so no repo CLAUDE.md, memory or git
# status (whose branch names carry the arm) reaches the judge's context.
judge_call() {
  local prompt="$1" cwd="$2"
  (cd "$cwd" && "$BENCH_CLAUDE" -p \
    --model "$JUDGE_MODEL" --effort "$JUDGE_EFFORT" \
    --tools "" --disable-slash-commands --strict-mcp-config \
    --no-session-persistence --output-format json \
    --system-prompt "You are a careful, impartial code reviewer. Answer with the single JSON object the rubric asks for." \
    <"$prompt")
}

judge_one() {
  local id="$1" patch="$2" work="$3" scores="" attempt
  judge_prompt "$OUT/judging/ISSUE.md" "$work/reference.diff" "$patch" >"$work/$id.prompt"
  mkdir -p "$work/$id.cwd"
  for attempt in 1 2; do
    if scores="$(judge_call "$work/$id.prompt" "$work/$id.cwd" 2>"$work/$id.err" | parse_judgment 2>/dev/null)"; then
      break
    fi
    scores=""
  done
  if [ -z "$scores" ]; then
    warn "judge: $id got no valid scores after $attempt attempts; delete its judge-error row to retry"
    append_score "$id" "" "" "" "" "" "$JUDGE_MODEL" "$JUDGE_EFFORT" judge-error
    return 0
  fi
  local c t s q o r
  IFS=$'\t' read -r c t s q o r <<<"$scores"
  append_score "$id" "$c" "$t" "$s" "$q" "$o" "$JUDGE_MODEL" "$JUDGE_EFFORT" "$r"
}

cmd_judge() {
  [ $# -eq 1 ] || die "usage: bench.sh judge TASK"
  load_task "$1"
  OUT="${BENCH_OUT:-bench/results/$1}"
  BENCH_CLAUDE="${BENCH_CLAUDE:-claude}"
  JUDGE_MODEL="${BENCH_JUDGE_MODEL:-claude-opus-5-5}"
  JUDGE_EFFORT="${BENCH_JUDGE_EFFORT:-high}"
  local jobs="${BENCH_JUDGE_JOBS:-4}"
  [[ "$jobs" =~ ^[1-9][0-9]*$ ]] || die "BENCH_JUDGE_JOBS must be a positive integer"
  [ -f "$OUT/judging/ISSUE.md" ] || die "no $OUT/judging/ISSUE.md; run the task first"
  [ -f "$BENCH_JUDGE_RUBRIC" ] || die "no rubric at $BENCH_JUDGE_RUBRIC"
  command -v jq >/dev/null || die "jq not found on PATH"

  SCORES="$OUT/judging/SCORES.tsv"
  [ -f "$SCORES" ] || printf '%s\n' "$SCORES_HEADER" >"$SCORES"
  local work
  work="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand now: work is local
  trap "rm -rf '$work'" EXIT
  git -C "$BENCH_SRC" diff "$BASE" "$FIX" >"$work/reference.diff"

  local -a todo=()
  local patch id skipped=0 running=0
  for patch in "$OUT/judging"/*.patch; do
    [ -e "$patch" ] || continue
    id="$(basename "$patch" .patch)"
    if awk -F'\t' -v id="$id" 'NR > 1 && $1 == id { found = 1; exit } END { exit !found }' "$SCORES"; then
      skipped=$((skipped + 1))
      continue
    fi
    todo+=("$id")
    if [ ! -s "$patch" ]; then
      append_score "$id" 0 0 0 0 0 none none "empty diff"
      continue
    fi
    if [ "$running" -ge "$jobs" ]; then
      wait -n || true
      running=$((running - 1))
    fi
    judge_one "$id" "$patch" "$work" &
    running=$((running + 1))
  done
  wait

  local errors=0
  [ ${#todo[@]} -eq 0 ] || errors="$(awk -F'\t' -v ids="${todo[*]}" '
    BEGIN { n = split(ids, a, " "); for (i = 1; i <= n; i++) want[a[i]] = 1 }
    NR > 1 && ($1 in want) && $9 == "judge-error" { e++ }
    END { print e + 0 }' "$SCORES")"
  info "judge $1: scored $((${#todo[@]} - errors)), skipped $skipped, errors $errors ($SCORES)"
}
