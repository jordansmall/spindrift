#!/usr/bin/env bash
# Runs INSIDE the disposable container (one per issue). Clones the target repo
# fresh — zero shared host filesystem — cuts a branch, then hands off to a
# headless Claude Code agent that implements the issue and opens a PR.
#
# Baked into the image at /agent/entrypoint.sh alongside /agent/prompts (see
# lib/mkHarness.nix); nothing is bind-mounted from a source checkout.
#
# --dangerously-skip-permissions is safe here precisely because the container
# IS the isolation boundary: the agent can do anything it likes, but only to a
# throwaway clone with a scoped token and no host access.
set -euo pipefail

: "${REPO_SLUG:?REPO_SLUG (owner/repo) is required}"
: "${ISSUE_NUMBER:?ISSUE_NUMBER is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"
: "${GIT_USER_NAME:?GIT_USER_NAME is required}"
: "${GIT_USER_EMAIL:?GIT_USER_EMAIL is required}"

BASE_BRANCH="${BASE_BRANCH:-main}"
BRANCH_PREFIX="${BRANCH_PREFIX:-agent/issue-}"
BRANCH="${BRANCH_PREFIX}${ISSUE_NUMBER}"

# Baked-in locations; overridable only so the harness can be exercised on the
# host without a container. Production leaves these at their defaults.
WORK_DIR="${WORK_DIR:-/work}"
PROMPTS_DIR="${PROMPTS_DIR:-/agent/prompts}"

export GH_TOKEN
git config --global user.name "$GIT_USER_NAME"
git config --global user.email "$GIT_USER_EMAIL"
git config --global init.defaultBranch main
gh auth setup-git

echo "==> cloning $REPO_SLUG"
git clone "https://github.com/${REPO_SLUG}.git" "$WORK_DIR"
cd "$WORK_DIR"
git checkout -b "$BRANCH" "origin/${BASE_BRANCH}"

# Optional cache warm-up, configured via mkHarness `prefetch` (baked into the
# image env). Runs in the freshly cloned work tree; no-op when unset.
if [ -n "${PREFETCH:-}" ]; then
  eval "$PREFETCH"
fi

# Only substitute our known placeholders so any literal `$` in the prompt body
# (shell snippets, etc.) is left untouched. envsubst's variable list is meant to
# be a literal, not expanded — hence the single quotes.
# shellcheck disable=SC2016
prompt="$(
  ISSUE_NUMBER="$ISSUE_NUMBER" \
    ISSUE_TITLE="${ISSUE_TITLE:-}" \
    BRANCH="$BRANCH" \
    BASE_BRANCH="$BASE_BRANCH" \
    envsubst '$ISSUE_NUMBER $ISSUE_TITLE $BRANCH $BASE_BRANCH' \
    <"${PROMPTS_DIR}/issue-prompt.md"
)"

echo "==> claude implementing issue #$ISSUE_NUMBER on $BRANCH"
claude -p "$prompt" \
  --model claude-opus-4-8 \
  --dangerously-skip-permissions

echo "==> entrypoint complete for issue #$ISSUE_NUMBER"
