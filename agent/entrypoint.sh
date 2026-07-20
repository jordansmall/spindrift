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

# configure_env sets up the config/env-derived globals every later phase
# reads; it is not itself a numbered phase, just the setup every phase shares.
configure_env() {
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
  # REPO_MOUNT_DIR is the read-only Accumulation-repo mount CODE_FORGE=local
  # clones from instead of a network remote (ADR 0033, issue #1697's /repo
  # mount); unused otherwise.
  REPO_MOUNT_DIR="${REPO_MOUNT_DIR:-/repo}"

  # The canonical SPINDRIFT_OUTCOME contract (issue #419), baked at a sibling
  # path to /agent/prompts so a SPINDRIFT_PROMPT_DIR mount -- which shadows only
  # /agent/prompts -- never hides it (issue #420).
  OUTCOME_CONTRACT_MARKER="# LAND THE CHANGE"
  OUTCOME_CONTRACT_FILE="${OUTCOME_CONTRACT_FILE:-/agent/outcome-contract.md}"

  # The COMMS and CHECK/COMMIT blocks fix-prompt.md shares with issue-prompt.md
  # (issue #455 extends #419/#420's slice mechanism beyond the outcome contract):
  # baked and injected the same way, so a SPINDRIFT_PROMPT_DIR override of the
  # fix prompt gets the identical treatment.
  COMMS_CONTRACT_MARKER="# COMMS"
  COMMS_CONTRACT_FILE="${COMMS_CONTRACT_FILE:-/agent/comms-contract.md}"
  CHECK_CONTRACT_MARKER="# CHECK"
  CHECK_CONTRACT_FILE="${CHECK_CONTRACT_FILE:-/agent/check-contract.md}"

  # The research dispatch kind's own harness-owned outcome contract (ADR 0022,
  # issue #640): posting the verdict comment and emitting the outcome line.
  # Baked and injected the same way as the work contract above, so a
  # SPINDRIFT_PROMPT_DIR override of research-prompt.md gets it too.
  RESEARCH_OUTCOME_CONTRACT_MARKER="# POST THE VERDICT"
  RESEARCH_OUTCOME_CONTRACT_FILE="${RESEARCH_OUTCOME_CONTRACT_FILE:-/agent/research-outcome-contract.md}"

  # DRIVER_BIN, DRIVER_FLAGS_COMMON, and DRIVER_SKILLS_DIR are baked by the
  # selected Driver's lib/drivers/<name>.nix registry entry (ADR 0009, issue
  # #624) via the nix-rendered preamble prepended ahead of this file at image
  # build time. No fallback literal lives here: a Box built without that
  # preamble dies loudly instead of silently impersonating the claude Driver.
  : "${DRIVER_BIN:?DRIVER_BIN not set -- the nix-rendered Driver preamble did not run}"
  : "${DRIVER_FLAGS_COMMON:?DRIVER_FLAGS_COMMON not set -- the nix-rendered Driver preamble did not run}"
  : "${DRIVER_SKILLS_DIR:?DRIVER_SKILLS_DIR not set -- the nix-rendered Driver preamble did not run}"

  # _driver_extract_outcome and _driver_session_flags are defined by the Driver
  # registry (lib/drivers/<name>.nix); a nix-built image prepends them via
  # driverPreamble (lib/mkHarness.nix), and the bats harness sources the same
  # registry-rendered bodies via DRIVER_PREAMBLE_FILE (issue #433).
}

# clone_repo authenticates, clones the target repo into WORK_DIR, sets the
# repo-local git identity, and fetches the latest refs.
clone_repo() {
  # CODE_FORGE=local clones from a local filesystem mount, never github.com,
  # so there is nothing for gh's credential helper to apply to -- skipping it
  # keeps this path a genuine no-forge-network-call guarantee (ADR 0033)
  # rather than merely "the actual clone happens not to use it."
  if [ "${CODE_FORGE:-github}" != "local" ]; then
    export GH_TOKEN
    gh auth setup-git
  fi

  # CODE_FORGE=git clones from and pushes to a configured plain git remote
  # instead of the target GitHub repo (ADR 0013); CODE_FORGE=local clones from
  # the read-only Accumulation-repo mount instead of any network remote (ADR
  # 0033) -- REPO_SLUG still resolves the Issue Tracker regardless of either.
  # Gated on the exact CODE_FORGE value so a stray CODE_FORGE_REMOTE_URL left
  # set in the environment can't silently redirect a CODE_FORGE=github
  # (default) deployment to the wrong remote.
  local CLONE_URL="https://github.com/${REPO_SLUG}.git"
  if [ "${CODE_FORGE:-github}" = "git" ]; then
    CLONE_URL="${CODE_FORGE_REMOTE_URL:?CODE_FORGE_REMOTE_URL is required when CODE_FORGE=git}"
  elif [ "${CODE_FORGE:-github}" = "local" ]; then
    CLONE_URL="$REPO_MOUNT_DIR"
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
}

# phase_branch_recovery adopts prior work on an open PR or force-resets a
# stale branch with no open PR, so this Box always starts from a clean base.
# Sets _rebase_and_publish, read by phase_prework_rebase and
# phase_conflict_resolve.
phase_branch_recovery() {
  # A prior run may have already pushed agent/issue-N before dying.  When no
  # open PR exists, force-reset the remote branch so this Box starts clean and
  # its first incremental push is never rejected non-fast-forward.  When an
  # open PR exists, check out the prior work so the pre-work rebase can replay
  # it onto current origin/BASE_BRANCH before the agent begins.
  _rebase_and_publish=""

  # CODE_FORGE=local has no PR concept and no writable origin -- the
  # Accumulation-repo mount is read-only (ADR 0033), and nothing is ever
  # pushed there mid-session, only bundled out at the very end. A
  # refs/remotes/origin/$BRANCH left by an earlier, abandoned attempt (a
  # landed-then-conflicting bundle, say) is simply superseded by a fresh
  # checkout: there is nothing to adopt via a gh call that would violate the
  # no-forge-network-calls guarantee, and no remote branch this Box could
  # force-push to reset even if it wanted to.
  if [ "${CODE_FORGE:-github}" = "local" ]; then
    echo "==> CODE_FORGE=local: starting $BRANCH fresh from origin/${BASE_BRANCH:-}"
    git checkout -b "$BRANCH" "origin/${BASE_BRANCH:-}"
    return
  fi

  if git rev-parse --verify "refs/remotes/origin/$BRANCH" >/dev/null 2>&1; then
    # Fail hard on gh errors: a silent empty response (network/auth failure)
    # is indistinguishable from "no PR" and must not trigger the force-reset.
    local open_prs
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
}

# phase_prework_rebase rebases the branch onto the latest base before the
# agent starts. Sets _had_rebase_conflict, read by phase_conflict_resolve;
# reads _rebase_and_publish from phase_branch_recovery.
phase_prework_rebase() {
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
}

# phase_toolchain_nudge emits a one-time hint for a cold run with a
# recognized lockfile and no prefetch configured.
phase_toolchain_nudge() {
  # Cold-run toolchain nudge: when no prefetch is configured and a recognized
  # lockfile is present, emit a one-time hint pointing at the two knobs that
  # actually help (prefetch for per-run cache warm, packages for a baked
  # cross-run toolchain). Unknown ecosystems emit nothing.
  if [ -z "${PREFETCH:-}" ]; then
    local _nudge_ecosystem=""
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
}

# phase_devshell_probe detects a Nix devShell in the cloned repo. Sets
# _use_dev_shell (read by phase_prefetch and run_driver_in_env's --devshell
# switch) and _harness_path (read by phase_prefetch only -- run_driver_in_env
# delegates devShell PATH handling to driver-exec, issue #626).
phase_devshell_probe() {
  # Detect a Nix devShell in the cloned repo. When found the prefetch hook and
  # Driver run inside `nix develop` so the agent operates in the Target's exact
  # pinned environment. DEV_SHELL_PROBE_TIMEOUT is nix-baked (env-schema.nix
  # default 300 s) so a heavy consumer devShell eval cannot stall the box.
  # DEV_SHELL_NAME selects which devShell to enter (default "default").
  _use_dev_shell=0
  _harness_path="$PATH"
  if [ -f "flake.nix" ]; then
    echo "==> flake.nix found in cloned repo; probing for devShell"
    local _probe_rc=0
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
}

# phase_prefetch runs the optional mkHarness `prefetch` cache warm-up hook,
# inside the devShell when phase_devshell_probe found one.
phase_prefetch() {
  # Optional cache warm-up (mkHarness `prefetch`, baked into the image env); no-op
  # when unset. When a devShell is available, run inside it so the prefetch
  # command sees the Target's exact toolchain and env vars.
  if [ -n "${PREFETCH:-}" ]; then
    if [ "$_use_dev_shell" = "1" ]; then
      local _pf_wrapper
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
}

# Substitute only known placeholders so literal `$` in the prompt body (shell
# snippets, etc.) survives. The shared vars below are fixed; the Conditional
# fragment registry (lib/fragments.nix, issue #622) contributes the rest via
# the nix-rendered _FRAGMENT_SUBST_VARS array (see fragmentRegistryPreamble in
# lib/mkHarness.nix) — a fragment can reference only what its registry row
# declares, and a forgotten allowlist entry is impossible by construction:
# adding a row needs no edit here. Defined ahead of the fragment loop below
# (issue #463) since each row is itself rendered through this function.
_subst() {
  local f="$1" v
  local -a _names=(
    ISSUE_NUMBER
    ISSUE_TITLE
    BRANCH
    BASE_BRANCH
    IN_PROGRESS_LABEL
    COMPLETE_LABEL
    "${_FRAGMENT_SUBST_VARS[@]}"
  )
  local -a _assign=()
  local _vars=""
  for v in "${_names[@]}"; do
    _assign+=("$v=${!v:-}")
    _vars+="\$$v "
  done
  env "${_assign[@]}" envsubst "$_vars" <"$f"
}

# _is_research_kind reports (via exit status) whether this dispatch is the
# advise-only research kind (ADR 0022, issue #640) rather than work/fix; the
# default is work, so an unset DISPATCH_KIND is never mistaken for research.
_is_research_kind() {
  [ "${DISPATCH_KIND:-work}" = "research" ]
}

# A SPINDRIFT_PROMPT_DIR mount replaces the whole prompt dir, so a rendered
# prompt that dropped a shared block (issue #419, extended to COMMS/CHECK by
# #455) never gets the build-time injection; append the canonical block here,
# at run time, unless it is already present (idempotent, mirrors
# lib/mkHarness.nix).
_inject_shared_block() {
  local marker="$1" file="$2"
  if [[ "$prompt" != *"$marker"* ]]; then
    # A direct assignment from the substitution (rather than nesting it as a
    # printf argument) so a missing/unreadable contract file fails loudly
    # under `set -e` instead of silently rendering an empty block.
    local block
    block="$(_subst "$file")"
    prompt="$(printf '%s\n\n%s' "${prompt%$'\n'}" "$block")"
  fi
}

# phase_conflict_resolve spawns a conflict-resolve agent when
# phase_prework_rebase hit a conflict, and handles the CONFLICT_RESOLVE_PR_URL
# resolve-only dispatch mode. Reads _had_rebase_conflict and
# _rebase_and_publish.
phase_conflict_resolve() {
  # When the pre-work rebase produced conflicts, spawn a conflict-resolve agent to
  # re-map the branch onto current main.  Only escalate to exit 1 if the agent
  # genuinely cannot resolve.
  if [ -n "${_had_rebase_conflict:-}" ]; then
    echo "==> pre-work rebase conflict detected — invoking conflict-resolve agent"
    local _cr_prompt
    _cr_prompt="$(_subst "${PROMPTS_DIR}/conflict-resolve-prompt.md")"
    # No agents config or session to pin/resume for this pass; its exit
    # status isn't checked here either — success is read off the rebase
    # state below instead. Shadows _use_dev_shell to 0 for this call only
    # (bash dynamic scoping resolves to the nearest enclosing local, same
    # mechanism issue #515 documents for the other cross-phase sentinels):
    # this pass ran outside the devShell before the two invocations were
    # unified, and stays there — only the main run enters it.
    local _use_dev_shell=0
    run_driver_in_env "$_cr_prompt" "" "" || true
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
}

# phase_prompt_assembly renders the conditional opt-in prompt fragments,
# runs phase_conflict_resolve, selects the issue/fix prompt, injects the
# shared COMMS/CHECK/OUTCOME blocks, and builds the --agents JSON. Sets
# prompt, _driver_session_mode, and agents_json, all read by
# run_driver_in_env in main.
phase_prompt_assembly() {
  # The conditional prompt steps below are rendered from fragment files under
  # PROMPTS_DIR/fragments (issue #463) instead of heredocs authored in this
  # script, so all instruction prose lives with the rest of the prompt surface
  # and a SPINDRIFT_PROMPT_DIR override supplies its own fragment for any knob
  # it enables, exactly as it already must supply filer-prompt.md when the
  # filer is configured (documented in docs/reference.md).
  #
  # Each fragment file ends with the blank line that separates it from the
  # heading it precedes in the rendered prompt (e.g. issue-prompt.md's
  # `${AUTO_FORMAT_STEP}${AUTO_LINT_STEP}# COMMIT`), but `$(...)` command
  # substitution strips every trailing newline, blank line included -- and
  # stripping happens again at the OUTER `$(...)` around any function that
  # tries to reappend it internally, so the concatenation has to happen at the
  # assignment site itself, outside every command substitution: `"$(_subst
  # "$f")"$'\n\n'`, never `"$(some_wrapper "$f")"`.

  # Discover available skills at DRIVER_SKILLS_DIR and build a directive to
  # prefer them over the inline guidance where they apply -- the gate
  # variable the skill-preamble registry row (lib/fragments.nix) names.
  # Claude Code discovers a skill as a directory holding a SKILL.md
  # (DRIVER_SKILLS_DIR/<name>/SKILL.md), never a flat <name>.md file, so the
  # skill name advertised in SKILLS_FOUND is the directory basename.
  local SKILLS_FOUND=""
  if [ -d "$DRIVER_SKILLS_DIR" ]; then
    local _sf _sn
    for _sf in "${DRIVER_SKILLS_DIR}/"*/SKILL.md; do
      [ -f "$_sf" ] || continue
      _sn="$(basename "$(dirname "$_sf")")"
      SKILLS_FOUND="${SKILLS_FOUND:+${SKILLS_FOUND}, }${_sn}"
    done
  fi

  # One-liners setting the computed gates the caveman-default,
  # tdd-default, commit-default, code-review-default, and
  # file-issues registry rows name: each per-skill gate is
  # the specific skill actually baked at DRIVER_SKILLS_DIR/<name>/SKILL.md, so
  # the prompt step directing the agent to that skill renders only when it is
  # present (issue #487); and the filer opt-in provisioned in
  # AGENTS_JSON_TEMPLATE. These sit alongside the generic SKILLS_FOUND preamble:
  # that lists every baked skill, while these place a deferral at the exact
  # section whose inline guidance the named skill supersedes.
  local CAVEMAN_BAKED=""
  # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
  [ -f "${DRIVER_SKILLS_DIR}/caveman/SKILL.md" ] && CAVEMAN_BAKED=1
  local TDD_BAKED=""
  # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
  [ -f "${DRIVER_SKILLS_DIR}/tdd/SKILL.md" ] && TDD_BAKED=1
  local COMMIT_BAKED=""
  # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
  [ -f "${DRIVER_SKILLS_DIR}/commit/SKILL.md" ] && COMMIT_BAKED=1
  local CODE_REVIEW_BAKED=""
  # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
  [ -f "${DRIVER_SKILLS_DIR}/code-review/SKILL.md" ] && CODE_REVIEW_BAKED=1
  local FILER_ENABLED=""
  if [ -n "${AGENTS_JSON_TEMPLATE:-}" ] && printf '%s' "$AGENTS_JSON_TEMPLATE" | jq -e 'has("filer")' >/dev/null 2>&1; then
    # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
    FILER_ENABLED=1
  fi

  # The PR-body ticket-reference gates the pr-body-closes/pr-body-local-ref/
  # pr-body-local-noref registry rows name (issue #1429, ADR 0029): exactly
  # one is ever on, picked from ISSUE_TRACKER x LOCAL_ISSUE_REFERENCE (both
  # launcher-delivered knobs) rather than a single knob's presence, since no
  # one knob alone selects among three cases. github always keeps the
  # unconditional `Closes #ISSUE_NUMBER`; local's default is no reference to
  # the (private, possibly numeric-slugged) ticket at all, closing the
  # Closes-#<slug> auto-close footgun; local's opt-in swaps in a non-auto-
  # closing `Local-issue: <slug>` breadcrumb instead. jira falls into the
  # same `else` branch as github here -- issue #1429's footgun is
  # local-only (a jira key isn't a bare number GitHub's auto-close syntax
  # would match), so jira's `Closes #ISSUE_NUMBER` stays exactly as it was
  # pre-#1429, unconditional and out of this issue's scope.
  local PR_BODY_CLOSES=""
  local PR_BODY_LOCAL_REF=""
  local PR_BODY_LOCAL_NOREF=""
  if [ "${ISSUE_TRACKER:-github}" = "local" ]; then
    if [ -n "${LOCAL_ISSUE_REFERENCE:-}" ]; then
      # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
      PR_BODY_LOCAL_REF=1
    else
      # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
      PR_BODY_LOCAL_NOREF=1
    fi
  else
    # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
    PR_BODY_CLOSES=1
  fi

  # The issue-read step gate (issue #1691, ADR 0032): local issues have no
  # in-box reachability -- there is no server to reach and gh issue view
  # ${ISSUE_NUMBER} either fails or, for a numeric slug, silently fetches an
  # unrelated real issue -- so the read step branches on ISSUE_TRACKER between
  # gh issue view (github, and jira, which shares github's in-box reachability)
  # and the read-only /issues mount (local).
  local ISSUE_TRACKER_GITHUB=""
  local ISSUE_TRACKER_LOCAL=""
  if [ "${ISSUE_TRACKER:-github}" = "local" ]; then
    # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
    ISSUE_TRACKER_LOCAL=1
  else
    # shellcheck disable=SC2034 # read indirectly via "${!_fgate}" in the loop below
    ISSUE_TRACKER_GITHUB=1
  fi

  # One loop over the Conditional fragment registry (lib/fragments.nix, issue
  # #622), rendered into _FRAGMENT_ROWS by lib/mkHarness.nix's
  # fragmentRegistryPreamble: each row's gate variable (a knob env var for
  # auto-format/auto-lint/CI-failure, or one of the precompute
  # variables set above) controls whether its fragment renders into its
  # substitution variable or is left empty -- the same conditional-residue
  # mechanism the six blocks this replaced each had: a default box's
  # rendered prompt carries no trace of a step whose gate is off. Adding a
  # row is a nix-only change (lib/fragments.nix): no edit here.
  local _frow _fgate _ffile _fvar
  for _frow in "${_FRAGMENT_ROWS[@]}"; do
    IFS='|' read -r _fgate _ffile _fvar <<<"$_frow"
    local "$_fvar"
    if [ -n "${!_fgate:-}" ]; then
      printf -v "$_fvar" '%s' "$(_subst "${PROMPTS_DIR}/fragments/${_ffile}")"$'\n\n'
    else
      printf -v "$_fvar" '%s' ""
    fi
  done

  phase_conflict_resolve

  # DISPATCH_KIND=research (ADR 0022, issue #640) selects the research prompt
  # instead of the work issue-prompt.md; it takes precedence over FIX_PASS
  # since a research dispatch never has a warm fix pass. Kind is forwarded by
  # the launcher (cmd/launcher/internal/dispatch); defaults to "work" so every
  # pre-existing (kind-unaware) construction site is unaffected.
  #
  # FIX_PASS is set by the launcher on a fix box (dispatched when CI comes back
  # red on an already-open PR, ADR: selfHeal/runFix in cmd/launcher). A warm fix
  # pass already has the branch checked out and prior work in place, so it runs
  # a dedicated fix-prompt instead of the cold issue-prompt a fresh run uses.
  if _is_research_kind; then
    prompt="$(_subst "${PROMPTS_DIR}/research-prompt.md")"
    _driver_session_mode="initial"
  elif [ -n "${FIX_PASS:-}" ] && [ "${FIX_PASS}" -gt 0 ]; then
    prompt="$(_subst "${PROMPTS_DIR}/fix-prompt.md")"
    _driver_session_mode="resume"
  else
    prompt="$(_subst "${PROMPTS_DIR}/issue-prompt.md")"
    _driver_session_mode="initial"
  fi
  # Applied in COMMS, CHECK, OUTCOME order so a prompt missing all three (e.g.
  # fix-prompt.md's fix-specific-preamble-only default) ends up with them in
  # the same order the issue prompt carries them. The research prompt carries
  # none of these work-only blocks -- it gets its own outcome contract instead.
  if _is_research_kind; then
    _inject_shared_block "$RESEARCH_OUTCOME_CONTRACT_MARKER" "$RESEARCH_OUTCOME_CONTRACT_FILE"
  else
    _inject_shared_block "$COMMS_CONTRACT_MARKER" "$COMMS_CONTRACT_FILE"
    _inject_shared_block "$CHECK_CONTRACT_MARKER" "$CHECK_CONTRACT_FILE"
    _inject_shared_block "$OUTCOME_CONTRACT_MARKER" "$OUTCOME_CONTRACT_FILE"
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
    local scout_prompt review_prompt filer_prompt
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
  else
    agents_json=""
  fi
}

# run_driver_in_env runs the Driver against $1 (the assembled prompt), with
# $2 (--agents JSON, or "" to omit the flag) and $3 (session mode, forwarded
# verbatim to the nix-supplied _driver_session_flags — "initial"/"resume" pin
# or resume the issue's session id; any other value, e.g. "" for the
# conflict-resolve pass, yields no session flags). Delegates to driver-exec
# (issue #626), the in-box Go unit that owns "run the Driver, optionally
# inside the Project devShell" as one code path: it takes the prompt/agents/
# session as file paths (a compiled binary crosses the devShell process
# boundary with a plain argv, so none of the temp-file/eval marshalling this
# function used to do is needed here), spawns the Driver directly or via
# `nix develop --command` when phase_devshell_probe found one
# (_use_dev_shell, read via bash's dynamic scoping like every other phase
# function), tees the stream to a log path, filters heartbeats in-process,
# and returns the Driver's exit status.
run_driver_in_env() {
  local prompt="$1" agents_json="$2" session_mode="$3"

  # An unrecognized session_mode (e.g. "" for the conflict-resolve pass, which
  # pins/resumes no session) falls through _driver_session_flags' case with no
  # output, so the session file below ends up empty — same as before.
  local _driver_session_flags_rendered
  _driver_session_flags_rendered="$(_driver_session_flags "$session_mode")"

  # The prompt/agents/session data crosses into driver-exec as file paths --
  # a compiled binary, unlike the devShell wrapper, needs no quoting-hazard
  # workaround for the prompt or word-splitting-hazard workaround for JSON.
  local _prompt_file _agents_file _session_file stream_log
  _prompt_file="$(mktemp)"
  printf '%s' "$prompt" > "$_prompt_file"
  _agents_file="$(mktemp)"
  if [ -n "$agents_json" ]; then
    printf '%s' "$agents_json" > "$_agents_file"
  fi
  _session_file="$(mktemp)"
  printf '%s' "$_driver_session_flags_rendered" > "$_session_file"

  # stream_log is driver-exec's teed copy of the Driver's raw stdout, read
  # below by _driver_extract_outcome -- the launcher's own capture of stdout
  # (logs/issue-<n>.log, byte-exact, unchanged) is separate and untouched.
  stream_log="$(mktemp)"

  local -a _devshell_flags=()
  if [ "$_use_dev_shell" = "1" ]; then
    _devshell_flags=(--devshell --devshell-name "${DEV_SHELL_NAME:-default}")
  fi

  local claude_rc=0
  set +e
  driver-exec \
    --prompt-file "$_prompt_file" \
    --agents-file "$_agents_file" \
    --session-file "$_session_file" \
    --driver-bin "$DRIVER_BIN" \
    --driver-flags "$DRIVER_FLAGS_COMMON" \
    --model "${MODEL:-}" \
    --issue "$ISSUE_NUMBER" \
    --log-path "$stream_log" \
    "${_devshell_flags[@]}"
  claude_rc=$?
  set -e
  rm -f "$_prompt_file" "$_agents_file" "$_session_file"

  # The launcher greps '^SPINDRIFT_OUTCOME ' from the container log, but the
  # Driver's raw transcript format buries it (claude wraps it in a stream-json
  # result event); _driver_extract_outcome surfaces it as a bare line so that
  # contract is unchanged. Captured (rather than left to print directly) so
  # main's post-return backstop (issue #593) can tell whether the Driver
  # actually emitted one.
  _last_outcome_line="$(_driver_extract_outcome "$stream_log")"
  rm -f "$stream_log"
  if [ -n "$_last_outcome_line" ]; then
    printf '%s\n' "$_last_outcome_line"
  fi

  return "$claude_rc"
}

# emit_outcome_backstop pushes any committed work on BRANCH best-effort, then
# prints a single synthetic status=blocked SPINDRIFT_OUTCOME line -- called
# from main only when the Driver's run produced no parseable outcome line, so
# the launcher always gets a terminal signal to classify (issue #593) instead
# of a silent gap.
#
# Draft-ness retired as a salvage signal (issue #1654): the Driver no longer
# flips a PR out of draft itself (#1653) -- the launcher owns that flip, once
# CI is green, before it merges. So a PR's draft state here says nothing
# about whether the Driver reached status=ready and just lost the line on
# the way out; synthesize status=blocked unconditionally, exactly as for a
# missing PR. The launcher's own no-outcome path agrees: it never adopts a
# PR off draft-ness either (cmd/launcher/internal/settle/gate.go), so both
# sides land on the same terminal classification instead of one side staying
# silent for the other to settle.
emit_outcome_backstop() {
  local note="driver exited without emitting an outcome"
  if [ -n "${_recovery_attempted:-}" ]; then
    note="${note}; a resume attempt also produced no outcome"
  fi
  echo "==> driver produced no SPINDRIFT_OUTCOME line — emitting synthetic backstop"
  # A research dispatch never cuts a branch (ADR 0022) -- there is nothing to
  # push best-effort, and no landing reference beyond "none".
  if _is_research_kind; then
    echo "SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=none status=blocked note=${note}"
    return
  fi
  # Nothing to preserve if BRANCH never advanced past the base -- pushing it
  # anyway would publish an empty branch that looks like lost work (#1606).
  # Fall back to "assume there is work" rather than let a resolution failure
  # abort this function under `set -e` -- that would skip the always-emit
  # outcome invariant (#593) entirely, worse than a needless push attempt.
  local commit_count
  commit_count="$(git rev-list --count "origin/${BASE_BRANCH:-}..${BRANCH}" 2>/dev/null)" || commit_count=1
  if [ "$commit_count" -eq 0 ]; then
    note="${note}; no work to preserve"
  elif [ "${CODE_FORGE:-github}" = "local" ]; then
    # origin is the read-only Accumulation-repo mount under CODE_FORGE=local
    # (ADR 0033) -- a push here would only ever fail, and the commits are
    # already sitting in this Box's own clone regardless, unlike git/github
    # where a push is the only way work survives the container exiting.
    note="${note}; no bundle was ever emitted (no writable remote under CODE_FORGE=local)"
  else
    local push_log
    push_log="$(mktemp)"
    if ! git push --force-with-lease origin "$BRANCH" 2>"$push_log"; then
      note="${note}; push failed: $(tail -1 "$push_log")"
    fi
    rm -f "$push_log"
  fi

  echo "SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=blocked note=${note}"
}

main() {
  # Cross-phase sentinels: declared local here so bash's dynamic scoping lets
  # each phase function assign them by plain (non-local) assignment while
  # keeping them out of true global scope (issue #515).
  local _rebase_and_publish _had_rebase_conflict
  local _use_dev_shell _harness_path
  local prompt agents_json _driver_session_mode
  local _last_outcome_line _recovery_attempted

  configure_env
  clone_repo
  # A research dispatch (ADR 0022, issue #640) explores the fresh clone but
  # never lands code: no branch to cut, adopt, or rebase.
  if ! _is_research_kind; then
    phase_branch_recovery
    phase_prework_rebase
  fi
  phase_toolchain_nudge
  phase_devshell_probe
  phase_prefetch
  phase_prompt_assembly

  if _is_research_kind; then
    echo "==> claude researching issue #$ISSUE_NUMBER"
  else
    echo "==> claude implementing issue #$ISSUE_NUMBER on $BRANCH"
  fi
  local claude_rc=0
  _recovery_attempted=""
  run_driver_in_env "$prompt" "$agents_json" "$_driver_session_mode" || claude_rc=$?

  # A driver that exited cleanly yet told us nothing most often just ended
  # its turn early (issue #1542: ~15 minutes of scouting thrown away because
  # the run ended "waiting" on a backgrounded task) rather than actually
  # failing. Before falling back to the synthetic backstop, resume the same
  # pinned session exactly once with a corrective nudge (issue #1607).
  # Research dispatches pin no session worth resuming (ADR 0022) so they skip
  # straight to the backstop below, same as before. The same --agents JSON as
  # the first pass rides along too -- the run may still need to reach the
  # scout/reviewer/filer step it never got to, and the pinned session has no
  # other way to learn about them.
  if [ "$claude_rc" -eq 0 ] && [ -z "$_last_outcome_line" ] && ! _is_research_kind; then
    echo "==> driver produced no SPINDRIFT_OUTCOME line — resuming the session once with a nudge"
    _recovery_attempted=1
    local recovery_prompt="The run ended without printing a SPINDRIFT_OUTCOME line. Finish the workflow: run any remaining checks/gates in the foreground, then print the required SPINDRIFT_OUTCOME line as your final message."
    run_driver_in_env "$recovery_prompt" "$agents_json" "resume" || claude_rc=$?
  fi

  # Only a driver that exited cleanly yet told us nothing gets the synthetic
  # backstop. A non-zero exit is left to propagate untouched -- the
  # launcher's own ClassifyTransient/retry path (cmd/launcher/internal/dispatch)
  # already owns that case, and only runs when the container's own exit code
  # is non-zero; forcing exit 0 here would silently turn a retryable
  # transient failure into a terminal synthetic status=blocked (issue #593).
  if [ "$claude_rc" -eq 0 ] && [ -z "$_last_outcome_line" ]; then
    emit_outcome_backstop
    exit 0
  fi

  [ "$claude_rc" -eq 0 ] || exit "$claude_rc"
  echo "==> entrypoint complete for issue #$ISSUE_NUMBER"
}

main "$@"
