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

# BASE_BRANCH, BRANCH_PREFIX, MODEL, SCOUT_MODEL, REVIEW_MODEL,
# IN_PROGRESS_LABEL, and COMPLETE_LABEL are injected by the nix-rendered
# defaults preamble prepended at image-build time (env-schema.nix).
# AGENTS_JSON_TEMPLATE is a nix-computed derived value also prepended at
# image-build time; it is not a schema knob.  The :-  expansions below keep
# shellcheck and `set -u` happy for standalone use.
BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"

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
git checkout -b "$BRANCH" "origin/${BASE_BRANCH:-}"
# A prior run may have already pushed agent/issue-N before dying.  When no
# open PR exists, force-reset the remote branch so this Box starts clean and
# its first incremental push is never rejected non-fast-forward.  When an
# open PR exists, leave the branch alone — the #122 adoption path will take
# over instead.
if git rev-parse --verify "refs/remotes/origin/$BRANCH" >/dev/null 2>&1; then
  # Fail hard on gh errors: a silent empty response (network/auth failure)
  # is indistinguishable from "no PR" and must not trigger the force-reset.
  open_prs="$(gh pr list --repo "$REPO_SLUG" --head "$BRANCH" --state open)" || {
    echo "==> gh pr list failed on $BRANCH; aborting to protect any open PR"
    exit 1
  }
  if [ -n "$open_prs" ]; then
    echo "==> open PR exists on $BRANCH; skipping force-reset (adoption path)"
  else
    echo "==> stale remote branch $BRANCH found (no open PR); force-resetting to ${BASE_BRANCH:-}"
    git push --force-with-lease origin "$BRANCH" || {
      echo "==> force-with-lease push failed on $BRANCH; concurrent Box may be ahead"
      exit 1
    }
  fi
fi

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
_subst() {
  # shellcheck disable=SC2016
  ISSUE_NUMBER="$ISSUE_NUMBER" \
    ISSUE_TITLE="${ISSUE_TITLE:-}" \
    BRANCH="$BRANCH" \
    BASE_BRANCH="${BASE_BRANCH:-}" \
    IN_PROGRESS_LABEL="${IN_PROGRESS_LABEL:-}" \
    COMPLETE_LABEL="${COMPLETE_LABEL:-}" \
    envsubst '$ISSUE_NUMBER $ISSUE_TITLE $BRANCH $BASE_BRANCH $IN_PROGRESS_LABEL $COMPLETE_LABEL' \
    <"$1"
}
prompt="$(_subst "${PROMPTS_DIR}/issue-prompt.md")"

# Forward the nix-baked --agents JSON to the Agent. AGENTS_JSON_TEMPLATE is
# computed by nix (builtins.toJSON) and set to empty when either subagent model
# is unset, so the conditional is resolved at build time, not here.
# When the template is present, inject the runtime-substituted prompts and
# forward the completed JSON; otherwise omit the flag entirely.
if [ -n "${AGENTS_JSON_TEMPLATE:-}" ]; then
  scout_prompt="$(_subst "${PROMPTS_DIR}/scout-prompt.md")"
  review_prompt="$(_subst "${PROMPTS_DIR}/review-prompt.md")"
  agents_json="$(jq -n \
    --argjson template "$AGENTS_JSON_TEMPLATE" \
    --arg scout_prompt "$scout_prompt" \
    --arg review_prompt "$review_prompt" \
    '$template | .scout.prompt = $scout_prompt | .reviewer.prompt = $review_prompt')"
  agents_args=(--agents "$agents_json")
else
  agents_args=()
fi

echo "==> claude implementing issue #$ISSUE_NUMBER on $BRANCH"
# Stream the transcript live (visible via `podman logs -f`) while capturing it.
# stream-json is the only --print format that emits events in realtime; plain
# text stays silent until the very end, so the box looks dead for the whole run.
stream_log="$(mktemp)"
set +e
claude -p "$prompt" \
  --model "${MODEL:-}" \
  "${agents_args[@]}" \
  --verbose \
  --output-format stream-json \
  --dangerously-skip-permissions \
  | tee "$stream_log"
claude_rc="${PIPESTATUS[0]}"
set -e

# The launcher greps '^SPINDRIFT_OUTCOME ' from the container log, but under
# stream-json the agent's final line is wrapped in a result event. Surface it as
# a bare line so that contract is unchanged.
jq -r 'select(.type == "result") | .result // empty' "$stream_log" 2>/dev/null \
  | grep '^SPINDRIFT_OUTCOME ' | tail -1 || true
rm -f "$stream_log"

[ "$claude_rc" -eq 0 ] || exit "$claude_rc"
echo "==> entrypoint complete for issue #$ISSUE_NUMBER"
