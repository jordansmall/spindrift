#!/usr/bin/env bash
# Runs inside the disposable container: clones the target repo, cuts a branch, hands
# off to a headless agent. SPINDRIFT_PROMPT_DIR overrides the baked /agent/prompts
# without a rebuild. --dangerously-skip-permissions is safe because the container is
# the boundary: the agent acts freely, but only on a throwaway clone, never the host.
set -euo pipefail

# Fully-local mode talks to no real forge, so REPO_SLUG and GH_TOKEN have
# nothing to resolve against. The launcher's own validate() (cmd/launcher/
# main.go) already made this call and forwards it as BOX_FULLY_LOCAL (issue
# #2527); read that rather than re-deriving it from CODE_FORGE/ISSUE_TRACKER.
fully_local=false
if [ -n "${BOX_FULLY_LOCAL:-}" ]; then
  fully_local=true
fi
# Self-contained research (issue #2202) clones no repo, so REPO_SLUG/GH_TOKEN
# have nothing to resolve against either. SELF_CONTAINED stays a raw runtime
# input (a per-dispatch knob, not a nix-resolved capability signal); the
# local-tracker half arrives as the forwarded BOX_IN_BOX_UNREACHABLE_TRACKER
# signal (issue #2527) instead of a raw ISSUE_TRACKER=local comparison.
no_repo=false
if [ "${SELF_CONTAINED:-}" = 1 ] && [ -n "${BOX_IN_BOX_UNREACHABLE_TRACKER:-}" ]; then
  no_repo=true
fi
[ "$fully_local" = true ] || [ "$no_repo" = true ] || : "${GH_TOKEN:?GH_TOKEN is required}"
# The kind's axes, exported by dispatch.buildBoxEnv (ADR 0056, issue #3996).
# This file reads them and never branches on DISPATCH_KIND, which is
# display-only here (tests/entrypoint-kind-axes.bats pins that).
: "${DISPATCH_KEY:?DISPATCH_KEY is required}"
: "${DISPATCH_KEYING:?DISPATCH_KEYING is required}"
: "${DISPATCH_ANNOUNCE_VERB:?DISPATCH_ANNOUNCE_VERB is required}"
# A chore-keyed Dispatch (the butler) carries one Ledger Chore, never a
# tracker issue, so it requires CHORE_NAME in ISSUE_NUMBER's place.
if [ "$DISPATCH_KEYING" = "chore" ]; then
  : "${CHORE_NAME:?CHORE_NAME is required}"
else
  : "${ISSUE_NUMBER:?ISSUE_NUMBER is required}"
fi
[ "$fully_local" = true ] || [ "$no_repo" = true ] || : "${REPO_SLUG:?REPO_SLUG (owner/repo) is required}"
: "${GIT_USER_NAME:?GIT_USER_NAME is required}"
: "${GIT_USER_EMAIL:?GIT_USER_EMAIL is required}"

# configure_env is the shared setup every phase_* function depends on; it is not
# itself a numbered phase.
configure_env() {
  # NIX_STORE_WRITABLE is baked by mkHarness's nixStoreWritable knob (ADR 0018,
  # issue #469): self-test mode trades hermeticity for in-box `nix flake check`
  # feedback, so it must be loud at Box start. New store paths land only in this
  # container's ephemeral layer; the image and shared volumes are never mutated.
  if [ "${NIX_STORE_WRITABLE:-false}" = "true" ]; then
    echo "==> WARNING: /nix/store is writable (self-test mode) — this Box is not hermetic; do not use for untrusted issues"
  fi

  # BASE_BRANCH, BRANCH_PREFIX, MODEL, SCOUT_MODEL, REVIEW_MODEL,
  # IN_PROGRESS_LABEL and COMPLETE_LABEL
  # come from the nix-rendered defaults preamble (env-schema.nix) prepended at
  # image-build time; AGENTS_JSON_TEMPLATE rides that preamble as a derived
  # value, not a schema knob. The :- expansions keep set -u and the linter happy.
  # A butler Box never checks this branch out (advise-only, ADR 0022) or
  # pushes it, so the value only has to be legal and stable across a rerun.
  export BRANCH="${BRANCH_PREFIX:-}${DISPATCH_KEY}"

  # Overridable only so the harness can be exercised on the host without a
  # container. WORK_DIR/REPO_MOUNT_DIR/OUTBOX_DIR are true runtime mount points,
  # not baked artifacts, so they keep literal defaults here; PROMPTS_DIR moved to
  # the nix-rendered agent-paths preamble (lib/preambles.nix, issue #2531).
  WORK_DIR="${WORK_DIR:-/work}"
  # REPO_MOUNT_DIR is the read-only Accumulation-repo mount CODE_FORGE=local
  # clones from instead of a network remote (ADR 0033, issue #1697); unused
  # otherwise.
  REPO_MOUNT_DIR="${REPO_MOUNT_DIR:-/repo}"
  # OUTBOX_DIR is the writable mount box's bundle-out step writes
  # CODE_FORGE=local's seam bundle into (ADR 0033, issue #1808); unused
  # otherwise.
  OUTBOX_DIR="${OUTBOX_DIR:-/outbox}"
  # REGISTRY_PROXY_MANIFEST (ADR 0045, issue #3141) carries the registry proxy's
  # endpoint and route table as one JSON env var. box parses it straight out of
  # its own inherited environment, so this file needs no default, no override,
  # and no flag to pass it through.
  # REGISTRY_PROXY_TCP_SECRET is never read here and never touches argv.

  # HARNESS_SKILLS_DIR holds the baked harness-owned and Consumer-configured
  # skills (lib/image.nix); OPERATOR_SKILLS_DIR is where SPINDRIFT_SKILLS_DIR's
  # runtime override mounts (issue #2489), a path distinct from
  # DRIVER_SKILLS_DIR because a mount directly onto that would replace its
  # contents and hide the baked skills. box copies both into DRIVER_SKILLS_DIR
  # (issue #4296).
  HARNESS_SKILLS_DIR="${HARNESS_SKILLS_DIR:-/agent/skills}"
  OPERATOR_SKILLS_DIR="${OPERATOR_SKILLS_DIR:-/operator-skills}"

  # HARNESS_HOME_AGENT_DIR is where bwrap.go stages baked /home/agent content
  # (hooks, settings.json, opencode agent files) read-only (issue #2843). It is
  # top-level rather than under /agent because /agent is already bound read-only
  # by then, and bwrap cannot fabricate a mountpoint inside a read-only bind. The
  # OCI image bakes the same content writable at the real /home/agent.
  HARNESS_HOME_AGENT_DIR="${HARNESS_HOME_AGENT_DIR:-/home-agent-staged}"

  # DRIVER_NAME, DRIVER_BIN, DRIVER_FLAGS_COMMON, and DRIVER_SKILLS_DIR are baked
  # by the selected Driver's lib/drivers/<name>.nix registry entry (ADR 0009,
  # issue #624). No fallback literal or runtime guard here: nix/checks/image.nix's
  # driver-preamble-baked-into-image check catches an omitted preamble at build
  # time instead (issue #2531).

  # OUTCOME_CONTRACT_FILE, COMMS_CONTRACT_FILE, CHECK_CONTRACT_FILE,
  # RESEARCH_OUTCOME_CONTRACT_FILE, PROMPTASSEMBLY_REGISTRY_FILE,
  # PROMPT_CONTRACT_REGISTRY_FILE, and FORBIDDEN_MARKERS_REGISTRY_FILE are baked
  # by the agent-paths preamble too. See lib/agent-paths.nix for what each path
  # is and which driver-exec verb reads it (issue #2531).

  # The Driver registry (lib/drivers/<name>.nix) renders the DRIVER_* variables
  # and the _driver_extract_* helpers; a nix-built image prepends them via
  # driverPreamble (lib/mkHarness.nix), and the bats harness sources the same
  # bytes via DRIVER_PREAMBLE_FILE (issue #433). Session flags are rendered in
  # Go by the Driver strategy's SessionFlags.
}

# clone_repo authenticates, clones the target repo into WORK_DIR, sets the
# repo-local git identity, and fetches the latest refs.
clone_repo() {
  # CODE_FORGE=local clones from a filesystem mount and CODE_FORGE=forgejo from a
  # FORGEJO_TOKEN-authenticated URL (ADR 0038); neither target is github.com, so
  # gh's credential helper has nothing to apply, and running it would fail a
  # forgejo Box that carries no GH_TOKEN.
  case "${CODE_FORGE:-github}" in
  local | forgejo) ;;
  *)
    export GH_TOKEN
    gh auth setup-git
    ;;
  esac

  # CODE_FORGE=git uses a configured plain git remote (ADR 0013); CODE_FORGE=local
  # uses the read-only Accumulation-repo mount (ADR 0033). REPO_SLUG still
  # resolves the Issue Tracker either way. Gated on the exact CODE_FORGE value so
  # a stray CODE_FORGE_REMOTE_URL cannot silently redirect a default github
  # deployment to the wrong remote.
  local CLONE_URL="https://github.com/${REPO_SLUG}.git"
  if [ "${CODE_FORGE:-github}" = "git" ]; then
    CLONE_URL="${CODE_FORGE_REMOTE_URL:?CODE_FORGE_REMOTE_URL is required when CODE_FORGE=git}"
  elif [ "${CODE_FORGE:-github}" = "forgejo" ]; then
    # FORGEJO_TOKEN rides the remote URL's userinfo, the same
    # https://<token>@<host>/<owner>/<repo>.git shape the launcher's
    # forgejoGitRemoteURL builds host-side (ADR 0038), so the branch this Box
    # pushes and the branch the launcher's Merge later clones name one remote.
    : "${FORGEJO_TOKEN:?FORGEJO_TOKEN is required when CODE_FORGE=forgejo}"
    local _fj_base="${FORGEJO_BASE_URL:-https://codeberg.org}"
    _fj_base="${_fj_base%/}"
    CLONE_URL="${_fj_base%%://*}://${FORGEJO_TOKEN}@${_fj_base#*://}/${REPO_SLUG}.git"
  elif [ "${CODE_FORGE:-github}" = "local" ]; then
    CLONE_URL="$REPO_MOUNT_DIR"
    # Under rootless podman the Box's mapped uid never matches the host-owned
    # bind mount's uid, so git's dubious-ownership guard rejects
    # $REPO_MOUNT_DIR before the clone copies a single object (#1720). Both
    # paths outlive the clone step, so this is a standing global config entry
    # and the one CODE_FORGE=local exception to #404's invariant below.
    git config --global --add safe.directory "$REPO_MOUNT_DIR"
    git config --global --add safe.directory "$WORK_DIR"
  fi
  # Redact embedded userinfo before echoing: CODE_FORGE=forgejo always carries
  # FORGEJO_TOKEN there and CODE_FORGE_REMOTE_URL commonly carries credentials
  # too, so echoing verbatim would leak a secret to Box stdout. Mirrors the Go
  # launcher's forge.RedactURLCredentials.
  echo "==> cloning $(printf '%s' "$CLONE_URL" | sed -E 's#://[^/@[:space:]]+@#://#')"
  git clone "$CLONE_URL" "$WORK_DIR"
  cd "$WORK_DIR"
  # Identity is repo-local, not global (#404): CI's hermetic check environment
  # has no global git config, so a global identity here would let git-shelling
  # tests observe config the Box has but CI doesn't.
  git config user.name "$GIT_USER_NAME"
  git config user.email "$GIT_USER_EMAIL"
  # Fetch the latest refs so the pre-work rebase positions the branch on current
  # origin/BASE_BRANCH, not the state captured at clone time.
  git fetch origin
  install_readonly_guards
}

# install_readonly_guards installs the read-only guards via `driver-exec
# readonly-guards` (issues #2463, #2465, #2509): a git push hook plus command
# shims for gh and fj. Every guard and every rejection message comes from the
# forbiddenMarkers registry (lib/prompt-contract.nix), never hand-copied shell.
# The verb skips an argv0 absent from PATH, so a github Box with no fj is fine.
install_readonly_guards() {
  if [ -n "${BOX_WRITE_ENABLED:-}" ]; then
    return 0
  fi
  # A deterministic path, not mktemp: the PATH mutation below never survives
  # back to a caller inspecting the Box, so the location must be predictable.
  # $HOME rather than $WORK_DIR's parent, which in production is the root-owned
  # `/` while the Box runs as uid 1000, so the verb's mkdir would fail with
  # EACCES and `set -e` would kill the Box mid-clone.
  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  local -a _rg_args=(
    readonly-guards
    --forbidden-markers-registry "$FORBIDDEN_MARKERS_REGISTRY_FILE"
    --shim-dir "$shim_dir"
  )
  # Only the git-hook guard is gated on outbox capability: a read-only Box whose
  # hand-off IS a real `git push` must never get that push blocked locally, since
  # it has no other way to hand off its work. No backend registered today leaves
  # both unset (issue #2927 gave forgejo OutboxRelayCapable), but the branch
  # stays live for a future one. The command shims install unconditionally.
  if [ -n "${BOX_HOST_MEDIATED_REMOTE:-}" ] || [ -n "${BOX_OUTBOX_RELAY_CAPABLE:-}" ]; then
    # A bare decoy outside $WORK_DIR, never $WORK_DIR itself: every real push
    # targets the branch already checked out here, so a pushurl pointing at
    # $WORK_DIR would resolve as "Everything up-to-date" and exit 0 without
    # firing a hook. --extra-repo-dir installs the hook in $WORK_DIR/.git/hooks
    # too, catching a push to an explicit URL that bypasses origin's pushurl.
    local decoy
    decoy="$(mktemp -d)/readonly-push-guard.git"
    git init --bare -q "$decoy"
    git -C "$WORK_DIR" config remote.origin.pushurl "$decoy"
    _rg_args+=(--repo-dir "$decoy" --extra-repo-dir "$WORK_DIR")
  else
    _rg_args+=(--skip-git-hook)
  fi
  driver-exec "${_rg_args[@]}"
  export PATH="$shim_dir:$PATH"
}

# phase_branch_recovery adopts prior work on an open PR or force-resets a stale
# branch with no open PR. Sets _rebase_and_publish, read by phase_prework_rebase
# and handed to box, which publishes after its conflict-resolve pass.
phase_branch_recovery() {
  # A prior run may have pushed agent/issue-N before dying. Force-resetting when
  # no open PR exists keeps this Box's first incremental push a fast-forward.
  _rebase_and_publish=""

  # gh can answer "is there an open PR?" only for CODE_FORGE=github. local and
  # git have no PR concept (ADR 0033, ADR 0013); forgejo's PRs are not on
  # github.com and its Box carries no GH_TOKEN, so a gh query would abort every
  # retry or read "no PR" and force-reset a live Forgejo PR's branch (issue
  # #3942). A stale refs/remotes/origin/$BRANCH is superseded by a fresh checkout.
  if [ "${CODE_FORGE:-github}" != "github" ]; then
    echo "==> CODE_FORGE=${CODE_FORGE:-github}: starting $BRANCH fresh from origin/${BASE_BRANCH:-}"
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
      # The rebased branch must be published so the agent's first incremental
      # push is a fast-forward, not a rejection.
      _rebase_and_publish=1
    else
      echo "==> stale remote branch $BRANCH found (no open PR); force-resetting to ${BASE_BRANCH:-}"
      git checkout -b "$BRANCH" "origin/${BASE_BRANCH:-}"
      publish_rebased_branch "$BRANCH" || {
        echo "==> publishing reset branch failed on $BRANCH; concurrent Box may be ahead"
        exit 1
      }
    fi
  else
    git checkout -b "$BRANCH" "origin/${BASE_BRANCH:-}"
  fi
}

# publish_rebased_branch lands a just-rebased branch so a later step never sees
# the stale pre-rebase state. A read-only Box holds no push-capable token (issue
# #1979: a force-push here 403s before the agent ever runs), so it relays through
# the outbox bundle instead, behind the same BOX_WRITE_ENABLED fail-closed gate
# the OPEN A PULL REQUEST contract uses (issue #1918, bundle verb issue #1808).
publish_rebased_branch() {
  local branch="$1"
  if [ -n "${BOX_WRITE_ENABLED:-}" ]; then
    git push --force-with-lease origin "$branch"
  else
    driver-exec bundle-out \
      --repo "$WORK_DIR" \
      --base "origin/${BASE_BRANCH:-}" \
      --branch "$branch" \
      --outbox "$OUTBOX_DIR"
  fi
}

# phase_prework_rebase rebases the branch onto the latest base before the
# agent starts. Sets _had_rebase_conflict, which main() hands to box for its
# conflict-resolve pass; reads _rebase_and_publish from phase_branch_recovery.
phase_prework_rebase() {
  # A conflict here means the prior branch diverged in a way that cannot be
  # resolved mechanically; fail fast with a distinct signal rather than proceed
  # on a stale base.
  echo "==> rebasing $BRANCH onto latest origin/${BASE_BRANCH:-}"
  _had_rebase_conflict=""
  git rebase "origin/${BASE_BRANCH:-}" || _had_rebase_conflict=1
  # Only needed in the adoption path, where the rebase rewrote history already on
  # the remote. A conflict defers publication until the conflict-resolve agent
  # below has run.
  if [ -z "${_had_rebase_conflict:-}" ] && [ -n "${_rebase_and_publish:-}" ]; then
    echo "==> publishing rebased $BRANCH"
    publish_rebased_branch "$BRANCH" || {
      echo "==> publishing rebased branch failed after pre-work rebase on $BRANCH"
      exit 1
    }
  fi
}

# _is_advise_only reports whether this dispatch's kind never lands code (ADR
# 0022, issue #640), per the kind's descriptor as main() resolved it into
# _advise_only (issue #3901).
_is_advise_only() {
  [ "${_advise_only:-}" = "1" ]
}

# _is_self_contained reports whether this is the research kind's no-repo sub-mode
# (issue #2202): the Box clones no repo and explores none. Unset defaults to off.
_is_self_contained() {
  [ "${SELF_CONTAINED:-}" = "1" ]
}

# export_driver_bash_timeout (issue #4409) exports the Consumer's Bash-timeout
# knob under each env var name the Driver's registry entry lists in
# DRIVER_BASH_TIMEOUT_ENV. Exported (not just set) so the orchestrator and,
# via the final exec, box's resume runs inherit it. Unset knob or a Driver
# listing no names exports nothing, leaving the Driver's own limits. A value
# that is not a positive integer is skipped with a warning rather than passed
# on: Claude Code may silently fall back to its 10-minute cap on a bad value.
export_driver_bash_timeout() {
  [ -n "${DRIVER_BASH_TIMEOUT_MS:-}" ] || return 0
  case "$DRIVER_BASH_TIMEOUT_MS" in
    0* | *[!0-9]*)
      echo "==> WARNING: DRIVER_BASH_TIMEOUT_MS='${DRIVER_BASH_TIMEOUT_MS}' is not a positive integer (milliseconds) — leaving the Driver's own Bash timeout"
      return 0
      ;;
  esac
  local _name
  for _name in ${DRIVER_BASH_TIMEOUT_ENV:-}; do
    export "$_name=$DRIVER_BASH_TIMEOUT_MS"
  done
}

main() {
  # Cross-phase sentinels: declared local here so bash's dynamic scoping lets
  # each phase function assign them by plain (non-local) assignment while
  # keeping them out of true global scope (issue #515).
  local _rebase_and_publish _had_rebase_conflict
  local _advise_only

  configure_env
  export_driver_bash_timeout

  # The Box's one input for its advise-only posture (issue #3901), resolved
  # before clone_repo so an unrecognized kind fails closed instead of
  # defaulting to the work posture. Assigned apart from its `local` above so
  # `||` sees the substitution's exit status, not `local`'s.
  _advise_only=$(driver-exec advise-only --dispatch-kind "${DISPATCH_KIND:-work}") || {
    echo "==> unrecognized DISPATCH_KIND=${DISPATCH_KIND:-work}; aborting before clone"
    exit 1
  }

  if _is_self_contained; then
    # No repo to clone or explore (issue #2202): stand up an empty working
    # directory for the Driver and skip every clone/branch phase; box skips its
    # toolchain decision too.
    mkdir -p "$WORK_DIR"
    cd "$WORK_DIR"
  else
    clone_repo
    # An advise-only dispatch (research today, ADR 0022, issue #640) explores
    # the clone but never lands code: no branch to cut, adopt, or rebase.
    if ! _is_advise_only; then
      phase_branch_recovery
      phase_prework_rebase
    fi
  fi
  # box first sets up the Forgejo CLI credential (issue #4299), binds the
  # registry proxy (the Forwarder, the home configs and the in-tree rewrite,
  # reverted on exit; issue #4298), decides the toolchain (the devShell probe,
  # the toolchain hint and the prefetch hook; issue #4297), lays
  # out the Driver skills dir and the home agent files (issue #4296), then runs
  # the conflict-resolve pass when phase_prework_rebase left a conflict, then
  # assembles the prompt, runs the first Driver run and everything after it: the
  # required-marker nudges, the synthetic outcome backstop, the already-resolved
  # demotion, the lockfile scan and bundle-out, and it exits with the run's exit
  # code (ADR 0058, issues #4292, #4293, #4294, #4295). The flags carry the
  # shell-local values assembly needs: exporting them would put them in the
  # Driver's environment. Every value rides a flag, bools as explicit 0/1,
  # because box rejects a missing flag instead of defaulting it.
  local _model_omit_empty=0
  local _prework_rebase_conflict=0 _publish_rebase=0
  [ -z "${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}" ] || _model_omit_empty=1
  [ -z "${_had_rebase_conflict:-}" ] || _prework_rebase_conflict=1
  [ -z "${_rebase_and_publish:-}" ] || _publish_rebase=1
  # box reads the devShell probe's knobs from its environment; the defaults
  # preamble sets them without export.
  export DEV_SHELL_NAME DEV_SHELL_PROBE_TIMEOUT
  exec box \
    --work-dir "$WORK_DIR" \
    --outbox-dir "$OUTBOX_DIR" \
    --registry "$PROMPTASSEMBLY_REGISTRY_FILE" \
    --validate-markers-registry "$PROMPT_CONTRACT_REGISTRY_FILE" \
    --driver-skills-dir "$DRIVER_SKILLS_DIR" \
    --harness-skills-dir "$HARNESS_SKILLS_DIR" \
    --operator-skills-dir "$OPERATOR_SKILLS_DIR" \
    --harness-home-agent-dir "$HARNESS_HOME_AGENT_DIR" \
    --driver-session-cache-dir "${DRIVER_SESSION_CACHE_DIR:-}" \
    --prompts-dir "$PROMPTS_DIR" \
    --agents-prompt-files "${AGENTS_PROMPT_FILES:-}" \
    --driver-agent-files-dir "${DRIVER_AGENT_FILES_DIR:-}" \
    --comms-contract-file "$COMMS_CONTRACT_FILE" \
    --check-contract-file "$CHECK_CONTRACT_FILE" \
    --outcome-contract-file "$OUTCOME_CONTRACT_FILE" \
    --research-outcome-contract-file "$RESEARCH_OUTCOME_CONTRACT_FILE" \
    --argv-prompt-style "$DRIVER_ARGV_PROMPT_STYLE" \
    --argv-prompt-flag "${DRIVER_ARGV_PROMPT_FLAG:-}" \
    --argv-model-flag "$DRIVER_ARGV_MODEL_FLAG" \
    --argv-model-omit-empty="$_model_omit_empty" \
    --argv-agents-flag "${DRIVER_ARGV_AGENTS_FLAG:-}" \
    --argv-effort-flag "$DRIVER_ARGV_EFFORT_FLAG" \
    --argv-order "$DRIVER_ARGV_ORDER" \
    --model "${MODEL:-}" \
    --effort "${EFFORT:-}" \
    --driver "$DRIVER_NAME" \
    --driver-bin "$DRIVER_BIN" \
    --driver-flags "$DRIVER_FLAGS_COMMON" \
    --heartbeat-log "${HEARTBEAT_LOG:-}" \
    --max-budget-tokens "${MAX_BUDGET_TOKENS:-0}" \
    --max-budget-usd "${MAX_BUDGET_USD:-0}" \
    --prework-rebase-conflict="$_prework_rebase_conflict" \
    --publish-rebase="$_publish_rebase"
}

main "$@"
