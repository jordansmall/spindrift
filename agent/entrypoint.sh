#!/usr/bin/env bash
# Runs INSIDE the disposable container (one per issue): clones the target repo
# fresh — zero shared host filesystem — cuts a branch, then hands off to a
# headless Claude Code agent that implements the issue and opens a PR.
#
# Baked into the image at /agent/entrypoint.sh (see lib/mkHarness.nix); the
# prompt template is bind-mounted at /agent/prompts by `run`, hot-overridable
# via SPINDRIFT_PROMPT_DIR.
#
# --dangerously-skip-permissions is safe here precisely because the container
# IS the isolation boundary: the agent can do anything, but only to a throwaway
# clone with a scoped token and no host access.
set -euo pipefail

: "${REPO_SLUG:?REPO_SLUG (owner/repo) is required}"
: "${ISSUE_NUMBER:?ISSUE_NUMBER is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"
: "${GIT_USER_NAME:?GIT_USER_NAME is required}"
: "${GIT_USER_EMAIL:?GIT_USER_EMAIL is required}"

BASE_BRANCH="${BASE_BRANCH:-main}"
BRANCH_PREFIX="${BRANCH_PREFIX:-agent/issue-}"
BRANCH="${BRANCH_PREFIX}${ISSUE_NUMBER}"

# `run` passes MODEL per issue; this fallback keeps the entrypoint runnable
# standalone.
MODEL="${MODEL:-claude-opus-4-8}"
SCOUT_MODEL="${SCOUT_MODEL:-}"
REVIEW_MODEL="${REVIEW_MODEL:-}"
IN_PROGRESS_LABEL="${IN_PROGRESS_LABEL:-agent-in-progress}"
COMPLETE_LABEL="${COMPLETE_LABEL:-agent-complete}"

# Baked-in locations; overridable only so the harness can be exercised on the
# host without a container.
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

# Detect a Nix devShell in the cloned repo. When found the prompt guides the
# agent to run checks inside `nix develop`; absence or probe failure degrades
# gracefully to the baked toolchain.
if [ -f "flake.nix" ]; then
  echo "==> flake.nix found in cloned repo; probing for devShell"
  if command -v nix >/dev/null 2>&1 && nix develop --command true 2>/dev/null; then
    echo "==> devShell found — agent will use nix develop for checks"
  else
    echo "==> no devShell in flake (or nix develop failed) — using baked toolchain"
  fi
fi

# Optional cache warm-up (mkHarness `prefetch`, baked into the image env); no-op
# when unset.
if [ -n "${PREFETCH:-}" ]; then
  eval "$PREFETCH"
fi

# Substitute only known placeholders so literal `$` in the prompt body (shell
# snippets, etc.) survives. The single-quoted variable list is envsubst's
# literal, not a shell expansion — hence SC2016.
# shellcheck disable=SC2016
prompt="$(
  ISSUE_NUMBER="$ISSUE_NUMBER" \
    ISSUE_TITLE="${ISSUE_TITLE:-}" \
    BRANCH="$BRANCH" \
    BASE_BRANCH="$BASE_BRANCH" \
    IN_PROGRESS_LABEL="$IN_PROGRESS_LABEL" \
    COMPLETE_LABEL="$COMPLETE_LABEL" \
    envsubst '$ISSUE_NUMBER $ISSUE_TITLE $BRANCH $BASE_BRANCH $IN_PROGRESS_LABEL $COMPLETE_LABEL' \
    <"${PROMPTS_DIR}/issue-prompt.md"
)"

# Build --agents JSON when both subagent models are configured; omit the flag
# entirely when either is unset so single-model runs are unaffected.
agents_args=()
if [ -n "$SCOUT_MODEL" ] && [ -n "$REVIEW_MODEL" ]; then
  agents_json='[{"name":"scout","model":"'"$SCOUT_MODEL"'"},{"name":"reviewer","model":"'"$REVIEW_MODEL"'"}]'
  agents_args=(--agents "$agents_json")
fi

echo "==> claude implementing issue #$ISSUE_NUMBER on $BRANCH"
claude -p "$prompt" \
  --model "$MODEL" \
  "${agents_args[@]}" \
  --dangerously-skip-permissions

echo "==> entrypoint complete for issue #$ISSUE_NUMBER"
