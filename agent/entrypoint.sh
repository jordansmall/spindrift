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
# IN_PROGRESS_LABEL, COMPLETE_LABEL, DEV_SHELL_NAME, and DEV_SHELL_PROBE_TIMEOUT
# are injected by the nix-rendered defaults preamble prepended at image-build
# time (env-schema.nix).
# AGENTS_JSON_TEMPLATE is a nix-computed derived value also prepended at
# image-build time; it is not a schema knob.  The :-  expansions below keep
# set -u and the linter happy for standalone use.
BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"

# Baked-in locations; overridable only so the harness can be exercised on the
# host without a container.
WORK_DIR="${WORK_DIR:-/work}"
PROMPTS_DIR="${PROMPTS_DIR:-/agent/prompts}"

export GH_TOKEN
gh auth setup-git

# CODE_FORGE=git clones from and pushes to a configured plain git remote
# instead of the target GitHub repo (ADR 0013); REPO_SLUG still resolves the
# Issue Tracker regardless. Gated on CODE_FORGE=git so a stray
# CODE_FORGE_REMOTE_URL left set in the environment can't silently redirect a
# CODE_FORGE=github (default) deployment to the wrong remote.
CLONE_URL="https://github.com/${REPO_SLUG}.git"
if [ "${CODE_FORGE:-github}" = "git" ]; then
  CLONE_URL="${CODE_FORGE_REMOTE_URL:?CODE_FORGE_REMOTE_URL is required when CODE_FORGE=git}"
fi
echo "==> cloning $CLONE_URL"
git clone "$CLONE_URL" "$WORK_DIR"
cd "$WORK_DIR"
# Identity is repo-local, not global (#404): CI's hermetic check environment
# has no global git config, so a global identity here would let git-shelling
# tests observe config the Box has but CI doesn't. Setting it locally on this
# clone keeps the Box's global surface CI-equivalent while commits/pushes
# still carry the correct Agent identity.
git config user.name "$GIT_USER_NAME"
git config user.email "$GIT_USER_EMAIL"
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

# Cold-run toolchain nudge: when no prefetch is configured and a recognized
# lockfile is present, emit a one-time hint pointing at the two knobs that
# actually help (prefetch for per-run cache warm, packages for a baked
# cross-run toolchain). Unknown ecosystems emit nothing.
if [ -z "${PREFETCH:-}" ]; then
  _nudge_ecosystem=""
  if [ -f "Cargo.lock" ]; then
    _nudge_ecosystem="cargo"
  elif [ -f "package-lock.json" ] || [ -f "pnpm-lock.yaml" ] || [ -f "yarn.lock" ]; then
    _nudge_ecosystem="npm/pnpm/yarn"
  elif [ -f "go.sum" ]; then
    _nudge_ecosystem="go mod"
  fi
  if [ -n "$_nudge_ecosystem" ]; then
    echo "==> hint: ${_nudge_ecosystem} project detected; set 'prefetch' to warm dependency caches per run, or 'packages' to bake a toolchain into the image"
  fi
fi

# Detect a Nix devShell in the cloned repo. When found the prefetch hook and
# Driver run inside `nix develop` so the agent operates in the Target's exact
# pinned environment. DEV_SHELL_PROBE_TIMEOUT is nix-baked (env-schema.nix
# default 300 s) so a heavy consumer devShell eval cannot stall the box.
# DEV_SHELL_NAME selects which devShell to enter (default "default").
_use_dev_shell=0
_harness_path="$PATH"
if [ -f "flake.nix" ]; then
  echo "==> flake.nix found in cloned repo; probing for devShell"
  _probe_rc=0
  if command -v nix >/dev/null 2>&1; then
    timeout "${DEV_SHELL_PROBE_TIMEOUT}" \
      nix develop ".#${DEV_SHELL_NAME:-default}" --command true 2>/dev/null \
      || _probe_rc=$?
  else
    _probe_rc=1
  fi
  if [ "$_probe_rc" -eq 0 ]; then
    echo "==> devShell found — lifecycle will run inside nix develop"
    _use_dev_shell=1
  elif [ "$_probe_rc" -eq 124 ]; then
    echo "==> devShell probe timed out (${DEV_SHELL_PROBE_TIMEOUT}s) — using baked toolchain"
  else
    echo "==> no devShell in flake (or nix develop failed) — using baked toolchain"
  fi
fi

# Optional cache warm-up (mkHarness `prefetch`, baked into the image env); no-op
# when unset. When a devShell is available, run inside it so the prefetch
# command sees the Target's exact toolchain and env vars.
if [ -n "${PREFETCH:-}" ]; then
  if [ "$_use_dev_shell" = "1" ]; then
    _pf_wrapper="$(mktemp --suffix=.sh)"
    # eval "$PREFETCH" so shell constructs in the hook (|| true, etc.)
    # are interpreted; match the non-devShell path exactly.
    # $PATH and $PREFETCH are literal in the generated script — SC2016.
    # shellcheck disable=SC2016
    printf '#!/bin/bash\nexport PATH="%s:$PATH"\neval "$PREFETCH"\n' \
      "$_harness_path" > "$_pf_wrapper"
    chmod +x "$_pf_wrapper"
    # Prefetch failures are non-fatal — ignore nix rc.
    nix develop ".#${DEV_SHELL_NAME:-default}" --command bash "$_pf_wrapper" || true
    rm -f "$_pf_wrapper"
  else
    eval "$PREFETCH"
  fi
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
# computed by nix (builtins.toJSON) and set to empty when no subagent model is
# configured, so the conditional is resolved at build time, not here. Each
# subagent is baked independently, so the template may carry scout only,
# reviewer only, both, or (empty string) neither.
# When the template is present, inject the runtime-substituted prompt for
# whichever agents it actually carries and forward the completed JSON;
# otherwise omit the flag entirely.
if [ -n "${AGENTS_JSON_TEMPLATE:-}" ]; then
  scout_prompt="$(_subst "${PROMPTS_DIR}/scout-prompt.md")"
  review_prompt="$(_subst "${PROMPTS_DIR}/review-prompt.md")"
  filer_prompt="$(_subst "${PROMPTS_DIR}/filer-prompt.md")"
  agents_json="$(jq -n \
    --argjson template "$AGENTS_JSON_TEMPLATE" \
    --arg scout_prompt "$scout_prompt" \
    --arg review_prompt "$review_prompt" \
    --arg filer_prompt "$filer_prompt" \
    '$template
     | if has("scout") then .scout.prompt = $scout_prompt else . end
     | if has("reviewer") then .reviewer.prompt = $review_prompt else . end
     | if has("filer") then .filer.prompt = $filer_prompt else . end')"
  agents_args=(--agents "$agents_json")
else
  agents_args=()
fi

echo "==> claude implementing issue #$ISSUE_NUMBER on $BRANCH"
# Decoupled output channels (#183, superseding the #123 "raw on stdout" note):
#
#   stdout → launcher captures verbatim into logs/issue-<n>.log (byte-exact,
#   unchanged). outcome.Classify scans that log for transient markers
#   (rate_limit_error, resetsAt, ...) and greps for SPINDRIFT_OUTCOME; the raw
#   events must never be filtered or transformed on this channel.
#
#   /tmp/heartbeat.log → cleaned coarse status lines written by
#   spindrift-heartbeat-filter, which reuses the #182 heartbeat parser. A human
#   shelling into the box can run:  tail -f /tmp/heartbeat.log
#   For OCI boxes:  podman exec <box> tail -f /tmp/heartbeat.log
#
# spindrift-heartbeat-filter is a transparent stdin→stdout passthrough; it adds
# no bytes to the stream and does not affect PIPESTATUS[0] (claude's exit code).
stream_log="$(mktemp)"
_claude_rc_file="$(mktemp)"
printf '1' > "$_claude_rc_file"

_run_driver() {
  # Inner function: run the claude pipeline; write PIPESTATUS[0] to
  # $_claude_rc_file. Runs either directly or inside the devShell wrapper.
  set +e
  claude -p "$prompt" \
    --model "${MODEL:-}" \
    "${agents_args[@]}" \
    --verbose \
    --output-format stream-json \
    --dangerously-skip-permissions \
    | spindrift-heartbeat-filter -n "$ISSUE_NUMBER" -f /tmp/heartbeat.log \
    | tee "$stream_log"
  printf '%s' "${PIPESTATUS[0]}" > "$_claude_rc_file"
  set -e
}

_nix_rc=0
if [ "$_use_dev_shell" = "1" ]; then
  # Write the prompt to a temp file so the wrapper can read it without
  # embedding the full text (avoids quoting hazards).
  _prompt_file="$(mktemp)"
  printf '%s' "$prompt" > "$_prompt_file"
  # Write agents JSON to a temp file so the wrapper can pass it to claude
  # as a single quoted argument — avoiding word-splitting on spaces in JSON.
  _agents_file="$(mktemp)"
  if [ "${#agents_args[@]}" -gt 0 ]; then
    printf '%s' "${agents_args[1]}" > "$_agents_file"
  fi
  # Write a wrapper that drives the claude pipeline inside the devShell.
  # _harness_path is prepended so baked tools (spindrift-heartbeat-filter,
  # tee, etc.) remain reachable after nix develop rewrites PATH.
  _driver_wrapper="$(mktemp --suffix=.sh)"
  cat > "$_driver_wrapper" << 'DRIVER_WRAPPER_EOF'
#!/bin/bash
export PATH="$_HARNESS_PATH:$PATH"
set +e
_agents_arg=()
if [ -s "$_AGENTS_FILE" ]; then
  _agents_arg=("--agents" "$(cat "$_AGENTS_FILE")")
fi
claude -p "$(cat "$_PROMPT_FILE")" \
  --model "${MODEL:-}" \
  "${_agents_arg[@]}" \
  --verbose \
  --output-format stream-json \
  --dangerously-skip-permissions \
  | spindrift-heartbeat-filter -n "$ISSUE_NUMBER" -f /tmp/heartbeat.log \
  | tee "$_STREAM_LOG"
printf '%s' "${PIPESTATUS[0]}" > "$_CLAUDE_RC_FILE"
DRIVER_WRAPPER_EOF
  chmod +x "$_driver_wrapper"
  export _HARNESS_PATH="$_harness_path"
  export _PROMPT_FILE="$_prompt_file"
  export _AGENTS_FILE="$_agents_file"
  export _STREAM_LOG="$stream_log"
  export _CLAUDE_RC_FILE="$_claude_rc_file"
  # MODEL comes from the preamble (not exported by default); export it so the
  # devShell child process sees the correct resolved value, not an empty string.
  export MODEL="${MODEL:-}"
  set +e
  nix develop ".#${DEV_SHELL_NAME:-default}" --command bash "$_driver_wrapper"
  _nix_rc=$?
  set -e
  rm -f "$_driver_wrapper" "$_prompt_file" "$_agents_file"
  # Launch-failure: nix develop itself failed before claude ran (stream empty).
  # Relaunch once in the baked env so transient nix failures don't kill the run.
  if [ "$_nix_rc" -ne 0 ] && [ ! -s "$stream_log" ]; then
    echo "==> nix develop failed to launch Driver (rc=$_nix_rc, empty stream) — relaunching in baked env"
    _run_driver
  fi
else
  _run_driver
fi
claude_rc="$(cat "$_claude_rc_file")"
rm -f "$_claude_rc_file"

# The launcher greps '^SPINDRIFT_OUTCOME ' from the container log, but under
# stream-json the agent's final line is wrapped in a result event. Surface it as
# a bare line so that contract is unchanged.
jq -r 'select(.type == "result") | .result // empty' "$stream_log" 2>/dev/null \
  | grep '^SPINDRIFT_OUTCOME ' | tail -1 || true
rm -f "$stream_log"

[ "$claude_rc" -eq 0 ] || exit "$claude_rc"
echo "==> entrypoint complete for issue #$ISSUE_NUMBER"
