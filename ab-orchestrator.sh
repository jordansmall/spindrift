#!/usr/bin/env bash
# ab-orchestrator.sh — paired A/B experiment for the in-box orchestrator (issue #1627).
#
# For each issue you name, dispatches the SAME issue twice against a pinned
# image — once with the in-box orchestrator OFF (today's single-pass
# driver-exec) and once ON (the multi-pass loop) — then collects cost, tokens,
# pass/verdict/decision counts, terminal outcome, and the produced branch diff
# so you can compare the two arms and judge quality blind.
#
# It runs the FULL implement/review loop each arm but opens no PR and merges
# nothing (CODE_FORGE=git + MERGE_MODE=manual): each arm just pushes its branch
# to the throwaway remote you point it at. The only thing that differs between
# the two runs of an issue is ORCHESTRATOR_ENABLED — everything else (image,
# model, base commit, caps) is held fixed, so difference is attributable to the
# knob, not to drift. Arm order is randomised per issue for cache hygiene.
#
# WHAT THIS DOES NOT DO
#   - It does not judge quality for you. It stages the two diffs per issue under
#     an un-labelled judging/ bundle (variant-1/variant-2 + a separate KEY.tsv)
#     so a human or an LLM judge can score them blind against the issue's
#     acceptance criteria. Blind scoring is the whole point — don't peek at KEY.
#   - It does not decide anything. Pre-register your decision rule first, e.g.
#     "orchestrator earns more slices only if, on the context-heavy tail, it is
#     >= quality at <= cost AND its no-outcome rate is no worse."
#   - It does not pin the source commit for you. Run it from a checkout parked
#     at the commit you want to measure and do NOT pull between runs; the image
#     is built once up front and reused (--no-build) so both arms share it.
#
# SAFETY
#   Each run dispatches a real Box (real spend) and pushes a branch to
#   $AB_REMOTE. With ISSUE_TRACKER=github it ALSO mutates the tracker issue:
#   label swap (-> complete/failed) and a "## Run usage" comment, on every run.
#   So point AB_REMOTE and AB_REPO_SLUG at a THROWAWAY MIRROR repo, or set
#   ISSUE_TRACKER=local. The script refuses to start until you confirm.
#
# USAGE
#   ab-orchestrator.sh <issue> [<issue> ...]
#   AB_ISSUES="42 57" ab-orchestrator.sh
#
# REQUIRED ENV
#   AB_REMOTE       git remote URL branches are pushed to (CODE_FORGE_REMOTE_URL).
#                   MUST be a throwaway/mirror, not your primary origin.
#   AB_REPO_SLUG    owner/name the launcher reads issues from (REPO_SLUG).
#
# OPTIONAL ENV (defaults in parentheses)
#   AB_BASE         base branch/commit both arms branch from     (main)
#   AB_MODEL        implementor model                            (claude-sonnet-5)
#   AB_PREFIX_OFF   branch prefix for the OFF arm                (ab-off/issue-)
#   AB_PREFIX_ON    branch prefix for the ON arm                 (ab-on/issue-)
#   AB_OUTDIR       results directory              (./ab-results/<timestamp>)
#   AB_ENV_FILE     file to `source` for launcher secrets first  (harness.env if present)
#   AB_NO_BUILD     reuse the one up-front build via --no-build  (1)
#   AB_CONFIRM      set to 1 to skip the interactive confirmation (unset)
#   AB_DRY_RUN      print the launcher commands, don't dispatch   (unset)
#
# Secrets (GH_TOKEN[_CMD], CLAUDE_CODE_OAUTH_TOKEN[_CMD] or ANTHROPIC_API_KEY,
# GIT_USER_NAME, GIT_USER_EMAIL) are resolved by the launcher from the
# environment exactly as `dogfood.sh` expects — source your harness.env (or set
# AB_ENV_FILE) so they are present.

set -euo pipefail

# ---------------------------------------------------------------- helpers ----
info() { printf '==> %s\n' "$*" >&2; }
warn() { printf '!! %s\n' "$*" >&2; }
die()  { printf '!! %s\n' "$*" >&2; exit 1; }

# ------------------------------------------------------------------ config ----
AB_BASE="${AB_BASE:-main}"
AB_MODEL="${AB_MODEL:-claude-sonnet-5}"
AB_PREFIX_OFF="${AB_PREFIX_OFF:-ab-off/issue-}"
AB_PREFIX_ON="${AB_PREFIX_ON:-ab-on/issue-}"
AB_NO_BUILD="${AB_NO_BUILD:-1}"
AB_OUTDIR="${AB_OUTDIR:-./ab-results/$(date +%Y%m%d-%H%M%S)}"

# Collect issue numbers from args or AB_ISSUES.
issues=("$@")
if [ "${#issues[@]}" -eq 0 ] && [ -n "${AB_ISSUES:-}" ]; then
  # shellcheck disable=SC2206 # word-splitting a space/comma list is intended
  issues=(${AB_ISSUES//,/ })
fi
[ "${#issues[@]}" -gt 0 ] || die "no issues given (pass as args or set AB_ISSUES)"
for n in "${issues[@]}"; do
  [[ "$n" =~ ^[0-9]+$ ]] || die "issue '$n' is not a number"
done

# Source launcher secrets/env if asked (or a repo-local harness.env by default).
ab_env_file="${AB_ENV_FILE:-harness.env}"
if [ -n "${AB_ENV_FILE:-}" ] || [ -f "$ab_env_file" ]; then
  [ -f "$ab_env_file" ] || die "AB_ENV_FILE '$ab_env_file' not found"
  info "sourcing launcher env from $ab_env_file"
  set -a; # shellcheck disable=SC1090
  . "$ab_env_file"; set +a
fi

: "${AB_REMOTE:?set AB_REMOTE to a THROWAWAY remote URL (CODE_FORGE_REMOTE_URL)}"
: "${AB_REPO_SLUG:?set AB_REPO_SLUG to owner/name the launcher reads issues from}"

# The tracker slug must name the same repo the branches are pushed to, or the
# A/B reads/mutates one repo (the tracker) while pushing work to another — the
# subtle way you end up A/B-ing against the real repo. Compare and refuse.
remote_slug="$(sed -E 's#^(git@github\.com:|https?://github\.com/)##; s#\.git$##' <<<"$AB_REMOTE")"
if [ "$remote_slug" != "$AB_REPO_SLUG" ] && [ -z "${AB_ALLOW_SLUG_MISMATCH:-}" ]; then
  die "AB_REPO_SLUG ('$AB_REPO_SLUG') does not match the repo in AB_REMOTE ('$remote_slug') -- these must be the same repo, or the experiment targets a different tracker than it pushes to. Fix AB_REPO_SLUG, or set AB_ALLOW_SLUG_MISMATCH=1 if that split is deliberate."
fi

command -v jq  >/dev/null || die "jq not found on PATH"
command -v git >/dev/null || die "git not found on PATH"
command -v nix >/dev/null || die "nix not found on PATH"
[ -e flake.nix ] || die "run this from the repo root (flake.nix not found in $PWD)"

tracker="${ISSUE_TRACKER:-github}"

# --------------------------------------------------------- confirmation gate ----
cat >&2 <<EOF

  A/B experiment — orchestrator OFF vs ON
  ---------------------------------------
  issues        : ${issues[*]}
  base          : $AB_BASE
  model         : $AB_MODEL
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
if [ -z "${AB_CONFIRM:-}" ] && [ -z "${AB_DRY_RUN:-}" ]; then
  printf '\n  Type "yes" to proceed: ' >&2
  read -r reply
  [ "$reply" = "yes" ] || die "aborted"
fi

mkdir -p "$AB_OUTDIR/logs" "$AB_OUTDIR/judging"
summary_tsv="$AB_OUTDIR/metrics.tsv"
printf 'issue\tarm\toutcome\tcost_usd\tin_tok\tout_tok\tcache_read\tcache_create\tturns\tduration_ms\tpasses\tverdicts\tdecision\n' >"$summary_tsv"

# ---------------------------------------------------------- metric parsers ----
# Sum every "type":"result" event (orchestrator ON emits one per pass; the
# launcher's own comment keeps only the last, so summing is the honest total).
# jq -Rn + fromjson? tolerates the log's bare non-JSON lines (==>, SPINDRIFT_*).
parse_usage() { # $1=log  -> "cost in out cread ccreate turns ms"
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

# Orchestrator heartbeat ops (#2027); empty on the OFF arm.
parse_passes()  { jq -Rn '[inputs|fromjson?]|map(select(.type=="spindrift_op" and .spindrift_op.op=="pass_start"))|length' "$1" 2>/dev/null || echo 0; }
parse_verdicts(){ jq -Rn '[inputs|fromjson?]|map(select(.type=="spindrift_op" and .spindrift_op.op=="verdict")|.spindrift_op.verdict)|join(",")' "$1" 2>/dev/null | tr -d '"' || echo ""; }
parse_decision(){ jq -Rn '[inputs|fromjson?]|map(select(.type=="spindrift_op" and .spindrift_op.op=="decision")|"\(.spindrift_op.decision):\(.spindrift_op.reason)")|last // ""' "$1" 2>/dev/null | tr -d '"' || echo ""; }

# Terminal outcome: bare "SPINDRIFT_OUTCOME ... status=X" line, else no-outcome.
parse_outcome() { # $1=log -> ready|blocked|failed|none
  local line
  line="$(grep -a '^SPINDRIFT_OUTCOME ' "$1" 2>/dev/null | tail -1 || true)"
  if [ -z "$line" ]; then echo none; return; fi
  sed -n 's/.*status=\([^ ]*\).*/\1/p' <<<"$line" | head -1
}

# --------------------------------------------------------------- run an arm ----
run_arm() { # $1=issue $2=arm(off|on) $3=orch_value $4=branch_prefix
  local issue="$1" arm="$2" orch="$3" prefix="$4"
  local armdir="$AB_OUTDIR/$issue/$arm"
  mkdir -p "$armdir"
  local host_log="logs/issue-${issue}.log"
  # Clear any prior host log so we capture only this arm's stream.
  rm -f "$host_log"

  local -a nb=(); [ "$AB_NO_BUILD" = "1" ] && nb=(--no-build)

  info "issue #$issue [$arm] ORCHESTRATOR_ENABLED='${orch}' prefix='$prefix'"
  # Pass every knob as a --flag, not an env var: flags are set via os.Setenv
  # before the launcher's ambient-knob check, so they beat whatever harness.env
  # exported (which is how a read-only harness.env or a stray REPO_SLUG would
  # otherwise leak in). --box-forge-and-issue-access read-write is forced for
  # the same reason: a read-only harness.env is incompatible with CODE_FORGE=git.
  # --orchestrator-enabled "" (empty) on the OFF arm overrides harness.env=1.
  local -a cmd=(
    nix run ".#" -- dispatch "${nb[@]}" --yes
    --repo-slug "$AB_REPO_SLUG"
    --code-forge git
    --code-forge-remote-url "$AB_REMOTE"
    --base-branch "$AB_BASE"
    --branch-prefix "$prefix"
    --merge-mode manual
    --model "$AB_MODEL"
    --issue-tracker "$tracker"
    --box-forge-and-issue-access read-write
    --orchestrator-enabled "$orch"
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
  # Also grab fix-pass logs if any.
  for fx in logs/issue-"${issue}"-*.log; do
    [ -e "$fx" ] && cp "$fx" "$armdir/"
  done

  # Fetch the pushed branch + base and stage the diff (best-effort).
  local branch="${prefix}${issue}"
  if git fetch -q "$AB_REMOTE" "+${branch}:refs/ab/${arm}-${issue}" "+${AB_BASE}:refs/ab/base-${issue}" 2>/dev/null; then
    git diff "refs/ab/base-${issue}...refs/ab/${arm}-${issue}" >"$armdir/diff.patch" 2>/dev/null || true
  else
    warn "issue #$issue [$arm]: branch '$branch' not found on remote (no-outcome run pushes nothing?)"
    : >"$armdir/diff.patch"
  fi

  # Parse metrics from the captured log.
  local log="$armdir/box.log" outcome usage passes verdicts decision
  [ -f "$log" ] || log=/dev/null
  outcome="$(parse_outcome "$log")"
  usage="$(parse_usage "$log")"
  passes="$(parse_passes "$log")"
  verdicts="$(parse_verdicts "$log")"
  decision="$(parse_decision "$log")"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$issue" "$arm" "$outcome" "$usage" "$passes" "$verdicts" "$decision" >>"$summary_tsv"
}

# ------------------------------------------------------- build once, then run ----
if [ "$AB_NO_BUILD" = "1" ] && [ -z "${AB_DRY_RUN:-}" ]; then
  info "building the image once so both arms share it (pins the experiment)"
  nix run ".#" -- build >"$AB_OUTDIR/logs/build.log" 2>&1 \
    || die "up-front build failed; see $AB_OUTDIR/logs/build.log"
fi

for issue in "${issues[@]}"; do
  # Randomise arm order per issue (cache/warmth hygiene).
  if [ $((RANDOM % 2)) -eq 0 ]; then
    run_arm "$issue" off "" "$AB_PREFIX_OFF"
    run_arm "$issue" on  1  "$AB_PREFIX_ON"
  else
    run_arm "$issue" on  1  "$AB_PREFIX_ON"
    run_arm "$issue" off "" "$AB_PREFIX_OFF"
  fi

  # Blind judging bundle: neutral variant names + a separate un-blinding key.
  jdir="$AB_OUTDIR/judging/$issue"
  mkdir -p "$jdir"
  if [ $((RANDOM % 2)) -eq 0 ]; then v1=off; v2=on; else v1=on; v2=off; fi
  cp "$AB_OUTDIR/$issue/$v1/diff.patch" "$jdir/variant-1.patch" 2>/dev/null || true
  cp "$AB_OUTDIR/$issue/$v2/diff.patch" "$jdir/variant-2.patch" 2>/dev/null || true
  printf '%s\tvariant-1\t%s\n%s\tvariant-2\t%s\n' "$issue" "$v1" "$issue" "$v2" >>"$AB_OUTDIR/judging/KEY.tsv"
  # Stage the acceptance criteria for the judge, if gh is available.
  if command -v gh >/dev/null; then
    gh issue view "$issue" --repo "$AB_REPO_SLUG" --json title,body \
      --jq '"# #\(.number // "'"$issue"'") \(.title)\n\n\(.body)"' \
      >"$jdir/ISSUE.md" 2>/dev/null || true
  fi
done

# ----------------------------------------------------------------- summary ----
{
  echo "# Orchestrator A/B — OFF vs ON"
  echo
  echo "Base: \`$AB_BASE\` · Model: \`$AB_MODEL\` · Issues: ${issues[*]}"
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
  echo "Blind judging: score \`judging/<issue>/variant-{1,2}.patch\` against \`ISSUE.md\`"
  echo "WITHOUT reading \`judging/KEY.tsv\`, then un-blind with KEY.tsv."
} >"$AB_OUTDIR/summary.md"

info "done — results in $AB_OUTDIR"
info "  metrics : $summary_tsv"
info "  summary : $AB_OUTDIR/summary.md"
info "  judging : $AB_OUTDIR/judging/ (KEY.tsv is the un-blinding key — judge first)"
