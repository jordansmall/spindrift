#!/usr/bin/env bash

# Benchmark Harness configurations on frozen tasks (issue #2057's A/B
# harness, grown into a benchmark). A task (bench/tasks/<name>/) pins an issue
# snapshot, the BASE commit before its real fix, the FIX commit, and the
# hidden tests FIX added. Each arm (a line of bench/arms: name, flake app,
# extra dispatch flags) runs the task BENCH_REPS times, in shuffled order.
#
# Before every run the bench remote is reset to BASE alone, so the Box never
# sees the fix or an earlier attempt. Afterwards the run's branch is fetched,
# deleted from the remote, and graded by overlaying FIX's hidden tests and
# running GRADE_CMD, then CHECK_CMD if set. Results accumulate in BENCH_OUT
# across invocations, so more reps or arms can be added later.
#
#   bench.sh new-task --issue N --fix SHA [--base SHA] [--name NAME]
#   bench.sh validate TASK
#   bench.sh run TASK [ARM...]
#   bench.sh summarize TASK
#   bench.sh --breakdown BOX_LOG
#   bench.sh --compare [LABEL=]TSV...
#
# SAFETY: `run` dispatches real Boxes (real spend) and force-resets
# BENCH_PUSH_URL: main becomes BASE and every other branch and tag is
# deleted. Point it only at a throwaway repo.
#
# Subagent models and efforts are baked at image time (#5254), so each such
# variant is a `.#ab-<name>` app from nix/ab-arms.nix. Coordinator
# --model/--effort and --review-model/--review-effort are dispatch-time arm
# flags on any app.
#
# Boxes run read-only through the GitHub forge, as dogfood does: the Box
# clones with BOX_GH_TOKEN[_CMD] (read-only, distinct from GH_TOKEN) and hands
# back a bundle, which the host pushes as the run's branch and opens a draft
# PR for with GH_TOKEN[_CMD]. The host then waits on the bench repo's CI and
# runs fix passes on red, so the branch graded is the one dogfood would have
# landed. Both tokens must reach the bench repo. The
# launcher reads them, plus CLAUDE_CODE_OAUTH_TOKEN[_CMD] or ANTHROPIC_API_KEY,
# GIT_USER_NAME and GIT_USER_EMAIL, from the environment: source harness.env
# or point BENCH_ENV_FILE at it.

set -euo pipefail

info() { printf '==> %s\n' "$*" >&2; }
warn() { printf '!! %s\n' "$*" >&2; }
die()  { printf '!! %s\n' "$*" >&2; exit 1; }

BENCH_NIX="${BENCH_NIX:-nix}"
BENCH_GH="${BENCH_GH:-gh}"
BENCH_SRC="${BENCH_SRC:-.}"
BENCH_TASKS_DIR="${BENCH_TASKS_DIR:-bench/tasks}"
BENCH_ARMS_FILE="${BENCH_ARMS_FILE:-bench/arms}"
BENCH_REMOTE="${BENCH_REMOTE:-https://github.com/jordansmall/spindrift-ab.git}"
BENCH_PUSH_URL="${BENCH_PUSH_URL:-git@github.com:jordansmall/spindrift-ab.git}"
BENCH_REPO_SLUG="${BENCH_REPO_SLUG:-$(sed -E 's#^(git@github\.com:|https?://github\.com/)##; s#\.git$##' <<<"$BENCH_REMOTE")}"
BENCH_SOURCE_SLUG="${BENCH_SOURCE_SLUG:-jordansmall/spindrift}"
BENCH_REPS="${BENCH_REPS:-1}"
BENCH_NO_BUILD="${BENCH_NO_BUILD:-1}"

METRICS_HEADER=$'task\tarm\trep\tblind\toutcome\tresolved\tgrade\tchecks\tcost_usd\tin_tok\tout_tok\tcache_read\tcache_create\tturns\tduration_ms\tpasses\tverdicts\tdecision\tharness\tspec'

# Sum every "type":"result" event: the orchestrator emits one per pass and the
# launcher's own comment keeps only the last, so summing is the honest total.
# fromjson? tolerates the log's bare non-JSON lines (==>, SPINDRIFT_*). Prints
# one row: cost, input, output, cache read, cache create, turns, duration ms.
parse_usage() {
  jq -Rnr '
    [inputs | fromjson?] | map(select(.type=="result")) as $r
    | [ ($r|map(.total_cost_usd // 0)|add // 0),
        ($r|map(.usage.input_tokens // 0)|add // 0),
        ($r|map(.usage.output_tokens // 0)|add // 0),
        ($r|map(.usage.cache_read_input_tokens // 0)|add // 0),
        ($r|map(.usage.cache_creation_input_tokens // 0)|add // 0),
        ($r|map(.num_turns // 0)|add // 0),
        ($r|map(.duration_ms // 0)|add // 0) ]
    | @tsv' "$1" 2>/dev/null || printf '0\t0\t0\t0\t0\t0\t0'
}

# Orchestrator heartbeat ops (#2027).
parse_passes()  { jq -Rn '[inputs|fromjson?]|map(select(.type=="spindrift_op" and .spindrift_op.op=="pass_start"))|length' "$1" 2>/dev/null || echo 0; }
parse_verdicts(){ jq -Rn '[inputs|fromjson?]|map(select(.type=="spindrift_op" and .spindrift_op.op=="verdict")|.spindrift_op.verdict)|join(",")' "$1" 2>/dev/null | tr -d '"' || echo ""; }
parse_decision(){ jq -Rn '[inputs|fromjson?]|map(select(.type=="spindrift_op" and .spindrift_op.op=="decision")|"\(.spindrift_op.decision):\(.spindrift_op.reason)")|last // ""' "$1" 2>/dev/null | tr -d '"' || echo ""; }

parse_outcome() { # Prints the last SPINDRIFT_OUTCOME status, or none.
  local line
  line="$(grep -a '^SPINDRIFT_OUTCOME ' "$1" 2>/dev/null | tail -1 || true)"
  if [ -z "$line" ]; then echo none; return; fi
  sed -n 's/.*status=\([^ ]*\).*/\1/p' <<<"$line" | head -1
}

# Public Claude list prices per MTok as of 2026-10; override via BENCH_PRICES.
# Keys are model-name prefixes matched against a message's "model" field
# (longest match wins); each value is [input, output, cache_read, cache_write]
# in $/MTok, optionally followed by [prompt_threshold, input, output,
# cache_read, cache_write] for a turn whose whole prompt (fresh + cache read +
# cache write) exceeds the threshold. cache_write is the 1h rate (2x input);
# 5m writes cost 1.25x input. Haiku 5.5's long-prompt cache rates assume
# the same 5x step as its input. A model with no matching prefix costs 0, but
# its tokens are still reported.
BENCH_DEFAULT_PRICES='{"claude-opus-5-5":[4,20,0.2,8],"claude-opus":[5,25,0.5,10],"claude-sonnet-5":[2,10,0.2,4],"claude-haiku-5-5":[0.1,0.5,0.01,0.2,100000,0.5,2.5,0.05,1],"claude-haiku":[1,5,0.1,2]}'

# Per-role/model token and effective-cost TSV from a stream-json Box log (issue
# #2057), following the launcher's claude/usage.go: one pass dedups by
# message.id, maps each Task/Agent spawn id to its subagent_type, and
# attributes a top-level message to implementor or reviewer by the latest
# pass_start. Each message is priced on its own, since a tier applies per
# turn, and the last column is the largest single-turn prompt.
cmd_breakdown() {
  local log="$1"
  local prices_json="${BENCH_PRICES:-$BENCH_DEFAULT_PRICES}"
  jq -Rnr --argjson prices "$prices_json" '
    # NB: jq function-parameter filters are re-evaluated against whatever "."
    # is current at their use site, not the calling contexts "." -- both
    # helpers below bind their filter param to a $-variable up front so a
    # later context switch (e.g. piping into $order or to_entries) does not
    # change what the parameter means.
    def role_rank(r):
      r as $r
      | ["implementor","worker","scout","reviewer","filer","subagent"] as $order
      | ($order | index($r)) as $i
      | if $i == null then 999 else $i end;

    def rate_for(model; prompt):
      model as $model | prompt as $prompt
      | ($prices | to_entries
        | map(select(.key as $k | $model | startswith($k)))
        | sort_by(.key | length)
        | last) as $e
      | if $e == null then [0,0,0,0]
        elif ($e.value | length) >= 9 and $prompt > $e.value[4] then $e.value[5:9]
        else $e.value[0:4] end;

    # Format a non-negative number to a fixed 6-decimal string (no scientific
    # notation, no float-formatting surprises on trailing zeros).
    def fmt6:
      (. * 1000000 | round) as $micros
      | ($micros / 1000000 | floor) as $whole
      | ($micros - ($whole * 1000000)) as $frac
      | "\($whole).\($frac | tostring | if length < 6 then ("0" * (6 - length)) + . else . end)";

    # Per-message output_tokens is a ~100x-low placeholder (#3183); the
    # modelUsage on the result event ending the session is the real count,
    # split evenly over the messages of that model in the session.
    def settle(mu):
      mu as $mu
      | (map(.model) | group_by(.) | map({key: .[0], value: length}) | from_entries) as $n
      | map(if $mu[.model].outputTokens != null
            then .output = ($mu[.model].outputTokens / $n[.model]) else . end);

    reduce (inputs | fromjson? | objects) as $ev (
      {top: "implementor", task: {}, seen: {}, cur: [], done: []};
      if $ev.type == "spindrift_op" and $ev.spindrift_op.op == "pass_start" then
        ($ev.spindrift_op.role // "") as $p
        | if $p == "review" or $p == "delta-review" then .top = "reviewer"
          elif $p == "implement" or $p == "fix" or $p == "land" then .top = "implementor"
          else . end
      elif $ev.type == "result" then
        .done += (.cur | settle($ev.modelUsage // {})) | .cur = []
      elif $ev.type == "assistant" and $ev.message != null then
        # Spawns are collected before the dedup: a repeated message id may
        # carry the tool_use block its first copy lacked.
        reduce ($ev.message.content // [] | .[] | objects
            | select(.type == "tool_use" and (.name == "Task" or .name == "Agent") and ((.id // "") != ""))) as $b (.;
          .task[$b.id] = (($b.input.subagent_type // "") | if . == "" then "subagent" else . end))
        | ($ev.message.id // "") as $id
        | if $id != "" and .seen[$id] then .
          else
            (if $id != "" then .seen[$id] = true else . end)
            | ($ev.parent_tool_use_id // "") as $parent
            | ($ev.message.usage // {}) as $u
            | $u.cache_creation as $cc
            | .cur += [{
                role: (if $parent == "" then .top else (.task[$parent] // "subagent") end),
                model: ($ev.message.model // "unknown"),
                fresh_input: ($u.input_tokens // 0),
                cache_read: ($u.cache_read_input_tokens // 0),
                cw5: (if $cc != null then ($cc.ephemeral_5m_input_tokens // 0) else ($u.cache_creation_input_tokens // 0) end),
                cw1: (if $cc != null then ($cc.ephemeral_1h_input_tokens // 0) else 0 end),
                output: ($u.output_tokens // 0) }]
          end
      else . end)
    | (.done + .cur | map(
        (.fresh_input + .cache_read + .cw5 + .cw1) as $prompt
        | rate_for(.model; $prompt) as $r
        | { role, model, cache_read, cache_write: (.cw5 + .cw1), fresh_input, output, prompt: $prompt,
            cost: (((.fresh_input * $r[0]) + (.output * $r[1]) + (.cache_read * $r[2])
              + (.cw5 * $r[0] * 1.25) + (.cw1 * $r[3])) / 1000000) }
      )) as $rows
    | ($rows | group_by([.role, .model]) | map({
        role: .[0].role, model: .[0].model,
        cache_read:  (map(.cache_read)  | add),
        cache_write: (map(.cache_write) | add),
        fresh_input: (map(.fresh_input) | add),
        output:      (map(.output)      | add | round),
        cost:        (map(.cost)        | add),
        max_prompt:  (map(.prompt)      | max)
      }))
    | sort_by([role_rank(.role), .role, .model])[]
    | [ .role, .model, (.cache_read|tostring), (.cache_write|tostring), (.fresh_input|tostring),
        (.output|tostring), (.cost|fmt6), (.max_prompt|tostring) ]
    | join("\t")
  ' "$log"
}

# Side-by-side per-role/model table over --breakdown TSVs (issue #2057). Each
# argument is LABEL=TSV, or a bare TSV labelled control, variant, arm3, ... by
# position. Repeating a label averages its runs (max prompt takes the max).
# Then a per-model and total effective-$ table with every label's delta
# against the first.
cmd_compare() {
  local -a labels=() files=()
  local a i=0 defaults=(control variant)
  for a in "$@"; do
    if [[ "$a" == *=* ]]; then
      labels+=("${a%%=*}") files+=("${a#*=}")
    else
      labels+=("${defaults[i]:-arm$((i + 1))}") files+=("$a")
    fi
    i=$((i + 1))
  done
  for i in "${!files[@]}"; do
    [ -f "${files[i]}" ] && awk -v l="${labels[i]}" -F'\t' 'NF { print l "\t" $0 }' "${files[i]}"
  done | awk -F'\t' -v labels="$(IFS=$'\t'; echo "${labels[*]}")" '
    BEGIN {
      n = split(labels, L, "\t")
      for (i = 1; i <= n; i++) {
        if (!(L[i] in runs)) lorder[++nl] = L[i]
        runs[L[i]]++
      }
    }
    {
      lab = $1; key = $2 SUBSEP $3
      if (!(key in seen)) { seen[key] = 1; order[++nk] = key; role[key] = $2; model[key] = $3 }
      if (!($3 in mseen)) { mseen[$3] = 1; morder[++nm] = $3 }
      cr[lab, key] += $4; cw[lab, key] += $5; fi[lab, key] += $6; out[lab, key] += $7
      cost[lab, key] += $8; mcost[lab, $3] += $8; total[lab] += $8
      if ($9 > maxp[lab, key]) maxp[lab, key] = $9
    }
    END {
      print "| arm | role | model | cache_read | cache_write | fresh_input | output | cost $ | max prompt |"
      print "| --- | --- | --- | --- | --- | --- | --- | --- | --- |"
      for (k = 1; k <= nk; k++) {
        key = order[k]
        for (j = 1; j <= nl; j++) {
          lab = lorder[j]; r = runs[lab]
          printf "| %s | %s | %s | %.0f | %.0f | %.0f | %.0f | %.6f | %d |\n", \
            lab, role[key], model[key], cr[lab, key] / r, cw[lab, key] / r, \
            fi[lab, key] / r, out[lab, key] / r, cost[lab, key] / r, maxp[lab, key] + 0
        }
      }
      print ""
      print "## Effective-$ per run, with delta against " lorder[1]
      print ""
      head = "| model |"; rule = "| --- |"
      for (j = 1; j <= nl; j++) { head = head " " lorder[j] " $ |"; rule = rule " --- |" }
      for (j = 2; j <= nl; j++) { head = head " delta " lorder[j] " $ |"; rule = rule " --- |" }
      print head; print rule
      for (m = 1; m <= nm; m++) row(morder[m], "model")
      row("**total**", "total")
    }
    function row(name, kind,    j, v, base, line) {
      line = "| " name " |"
      for (j = 1; j <= nl; j++) {
        v[j] = (kind == "total" ? total[lorder[j]] : mcost[lorder[j], name]) / runs[lorder[j]]
        line = line sprintf(" %.6f |", v[j])
      }
      for (j = 2; j <= nl; j++) line = line sprintf(" %.6f |", v[j] - v[1])
      print line
    }
  '
}

check_task_name() {
  [[ "$1" =~ ^[a-z][a-z0-9-]*$ ]] \
    || die "task name '$1' must start with a letter and use only a-z, 0-9 and '-': it is the local issue slug, and a numeric slug lets an in-box \`gh issue view\` fetch a real issue"
}

load_task() {
  local task="$1"
  check_task_name "$task"
  TASK_DIR="$BENCH_TASKS_DIR/$task"
  [ -f "$TASK_DIR/task.env" ] || die "no task '$task' (expected $TASK_DIR/task.env)"
  [ -f "$TASK_DIR/issue.md" ] || die "task '$task' has no issue.md"
  BASE="" FIX="" TEST_PATHS="" GRADE_CMD="" CHECK_CMD=""
  # shellcheck disable=SC1091
  . "$TASK_DIR/task.env"
  [ -n "$BASE" ] && [ -n "$FIX" ] && [ -n "$GRADE_CMD" ] \
    || die "task '$task' must set BASE, FIX and GRADE_CMD in task.env"
}

# A bare clone sharing BENCH_SRC's objects, so worktrees can check out BASE,
# FIX and fetched run branches without copying history.
ensure_grading_repo() {
  [ -d "$1" ] || git clone -q --bare --shared "$BENCH_SRC" "$1"
}

# Checks out REV into a fresh worktree DIR and overlays FIX's hidden tests.
prepare_tree() {
  local repo="$1" rev="$2" dir="$3"
  git -C "$repo" worktree add -q --detach "$dir" "$rev"
  if [ -n "$TEST_PATHS" ]; then
    # shellcheck disable=SC2086 # TEST_PATHS is a space-separated list
    git -C "$dir" checkout -q "$FIX" -- $TEST_PATHS
  fi
}

drop_tree() {
  git -C "$1" worktree remove --force "$2" 2>/dev/null || rm -rf "$2"
  git -C "$1" worktree prune
}

run_in() { (cd "$1" && bash -c "$2") >"$3" 2>&1; }

cmd_validate() {
  [ $# -eq 1 ] || die "usage: bench.sh validate TASK"
  load_task "$1"
  local tmp rc=0
  tmp="$(mktemp -d)"
  ensure_grading_repo "$tmp/repo.git"
  prepare_tree "$tmp/repo.git" "$BASE" "$tmp/base"
  if run_in "$tmp/base" "$GRADE_CMD" "$tmp/base.log"; then
    tail -n 20 "$tmp/base.log" >&2
    warn "GRADE_CMD passes on BASE with the hidden tests applied, so it cannot tell a fix from no fix"
    rc=1
  fi
  if [ "$rc" -eq 0 ]; then
    git -C "$tmp/repo.git" worktree add -q --detach "$tmp/fix" "$FIX"
    if ! run_in "$tmp/fix" "$GRADE_CMD" "$tmp/fix.log"; then
      tail -n 20 "$tmp/fix.log" >&2
      warn "GRADE_CMD fails on FIX, so no run could pass it"
      rc=1
    fi
  fi
  rm -rf "$tmp"
  [ "$rc" -eq 0 ] || exit 1
  info "task $1 is valid: GRADE_CMD fails on BASE and passes on FIX"
}

# A starting GRADE_CMD from the hidden test paths: `go test` per Go module for
# *_test.go, `bats` per suite. new-task's caller reviews it before validate.
guess_grade_cmd() {
  local fix="$1"; shift
  local -A pkgs=()
  local -a parts=() bats=()
  local p d mod
  for p in "$@"; do
    case "$p" in
      *_test.go)
        d="$(dirname "$p")" mod="$d"
        while [ "$mod" != . ] && ! git -C "$BENCH_SRC" cat-file -e "$fix:$mod/go.mod" 2>/dev/null; do
          mod="$(dirname "$mod")"
        done
        pkgs[$mod]+=" ./${d#"$mod"}"
        pkgs[$mod]="${pkgs[$mod]//.\/\//./}"
        ;;
      *.bats) bats+=("$p") ;;
    esac
  done
  for mod in "${!pkgs[@]}"; do parts+=("(cd $mod && go test${pkgs[$mod]})"); done
  [ ${#bats[@]} -eq 0 ] || parts+=("bats ${bats[*]}")
  [ ${#parts[@]} -eq 0 ] && return 0
  local joined="${parts[0]}" part
  for part in "${parts[@]:1}"; do joined+=" && $part"; done
  printf 'nix develop -c bash -c "%s"' "$joined"
}

# One task.env assignment, single-quoted when it can be so it stays readable
# for hand edits; %q only for a value holding a single quote.
env_line() {
  if [[ "$2" == *"'"* ]]; then printf '%s=%q\n' "$1" "$2"; else printf "%s='%s'\n" "$1" "$2"; fi
}

cmd_new_task() {
  local issue="" fix="" base="" name=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --issue) issue="$2"; shift 2 ;;
      --fix) fix="$2"; shift 2 ;;
      --base) base="$2"; shift 2 ;;
      --name) name="$2"; shift 2 ;;
      *) die "new-task: unknown argument '$1'" ;;
    esac
  done
  [ -n "$issue" ] && [ -n "$fix" ] || die "usage: bench.sh new-task --issue N --fix SHA [--base SHA] [--name NAME]"
  name="${name:-t$issue}"
  check_task_name "$name"
  local dir="$BENCH_TASKS_DIR/$name"
  [ ! -e "$dir" ] || die "task '$name' already exists at $dir"
  fix="$(git -C "$BENCH_SRC" rev-parse --verify "$fix^{commit}")"
  base="$(git -C "$BENCH_SRC" rev-parse --verify "${base:-$fix^}^{commit}")"
  local -a tests=()
  mapfile -t tests < <(git -C "$BENCH_SRC" diff --name-only --diff-filter=AM "$base" "$fix" \
    | grep -E '(_test\.go|\.bats)$|^tests/|/testdata/' || true)
  local json
  json="$("$BENCH_GH" issue view "$issue" --repo "$BENCH_SOURCE_SLUG" --json title,body)" \
    || die "could not read issue #$issue from $BENCH_SOURCE_SLUG"
  mkdir -p "$dir"
  {
    echo ---
    # A JSON string is a valid YAML scalar, quotes and colons included.
    printf 'title: %s\n' "$(jq -r '.title | @json' <<<"$json")"
    echo 'state: ready-for-agent'
    echo ---
    jq -r '.body' <<<"$json"
  } >"$dir/issue.md"
  {
    echo "# bench.sh new-task from $BENCH_SOURCE_SLUG#$issue. Review, then run: bench.sh validate $name"
    echo "BASE=$base"
    echo "FIX=$fix"
    env_line TEST_PATHS "${tests[*]}"
    env_line GRADE_CMD "$(guess_grade_cmd "$fix" "${tests[@]}")"
    env_line CHECK_CMD "nix build .#checks-inbox"
  } >"$dir/task.env"
  info "wrote $dir (BASE ${base:0:10}, FIX ${fix:0:10}, ${#tests[@]} hidden test path(s))"
  [ ${#tests[@]} -gt 0 ] || warn "no test files changed in BASE..FIX: set TEST_PATHS and GRADE_CMD by hand"
  info "review $dir/issue.md (drop anything that leaks the fix) and task.env, then: bench.sh validate $name"
}

load_arms() {
  ARM_NAMES=() ARM_APPS=() ARM_FLAGS=()
  [ -f "$BENCH_ARMS_FILE" ] || die "no arms file at $BENCH_ARMS_FILE"
  local name app rest
  while read -r name app rest || [ -n "$name" ]; do
    [[ -z "$name" || "$name" == \#* ]] && continue
    [[ "$name" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "arm name '$name' must use only a-z, 0-9, '.', '_' and '-'"
    [ -n "$app" ] || die "arm '$name' names no flake app"
    ARM_NAMES+=("$name") ARM_APPS+=("$app") ARM_FLAGS+=("$rest")
  done <"$BENCH_ARMS_FILE"
}

arm_index() {
  local i
  for i in "${!ARM_NAMES[@]}"; do
    [ "${ARM_NAMES[i]}" = "$1" ] && { echo "$i"; return 0; }
  done
  return 1
}

# Leaves BENCH_PUSH_URL holding main at BASE and nothing else, so a Box's full
# clone carries neither the fix nor another run's branch.
reset_remote() {
  git -C "$BENCH_SRC" push -q --no-verify --force "$BENCH_PUSH_URL" "$BASE:refs/heads/main"
  local -a stale=()
  mapfile -t stale < <(git ls-remote --heads --tags "$BENCH_PUSH_URL" \
    | awk '$2 != "refs/heads/main" && $2 !~ /\^\{\}$/ { print $2 }')
  [ ${#stale[@]} -eq 0 ] || git -C "$BENCH_SRC" push -q --no-verify "$BENCH_PUSH_URL" --delete "${stale[@]}"
}

next_rep() {
  [ -f "$OUT/metrics.tsv" ] || { echo 0; return; }
  awk -F'\t' -v a="$1" 'NR > 1 && $2 == a && $3 + 0 > m { m = $3 + 0 } END { print m + 0 }' "$OUT/metrics.tsv"
}

# next_rep hands an interrupted rep's number out again, so drop what that
# attempt left: its grading worktree, its run dir, and any key row and patch.
clear_stale_rep() {
  local arm="$1" rep="$2" rundir="$3" key="$OUT/judging/KEY.tsv" id
  [ -d "$OUT/work.git" ] && drop_tree "$OUT/work.git" "$rundir/tree"
  rm -rf "$rundir"
  [ -f "$key" ] || return 0
  while IFS=$'\t' read -r id _; do
    rm -f "$OUT/judging/$id.patch"
  done < <(awk -F'\t' -v a="$arm" -v r="$rep" '$2 == a && $3 == r' "$key")
  awk -F'\t' -v a="$arm" -v r="$rep" '!($2 == a && $3 == r)' "$key" >"$key.tmp"
  mv "$key.tmp" "$key"
}

new_blind_id() {
  local id
  while :; do
    id="$(printf '%06x' $(((RANDOM << 15 | RANDOM) & 0xffffff)))"
    grep -q "^$id"$'\t' "$OUT/judging/KEY.tsv" 2>/dev/null || break
  done
  echo "$id"
}

harness_rev() {
  local rev
  rev="$(git rev-parse --short HEAD 2>/dev/null)" || { echo unknown; return; }
  git diff --quiet HEAD 2>/dev/null || rev="$rev-dirty"
  echo "$rev"
}

run_one() {
  local task="$1" i="$2" rep="$3"
  local arm="${ARM_NAMES[i]}" app="${ARM_APPS[i]}" flags="${ARM_FLAGS[i]}"
  local rundir="$OUT/runs/$arm/r$rep"
  local prefix="bench/$arm/r$rep/"
  local branch="$prefix$task" ref="refs/runs/$arm/r$rep"
  local -a extra=() nb=()
  read -ra extra <<<"$flags"
  [ "$BENCH_NO_BUILD" = "1" ] && nb=(--no-build)
  local issues="$rundir/issues"
  [[ "$issues" = /* ]] || issues="$PWD/$issues"

  local -a cmd=(
    "$BENCH_NIX" run "$app" -- dispatch "${nb[@]}" --yes
    --repo-slug "$BENCH_REPO_SLUG"
    --tracker local --local-dir "$issues"
    --forge-backend github
    --base-branch main --branch-prefix "$prefix"
    --merge-policy manual
    "${extra[@]}"
    "$task"
  )
  if [ -n "${BENCH_DRY_RUN:-}" ]; then
    printf '   DRY-RUN:' >&2; printf ' %q' "${cmd[@]}" >&2; printf '\n' >&2
    return 0
  fi

  clear_stale_rep "$arm" "$rep" "$rundir"
  mkdir -p "$issues"
  cp "$TASK_DIR/issue.md" "$issues/$task.md"
  info "$task [$arm r$rep] $app${flags:+ $flags}"
  reset_remote
  local host_log=".spindrift/logs/issue-$task.log" outbox=".spindrift/outbox/$task" fx rc=0
  # A stale bundle would carry this rep's branch name if the rep is a rerun.
  rm -rf "$host_log" .spindrift/logs/issue-"$task"-*.log "$outbox"
  "${cmd[@]}" >"$rundir/dispatch.out" 2>&1 || rc=$?
  info "$task [$arm r$rep] launcher exit=$rc"
  [ -f "$host_log" ] && cp "$host_log" "$rundir/box.log"
  for fx in .spindrift/logs/issue-"$task"-*.log; do
    [ -e "$fx" ] && cp "$fx" "$rundir/"
  done
  [ -f "$outbox/seam.bundle" ] && cp "$outbox/seam.bundle" "$rundir/"
  rm -rf "$outbox"

  # The remote branch comes first: CI fix passes land there after the
  # read-only Box's bundle handoff. The bundle covers a failed relay push.
  local grade=nobranch checks=skip got=""
  if git -C "$OUT/work.git" fetch -q "$BENCH_PUSH_URL" "+refs/heads/$branch:$ref" 2>/dev/null; then
    git -C "$BENCH_SRC" push -q --no-verify "$BENCH_PUSH_URL" --delete "$branch" 2>/dev/null || true
    got=remote
  elif [ -f "$rundir/seam.bundle" ] \
    && git -C "$OUT/work.git" fetch -q "$(cd "$rundir" && pwd)/seam.bundle" "+refs/heads/$branch:$ref" 2>/dev/null; then
    info "$task [$arm r$rep] branch never reached the remote; grading the outbox bundle"
    got=bundle
  fi
  if [ -n "$got" ]; then
    git -C "$OUT/work.git" diff "$BASE" "$ref" >"$rundir/diff.patch"
    prepare_tree "$OUT/work.git" "$ref" "$rundir/tree"
    if run_in "$rundir/tree" "$GRADE_CMD" "$rundir/grade.log"; then
      grade=pass
      if [ -n "$CHECK_CMD" ]; then
        if run_in "$rundir/tree" "$CHECK_CMD" "$rundir/checks.log"; then checks=pass; else checks=fail; fi
      fi
    else
      grade=fail
    fi
    drop_tree "$OUT/work.git" "$rundir/tree"
  else
    : >"$rundir/diff.patch"
  fi
  local resolved=0
  [ "$grade" = pass ] && [ "$checks" != fail ] && resolved=1

  local log="$rundir/box.log"
  [ -f "$log" ] || log=/dev/null
  local blind
  blind="$(new_blind_id)"
  cp "$rundir/diff.patch" "$OUT/judging/$blind.patch"
  printf '%s\t%s\t%s\n' "$blind" "$arm" "$rep" >>"$OUT/judging/KEY.tsv"
  cmd_breakdown "$log" >"$rundir/breakdown.tsv" 2>/dev/null || : >"$rundir/breakdown.tsv"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$task" "$arm" "$rep" "$blind" "$(parse_outcome "$log")" "$resolved" "$grade" "$checks" \
    "$(parse_usage "$log")" "$(parse_passes "$log")" "$(parse_verdicts "$log")" \
    "$(parse_decision "$log")" "$(harness_rev)" "$app${flags:+ $flags}" >>"$OUT/metrics.tsv"
  info "$task [$arm r$rep] grade=$grade checks=$checks resolved=$resolved"
}

cmd_run() {
  [ $# -ge 1 ] || die "usage: bench.sh run TASK [ARM...]"
  local task="$1"; shift
  load_task "$task"
  load_arms
  local -a picked=()
  local a i
  if [ $# -eq 0 ]; then
    picked=("${!ARM_NAMES[@]}")
  else
    for a in "$@"; do
      i="$(arm_index "$a")" || die "no arm '$a' in $BENCH_ARMS_FILE (have: ${ARM_NAMES[*]})"
      [[ " ${picked[*]} " != *" $i "* ]] || die "arm '$a' named twice; use BENCH_REPS for more runs"
      picked+=("$i")
    done
  fi
  [[ "$BENCH_REPS" =~ ^[1-9][0-9]*$ ]] || die "BENCH_REPS must be a positive integer"

  if [ -n "${BENCH_ENV_FILE:-}" ]; then
    [ -f "$BENCH_ENV_FILE" ] || die "BENCH_ENV_FILE '$BENCH_ENV_FILE' not found"
    set -a; # shellcheck disable=SC1090
    . "$BENCH_ENV_FILE"; set +a
  fi
  if [ -z "${BENCH_DRY_RUN:-}" ]; then
    command -v jq >/dev/null || die "jq not found on PATH"
    command -v git >/dev/null || die "git not found on PATH"
    command -v "$BENCH_NIX" >/dev/null || die "$BENCH_NIX not found on PATH"
    [ -e flake.nix ] || die "run this from the repo root (flake.nix not found in $PWD)"
  fi

  OUT="${BENCH_OUT:-bench/results/$task}"
  local names=""
  for i in "${picked[@]}"; do names+=" ${ARM_NAMES[i]}"; done
  cat >&2 <<EOF

  Benchmark — $task
  ---------------------------
  base / fix  : ${BASE:0:10} / ${FIX:0:10}
  arms        :$names
  reps        : $BENCH_REPS per arm
  bench repo  : $BENCH_REPO_SLUG (reset via $BENCH_PUSH_URL)
  out dir     : $OUT

  Each run is a real Box (real spend) and opens a draft PR on the bench repo.
  Before every run $BENCH_PUSH_URL is force-reset: main becomes BASE and
  every other branch and tag is DELETED.
EOF
  if [ -z "${BENCH_CONFIRM:-}" ] && [ -z "${BENCH_DRY_RUN:-}" ]; then
    local reply
    printf '\n  Type "yes" to proceed: ' >&2
    read -r reply
    [ "$reply" = "yes" ] || die "aborted"
  fi

  if [ -z "${BENCH_DRY_RUN:-}" ]; then
    mkdir -p "$OUT/runs" "$OUT/judging" "$OUT/logs"
    [ -f "$OUT/metrics.tsv" ] || printf '%s\n' "$METRICS_HEADER" >"$OUT/metrics.tsv"
    cp "$TASK_DIR/issue.md" "$OUT/judging/ISSUE.md"
    ensure_grading_repo "$OUT/work.git"
  fi

  if [ "$BENCH_NO_BUILD" = "1" ] && [ -z "${BENCH_DRY_RUN:-}" ]; then
    for i in "${picked[@]}"; do
      info "building ${ARM_NAMES[i]} (${ARM_APPS[i]}) once for every rep"
      "$BENCH_NIX" run "${ARM_APPS[i]}" -- build >"$OUT/logs/build-${ARM_NAMES[i]}.log" 2>&1 \
        || die "build failed for ${ARM_NAMES[i]}; see $OUT/logs/build-${ARM_NAMES[i]}.log"
    done
  fi

  # Shuffled so time-of-day and cache warmth don't line up with an arm.
  local -a plan=()
  local start r j tmp
  for i in "${picked[@]}"; do
    start="$(next_rep "${ARM_NAMES[i]}")"
    for ((r = 1; r <= BENCH_REPS; r++)); do plan+=("$i:$((start + r))"); done
  done
  for ((j = ${#plan[@]} - 1; j > 0; j--)); do
    r=$((RANDOM % (j + 1)))
    tmp="${plan[j]}" plan[j]="${plan[r]}" plan[r]="$tmp"
  done
  for a in "${plan[@]}"; do run_one "$task" "${a%%:*}" "${a#*:}"; done

  [ -z "${BENCH_DRY_RUN:-}" ] || return 0
  summarize "$task"
  info "done — $OUT/summary.md (judge $OUT/judging/*.patch before reading KEY.tsv)"
}

summarize() {
  local task="$1" m="$OUT/metrics.tsv"
  [ -f "$m" ] || die "no results at $m"
  # Arms-file order first, so the first arm (the control) is the delta
  # baseline; arms since dropped from the file follow in run order.
  local -a order=() ARM_NAMES=()
  [ -f "$BENCH_ARMS_FILE" ] && load_arms
  mapfile -t order < <({ printf '%s\n' "${ARM_NAMES[@]}"; awk -F'\t' 'NR > 1 { print $2 }' "$m"; } \
    | awk -v ran="$(awk -F'\t' 'NR > 1 { printf "%s ", $2 }' "$m")" '
        BEGIN { split(ran, r, " "); for (i in r) has[r[i]] = 1 }
        NF && has[$0] && !seen[$0]++')
  {
    echo "# Bench: $task"
    echo
    echo "BASE \`${BASE:0:10}\` · FIX \`${FIX:0:10}\` · $(($(wc -l <"$m") - 1)) run(s). Resolved means the hidden tests"
    echo "pass and CHECK_CMD (if set) does too. Cost is the Driver's own total_cost_usd."
    echo
    echo '| arm | runs | resolved | checks pass | ready | blocked | failed | none | other | cost $ mean | cost $ sd | minutes mean | out tok mean | passes mean |'
    echo '| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |'
    awk -F'\t' -v order="${order[*]}" '
      BEGIN { k = split(order, ord, " ") }
      NR == 1 { next }
      {
        a = $2; n[a]++; res[a] += $6; o[a, $5]++
        if ($8 == "pass") ck[a]++
        c[a] += $9; c2[a] += $9 * $9; dur[a] += $15; out[a] += $11; ps[a] += $16
      }
      END {
        for (i = 1; i <= k; i++) {
          a = ord[i]; mean = c[a] / n[a]
          sd = n[a] > 1 ? sqrt((c2[a] - n[a] * mean * mean) / (n[a] - 1)) : 0
          printf "| %s | %d | %d/%d | %d | %d | %d | %d | %d | %d | %.2f | %.2f | %.1f | %.0f | %.1f |\n", \
            a, n[a], res[a], n[a], ck[a], o[a, "ready"], o[a, "blocked"], o[a, "failed"], o[a, "none"], \
            n[a] - o[a, "ready"] - o[a, "blocked"] - o[a, "failed"] - o[a, "none"], \
            mean, sd, dur[a] / n[a] / 60000, out[a] / n[a], ps[a] / n[a]
        }
      }' "$m"
    awk -F'\t' '
      NR > 1 {
        if (!(($2, $20) in s)) { s[$2, $20] = 1; ns[$2]++ }
        if (!(($2, $19) in h)) { h[$2, $19] = 1; nh[$2]++ }
      }
      END {
        for (a in ns) if (ns[a] > 1) printf "\n!! arm %s ran under more than one spec; its rows are not comparable.\n", a
        for (a in nh) if (nh[a] > 1) printf "\n!! arm %s ran on more than one harness revision.\n", a
      }' "$m"
    echo
    echo "## Cost by role and model, mean per run"
    echo
    local -a args=()
    local arm dir
    for arm in "${order[@]}"; do
      for dir in "$OUT/runs/$arm"/r*; do
        [ -f "$dir/breakdown.tsv" ] && args+=("$arm=$dir/breakdown.tsv")
      done
    done
    [ ${#args[@]} -eq 0 ] || cmd_compare "${args[@]}"
    echo
    echo "Blind judging: score \`judging/*.patch\` against \`judging/ISSUE.md\` before reading \`judging/KEY.tsv\`."
  } >"$OUT/summary.md"
}

cmd_summarize() {
  [ $# -eq 1 ] || die "usage: bench.sh summarize TASK"
  load_task "$1"
  OUT="${BENCH_OUT:-bench/results/$1}"
  summarize "$1"
  info "wrote $OUT/summary.md"
}

main() {
  case "${1:-}" in
    --breakdown) shift; cmd_breakdown "$@" ;;
    --compare) shift; cmd_compare "$@" ;;
    new-task) shift; cmd_new_task "$@" ;;
    validate) shift; cmd_validate "$@" ;;
    run) shift; cmd_run "$@" ;;
    summarize) shift; cmd_summarize "$@" ;;
    *) sed -n '15,20p' "${BASH_SOURCE[0]}" | sed 's/^#  //' >&2; exit 2 ;;
  esac
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
