#!/usr/bin/env bash
# Launcher. Lists open issues with the configured label on the target repo and
# fans out one disposable container per issue, capped at MAX_PARALLEL. Each
# container clones fresh, implements its issue, and opens a PR — see the baked
# /agent/entrypoint.sh.
#
# The agent image is a nix store path baked in at build time (@imagePath@ is
# substituted by lib/mkHarness.nix); it is auto-loaded if not already present.
set -euo pipefail

IMAGE_ARCHIVE="@imagePath@"
# Container runtime baked in at build time (podman or docker). A build-time
# choice, not runtime env — the image path it loads is runtime-specific anyway.
RUNTIME="@runtime@"

# Config + secrets (gitignored). Read from the current working directory, since
# the harness is a store path with no working tree. Also overridable via the
# environment, which takes precedence over the file.
if [ -f "$PWD/harness.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$PWD/harness.env"
  set +a
fi

IMAGE="${IMAGE:-spindrift:latest}"
# Baked defaults (@label@ etc. substituted by lib/mkHarness.nix from the
# Consumer's `defaults`); a matching env var still wins at runtime.
LABEL="${LABEL:-@label@}"
BASE_BRANCH="${BASE_BRANCH:-@baseBranch@}"
MAX_PARALLEL="${MAX_PARALLEL:-@maxParallel@}"
BRANCH_PREFIX="${BRANCH_PREFIX:-@branchPrefix@}"

# Commit identity: explicit override wins, else inherit the host's git config.
# Required — there is no built-in default.
GIT_USER_NAME="${GIT_USER_NAME:-$(git config --get user.name 2>/dev/null || true)}"
GIT_USER_EMAIL="${GIT_USER_EMAIL:-$(git config --get user.email 2>/dev/null || true)}"

: "${REPO_SLUG:?set REPO_SLUG=owner/repo (the target GitHub repository)}"
: "${GIT_USER_NAME:?set GIT_USER_NAME, or configure git user.name on the host}"
: "${GIT_USER_EMAIL:?set GIT_USER_EMAIL, or configure git user.email on the host}"
: "${GH_TOKEN:?set GH_TOKEN (repo perms: Issues RW, Contents RW, Pull requests RW, Metadata R)}"
if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY" >&2
  exit 1
fi

command -v "$RUNTIME" >/dev/null 2>&1 || {
  echo "$RUNTIME not found on PATH." >&2
  exit 1
}

# Auto-load the baked image on first use (the old launcher errored instead).
if ! "$RUNTIME" image exists "$IMAGE"; then
  echo "==> image '$IMAGE' not loaded; loading from $IMAGE_ARCHIVE"
  "$RUNTIME" load -i "$IMAGE_ARCHIVE"
fi

# Pass through whichever auth the host has set.
auth_args=()
[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && auth_args+=(-e CLAUDE_CODE_OAUTH_TOKEN)
[ -n "${ANTHROPIC_API_KEY:-}" ] && auth_args+=(-e ANTHROPIC_API_KEY)

# Pass the resolved (and required) commit identity through.
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
    "$IMAGE" /agent/entrypoint.sh >"$log" 2>&1; then
    echo "    <- #$num done  (logs/issue-$num.log)"
  else
    echo "    !! #$num FAILED (logs/issue-$num.log)"
  fi
}

# bash 3.2-compatible parallelism cap (macOS ships 3.2; no `wait -n`).
while IFS=$'\t' read -r num title; do
  [ -n "$num" ] || continue
  run_one "$num" "$title" &
  while [ "$(jobs -r | wc -l | tr -d ' ')" -ge "$MAX_PARALLEL" ]; do sleep 1; done
done <<EOF
$issues_tsv
EOF

wait
echo "==> all agents finished — branches pushed and PRs opened on $REPO_SLUG."
