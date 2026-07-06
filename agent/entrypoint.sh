#!/usr/bin/env bash
# Runs INSIDE the disposable container (one per issue): clones the target repo
# fresh — zero shared host filesystem — cuts a branch, then hands off to a
# headless Claude Code agent that implements the issue and opens a PR.
#
# Baked into the image at /agent/entrypoint.sh (see lib/mkHarness.nix); the
# prompt templates are baked into the image at /agent/prompts. Set
# SPINDRIFT_PROMPT_DIR to override with a host directory without a rebuild.
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
# IN_PROGRESS_LABEL, COMPLETE_LABEL, and DEV_SHELL_PROBE_TIMEOUT are injected
# by the nix-rendered defaults preamble prepended at image-build time
# (env-schema.nix).
# AGENTS_JSON_TEMPLATE is a nix-computed derived value also prepended at
# image-build time; it is not a schema knob.  The :-  expansions below keep
# set -u and the linter happy for standalone use.
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
# Fetch the absolute latest refs so the pre-work rebase positions the branch
# on current origin/BASE_BRANCH, not the state captured at clone time.
git fetch origin
# A prior run may have already pushed agent/issue-N before dying.  When no
# open PR exists, force-reset the remote branch so this Box starts clean and
# its first incremental push is never rejected non-fast-forward.  When an
# open PR exists, check out the prior work so the pre-work rebase can replay
# it onto current origin/BASE_BRANCH before the agent begins.
_rebase_and_publish=""
if git rev-parse --verify "refs/remotes/origin/$BRANCH" >/dev/null 2>&1; then
  # Fail hard on gh errors: a silent empty response (network/auth failure)
  # is indistinguishable from "no PR" and must not trigger the force-reset.
  open_prs="$(gh pr list --repo "$REPO_SLUG" --head "$BRANCH" --state open)" || {
    echo "==> gh pr list failed on $BRANCH; aborting to protect any open PR"
    exit 1
  }
  if [ -n "$open_prs" ]; then
    echo "==> open PR exists on $BRANCH; skipping force-reset — checking out prior work for pre-work rebase"
    git checkout -b "$BRANCH" "origin/$BRANCH"
    # Mark that the rebased branch must be published after the rebase so the
    # agent's first incremental push is a fast-forward, not a rejection.
    _rebase_and_publish=1
  else
    echo "==> stale remote branch $BRANCH found (no open PR); force-resetting to ${BASE_BRANCH:-}"
    git checkout -b "$BRANCH" "origin/${BASE_BRANCH:-}"
    git push --force-with-lease origin "$BRANCH" || {
      echo "==> force-with-lease push failed on $BRANCH; concurrent Box may be ahead"
      exit 1
    }
  fi
else
  git checkout -b "$BRANCH" "origin/${BASE_BRANCH:-}"
fi
# Rebase onto the latest origin/BASE_BRANCH before the agent starts.  This
# ensures the agent works against current main rather than the state of
# origin at clone time, closing the stale-base defect.  A conflict here
# means the prior branch diverged in a way that cannot be resolved
# mechanically; fail fast with a distinct signal instead of proceeding on a
# stale base.
echo "==> rebasing $BRANCH onto latest origin/${BASE_BRANCH:-}"
_had_rebase_conflict=""
git rebase "origin/${BASE_BRANCH:-}" || _had_rebase_conflict=1
# Publish the rebased branch so the agent's first incremental push is a
# fast-forward.  Only needed in the adoption path where the rebase rewrote
# history that was already on the remote.  When a conflict is detected,
# publication is deferred until after the conflict-resolve agent runs below.
if [ -z "${_had_rebase_conflict:-}" ] && [ -n "${_rebase_and_publish:-}" ]; then
  echo "==> publishing rebased $BRANCH"
  git push --force-with-lease origin "$BRANCH" || {
    echo "==> force-with-lease push after pre-work rebase failed on $BRANCH"
    exit 1
  }
fi

# Detect a Nix devShell in the cloned repo. When found the prompt guides the
# agent to run checks inside `nix develop`; absence or probe failure degrades
# gracefully to the baked toolchain. DEV_SHELL_PROBE_TIMEOUT is nix-baked
# (env-schema.nix default 300 s) so a heavy consumer devShell eval cannot
# stall the box indefinitely.
if [ -f "flake.nix" ]; then
  echo "==> flake.nix found in cloned repo; probing for devShell"
  _probe_rc=0
  if command -v nix >/dev/null 2>&1; then
    timeout "${DEV_SHELL_PROBE_TIMEOUT}" nix develop --command true 2>/dev/null \
      || _probe_rc=$?
  else
    _probe_rc=1
  fi
  if [ "$_probe_rc" -eq 0 ]; then
    echo "==> devShell found — agent will use nix develop for checks"
  elif [ "$_probe_rc" -eq 124 ]; then
    echo "==> devShell probe timed out (${DEV_SHELL_PROBE_TIMEOUT}s) — using baked toolchain"
  else
    echo "==> no devShell in flake (or nix develop failed) — using baked toolchain"
  fi
fi

# Optional cache warm-up (mkHarness `prefetch`, baked into the image env); no-op
# when unset.
if [ -n "${PREFETCH:-}" ]; then
  eval "$PREFETCH"
fi

# Discover available skills at $HOME/.claude/skills/ and build a directive
# to prefer them over the inline guidance where they apply.
SKILL_PREAMBLE=""
_skills_found=""
if [ -d "${HOME:-}/.claude/skills" ]; then
  for _sf in "${HOME}/.claude/skills/"*.md; do
    [ -f "$_sf" ] || continue
    _sn="$(basename "$_sf" .md)"
    _skills_found="${_skills_found:+${_skills_found}, }${_sn}"
  done
fi
if [ -n "$_skills_found" ]; then
  SKILL_PREAMBLE="Skills available: ${_skills_found}. Prefer invoking with /skill-name; the inline guidance below is the fallback when a skill is absent.

"
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
    SKILL_PREAMBLE="${SKILL_PREAMBLE:-}" \
    envsubst '$ISSUE_NUMBER $ISSUE_TITLE $BRANCH $BASE_BRANCH $IN_PROGRESS_LABEL $COMPLETE_LABEL $SKILL_PREAMBLE' \
    <"$1"
}
# When the pre-work rebase produced conflicts, spawn a conflict-resolve agent to
# re-map the branch onto current main.  Only escalate to exit 1 if the agent
# genuinely cannot resolve.
if [ -n "${_had_rebase_conflict:-}" ]; then
  echo "==> pre-work rebase conflict detected — invoking conflict-resolve agent"
  _cr_prompt="$(_subst "${PROMPTS_DIR}/conflict-resolve-prompt.md")"
  set +e
  claude -p "$_cr_prompt" \
    --model "${MODEL:-}" \
    --verbose \
    --output-format stream-json \
    --dangerously-skip-permissions
  set -e
  if [ -d ".git/rebase-merge" ] || [ -d ".git/rebase-apply" ]; then
    git rebase --abort 2>/dev/null || true
    echo "==> pre-work rebase onto origin/${BASE_BRANCH:-} failed — conflict agent could not resolve"
    exit 1
  fi
  echo "==> pre-work rebase conflict resolved by agent"
  if [ -n "${_rebase_and_publish:-}" ]; then
    echo "==> publishing rebased $BRANCH (post-conflict-resolve)"
    git push --force-with-lease origin "$BRANCH" || {
      echo "==> force-with-lease push after conflict resolution failed on $BRANCH"
      exit 1
    }
  fi
fi

# CONFLICT_RESOLVE_PR_URL mode: this box was dispatched only to re-map the PR
# branch onto current main.  Exit after resolution without running the main agent.
if [ -n "${CONFLICT_RESOLVE_PR_URL:-}" ]; then
  echo "==> CONFLICT_RESOLVE_PR_URL: conflict resolved — exiting without main agent"
  exit 0
fi

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
#
# The raw stream-json is the canonical record: it is BOTH streamed to stdout —
# which the launcher captures verbatim into logs/issue-<n>.log — and tee'd to
# $stream_log for the SPINDRIFT_OUTCOME extraction below. The launcher's
# outcome.Classify scans that log for transient markers (rate_limit_error,
# resetsAt, ...) to drive hold-until-reset retries, so the raw events must reach
# stdout unmodified — never routed through a lossy formatter (see #123). For a
# human-readable view, pipe the saved log through agent/format-transcript.sh on
# the host: `format-transcript.sh < logs/issue-<n>.log`.
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
