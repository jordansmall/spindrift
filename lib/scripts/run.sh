# Lists open $LABEL issues on the target repo and fans out one disposable
# container per issue, capped at MAX_PARALLEL; each clones fresh and opens a PR.
#
# Body fragment: nix (lib/mkHarness.nix) wraps it with writeShellApplication,
# prepending the shebang, `set -euo pipefail`, the pinned runtimeInputs PATH, and
# a preamble defining the baked config (IMAGE_ARCHIVE, IMAGE_TAG, RUNTIME, the
# run defaults as `NAME="${NAME:-<default>}"`, PROMPT_DIR). A matching env var —
# or harness.env below — therefore wins at runtime.

# Config + secrets (gitignored), read from $PWD since the harness is a store path
# with no working tree. `set -a` makes its assignments override the baked defaults.
if [ -f "$PWD/harness.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$PWD/harness.env"
  set +a
fi

# Variables defined by the OCI preamble; absent (empty) in bwrap mode.
IMAGE_ARCHIVE="${IMAGE_ARCHIVE:-}"
IMAGE_TAG="${IMAGE_TAG:-spindrift:latest}"
IMAGE="${IMAGE:-$IMAGE_TAG}"

# Variables defined by the bwrap preamble (imagePreamble + bwrapRunPreamble);
# absent (empty) in OCI mode. The runtime guard in run_one() ensures they are
# never dereferenced on the wrong path.
AGENT_FILES="${AGENT_FILES:-}"
AGENT_ENV="${AGENT_ENV:-}"
BAKED_PREFETCH="${BAKED_PREFETCH:-}"

# OCI path: the prompt is baked into the image at /agent/prompts. Point
# SPINDRIFT_PROMPT_DIR at a real directory to bind-mount an override on top of
# it and iterate on the prompt with zero rebuilds. Empty by default so the box
# runs the baked prompt — which is what lets it work where the host /nix/store
# is not visible to the container runtime (e.g. a macOS podman machine).
# bwrap path: the baked prompt lives at ${AGENT_FILES}/agent/prompts, which is
# already bind-mounted at /agent; SPINDRIFT_PROMPT_DIR is handled inside
# run_one_bwrap() where the bwrap-specific --ro-bind flag is available.
prompt_args=()
if [ "$RUNTIME" != "bwrap" ] && [ -n "${SPINDRIFT_PROMPT_DIR:-}" ] && [ -d "$SPINDRIFT_PROMPT_DIR" ]; then
  echo "==> SPINDRIFT_PROMPT_DIR set; mounting $SPINDRIFT_PROMPT_DIR over the baked prompt"
  prompt_args=(-v "$SPINDRIFT_PROMPT_DIR:/agent/prompts:ro")
fi

# Commit identity: explicit override wins, else inherit the host's git config.
# Required — there is no built-in default.
GIT_USER_NAME="${GIT_USER_NAME:-$(git config --get user.name 2>/dev/null || true)}"
GIT_USER_EMAIL="${GIT_USER_EMAIL:-$(git config --get user.email 2>/dev/null || true)}"

: "${REPO_SLUG:?set REPO_SLUG=owner/repo (the target GitHub repository)}"
: "${GIT_USER_NAME:?set GIT_USER_NAME, or configure git user.name on the host}"
: "${GIT_USER_EMAIL:?set GIT_USER_EMAIL, or configure git user.email on the host}"
: "${GH_TOKEN:?set GH_TOKEN (fine-grained PAT scoped to the single target repo: Issues RW, Contents RW, Pull requests RW, Metadata R)}"
if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY" >&2
  exit 1
fi

command -v "$RUNTIME" >/dev/null 2>&1 || {
  echo "$RUNTIME not found on PATH." >&2
  exit 1
}

# Build and load the image on demand (OCI path only; bwrap has no image).
# build_box_image is defined by the OCI preamble (build-image.sh).
if [ "$RUNTIME" != "bwrap" ]; then
  if ! "$RUNTIME" image exists "$IMAGE"; then
    echo "==> image '$IMAGE' not found — building first"
    build_box_image
  fi
fi

auth_args=()
[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && auth_args+=(-e CLAUDE_CODE_OAUTH_TOKEN)
[ -n "${ANTHROPIC_API_KEY:-}" ] && auth_args+=(-e ANTHROPIC_API_KEY)

git_args=(-e "GIT_USER_NAME=$GIT_USER_NAME" -e "GIT_USER_EMAIL=$GIT_USER_EMAIL")

echo "==> querying open '$LABEL' issues in $REPO_SLUG"
# Dispatch oldest-first (age order). gh returns newest-first; sort_by(.number)
# is ascending, and issue numbers are monotonic in creation. Dependency waves
# then reorder within this by blocker readiness.
issues_tsv="$(gh issue list \
  --repo "$REPO_SLUG" --state open --label "$LABEL" --limit 100 \
  --json number,title --jq 'sort_by(.number) | .[] | [.number, .title] | @tsv')"

if [ -z "$issues_tsv" ]; then
  echo "no open '$LABEL' issues — nothing to do."
  exit 0
fi

# MAX_JOBS caps how many *unblocked* issues one invocation drains (0 = no limit).
# The selection happens after the dependency graph is built (below) so a blocked
# oldest issue is skipped rather than blindly head-capped — dogfooding sets
# MAX_JOBS=1 so each invocation drains a single unblocked issue, letting an outer
# loop git-pull the merged change and rebuild before the next.
MAX_JOBS="${MAX_JOBS:-0}"

count="$(printf '%s\n' "$issues_tsv" | wc -l | tr -d ' ')"
echo "==> $count issue(s); launching up to $MAX_PARALLEL container(s) at a time"
mkdir -p "$PWD/logs"

# Move an issue between lifecycle labels. Best-effort: `set -e` is active, so a
# labelling hiccup must not abort the run — warn and carry on.
swap_label() {
  local num="$1" add="$2" remove="$3"
  gh issue edit "$num" --repo "$REPO_SLUG" \
    --add-label "$add" --remove-label "$remove" >/dev/null 2>&1 ||
    echo "    ?? #$num: could not set label '$add' (remove '$remove')" >&2
}

run_one_bwrap() {
  local num="$1" title="$2"
  local log="$PWD/logs/issue-$num.log"
  echo "    -> #$num: $title"

  # Provide minimal /etc for uid mapping so the agent runs as uid 1000 inside
  # the sandbox regardless of the host uid (Claude Code refuses
  # --dangerously-skip-permissions under root).
  local etc_dir
  etc_dir="$(mktemp -d)"
  printf 'root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1000:agent:/home/agent:/bin/bash\n' \
    >"$etc_dir/passwd"
  printf 'root:x:0:\nagent:x:1000:\n' >"$etc_dir/group"

  # Bind-mount /etc/resolv.conf for DNS if it exists on the host.
  local resolv_args=()
  [ -f /etc/resolv.conf ] && resolv_args=(--ro-bind /etc/resolv.conf /etc/resolv.conf)

  # Prompt override: SPINDRIFT_PROMPT_DIR shadows the baked prompt at /agent/prompts.
  local bwrap_prompt_args=()
  if [ -n "${SPINDRIFT_PROMPT_DIR:-}" ] && [ -d "$SPINDRIFT_PROMPT_DIR" ]; then
    echo "==> SPINDRIFT_PROMPT_DIR set; mounting $SPINDRIFT_PROMPT_DIR over the baked prompt"
    bwrap_prompt_args=(--ro-bind "$SPINDRIFT_PROMPT_DIR" /agent/prompts)
  fi

  local bwrap_auth_args=()
  [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && bwrap_auth_args+=(--setenv CLAUDE_CODE_OAUTH_TOKEN "$CLAUDE_CODE_OAUTH_TOKEN")
  [ -n "${ANTHROPIC_API_KEY:-}" ] && bwrap_auth_args+=(--setenv ANTHROPIC_API_KEY "$ANTHROPIC_API_KEY")

  if bwrap \
    --ro-bind /nix/store /nix/store \
    --tmpfs /tmp \
    --tmpfs /work \
    --tmpfs /home/agent \
    --proc /proc \
    --dev /dev \
    --dir /etc \
    --ro-bind "$etc_dir/passwd" /etc/passwd \
    --ro-bind "$etc_dir/group" /etc/group \
    ${resolv_args[@]+"${resolv_args[@]}"} \
    --ro-bind "$AGENT_FILES/agent" /agent \
    ${bwrap_prompt_args[@]+"${bwrap_prompt_args[@]}"} \
    --clearenv \
    --setenv HOME /home/agent \
    --setenv PATH "$AGENT_ENV/bin" \
    --setenv SSL_CERT_FILE "$AGENT_ENV/etc/ssl/certs/ca-bundle.crt" \
    --setenv GIT_SSL_CAINFO "$AGENT_ENV/etc/ssl/certs/ca-bundle.crt" \
    --setenv GH_TOKEN "$GH_TOKEN" \
    "${bwrap_auth_args[@]}" \
    --setenv GIT_USER_NAME "$GIT_USER_NAME" \
    --setenv GIT_USER_EMAIL "$GIT_USER_EMAIL" \
    --setenv REPO_SLUG "$REPO_SLUG" \
    --setenv ISSUE_NUMBER "$num" \
    --setenv ISSUE_TITLE "$title" \
    --setenv BASE_BRANCH "$BASE_BRANCH" \
    --setenv BRANCH_PREFIX "$BRANCH_PREFIX" \
    --setenv MODEL "$MODEL" \
    --setenv SCOUT_MODEL "$SCOUT_MODEL" \
    --setenv REVIEW_MODEL "$REVIEW_MODEL" \
    --setenv IN_PROGRESS_LABEL "$IN_PROGRESS_LABEL" \
    --setenv COMPLETE_LABEL "$COMPLETE_LABEL" \
    --setenv PREFETCH "$BAKED_PREFETCH" \
    --unshare-user --uid 1000 --gid 1000 \
    --unshare-pid --unshare-ipc --unshare-uts \
    -- /agent/entrypoint.sh >"$log" 2>&1; then
    echo "    <- #$num done  (logs/issue-$num.log)"
  else
    echo "    !! #$num FAILED (logs/issue-$num.log)"
    swap_label "$num" "$FAILED_LABEL" "$IN_PROGRESS_LABEL"
  fi
  rm -rf "$etc_dir"
}

run_one() {
  local num="$1" title="$2"
  if [ "$RUNTIME" = "bwrap" ]; then
    run_one_bwrap "$num" "$title"
    return
  fi
  local log="$PWD/logs/issue-$num.log"
  echo "    -> #$num: $title"
  # `--rm` only fires on a clean exit; an interrupted prior run (Ctrl-C, reboot,
  # OOM) leaves the named container behind and the next `--name` collides. Reap
  # any stale one first. `rm -f` is a no-op when absent and works on both
  # runtimes (podman `--replace` would be podman-only).
  "$RUNTIME" rm -f "agent-issue-$num" >/dev/null 2>&1 || true
  if "$RUNTIME" run --rm \
    --name "agent-issue-$num" \
    -e GH_TOKEN "${auth_args[@]}" "${git_args[@]}" \
    -e REPO_SLUG="$REPO_SLUG" \
    -e ISSUE_NUMBER="$num" \
    -e ISSUE_TITLE="$title" \
    -e BASE_BRANCH="$BASE_BRANCH" \
    -e BRANCH_PREFIX="$BRANCH_PREFIX" \
    -e MODEL="$MODEL" \
    -e SCOUT_MODEL="$SCOUT_MODEL" \
    -e REVIEW_MODEL="$REVIEW_MODEL" \
    -e IN_PROGRESS_LABEL="$IN_PROGRESS_LABEL" \
    -e COMPLETE_LABEL="$COMPLETE_LABEL" \
    ${prompt_args[@]+"${prompt_args[@]}"} \
    "$IMAGE" /agent/entrypoint.sh >"$log" 2>&1; then
    # Success needs no terminal label — the merged PR's `Closes #N` closes it.
    echo "    <- #$num done  (logs/issue-$num.log)"
  else
    # Hand failures to human triage; re-labelling $LABEL retries. No auto-retry.
    echo "    !! #$num FAILED (logs/issue-$num.log)"
    swap_label "$num" "$FAILED_LABEL" "$IN_PROGRESS_LABEL"
  fi
}

# --- Dependency-wave helpers -------------------------------------------

# Scans each ready issue's body for blocker references ("depends on #N" or
# "blocked by #N", case-insensitive) and prints "child blocker" pairs to stdout.
parse_blockers() {
  local num body
  while IFS=$'\t' read -r num _; do
    [ -n "$num" ] || continue
    body="$(gh issue view "$num" --repo "$REPO_SLUG" \
      --json body --jq '.body // ""' 2>/dev/null || true)"
    [ -n "$body" ] || continue
    # Two blocker shapes: inline ("depends on #N" / "blocked by #N") and the
    # /to-issues filed format — a "## Blocked by" header followed by "- #N" list
    # items (a list line may name several: "- #56 (...) and #57 (...)"). The old
    # single-line regex matched only the inline shape, so header+list edges — the
    # format /to-issues actually files — went undetected. `|| true` keeps a
    # blocker-less body from aborting the run under pipefail + set -e.
    printf '%s\n' "$body" \
      | awk '
          function emit(str) {
            while (match(str, /#[0-9]+/)) {
              print substr(str, RSTART + 1, RLENGTH - 1)
              str = substr(str, RSTART + RLENGTH)
            }
          }
          {
            low = tolower($0)
            if (low ~ /^#+[ \t]*blocked by[ \t]*:?[ \t]*$/) { insec = 1; next }
            if (low ~ /^#+[ \t]+/) insec = 0
            s = low
            # emit() calls match(), clobbering the global RSTART/RLENGTH; the
            # final failed match leaves RSTART+RLENGTH = -1 and substr(s, -1)
            # re-yields the whole string — an infinite loop. Save the cursor
            # before emitting.
            while (match(s, /(depends on|blocked by)[ \t]*:?[ \t]*#[0-9]+/)) {
              seg = substr(s, RSTART, RLENGTH)
              nxt = RSTART + RLENGTH
              emit(seg)
              s = substr(s, nxt)
            }
            if (insec && $0 ~ /^[ \t]*[-*][ \t]*#[0-9]+/) emit($0)
          }
        ' \
      | sort -u \
      | while IFS= read -r dep; do
          [ -n "$dep" ] && printf '%s %s\n' "$num" "$dep"
        done || true
  done <<EOF
$issues_tsv
EOF
}

# Detects intra-batch cycles via Kahn's algorithm. Prints a cycle-member issue
# number and returns 1 if a cycle exists; returns 0 for an acyclic graph.
detect_cycle() {
  local deps_file="$1"
  local batch_nums cycle_member rc
  batch_nums="$(printf '%s\n' "$issues_tsv" | awk -F'\t' '{if ($1) print $1}')"
  cycle_member="$(printf '%s\n' "$batch_nums" | awk -v df="$deps_file" '
    { batch[$1] = 1 }
    END {
      while ((getline line < df) > 0) {
        n = split(line, f)
        if (n >= 2 && (f[1] in batch) && (f[2] in batch)) {
          indegree[f[1]]++
          adj[f[2]] = adj[f[2]] " " f[1]
          if (!(f[2] in indegree)) indegree[f[2]] = 0
        }
      }
      for (nd in batch) if (!(nd in indegree)) indegree[nd] = 0
      qs = 0
      for (nd in batch) if (indegree[nd] == 0) queue[++qs] = nd
      done = 0
      while (qs > 0) {
        node = queue[qs--]; done++
        m = split(adj[node], ch)
        for (i = 1; i <= m; i++)
          if (ch[i] != "" && --indegree[ch[i]] == 0) queue[++qs] = ch[i]
      }
      for (nd in batch) if (indegree[nd] > 0) { print nd; exit 1 }
      exit 0
    }
  ')"
  rc=$?
  [ "$rc" -eq 0 ] && return 0
  printf '%s\n' "$cycle_member"
  return 1
}

# Returns 0 (blocked) if any blocker of issue $1 lacks $COMPLETE_LABEL on
# GitHub; returns 1 (unblocked) when all blockers are complete or there are none.
# Reads edges from the global $DEPS_FILE.
is_blocked() {
  local num="$1" dep
  local dep_nums
  dep_nums="$(grep "^${num} " "$DEPS_FILE" | awk '{print $2}' || true)"
  [ -n "$dep_nums" ] || return 1
  while IFS= read -r dep; do
    [ -n "$dep" ] || continue
    if ! gh issue view "$dep" --repo "$REPO_SLUG" --json labels \
        --jq '.labels[].name' 2>/dev/null | grep -qxF "$COMPLETE_LABEL"; then
      return 0
    fi
  done <<EOF
$dep_nums
EOF
  return 1
}

# Dispatches all issues in remaining_file in dependency order: issues with no
# pending blockers fan out in parallel (up to MAX_PARALLEL); blocked issues are
# held and rechecked every DEPS_POLL_SECS seconds. Errors out after
# DEPS_WAIT_SECS seconds without progress (surfaces cycles/stalls).
dispatch_waves() {
  local remaining_file="$1"
  local wait_secs="${DEPS_WAIT_SECS:-7200}"
  local poll_secs="${DEPS_POLL_SECS:-30}"
  local elapsed=0 ready_file

  while [ -s "$remaining_file" ]; do
    ready_file="$PWD/logs/ready.$$"
    : >"$ready_file"

    while IFS=$'\t' read -r num title; do
      [ -n "$num" ] || continue
      if ! is_blocked "$num"; then
        printf '%s\t%s\n' "$num" "$title" >>"$ready_file"
      fi
    done <"$remaining_file"

    if [ ! -s "$ready_file" ]; then
      rm -f "$ready_file"
      if [ "$elapsed" -ge "$wait_secs" ]; then
        echo "ERROR: dependency deadlock — blockers did not reach '$COMPLETE_LABEL' after ${wait_secs}s" >&2
        cat "$remaining_file" >&2
        return 1
      fi
      echo "    .. all remaining issues blocked; retrying in ${poll_secs}s (${elapsed}s elapsed)"
      sleep "$poll_secs"
      elapsed=$((elapsed + poll_secs))
      continue
    fi

    elapsed=0
    while IFS=$'\t' read -r num title; do
      [ -n "$num" ] || continue
      swap_label "$num" "$IN_PROGRESS_LABEL" "$LABEL"
      run_one "$num" "$title" &
      while [ "$(jobs -r | wc -l | tr -d ' ')" -ge "$MAX_PARALLEL" ]; do sleep 1; done
    done <"$ready_file"
    wait

    while IFS=$'\t' read -r num _; do
      [ -n "$num" ] || continue
      awk -F'\t' -v n="$num" 'NF > 0 && $1 != n' "$remaining_file" \
        >"${remaining_file}.tmp" \
        && mv "${remaining_file}.tmp" "$remaining_file"
    done <"$ready_file"
    rm -f "$ready_file"
  done
}

# Reads each per-issue log for its SPINDRIFT_OUTCOME line and prints a roll-up.
# For issues that claim to be merged, independently verifies the PR state and
# issue label against GitHub rather than trusting the container's self-report.
print_outcome_report() {
  echo "==> outcome report"
  while IFS=$'\t' read -r num _; do
    [ -n "$num" ] || continue
    local log="$PWD/logs/issue-$num.log"
    local outcome_line=""
    [ -f "$log" ] && outcome_line="$(grep '^SPINDRIFT_OUTCOME ' "$log" 2>/dev/null | tail -1 || true)"
    if [ -z "$outcome_line" ]; then
      printf '    #%s  status=missing  note=no SPINDRIFT_OUTCOME in log\n' "$num"
      continue
    fi
    local pr status note
    pr="$(printf '%s' "$outcome_line" | grep -oE 'pr=[^ ]+' | cut -d= -f2-)"
    status="$(printf '%s' "$outcome_line" | grep -oE 'status=[^ ]+' | cut -d= -f2-)"
    note="$(printf '%s' "$outcome_line" | sed 's/.*note=//')"
    if [ "$status" = "blocked" ]; then
      printf '    #%s  pr=%s  status=%s  !! %s\n' "$num" "$pr" "$status" "$note"
    elif [ "$status" = "merged" ]; then
      local pr_state issue_labels
      pr_state="$(gh pr view "$pr" --json state --jq '.state' 2>/dev/null || true)"
      issue_labels="$(gh issue view "$num" --repo "$REPO_SLUG" --json labels \
        --jq '.labels[].name' 2>/dev/null || true)"
      if [ "$pr_state" = "MERGED" ] && \
         printf '%s\n' "$issue_labels" | grep -qxF "$COMPLETE_LABEL"; then
        printf '    #%s  pr=%s  status=verified-merged\n' "$num" "$pr"
      else
        local reason
        if [ "$pr_state" != "MERGED" ]; then
          reason="PR state is '${pr_state:-unknown}', expected MERGED"
        else
          reason="issue does not carry '$COMPLETE_LABEL'"
        fi
        printf '    #%s  pr=%s  status=failed  !! %s\n' "$num" "$pr" "$reason"
        swap_label "$num" "$FAILED_LABEL" "$IN_PROGRESS_LABEL"
      fi
    else
      printf '    #%s  pr=%s  status=%s\n' "$num" "$pr" "$status"
    fi
  done <<EOF
$issues_tsv
EOF
}

# Build the dependency graph for the ready batch, then dispatch in waves when
# edges are present or fall through to the original single-wave fan-out.
DEPS_FILE="$PWD/logs/deps.$$"
trap 'rm -f "$DEPS_FILE"' EXIT
parse_blockers >"$DEPS_FILE"

# MAX_JOBS > 0: drain up to N currently-unblocked issues, then exit — no in-
# process polling. A blocked oldest issue is skipped (never dispatched against an
# un-merged blocker, nor left stalling the slot); it waits for a later
# invocation, after the outer loop has merged its blocker and rebuilt. Blocker
# completion thus happens across invocations, which is the dogfood cadence.
if [ "$MAX_JOBS" -gt 0 ]; then
  if [ -s "$DEPS_FILE" ]; then
    cycle_num=""
    if ! cycle_num="$(detect_cycle "$DEPS_FILE")"; then
      echo "ERROR: dependency cycle detected (issue #${cycle_num} is in the cycle)" >&2
      exit 1
    fi
  fi
  selected=""
  selected_count=0
  while IFS=$'\t' read -r num title; do
    [ -n "$num" ] || continue
    if is_blocked "$num"; then
      echo "    ~~ #$num blocked (a blocker is not '$COMPLETE_LABEL'); skipping"
      continue
    fi
    selected="$selected$(printf '%s\t%s' "$num" "$title")
"
    selected_count=$((selected_count + 1))
    [ "$selected_count" -ge "$MAX_JOBS" ] && break
  done <<EOF
$issues_tsv
EOF
  if [ "$selected_count" -eq 0 ]; then
    echo "no unblocked '$LABEL' issues to drain — nothing to do."
    exit 0
  fi
  issues_tsv="$selected"
  echo "==> draining $selected_count unblocked issue(s) (MAX_JOBS=$MAX_JOBS)"
  while IFS=$'\t' read -r num title; do
    [ -n "$num" ] || continue
    swap_label "$num" "$IN_PROGRESS_LABEL" "$LABEL"
    run_one "$num" "$title" &
    while [ "$(jobs -r | wc -l | tr -d ' ')" -ge "$MAX_PARALLEL" ]; do sleep 1; done
  done <<EOF
$issues_tsv
EOF
  wait
  print_outcome_report
  echo "==> all agents finished — branches pushed and PRs opened on $REPO_SLUG."
  exit 0
fi

if [ -s "$DEPS_FILE" ]; then
  cycle_num=""
  if ! cycle_num="$(detect_cycle "$DEPS_FILE")"; then
    echo "ERROR: dependency cycle detected (issue #${cycle_num} is in the cycle)" >&2
    exit 1
  fi
  echo "==> dependency edges found; dispatching in waves"
  remaining="$PWD/logs/remaining.$$"
  printf '%s\n' "$issues_tsv" >"$remaining"
  dispatch_waves "$remaining" || exit 1
  rm -f "$remaining"
else
  # No declared deps — original single-wave fan-out (bash 3.2, no `wait -n`).
  while IFS=$'\t' read -r num title; do
    [ -n "$num" ] || continue
    # Claim synchronously before backgrounding so the issue drops out of the
    # $LABEL query immediately; re-running mid-flight then skips it.
    swap_label "$num" "$IN_PROGRESS_LABEL" "$LABEL"
    run_one "$num" "$title" &
    while [ "$(jobs -r | wc -l | tr -d ' ')" -ge "$MAX_PARALLEL" ]; do sleep 1; done
  done <<EOF
$issues_tsv
EOF
  wait
fi

print_outcome_report
echo "==> all agents finished — branches pushed and PRs opened on $REPO_SLUG."
