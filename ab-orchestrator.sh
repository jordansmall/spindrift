#!/usr/bin/env bash

# Paired A/B experiment between two Harness apps (issue #2057's harness).
# Dispatches the same issue once through $AB_APP_CONTROL (default `.#`, the
# dogfood) and once through $AB_APP_VARIANT (default `.#ab-variant`, the
# dogfood with a haiku-5-5 worker and a sonnet-5-5 reviewer), both built from
# one parked commit, so a difference is attributable to what the two images
# bake rather than to drift. Subagent models are image-time (#5254), which is
# why the arms are separate apps rather than one image with a dispatch flag.
# Arm order is randomised per issue.

# Each arm runs the full implement/review loop but opens no PR and merges
# nothing (CODE_FORGE=git + MERGE_MODE=manual); it only pushes its branch. The
# script stages the two diffs under judging/ as variant-1 and variant-2 with a
# separate KEY.tsv, so score them blind before reading KEY.

# SAFETY: every run dispatches a real Box (real spend), pushes a branch to
# $AB_REMOTE, and under ISSUE_TRACKER=github also swaps the issue's labels and
# posts a "## Run usage" comment. Point AB_REMOTE and AB_REPO_SLUG at a
# throwaway mirror repo, or set ISSUE_TRACKER=local.

# Run this from a checkout parked at the commit you want to measure and do not
# pull between runs: each arm's image is built once up front and reused
# (--no-build).

# The launcher resolves its secrets (GH_TOKEN[_CMD], CLAUDE_CODE_OAUTH_TOKEN[_CMD]
# or ANTHROPIC_API_KEY, GIT_USER_NAME, GIT_USER_EMAIL) from the environment, so
# source your harness.env or point AB_ENV_FILE at it.

set -euo pipefail

info() { printf '==> %s\n' "$*" >&2; }
warn() { printf '!! %s\n' "$*" >&2; }
die()  { printf '!! %s\n' "$*" >&2; exit 1; }

AB_BASE="${AB_BASE:-main}"
# Empty keeps each image's baked coordinator; a value overrides both arms.
AB_MODEL="${AB_MODEL:-}"
AB_APP_CONTROL="${AB_APP_CONTROL:-.#}"
AB_APP_VARIANT="${AB_APP_VARIANT:-.#ab-variant}"
AB_PREFIX_CONTROL="${AB_PREFIX_CONTROL:-ab-control/issue-}"
AB_PREFIX_VARIANT="${AB_PREFIX_VARIANT:-ab-variant/issue-}"
AB_NO_BUILD="${AB_NO_BUILD:-1}"
AB_OUTDIR="${AB_OUTDIR:-./ab-results/$(date +%Y%m%d-%H%M%S)}"

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

# Public Claude list prices per MTok as of 2026-10; override via AB_PRICES.
# Keys are model-name prefixes matched against a message's "model" field
# (longest match wins); each value is [input, output, cache_read, cache_write]
# in $/MTok. cache_write is the 1h rate (2x input), since dispatches cache
# for 1h. A model with no matching prefix costs 0, but its tokens are still
# reported.
AB_DEFAULT_PRICES='{"claude-opus-5-5":[4,20,0.2,8],"claude-opus":[5,25,0.5,10],"claude-sonnet-5":[2,10,0.2,4],"claude-haiku-5-5":[0.1,0.5,0.01,0.2],"claude-haiku":[1,5,0.1,2]}'

# Per-role/model token and effective-cost TSV from a stream-json Box log (issue
# #2057), mirroring breakdownByRoleFile in the launcher's claude/usage.go: pass
# 1 maps each implementor-issued Task tool_use id to its subagent_type, pass 2
# attributes each assistant message to implementor (no parent_tool_use_id) or
# its parent's role. It also buckets by model, since pricing varies by model.
cmd_breakdown() {
  local log="$1"
  local prices_json="${AB_PRICES:-$AB_DEFAULT_PRICES}"
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

    def rate_for(model):
      model as $model
      | ($prices | to_entries
        | map(select(.key as $k | $model | startswith($k)))
        | sort_by(.key | length)
        | last) as $e
      | if $e == null then [0,0,0,0] else $e.value end;

    # Format a non-negative number to a fixed 6-decimal string (no scientific
    # notation, no float-formatting surprises on trailing zeros).
    def fmt6:
      (. * 1000000 | round) as $micros
      | ($micros / 1000000 | floor) as $whole
      | ($micros - ($whole * 1000000)) as $frac
      | "\($whole).\($frac | tostring | if length < 6 then ("0" * (6 - length)) + . else . end)";

    [inputs | fromjson?] as $all
    | ($all | map(select(.type == "assistant"))) as $asst
    | (reduce ($asst[] | select((.parent_tool_use_id // "") == "")) as $ev ({};
        reduce ($ev.message.content // [])[] as $b (.;
          if ($b.type == "tool_use" and $b.name == "Task" and (($b.id // "") != "")) then
            (($b.input.subagent_type // "") as $st
              | .[$b.id] = (if $st == "" then "subagent" else $st end))
          else . end)
      )) as $taskRole
    | ($asst | map(
        (.parent_tool_use_id // "") as $parent
        | (if $parent == "" then "implementor" else ($taskRole[$parent] // "subagent") end) as $role
        | (.message.model // "unknown") as $model
        | { role: $role, model: $model,
            cache_read:  (.message.usage.cache_read_input_tokens // 0),
            cache_write: (.message.usage.cache_creation_input_tokens // 0),
            fresh_input: (.message.usage.input_tokens // 0),
            output:      (.message.usage.output_tokens // 0) }
      )) as $rows
    | ($rows | group_by([.role, .model]) | map({
        role: .[0].role, model: .[0].model,
        cache_read:  (map(.cache_read)  | add),
        cache_write: (map(.cache_write) | add),
        fresh_input: (map(.fresh_input) | add),
        output:      (map(.output)      | add)
      })) as $buckets
    | ($buckets
        | sort_by([role_rank(.role), .role, .model])
        | map(
            rate_for(.model) as $r
            | (((.fresh_input * $r[0]) + (.output * $r[1]) + (.cache_read * $r[2]) + (.cache_write * $r[3])) / 1000000) as $cost
            | [ .role, .model, (.cache_read|tostring), (.cache_write|tostring), (.fresh_input|tostring), (.output|tostring), ($cost|fmt6) ]
            | join("\t")
          )
      )[]
  ' "$log"
}

# Side-by-side per-role/model breakdown diff between two --breakdown TSVs
# (issue #2057): a markdown table with both arms' rows for every (role, model)
# key seen, plus a per-model and total effective-$ delta (variant - control).
cmd_compare() {
  local control="$1" variant="$2"
  [ -f "$control" ] || control=/dev/null
  [ -f "$variant" ] || variant=/dev/null
  awk -F'\t' -v controlf="$control" -v variantf="$variant" '
    {
      arm = (FILENAME == controlf) ? "control" : "variant"
      key = $1 SUBSEP $2
      if (!(key in seen)) {
        seen[key] = 1
        order[++n] = key
        role[key] = $1
        model[key] = $2
      }
      cache_read[arm, key]  = $3
      cache_write[arm, key] = $4
      fresh_input[arm, key] = $5
      output[arm, key]      = $6
      cost[arm, key]        = $7

      if (!(($2) in mseen)) {
        mseen[$2] = 1
        morder[++m] = $2
      }
      mcost[arm, $2] += $7
      total[arm] += $7
    }
    END {
      print "| arm | role | model | cache_read | cache_write | fresh_input | output | cost $ |"
      print "| --- | --- | --- | --- | --- | --- | --- | --- |"
      for (i = 1; i <= n; i++) {
        key = order[i]
        for (j = 1; j <= 2; j++) {
          arm = (j == 1) ? "control" : "variant"
          printf "| %s | %s | %s | %d | %d | %d | %d | %.6f |\n", \
            arm, role[key], model[key], \
            cache_read[arm, key] + 0, cache_write[arm, key] + 0, \
            fresh_input[arm, key] + 0, output[arm, key] + 0, \
            cost[arm, key] + 0
        }
      }
      print ""
      print "## Effective-$ delta (variant - control)"
      print ""
      print "| model | control $ | variant $ | delta $ |"
      print "| --- | --- | --- | --- |"
      for (i = 1; i <= m; i++) {
        mo = morder[i]
        control_c = mcost["control", mo] + 0
        variant_c = mcost["variant", mo] + 0
        printf "| %s | %.6f | %.6f | %.6f |\n", mo, control_c, variant_c, variant_c - control_c
      }
      control_t = total["control"] + 0
      variant_t = total["variant"] + 0
      printf "| **total** | %.6f | %.6f | %.6f |\n", control_t, variant_t, variant_t - control_t
    }
  ' "$control" "$variant"
}

parse_outcome() { # Prints ready, blocked, failed, or none.
  local line
  line="$(grep -a '^SPINDRIFT_OUTCOME ' "$1" 2>/dev/null | tail -1 || true)"
  if [ -z "$line" ]; then echo none; return; fi
  sed -n 's/.*status=\([^ ]*\).*/\1/p' <<<"$line" | head -1
}

run_arm() {
  local issue="$1" arm="$2" prefix="$3" app="$4"
  local armdir="$AB_OUTDIR/$issue/$arm"
  mkdir -p "$armdir"
  local host_log=".spindrift/logs/issue-${issue}.log"
  # Clear any prior host log so this arm captures only its own stream.
  rm -f "$host_log"

  local -a nb=(); [ "$AB_NO_BUILD" = "1" ] && nb=(--no-build)
  local -a model=(); [ -n "$AB_MODEL" ] && model=(--model "$AB_MODEL")

  info "issue #$issue [$arm] app='$app' prefix='$prefix'"
  # Pass every knob as a --flag, not an env var: flags are set via os.Setenv
  # before the launcher's ambient-knob check, so they beat whatever harness.env
  # exported, which is how a stray REPO_SLUG would otherwise leak in.
  # --box-forge-and-issue-access read-write is forced for the same reason, since
  # a read-only harness.env is incompatible with CODE_FORGE=git.
  local -a cmd=(
    nix run "$app" -- dispatch "${nb[@]}" --yes
    --repo-slug "$AB_REPO_SLUG"
    --code-forge git
    --code-forge-remote-url "$AB_REMOTE"
    --base-branch "$AB_BASE"
    --branch-prefix "$prefix"
    --merge-mode manual
    "${model[@]}"
    --issue-tracker "$tracker"
    --box-forge-and-issue-access read-write
    "$issue"
  )
  if [ -n "${AB_DRY_RUN:-}" ]; then
    printf '   DRY-RUN:' >&2; printf ' %q' "${cmd[@]}" >&2; printf '\n' >&2
    return 0
  fi

  local rc=0
  "${cmd[@]}" >"$armdir/dispatch.out" 2>&1 || rc=$?
  info "issue #$issue [$arm] launcher exit=$rc"

  # Capture the host log before the next arm's run rotates it away.
  [ -f "$host_log" ] && cp "$host_log" "$armdir/box.log"
  # Fix-pass logs, if any.
  for fx in .spindrift/logs/issue-"${issue}"-*.log; do
    [ -e "$fx" ] && cp "$fx" "$armdir/"
  done

  local branch="${prefix}${issue}"
  if git fetch -q "$AB_REMOTE" "+${branch}:refs/ab/${arm}-${issue}" "+${AB_BASE}:refs/ab/base-${issue}" 2>/dev/null; then
    git diff "refs/ab/base-${issue}...refs/ab/${arm}-${issue}" >"$armdir/diff.patch" 2>/dev/null || true
  else
    warn "issue #$issue [$arm]: branch '$branch' not found on remote (no-outcome run pushes nothing?)"
    : >"$armdir/diff.patch"
  fi

  local log="$armdir/box.log" outcome usage passes verdicts decision
  [ -f "$log" ] || log=/dev/null
  outcome="$(parse_outcome "$log")"
  usage="$(parse_usage "$log")"
  passes="$(parse_passes "$log")"
  verdicts="$(parse_verdicts "$log")"
  decision="$(parse_decision "$log")"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$issue" "$arm" "$outcome" "$usage" "$passes" "$verdicts" "$decision" >>"$summary_tsv"

  # summary.md diffs the two arms' breakdown.tsv side by side via --compare.
  cmd_breakdown "$log" >"$armdir/breakdown.tsv" 2>/dev/null || : >"$armdir/breakdown.tsv"
}

main() {
  # These subcommands (issue #2057) run before the tool and required-env checks
  # below, so they work in a sandbox with only jq present and need none of the
  # A/B dispatch env.
  case "${1:-}" in
    --breakdown) shift; cmd_breakdown "$@"; return $? ;;
    --compare) shift; cmd_compare "$@"; return $? ;;
  esac

  if [ -z "${AB_DRY_RUN:-}" ]; then
    command -v jq  >/dev/null || die "jq not found on PATH"
    command -v git >/dev/null || die "git not found on PATH"
    command -v nix >/dev/null || die "nix not found on PATH"
    [ -e flake.nix ] || die "run this from the repo root (flake.nix not found in $PWD)"
  fi

  local -a issues=("$@")
  local n
  if [ "${#issues[@]}" -eq 0 ] && [ -n "${AB_ISSUES:-}" ]; then
    # shellcheck disable=SC2206 # word-splitting a space/comma list is intended
    issues=(${AB_ISSUES//,/ })
  fi
  [ "${#issues[@]}" -gt 0 ] || die "no issues given (pass as args or set AB_ISSUES)"
  for n in "${issues[@]}"; do
    [[ "$n" =~ ^[0-9]+$ ]] || die "issue '$n' is not a number"
  done

  local ab_env_file="${AB_ENV_FILE:-harness.env}"
  if [ -n "${AB_ENV_FILE:-}" ] || [ -f "$ab_env_file" ]; then
    [ -f "$ab_env_file" ] || die "AB_ENV_FILE '$ab_env_file' not found"
    info "sourcing launcher env from $ab_env_file"
    set -a; # shellcheck disable=SC1090
    . "$ab_env_file"; set +a
  fi

  : "${AB_REMOTE:?set AB_REMOTE to a THROWAWAY remote URL (CODE_FORGE_REMOTE_URL)}"
  : "${AB_REPO_SLUG:?set AB_REPO_SLUG to owner/name the launcher reads issues from}"

  # The tracker slug must name the same repo the branches are pushed to.
  # Otherwise the experiment reads and mutates one repo while pushing work to
  # another, which is the subtle way you end up A/B-ing against the real repo.
  local remote_slug
  remote_slug="$(sed -E 's#^(git@github\.com:|https?://github\.com/)##; s#\.git$##' <<<"$AB_REMOTE")"
  if [ "$remote_slug" != "$AB_REPO_SLUG" ] && [ -z "${AB_ALLOW_SLUG_MISMATCH:-}" ]; then
    die "AB_REPO_SLUG ('$AB_REPO_SLUG') does not match the repo in AB_REMOTE ('$remote_slug') -- these must be the same repo, or the experiment targets a different tracker than it pushes to. Fix AB_REPO_SLUG, or set AB_ALLOW_SLUG_MISMATCH=1 if that split is deliberate."
  fi

  local tracker="${ISSUE_TRACKER:-github}"

  cat >&2 <<EOF

  A/B experiment — control vs variant
  -----------------------------------
  issues        : ${issues[*]}
  base          : $AB_BASE
  control app   : $AB_APP_CONTROL
  variant app   : $AB_APP_VARIANT
  model         : ${AB_MODEL:-baked per image}
  push remote   : $AB_REMOTE
  repo slug     : $AB_REPO_SLUG
  issue tracker : $tracker
  out dir       : $AB_OUTDIR

  Each issue runs TWICE (2 real Boxes = real spend). Branches are pushed to the
  remote above. No PR is opened and nothing is merged (CODE_FORGE=git,
  MERGE_MODE=manual).
EOF
  if [ "$tracker" = "github" ]; then
    warn "ISSUE_TRACKER=github: every run will swap this issue's labels and post a"
    warn "'## Run usage' comment on $AB_REPO_SLUG. Use a throwaway mirror repo, or"
    warn "set ISSUE_TRACKER=local, if you do not want the real tracker mutated."
  fi
  local reply
  if [ -z "${AB_CONFIRM:-}" ] && [ -z "${AB_DRY_RUN:-}" ]; then
    printf '\n  Type "yes" to proceed: ' >&2
    read -r reply
    [ "$reply" = "yes" ] || die "aborted"
  fi

  mkdir -p "$AB_OUTDIR/logs" "$AB_OUTDIR/judging"
  local summary_tsv="$AB_OUTDIR/metrics.tsv"
  printf 'issue\tarm\toutcome\tcost_usd\tin_tok\tout_tok\tcache_read\tcache_create\tturns\tduration_ms\tpasses\tverdicts\tdecision\n' >"$summary_tsv"

  if [ "$AB_NO_BUILD" = "1" ] && [ -z "${AB_DRY_RUN:-}" ]; then
    info "building each arm's image once so every issue reuses it (pins the experiment)"
    nix run "$AB_APP_CONTROL" -- build >"$AB_OUTDIR/logs/build-control.log" 2>&1 \
      || die "up-front control build failed; see $AB_OUTDIR/logs/build-control.log"
    nix run "$AB_APP_VARIANT" -- build >"$AB_OUTDIR/logs/build-variant.log" 2>&1 \
      || die "up-front variant build failed; see $AB_OUTDIR/logs/build-variant.log"
  fi

  local issue jdir v1 v2
  for issue in "${issues[@]}"; do
    # Randomise arm order per issue for cache hygiene.
    if [ $((RANDOM % 2)) -eq 0 ]; then
      run_arm "$issue" control "$AB_PREFIX_CONTROL" "$AB_APP_CONTROL"
      run_arm "$issue" variant "$AB_PREFIX_VARIANT" "$AB_APP_VARIANT"
    else
      run_arm "$issue" variant "$AB_PREFIX_VARIANT" "$AB_APP_VARIANT"
      run_arm "$issue" control "$AB_PREFIX_CONTROL" "$AB_APP_CONTROL"
    fi

    # Blind judging bundle: neutral candidate names and a separate un-blinding key.
    jdir="$AB_OUTDIR/judging/$issue"
    mkdir -p "$jdir"
    if [ $((RANDOM % 2)) -eq 0 ]; then v1=control; v2=variant; else v1=variant; v2=control; fi
    cp "$AB_OUTDIR/$issue/$v1/diff.patch" "$jdir/candidate-1.patch" 2>/dev/null || true
    cp "$AB_OUTDIR/$issue/$v2/diff.patch" "$jdir/candidate-2.patch" 2>/dev/null || true
    printf '%s\tcandidate-1\t%s\n%s\tcandidate-2\t%s\n' "$issue" "$v1" "$issue" "$v2" >>"$AB_OUTDIR/judging/KEY.tsv"
    # Stage the issue text so the judge has acceptance criteria to score against.
    if command -v gh >/dev/null; then
      gh issue view "$issue" --repo "$AB_REPO_SLUG" --json title,body \
        --jq '"# #\(.number // "'"$issue"'") \(.title)\n\n\(.body)"' \
        >"$jdir/ISSUE.md" 2>/dev/null || true
    fi
  done

  {
    echo "# Model A/B — control vs variant"
    echo
    echo "Base: \`$AB_BASE\` · Control: \`$AB_APP_CONTROL\` · Variant: \`$AB_APP_VARIANT\` · Model: \`${AB_MODEL:-baked}\` · Issues: ${issues[*]}"
    echo
    echo '| issue | arm | outcome | cost $ | out tok | passes | verdicts | final decision |'
    echo '| --- | --- | --- | --- | --- | --- | --- | --- |'
    # columns: 1 issue 2 arm 3 outcome 4 cost 5 in 6 out 7 cr 8 cc 9 turns 10 ms 11 passes 12 verdicts 13 decision
    tail -n +2 "$summary_tsv" | awk -F'\t' \
      '{printf "| %s | %s | %s | %s | %s | %s | %s | %s |\n",$1,$2,$3,$4,$6,$11,$12,$13}'
    echo
    echo "## No-outcome (the #2036 failure) count per arm"
    tail -n +2 "$summary_tsv" | awk -F'\t' '$3=="none"{c[$2]++} END{for(a in c) printf "- %s: %d\n",a,c[a]; if(length(c)==0) print "- none"}'
    echo

    echo "## Per-model token + effective-cost breakdown, with delta (variant - control)"
    for issue in "${issues[@]}"; do
      controlbd="$AB_OUTDIR/$issue/control/breakdown.tsv"
      variantbd="$AB_OUTDIR/$issue/variant/breakdown.tsv"
      if [ -s "$controlbd" ] || [ -s "$variantbd" ]; then
        echo
        echo "### issue #$issue"
        cmd_compare "$controlbd" "$variantbd"
      fi
    done
    echo

    echo "Blind judging: score \`judging/<issue>/candidate-{1,2}.patch\` against \`ISSUE.md\`"
    echo "WITHOUT reading \`judging/KEY.tsv\`, then un-blind with KEY.tsv."
  } >"$AB_OUTDIR/summary.md"

  info "done — results in $AB_OUTDIR"
  info "  metrics : $summary_tsv"
  info "  summary : $AB_OUTDIR/summary.md"
  info "  judging : $AB_OUTDIR/judging/ (KEY.tsv is the un-blinding key — judge first)"
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
