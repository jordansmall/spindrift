# Lists open $LABEL issues on the target repo and fans out one disposable
# container per issue, capped at MAX_PARALLEL; each clones fresh and opens a PR.
#
# Body fragment: nix (lib/mkHarness.nix) wraps it with writeShellApplication,
# prepending the shebang, `set -euo pipefail`, the pinned runtimeInputs PATH, and
# a preamble defining the baked config (IMAGE_ARCHIVE, RUNTIME, the run defaults
# as `NAME="${NAME:-<default>}"`, PROMPT_DIR). A matching env var — or
# harness.env below — therefore wins at runtime.

# Config + secrets (gitignored), read from $PWD since the harness is a store path
# with no working tree. `set -a` makes its assignments override the baked defaults.
if [ -f "$PWD/harness.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$PWD/harness.env"
  set +a
fi

IMAGE="${IMAGE:-spindrift:latest}"

# Point SPINDRIFT_PROMPT_DIR at a real directory to override the baked prompt
# store path and iterate on the prompt with zero rebuilds.
if [ -n "${SPINDRIFT_PROMPT_DIR:-}" ] && [ -d "$SPINDRIFT_PROMPT_DIR" ]; then
  echo "==> SPINDRIFT_PROMPT_DIR set; mounting $SPINDRIFT_PROMPT_DIR instead of baked prompt"
  PROMPT_DIR="$SPINDRIFT_PROMPT_DIR"
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

# Auto-load the baked image on first use.
if ! "$RUNTIME" image exists "$IMAGE"; then
  echo "==> image '$IMAGE' not loaded; loading from $IMAGE_ARCHIVE"
  "$RUNTIME" load -i "$IMAGE_ARCHIVE"
fi

auth_args=()
[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && auth_args+=(-e CLAUDE_CODE_OAUTH_TOKEN)
[ -n "${ANTHROPIC_API_KEY:-}" ] && auth_args+=(-e ANTHROPIC_API_KEY)

git_args=(-e "GIT_USER_NAME=$GIT_USER_NAME" -e "GIT_USER_EMAIL=$GIT_USER_EMAIL")

echo "==> querying open '$LABEL' issues in $REPO_SLUG"
issues_tsv="$(gh issue list \
  --repo "$REPO_SLUG" --state open --label "$LABEL" --limit 100 \
  --json number,title --jq '.[] | [.number, .title] | @tsv')"

if [ -z "$issues_tsv" ]; then
  echo "no open '$LABEL' issues — nothing to do."
  exit 0
fi

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

run_one() {
  local num="$1" title="$2"
  local log="$PWD/logs/issue-$num.log"
  echo "    -> #$num: $title"
  if "$RUNTIME" run --rm \
    --name "agent-issue-$num" \
    -e GH_TOKEN "${auth_args[@]}" "${git_args[@]}" \
    -e REPO_SLUG="$REPO_SLUG" \
    -e ISSUE_NUMBER="$num" \
    -e ISSUE_TITLE="$title" \
    -e BASE_BRANCH="$BASE_BRANCH" \
    -e BRANCH_PREFIX="$BRANCH_PREFIX" \
    -e MODEL="$MODEL" \
    -v "$PROMPT_DIR:/agent/prompts:ro" \
    "$IMAGE" /agent/entrypoint.sh >"$log" 2>&1; then
    # Success needs no terminal label — the merged PR's `Closes #N` closes it.
    echo "    <- #$num done  (logs/issue-$num.log)"
  else
    # Hand failures to human triage; re-labelling $LABEL retries. No auto-retry.
    echo "    !! #$num FAILED (logs/issue-$num.log)"
    swap_label "$num" "$FAILED_LABEL" "$IN_PROGRESS_LABEL"
  fi
}

# bash 3.2-compatible parallelism cap (macOS ships 3.2; no `wait -n`).
while IFS=$'\t' read -r num title; do
  [ -n "$num" ] || continue
  # Claim the issue up front — synchronously, before backgrounding — so it drops
  # out of the $LABEL query immediately. Re-running `run` mid-flight then skips
  # it instead of re-dispatching a duplicate Box.
  swap_label "$num" "$IN_PROGRESS_LABEL" "$LABEL"
  run_one "$num" "$title" &
  while [ "$(jobs -r | wc -l | tr -d ' ')" -ge "$MAX_PARALLEL" ]; do sleep 1; done
done <<EOF
$issues_tsv
EOF

wait
echo "==> all agents finished — branches pushed and PRs opened on $REPO_SLUG."
