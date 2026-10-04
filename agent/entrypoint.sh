#!/usr/bin/env bash
# Runs inside the disposable container: clones the target repo, cuts a branch, hands
# off to a headless agent. SPINDRIFT_PROMPT_DIR overrides the baked /agent/prompts
# without a rebuild. --dangerously-skip-permissions is safe because the container is
# the boundary: the agent acts freely, but only on a throwaway clone, never the host.
set -euo pipefail

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
  # The clone reads these (it leaves with #4302); box reads BRANCH to recover
  # the branch (issue #4301). The :- expansions keep set -u happy:
  # BRANCH_PREFIX comes from the nix-rendered defaults preamble.
  # A butler Box never checks this branch out (advise-only, ADR 0022) or pushes
  # it, so the value only has to be legal and stable across a rerun.
  export BRANCH="${BRANCH_PREFIX:-}${DISPATCH_KEY}"
  WORK_DIR="${WORK_DIR:-/work}"
  # The read-only Accumulation-repo mount CODE_FORGE=local clones from instead
  # of a network remote (ADR 0033, issue #1697).
  REPO_MOUNT_DIR="${REPO_MOUNT_DIR:-/repo}"
  # The writable mount box's bundle-out step writes CODE_FORGE=local's seam
  # bundle into (ADR 0033, issue #1808).
  OUTBOX_DIR="${OUTBOX_DIR:-/outbox}"

  export_driver_bash_timeout

  # Validation only: an unrecognized kind must fail closed before clone_repo
  # instead of defaulting to the work posture. box resolves the posture itself
  # from the same descriptor (issue #3901).
  driver-exec advise-only --dispatch-kind "${DISPATCH_KIND:-work}" >/dev/null || {
    echo "==> unrecognized DISPATCH_KIND=${DISPATCH_KIND:-work}; aborting before clone"
    exit 1
  }

  if _is_self_contained; then
    # No repo to clone or explore (issue #2202): stand up an empty working
    # directory for the Driver and skip the clone; box skips branch recovery
    # and its toolchain decision too.
    mkdir -p "$WORK_DIR"
    cd "$WORK_DIR"
  else
    clone_repo
  fi
  # box runs every remaining phase and exits with the run's exit code (ADR
  # 0058); the package doc in cmd/launcher/box/main.go lists them. The flags
  # carry the shell-local values assembly needs: exporting them would put them
  # in the Driver's environment. Every value rides a flag, bools as explicit
  # 0/1, because box rejects a missing flag instead of defaulting it.
  local _model_omit_empty=0
  [ -z "${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}" ] || _model_omit_empty=1
  # box reads the devShell probe's knobs from its environment; the defaults
  # preamble sets them without export.
  export DEV_SHELL_NAME DEV_SHELL_PROBE_TIMEOUT
  exec box \
    --work-dir "$WORK_DIR" \
    --outbox-dir "$OUTBOX_DIR" \
    --forbidden-markers-registry "$FORBIDDEN_MARKERS_REGISTRY_FILE" \
    --registry "$PROMPTASSEMBLY_REGISTRY_FILE" \
    --validate-markers-registry "$PROMPT_CONTRACT_REGISTRY_FILE" \
    --driver-skills-dir "$DRIVER_SKILLS_DIR" \
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
    --max-budget-usd "${MAX_BUDGET_USD:-0}"
}

main "$@"
