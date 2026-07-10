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

# NIX_STORE_WRITABLE is baked into the image Env by mkHarness's
# nixStoreWritable knob (ADR 0018, issue #469): self-test mode trades
# hermeticity for in-box `nix flake check` feedback, so it must be loud at
# Box start. New store paths land only in this container's ephemeral
# copy-on-write layer -- the image and any shared volumes are never mutated.
if [ "${NIX_STORE_WRITABLE:-false}" = "true" ]; then
  echo "==> WARNING: /nix/store is writable (self-test mode) — this Box is not hermetic; do not use for untrusted issues"
fi

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

# The canonical SPINDRIFT_OUTCOME contract (issue #419), baked at a sibling
# path to /agent/prompts so a SPINDRIFT_PROMPT_DIR mount -- which shadows only
# /agent/prompts -- never hides it (issue #420).
OUTCOME_CONTRACT_MARKER="# LAND THE CHANGE"
OUTCOME_CONTRACT_FILE="${OUTCOME_CONTRACT_FILE:-/agent/outcome-contract.md}"

# DRIVER_BIN, DRIVER_FLAGS_COMMON, and DRIVER_SKILLS_DIR are baked by the
# selected Driver's lib/drivers/<name>.nix (ADR 0009); the defaults here keep
# the :-expansions below quiet under set -u when the nix preamble is absent.
DRIVER_BIN="${DRIVER_BIN:-claude}"
DRIVER_FLAGS_COMMON="${DRIVER_FLAGS_COMMON:---verbose --output-format stream-json --dangerously-skip-permissions}"
DRIVER_SKILLS_DIR="${DRIVER_SKILLS_DIR:-${HOME:-}/.claude/skills}"

# _driver_extract_outcome and _driver_session_flags are defined by the Driver
# registry (lib/drivers/<name>.nix); a nix-built image prepends them via
# driverPreamble (lib/mkHarness.nix), and the bats harness sources the same
# registry-rendered bodies via DRIVER_PREAMBLE_FILE (issue #433).

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

# Discover available skills at DRIVER_SKILLS_DIR and build a directive to
# prefer them over the inline guidance where they apply.
SKILL_PREAMBLE=""
_skills_found=""
if [ -d "$DRIVER_SKILLS_DIR" ]; then
  for _sf in "${DRIVER_SKILLS_DIR}/"*.md; do
    [ -f "$_sf" ] || continue
    _sn="$(basename "$_sf" .md)"
    _skills_found="${_skills_found:+${_skills_found}, }${_sn}"
  done
fi
if [ -n "$_skills_found" ]; then
  SKILL_PREAMBLE="Skills available: ${_skills_found}. Prefer invoking with /skill-name; the inline guidance below is the fallback when a skill is absent.

"
fi

# The FILE ISSUES step is substituted into the issue prompt only when the
# filer opt-in is set (FILER_MODEL non-empty), the same conditional-residue
# mechanism as SKILL_PREAMBLE above: empty when off, so a filer-free box's
# rendered prompt carries no trace of the step.
FILE_ISSUES_STEP=""
if [ -n "${AGENTS_JSON_TEMPLATE:-}" ] && printf '%s' "$AGENTS_JSON_TEMPLATE" | jq -e 'has("filer")' >/dev/null 2>&1; then
  FILE_ISSUES_STEP="# FILE ISSUES

Delegate the review verdict's Non-blocking section to the filer subagent
before opening the PR. It is pre-provisioned via --agents; pass it the
Non-blocking findings verbatim, the issue number, and the PR URL (or branch,
if not yet opened) for provenance.

Best-effort: filing must never block the PR or change the outcome line.

- On success, use the filer's returned issue URLs in the PR body instead of
  the raw findings.
- On failure (the filer errors, times out, or returns nothing usable), fall
  back to pasting the raw Non-blocking findings into the PR body and proceed
  — exactly as when the filer is not configured.

"
fi

# AUTO_FORMAT_STEP is substituted into the issue prompt only when AUTO_FORMAT
# is non-empty (opt-in knob) — the same conditional-residue mechanism as
# FILE_ISSUES_STEP above: empty when off, so a default box carries no trace of
# the step and the formatter is never mentioned.
AUTO_FORMAT_STEP=""
if [ -n "${AUTO_FORMAT:-}" ]; then
  # Backticks are markdown formatting, not command substitution — SC2016.
  # shellcheck disable=SC2016
  AUTO_FORMAT_STEP='# AUTO-FORMAT

Before committing, auto-format the files you changed:

1. Detect the project'"'"'s formatter, in order of preference:
   - `nix fmt` when the target flake defines a formatter.
   - A `format` or `fmt` script/target in `package.json`, `Makefile`, or `justfile`.
   - The standard formatter for the language (e.g. `gofmt -w`, `cargo fmt`, `black`).
2. Run it only on the files you changed (from `git diff --name-only` vs the
   base branch), where the formatter accepts explicit paths. Fall back to a
   project-wide run when the formatter does not support per-file invocation.
3. Skip silently when no formatter is found — this must never fail the run.

'
fi

# AUTO_LINT_STEP is substituted into the issue prompt only when AUTO_LINT is
# non-empty (opt-in knob) — the same conditional-residue mechanism as
# AUTO_FORMAT_STEP above: empty when off, so a default box carries no trace of
# the step and the linter is never mentioned.
AUTO_LINT_STEP=""
if [ -n "${AUTO_LINT:-}" ]; then
  # Backticks are markdown formatting, not command substitution — SC2016.
  # shellcheck disable=SC2016
  AUTO_LINT_STEP='# AUTO-LINT

Before committing, lint the files you changed and resolve what you find:

1. Detect the project'"'"'s linter, in order of preference:
   - A `lint` target in the project'"'"'s build config (`package.json` script,
     `Makefile`, `justfile`), or a checker the flake/devShell exposes.
   - The standard linter for the language (e.g. `eslint`, `ruff`/`flake8`,
     `golangci-lint`/`go vet`, `clippy`, `statix`).
2. Run it only on the files you changed (from `git diff --name-only` vs the
   base branch), where the linter accepts explicit paths.
3. Apply the linter'"'"'s safe auto-fix mode where available, then manually
   resolve the remaining findings in the changed files before committing.
4. Skip silently when no linter is found — this must never fail the run.

'
fi

# CI_FAILURE_STEP is substituted into the fix prompt only when the launcher
# forwarded a CI_FAILURE_SUMMARY (selfHeal captured it on genuine-red, issue
# #426) — the same conditional-residue mechanism as SKILL_PREAMBLE above:
# empty when absent, so a fix box with no fetched detail carries no trace of
# the step and the prompt falls back to its own local-check flow with no error.
CI_FAILURE_STEP=""
if [ -n "${CI_FAILURE_SUMMARY:-}" ]; then
  CI_FAILURE_STEP="# CI FAILURE

The launcher captured this from the failing PR checks — treat it as the known
failure instead of re-discovering it from scratch:

${CI_FAILURE_SUMMARY}

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
    FILE_ISSUES_STEP="${FILE_ISSUES_STEP:-}" \
    AUTO_FORMAT_STEP="${AUTO_FORMAT_STEP:-}" \
    AUTO_LINT_STEP="${AUTO_LINT_STEP:-}" \
    CI_FAILURE_STEP="${CI_FAILURE_STEP:-}" \
    envsubst '$ISSUE_NUMBER $ISSUE_TITLE $BRANCH $BASE_BRANCH $IN_PROGRESS_LABEL $COMPLETE_LABEL $SKILL_PREAMBLE $FILE_ISSUES_STEP $AUTO_FORMAT_STEP $AUTO_LINT_STEP $CI_FAILURE_STEP' \
    <"$1"
}
# When the pre-work rebase produced conflicts, spawn a conflict-resolve agent to
# re-map the branch onto current main.  Only escalate to exit 1 if the agent
# genuinely cannot resolve.
if [ -n "${_had_rebase_conflict:-}" ]; then
  echo "==> pre-work rebase conflict detected — invoking conflict-resolve agent"
  _cr_prompt="$(_subst "${PROMPTS_DIR}/conflict-resolve-prompt.md")"
  set +e
  # shellcheck disable=SC2086
  "$DRIVER_BIN" -p "$_cr_prompt" \
    --model "${MODEL:-}" \
    $DRIVER_FLAGS_COMMON
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

# FIX_PASS is set by the launcher on a fix box (dispatched when CI comes back
# red on an already-open PR, ADR: selfHeal/runFix in cmd/launcher). A warm fix
# pass already has the branch checked out and prior work in place, so it runs
# a dedicated fix-prompt instead of the cold issue-prompt a fresh run uses.
if [ -n "${FIX_PASS:-}" ] && [ "${FIX_PASS}" -gt 0 ]; then
  prompt="$(_subst "${PROMPTS_DIR}/fix-prompt.md")"
  _driver_session_mode="resume"
else
  prompt="$(_subst "${PROMPTS_DIR}/issue-prompt.md")"
  _driver_session_mode="initial"
fi
# Computed once so both the direct and devShell-wrapped invocations below
# pin/resume the identical session id (issue #427); an empty result on
# "resume" means no prior session was found and contributes no extra argv.
read -ra _driver_session_args <<< "$(_driver_session_flags "$_driver_session_mode")"
# A SPINDRIFT_PROMPT_DIR mount replaces the whole prompt dir, so a rendered
# prompt that dropped the contract (issue #419) never gets the build-time
# injection; append the same canonical contract here, at run time, unless
# it is already present (idempotent, mirrors lib/mkHarness.nix).
if [[ "$prompt" != *"$OUTCOME_CONTRACT_MARKER"* ]]; then
  # A direct assignment from the substitution (rather than nesting it as a
  # printf argument) so a missing/unreadable OUTCOME_CONTRACT_FILE fails loudly
  # under `set -e` instead of silently rendering an empty contract block.
  outcome_contract="$(_subst "$OUTCOME_CONTRACT_FILE")"
  prompt="$(printf '%s\n\n%s' "${prompt%$'\n'}" "$outcome_contract")"
fi

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
  filer_prompt=""
  if printf '%s' "$AGENTS_JSON_TEMPLATE" | jq -e 'has("filer")' >/dev/null 2>&1; then
    filer_prompt="$(_subst "${PROMPTS_DIR}/filer-prompt.md")"
  fi
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
  # shellcheck disable=SC2086
  "$DRIVER_BIN" -p "$prompt" \
    --model "${MODEL:-}" \
    "${agents_args[@]}" \
    "${_driver_session_args[@]}" \
    $DRIVER_FLAGS_COMMON \
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
  # Session pin/resume args (issue #427) cross into the devShell wrapper's own
  # process the same way: written to a file, rebuilt into an array there — a
  # bash array cannot cross a process boundary via the environment.
  _session_file="$(mktemp)"
  printf '%s' "${_driver_session_args[*]}" > "$_session_file"
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
read -ra _session_arg <<< "$(cat "$_SESSION_FILE")"
# shellcheck disable=SC2086
"$DRIVER_BIN" -p "$(cat "$_PROMPT_FILE")" \
  --model "${MODEL:-}" \
  "${_agents_arg[@]}" \
  "${_session_arg[@]}" \
  $DRIVER_FLAGS_COMMON \
  | spindrift-heartbeat-filter -n "$ISSUE_NUMBER" -f /tmp/heartbeat.log \
  | tee "$_STREAM_LOG"
printf '%s' "${PIPESTATUS[0]}" > "$_CLAUDE_RC_FILE"
DRIVER_WRAPPER_EOF
  chmod +x "$_driver_wrapper"
  export _HARNESS_PATH="$_harness_path"
  export _PROMPT_FILE="$_prompt_file"
  export _AGENTS_FILE="$_agents_file"
  export _SESSION_FILE="$_session_file"
  export _STREAM_LOG="$stream_log"
  export _CLAUDE_RC_FILE="$_claude_rc_file"
  # MODEL comes from the preamble (not exported by default); export it so the
  # devShell child process sees the correct resolved value, not an empty string.
  export MODEL="${MODEL:-}"
  # DRIVER_BIN and DRIVER_FLAGS_COMMON are plain (unexported) assignments
  # above; export them so the devShell child process — which reads them from
  # its own environment, not this script's variables — sees the same values.
  export DRIVER_BIN
  export DRIVER_FLAGS_COMMON
  set +e
  nix develop ".#${DEV_SHELL_NAME:-default}" --command bash "$_driver_wrapper"
  _nix_rc=$?
  set -e
  rm -f "$_driver_wrapper" "$_prompt_file" "$_agents_file" "$_session_file"
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

# The launcher greps '^SPINDRIFT_OUTCOME ' from the container log, but the
# Driver's raw transcript format buries it (claude wraps it in a stream-json
# result event); _driver_extract_outcome surfaces it as a bare line so that
# contract is unchanged.
_driver_extract_outcome "$stream_log"
rm -f "$stream_log"

[ "$claude_rc" -eq 0 ] || exit "$claude_rc"
echo "==> entrypoint complete for issue #$ISSUE_NUMBER"
