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
: "${ISSUE_NUMBER:?ISSUE_NUMBER is required}"
[ "$fully_local" = true ] || [ "$no_repo" = true ] || : "${REPO_SLUG:?REPO_SLUG (owner/repo) is required}"
: "${GIT_USER_NAME:?GIT_USER_NAME is required}"
: "${GIT_USER_EMAIL:?GIT_USER_EMAIL is required}"

# BEGIN GENERATED OUTCOME STATUS WORDS -- nix run .#regen -- DO NOT EDIT
export RESEARCH_STATUS_ENUM="recommend|reject|unclear"
# END GENERATED OUTCOME STATUS WORDS

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
  # IN_PROGRESS_LABEL, COMPLETE_LABEL, DEV_SHELL_NAME and DEV_SHELL_PROBE_TIMEOUT
  # come from the nix-rendered defaults preamble (env-schema.nix) prepended at
  # image-build time; AGENTS_JSON_TEMPLATE rides that preamble as a derived
  # value, not a schema knob. The :- expansions keep set -u and the linter happy.
  export BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"

  # Overridable only so the harness can be exercised on the host without a
  # container. WORK_DIR/REPO_MOUNT_DIR/OUTBOX_DIR are true runtime mount points,
  # not baked artifacts, so they keep literal defaults here; PROMPTS_DIR moved to
  # the nix-rendered agent-paths preamble (lib/preambles.nix, issue #2531).
  WORK_DIR="${WORK_DIR:-/work}"
  # REPO_MOUNT_DIR is the read-only Accumulation-repo mount CODE_FORGE=local
  # clones from instead of a network remote (ADR 0033, issue #1697); unused
  # otherwise.
  REPO_MOUNT_DIR="${REPO_MOUNT_DIR:-/repo}"
  # OUTBOX_DIR is the writable mount driver-exec's bundle-out verb writes
  # CODE_FORGE=local's seam bundle into (ADR 0033, issue #1808); unused
  # otherwise.
  OUTBOX_DIR="${OUTBOX_DIR:-/outbox}"
  # REGISTRY_PROXY_MANIFEST (ADR 0045, issue #3141) carries the registry proxy's
  # endpoint and route table as one JSON env var. `driver-exec bind-registry`
  # parses it straight out of its own inherited environment, so this file needs
  # no default, no override, and no flag to pass it through.
  # REGISTRY_PROXY_TCP_SECRET is never read here and never touches argv.

  # HARNESS_SKILLS_DIR holds the baked harness-owned and Consumer-configured
  # skills (lib/image.nix); OPERATOR_SKILLS_DIR is where SPINDRIFT_SKILLS_DIR's
  # runtime override mounts (issue #2489), a path distinct from
  # DRIVER_SKILLS_DIR because a mount directly onto that would replace its
  # contents and hide the baked skills. Copying merges, so both are copied below.
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

  # _driver_extract_outcome and _driver_session_flags are defined by the Driver
  # registry (lib/drivers/<name>.nix); a nix-built image prepends them via
  # driverPreamble (lib/mkHarness.nix), and the bats harness sources the same
  # registry-rendered bodies via DRIVER_PREAMBLE_FILE (issue #433).
}

# configure_forgejo_cli wires FORGEJO_TOKEN into fj so the agent's `fj issue`
# and `fj pr` commands (issue #1963) run non-interactively. A no-op when fj
# isn't baked or no token is set, as on a read-only Box, whose prompt offers no
# fj write command anyway.
configure_forgejo_cli() {
  command -v fj >/dev/null 2>&1 || return 0
  [ -n "${FORGEJO_TOKEN:-}" ] || return 0
  local _fj_base="${FORGEJO_BASE_URL:-https://codeberg.org}"
  _fj_base="${_fj_base%/}"
  # The trailing slash is stripped so the host fj keys the token under is the
  # host clone_repo derives its remote from. The token is fed on stdin, never
  # argv. `auth add-key` (NAME positional) is the forgejo-cli 0.5.0
  # spelling baked into the image; a nixpkgs bump that renames it must update
  # this call in lockstep, or fj would store GIT_USER_NAME as the token.
  printf '%s' "$FORGEJO_TOKEN" | fj -H "$_fj_base" auth add-key "${GIT_USER_NAME:-spindrift-agent}" >/dev/null
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
  # Configured after the repo-local identity above so GIT_USER_NAME is available
  # for fj's key label.
  configure_forgejo_cli
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
# and phase_conflict_resolve.
phase_branch_recovery() {
  # A prior run may have pushed agent/issue-N before dying. Force-resetting when
  # no open PR exists keeps this Box's first incremental push a fast-forward.
  _rebase_and_publish=""

  # CODE_FORGE=local has no PR concept and no writable origin: the mount is
  # read-only (ADR 0033), and work is bundled out at the end rather than pushed.
  # A stale refs/remotes/origin/$BRANCH is simply superseded by a fresh checkout,
  # with nothing to adopt through a gh call that would break the
  # no-forge-network-calls guarantee.
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
# agent starts. Sets _had_rebase_conflict, read by phase_conflict_resolve;
# reads _rebase_and_publish from phase_branch_recovery.
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

# phase_registry_proxy_bindings brings up the in-Box Forwarder and wires
# cargo/npm/pnpm/yarn/Go at it through `driver-exec bind-registry` (ADR 0044,
# ADR 0045, issues #2849, #2931, #3141). A silent no-op when
# REGISTRY_PROXY_MANIFEST is unset. main() calls it before clone_repo and every
# other phase, so it precedes any place a cargo or npm build could first happen.
phase_registry_proxy_bindings() {
  local _bindings_env_out _bind_registry_rc=0 _source_rc=0
  # A verb failure here must never take the whole box run down. Unlike
  # phase_toolchain_nudge's cosmetic hint, these bindings apply unconditionally,
  # so their failure warnings are never suppressed.
  if ! _bindings_env_out="$(mktemp)"; then
    echo "==> WARNING: mktemp failed — skipping registry proxy bindings"
    return 0
  fi

  # See phase_toolchain_nudge's matching trap for why this is a RETURN trap that
  # unsets itself, not a plain `rm -f` at each return site.
  trap 'rm -f "$_bindings_env_out"; trap - RETURN' RETURN

  driver-exec bind-registry \
    --bindings-env-output "$_bindings_env_out" \
    || _bind_registry_rc=$?

  if [ "$_bind_registry_rc" -ne 0 ]; then
    echo "==> WARNING: driver-exec bind-registry failed (exit ${_bind_registry_rc}) — skipping registry proxy bindings"
    return 0
  fi

  # rc-captured rather than left to errexit: an unguarded `source` here would
  # abort the whole entrypoint mid-phase.
  # shellcheck disable=SC1090  # dynamic path (tempfile), sourced by design: the verb's own env-file output
  source "$_bindings_env_out" || _source_rc=$?
  if [ "$_source_rc" -ne 0 ]; then
    echo "==> WARNING: sourcing driver-exec bind-registry's env output failed (exit ${_source_rc}) — skipping registry proxy bindings"
    return 0
  fi
}

# intree_binding_apply wraps `driver-exec bind-registry`'s in-tree apply mode for
# npm, yarn and pnpm under $WORK_DIR (cargo retired, issue #3201; logic and the
# cargo#5416/ADR 0044 rationale in ApplyInTreeBinding). The same call renders
# $CARGO_HOME/config.toml, whose placeholder exports ride the sourced env file.
# On failure it reverts, leaving no partial apply on disk (issues #2932, #3027).
intree_binding_apply() {
  local _intree_apply_rc=0 _cargo_bindings_env_out _source_rc=0
  # A verb failure here must never take the whole box run down; mirrors
  # phase_registry_proxy_bindings' rc-capture above.
  if ! _cargo_bindings_env_out="$(mktemp)"; then
    echo "==> WARNING: mktemp failed — skipping cargo registry placeholder bindings"
    return 0
  fi

  # See phase_toolchain_nudge's matching trap for why this is a RETURN trap that
  # unsets itself, not a plain `rm -f` at each return site.
  trap 'rm -f "$_cargo_bindings_env_out"; trap - RETURN' RETURN

  driver-exec bind-registry \
    --intree-action apply \
    --intree-work-dir "$WORK_DIR" \
    --intree-bindings-env-output "$_cargo_bindings_env_out" \
    || _intree_apply_rc=$?
  if [ "$_intree_apply_rc" -ne 0 ]; then
    echo "==> WARNING: driver-exec bind-registry (in-tree apply) failed (exit ${_intree_apply_rc}) — skipping in-tree registry binding"
    intree_binding_revert
    return 0
  fi

  # rc-captured rather than left to errexit: an unguarded `source` here would
  # abort the whole entrypoint mid-phase.
  # shellcheck disable=SC1090  # dynamic path (tempfile), sourced by design: the verb's own env-file output
  source "$_cargo_bindings_env_out" || _source_rc=$?
  if [ "$_source_rc" -ne 0 ]; then
    echo "==> WARNING: sourcing driver-exec bind-registry's cargo placeholder env output failed (exit ${_source_rc}) — skipping cargo registry placeholder bindings"
    return 0
  fi
}

# intree_binding_revert wraps `driver-exec bind-registry`'s in-tree revert mode,
# undoing intree_binding_apply's rewrite (RevertInTreeBinding in
# cmd/launcher/internal/bindregistry/intreebinding.go). Called from main()'s
# re-apply dance and from phase_conflict_resolve's rebase-abort path, which has
# one case where the revert legitimately fails and this only warns.
intree_binding_revert() {
  local _intree_revert_rc=0
  driver-exec bind-registry --intree-action revert --intree-work-dir "$WORK_DIR" \
    || _intree_revert_rc=$?
  if [ "$_intree_revert_rc" -ne 0 ]; then
    echo "==> WARNING: driver-exec bind-registry (in-tree revert) failed (exit ${_intree_revert_rc})"
  fi
}

# lockfile_forwarder_scan wraps `driver-exec bind-registry`'s lockfile-scan mode
# (issue #3199), called from main()'s settle region right before bundle-out. The
# verb always exits 0 and stays silent on a clean run. Wrapped defensively anyway,
# so a future change that makes it fail warns rather than letting set -e take the
# whole run down over an advisory-only scan.
lockfile_forwarder_scan() {
  local _lockfile_scan_rc=0
  driver-exec bind-registry --lockfile-scan-work-dir "$WORK_DIR" \
    || _lockfile_scan_rc=$?
  if [ "$_lockfile_scan_rc" -ne 0 ]; then
    echo "==> WARNING: driver-exec bind-registry (lockfile Forwarder-URL scan) failed (exit ${_lockfile_scan_rc})"
  fi
}

# phase_toolchain_nudge emits a one-time hint for a cold run with a
# recognized dependency-manifest file and no prefetch configured.
phase_toolchain_nudge() {
  # Classification is delegated to `driver-exec bind-registry` (issue #2930),
  # which reads the shared ecosystem table instead of this function re-deriving
  # its own lockfile chain. The verb runs unconditionally so the env file is
  # always available; only the hint's emission is gated on PREFETCH.
  local _nudge_env_out _bind_registry_rc=0 _source_rc=0 _nudge_ecosystem=""
  # This phase is cosmetic-hint-only, so a verb or mktemp failure must not take
  # the whole box run down under set -euo pipefail. Bail before sourcing a
  # possibly-missing or garbage env file rather than letting it propagate.
  if ! _nudge_env_out="$(mktemp)"; then
    if [ -z "${PREFETCH:-}" ]; then
      echo "==> WARNING: mktemp failed — skipping toolchain nudge"
    fi
    return 0
  fi

  # Registered once mktemp has produced a path, so every exit from here on
  # removes the tempfile without repeating `rm -f` at each return site. The
  # handler unsets itself as it fires: a bash RETURN trap is process-global, not
  # function-scoped, so leaving it registered would fire again on the next
  # unrelated function's return and dereference a `local` that no longer exists.
  trap 'rm -f "$_nudge_env_out"; trap - RETURN' RETURN

  driver-exec bind-registry \
    --work-dir "$WORK_DIR" \
    --ecosystem-env-output "$_nudge_env_out" \
    || _bind_registry_rc=$?

  if [ "$_bind_registry_rc" -ne 0 ]; then
    if [ -z "${PREFETCH:-}" ]; then
      echo "==> WARNING: driver-exec bind-registry failed (exit ${_bind_registry_rc}) — skipping toolchain nudge"
    fi
    return 0
  fi

  # rc-captured rather than left to errexit: an unguarded `source` failure would
  # abort the whole script mid-phase, which the RETURN trap above can never clean
  # up after, since errexit unwinds without returning from any function.
  # shellcheck disable=SC1090  # dynamic path (tempfile), sourced by design: the verb's own env-file output
  source "$_nudge_env_out" || _source_rc=$?
  if [ "$_source_rc" -ne 0 ]; then
    if [ -z "${PREFETCH:-}" ]; then
      echo "==> WARNING: sourcing driver-exec bind-registry's env output failed (exit ${_source_rc}) — skipping toolchain nudge"
    fi
    return 0
  fi
  # NUDGE_ECOSYSTEM is the env file's own on-disk variable name
  # (cmd/launcher/driver-exec/bindregistry_cmd.go), not this shell's convention,
  # so it is captured into a phase-local and unset rather than left to outlive
  # the phase.
  _nudge_ecosystem="${NUDGE_ECOSYSTEM:-}"
  unset NUDGE_ECOSYSTEM

  if [ -z "${PREFETCH:-}" ] && [ -n "$_nudge_ecosystem" ]; then
    echo "==> hint: ${_nudge_ecosystem} project detected; set 'prefetch' to warm dependency caches per run, or 'packages' to bake a toolchain into the image"
  fi
}

# phase_devshell_probe detects a Nix devShell in the cloned repo. Sets
# _use_dev_shell (read by phase_prefetch and run_driver_in_env) and _harness_path
# (read by phase_prefetch only; run_driver_in_env delegates devShell PATH
# handling to driver-exec, issue #626).
phase_devshell_probe() {
  # When one is found, the prefetch hook and Driver run inside `nix develop` so
  # the agent operates in the Target's pinned environment.
  # DEV_SHELL_PROBE_TIMEOUT is nix-baked (env-schema.nix, 300 s) so a heavy
  # consumer devShell eval cannot stall the box.
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
  # Optional cache warm-up (mkHarness `prefetch`); no-op when unset. Run inside
  # the devShell when one exists, so the hook sees the Target's toolchain.
  if [ -n "${PREFETCH:-}" ]; then
    if [ "$_use_dev_shell" = "1" ]; then
      local _pf_wrapper
      _pf_wrapper="$(mktemp --suffix=.sh)"
      # eval "$PREFETCH" so shell constructs in the hook are interpreted,
      # matching the non-devShell path exactly. $PATH and $PREFETCH stay literal
      # in the generated script.
      # shellcheck disable=SC2016
      printf '#!/bin/bash\nexport PATH="%s:$PATH"\neval "$PREFETCH"\n' \
        "$_harness_path" > "$_pf_wrapper"
      chmod +x "$_pf_wrapper"
      # Prefetch failures are non-fatal, so the nix exit status is ignored.
      nix develop ".#${DEV_SHELL_NAME:-default}" --command bash "$_pf_wrapper" || true
      rm -f "$_pf_wrapper"
    else
      eval "$PREFETCH"
    fi
  fi
}

# Substitute only known placeholders so a literal `$` in the prompt body
# survives. The Conditional fragment registry (lib/fragments.nix, issue #622)
# contributes the rest through the nix-rendered _FRAGMENT_SUBST_VARS array, so a
# forgotten allowlist entry is impossible: adding a row needs no edit here.
# Defined ahead of the fragment loop (issue #463), which renders through it.
_subst() {
  local f="$1" v
  local -a _names=(
    ISSUE_NUMBER
    ISSUE_TITLE
    BRANCH
    BASE_BRANCH
    IN_PROGRESS_LABEL
    COMPLETE_LABEL
    RUN_NONCE
    RESEARCH_STATUS_ENUM
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

# _is_research_kind reports whether this dispatch is the advise-only research
# kind (ADR 0022, issue #640); the default is work, so an unset DISPATCH_KIND is
# never mistaken for research.
_is_research_kind() {
  [ "${DISPATCH_KIND:-work}" = "research" ]
}

# _is_self_contained reports whether this is the research kind's no-repo sub-mode
# (issue #2202): the Box clones no repo and explores none. Unset defaults to off.
_is_self_contained() {
  [ "${SELF_CONTAINED:-}" = "1" ]
}

# _is_readonly_outbox_relay reports whether this Box is read-only (no
# push-capable token was ever issued, so a force-push can only 403) and its
# backend is outbox-relay-capable per lib/backends/default.nix, forwarded as
# BOX_OUTBOX_RELAY_CAPABLE (issues #2267, #2527, #2927) rather than compared
# against the raw CODE_FORGE name. Such a Box hands off via the outbox (#2094).
_is_readonly_outbox_relay() {
  [ -z "${BOX_WRITE_ENABLED:-}" ] && [ -n "${BOX_OUTBOX_RELAY_CAPABLE:-}" ]
}

# _needs_outbox reports whether the outbox is expected to be mounted host-side
# for this run (mirrors needsOutbox in cmd/launcher/internal/dispatch/box.go).
_needs_outbox() {
  [ -n "${BOX_HOST_MEDIATED_REMOTE:-}" ] || _is_readonly_outbox_relay
}

# _handoff_field extracts field $2 from the raw Handoff descriptor JSON $1
# (issue #2355), defaulting to empty when the field is absent or null.
_handoff_field() {
  printf '%s' "$1" | jq -r ".$2 // empty"
}

# _populate_driver_skills_dir copies HARNESS_SKILLS_DIR then OPERATOR_SKILLS_DIR
# into DRIVER_SKILLS_DIR (issue #2489), so an operator skill wins a name
# collision but a harness skill the operator didn't override survives. The driver
# discovers skills only at DRIVER_SKILLS_DIR, so every call site that spawns a
# driver invocation naming a skill must run this first. Idempotent (issue #2706).
_populate_driver_skills_dir() {
  mkdir -p "$DRIVER_SKILLS_DIR"
  if [ -d "$HARNESS_SKILLS_DIR" ]; then
    cp -r "$HARNESS_SKILLS_DIR"/. "$DRIVER_SKILLS_DIR"/
  fi
  if [ -d "$OPERATOR_SKILLS_DIR" ]; then
    cp -r "$OPERATOR_SKILLS_DIR"/. "$DRIVER_SKILLS_DIR"/
  fi
}

# _populate_home_agent_files copies HARNESS_HOME_AGENT_DIR's staged content into
# the real $HOME (issue #2843). A no-op under OCI, which bakes /home/agent
# directly; under bwrap the staged tree is a read-only bind. Every copied file
# and directory is made writable because `cp -r` preserves the source's
# read-only mode bits (commit 8961b62e), which broke opencode under bwrap.
_populate_home_agent_files() {
  local _src _target
  if [ -d "$HARNESS_HOME_AGENT_DIR" ]; then
    cp -r "$HARNESS_HOME_AGENT_DIR"/. "$HOME"/
    while IFS= read -r _src; do
      _target="$HOME/${_src#"$HARNESS_HOME_AGENT_DIR"/}"
      # Skip the driver's session-cache dir: lib/image.nix pre-creates an empty
      # placeholder there, and bwrap binds the live HOST directory at that same
      # path, so chmod'ing it would mutate a directory outside the sandbox and,
      # on failure, abort box startup under set -e. The trailing slash is
      # stripped because sessionCacheDirRelative has no shape validation (#2845).
      if [ -d "$_src" ] && [ -n "${DRIVER_SESSION_CACHE_DIR:-}" ] && [ "${_target%/}" = "${DRIVER_SESSION_CACHE_DIR%/}" ]; then
        continue
      fi
      chmod u+w "$_target"
    done < <(find "$HARNESS_HOME_AGENT_DIR" -mindepth 1)
  fi
}

# _scan_skills_found echoes a comma-joined list of skill names under "$1". A
# skill is a directory holding a SKILL.md, never a flat <name>.md file, matching
# how Claude Code itself discovers one. Shared by phase_prompt_assembly and
# phase_conflict_resolve.
_scan_skills_found() {
  local _dir="$1" _sf _sn _found=""
  if [ -d "$_dir" ]; then
    for _sf in "${_dir}/"*/SKILL.md; do
      [ -f "$_sf" ] || continue
      _sn="$(basename "$(dirname "$_sf")")"
      _found="${_found:+${_found}, }${_sn}"
    done
  fi
  printf '%s' "$_found"
}

# phase_conflict_resolve spawns a conflict-resolve agent when
# phase_prework_rebase hit a conflict, and handles the CONFLICT_RESOLVE_PR_URL
# resolve-only dispatch mode. Reads _had_rebase_conflict and _rebase_and_publish.
# main() calls it before phase_prompt_assembly (issue #2354) so its two
# early-exit paths fire before the assemble-prompt verb runs for nothing.
phase_conflict_resolve() {
  # Escalate to exit 1 only when the agent genuinely cannot resolve.
  if [ -n "${_had_rebase_conflict:-}" ]; then
    echo "==> pre-work rebase conflict detected — invoking conflict-resolve agent"
    # Precompute the fragments conflict-resolve-prompt.md references (issue
    # #2706): this prompt renders through the bash-only `_subst` path, not the
    # assemble-prompt verb, so nothing else populates these vars; left unset,
    # `_subst`'s `${!v:-}` would substitute them as permanently empty. Declared
    # `local` and reached by `_subst` through dynamic scoping (issue #515).
    local SKILLS_FOUND
    SKILLS_FOUND="$(_scan_skills_found "$DRIVER_SKILLS_DIR")"
    local CAVEMAN_STEP=""
    if [ -f "$DRIVER_SKILLS_DIR/caveman/SKILL.md" ]; then
      # shellcheck disable=SC2034 # consumed by _subst's envsubst allowlist via ${!v:-} indirection
      CAVEMAN_STEP="$(_subst "${PROMPTS_DIR}/fragments/caveman-default.md")"$'\n\n'
    fi
    local SKILL_PREAMBLE=""
    if [ -n "$SKILLS_FOUND" ]; then
      # shellcheck disable=SC2034 # consumed by _subst's envsubst allowlist via ${!v:-} indirection
      SKILL_PREAMBLE="$(_subst "${PROMPTS_DIR}/fragments/skill-preamble.md")"$'\n\n'
    fi
    # CODE_COMMENTS_STEP follows the same baked-gated shape as its two siblings
    # above (issue #3221): the /code-comments anchor renders only once the Box
    # actually carries that skill.
    local CODE_COMMENTS_STEP=""
    if [ -f "$DRIVER_SKILLS_DIR/code-comments/SKILL.md" ]; then
      # shellcheck disable=SC2034 # consumed by _subst's envsubst allowlist via ${!v:-} indirection
      CODE_COMMENTS_STEP="$(_subst "${PROMPTS_DIR}/fragments/code-comments-default.md")"$'\n\n'
    fi
    local _cr_prompt
    _cr_prompt="$(_subst "${PROMPTS_DIR}/conflict-resolve-prompt.md")"
    # No session to pin or resume for this pass, and its exit status is not
    # checked: success is read off the rebase state below. Shadows
    # _use_dev_shell to 0 for this call only (dynamic scoping, issue #515),
    # since only the main run enters the devShell.
    local _use_dev_shell=0
    run_driver_in_env "$_cr_prompt" "" "" "" || true
    if [ -d ".git/rebase-merge" ] || [ -d ".git/rebase-apply" ]; then
      # Best-effort revert before the abort below re-checks out HEAD (ADR 0044,
      # issue #2851). When an in-tree config file is itself one of the unmerged
      # conflicting paths, git refuses to check it out and
      # intree_binding_revert warns; the `git rebase --abort` below cleans up
      # regardless, so that warning is expected rather than a real problem.
      intree_binding_revert
      git rebase --abort 2>/dev/null || true
      echo "==> pre-work rebase onto origin/${BASE_BRANCH:-} failed — conflict agent could not resolve"
      exit 1
    fi
    echo "==> pre-work rebase conflict resolved by agent"
    if [ -n "${_rebase_and_publish:-}" ]; then
      echo "==> publishing rebased $BRANCH (post-conflict-resolve)"
      publish_rebased_branch "$BRANCH" || {
        echo "==> publishing rebased branch failed after conflict resolution on $BRANCH"
        exit 1
      }
    fi
  fi

  # CONFLICT_RESOLVE_PR_URL mode: this box was dispatched only to re-map the PR
  # branch onto current main, so exit after resolution without the main agent.
  if [ -n "${CONFLICT_RESOLVE_PR_URL:-}" ]; then
    echo "==> CONFLICT_RESOLVE_PR_URL: conflict resolved — exiting without main agent"
    exit 0
  fi
}

# phase_prompt_assembly delegates prompt/roster assembly to the driver-exec
# assemble-prompt verb (ADR 0036, ADR 0007's thin-exec-glue tier, issue #2354);
# the gate, fragment, base-prompt and roster logic lives in
# cmd/launcher/internal/promptassembly (issues #2349-#2353). Sets prompt,
# _handoff (the descriptor JSON) and _handoff_file (issues #2355, #2975).
phase_prompt_assembly() {
  # Filesystem discovery is the one input only bash can produce; the verb takes
  # the result as --skills-found. A skill is a directory holding a SKILL.md, so
  # the name advertised in SKILLS_FOUND is the directory basename. main() already
  # called _populate_driver_skills_dir (issue #2706); calling it again keeps this
  # function self-contained when a test invokes it in isolation.
  _populate_driver_skills_dir

  local SKILLS_FOUND
  SKILLS_FOUND="$(_scan_skills_found "$DRIVER_SKILLS_DIR")"

  # What remains here is paths, the skill-baked probes, and Handoff passthrough
  # (issue #2975): every other Box env var is read by assembleprompt_cmd.go
  # straight off the environment (issue #2979). Boolean gates ride bare flags
  # appended only when true, so an unset knob is indistinguishable from an
  # explicit off.
  local -a _ap_args=(
    --registry "$PROMPTASSEMBLY_REGISTRY_FILE"
    --validate-markers-registry "$PROMPT_CONTRACT_REGISTRY_FILE"
    --prompts-dir "$PROMPTS_DIR"
    --agents-prompt-files "${AGENTS_PROMPT_FILES:-}"
    --driver-agent-files-dir "${DRIVER_AGENT_FILES_DIR:-}"
    --comms-contract-file "$COMMS_CONTRACT_FILE"
    --check-contract-file "$CHECK_CONTRACT_FILE"
    --outcome-contract-file "$OUTCOME_CONTRACT_FILE"
    --research-outcome-contract-file "$RESEARCH_OUTCOME_CONTRACT_FILE"
    --skills-found "$SKILLS_FOUND"
    # Driver-invocation passthrough (issue #2975): the facts run_driver_in_env
    # used to rebuild into ~20 per-call flags now ride the Handoff descriptor,
    # which driver-exec and the orchestrator read at run time.
    --argv-prompt-style "$DRIVER_ARGV_PROMPT_STYLE"
    --argv-prompt-flag "${DRIVER_ARGV_PROMPT_FLAG:-}"
    --argv-model-flag "$DRIVER_ARGV_MODEL_FLAG"
    --argv-agents-flag "${DRIVER_ARGV_AGENTS_FLAG:-}"
    --argv-effort-flag "$DRIVER_ARGV_EFFORT_FLAG"
    --argv-order "$DRIVER_ARGV_ORDER"
    --model "${MODEL:-}"
    --effort "${EFFORT:-}"
    --driver "$DRIVER_NAME"
    --driver-bin "$DRIVER_BIN"
    --driver-flags "$DRIVER_FLAGS_COMMON"
    --heartbeat-log "${HEARTBEAT_LOG:-}"
    --max-budget-tokens "${MAX_BUDGET_TOKENS:-0}"
    --max-budget-usd "${MAX_BUDGET_USD:-0}"
  )
  # BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT
  [ -f "$DRIVER_SKILLS_DIR/caveman/SKILL.md" ] && _ap_args+=(--caveman-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/tdd/SKILL.md" ] && _ap_args+=(--tdd-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/commit/SKILL.md" ] && _ap_args+=(--commit-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/code-review/SKILL.md" ] && _ap_args+=(--code-review-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/auto-format/SKILL.md" ] && _ap_args+=(--auto-format-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/auto-lint/SKILL.md" ] && _ap_args+=(--auto-lint-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/check-hygiene/SKILL.md" ] && _ap_args+=(--check-hygiene-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/code-comments/SKILL.md" ] && _ap_args+=(--code-comments-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/nix-checks/SKILL.md" ] && _ap_args+=(--nix-checks-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/principle-fix-root-causes/SKILL.md" ] && _ap_args+=(--principle-fix-root-causes-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/principle-laziness-protocol/SKILL.md" ] && _ap_args+=(--principle-laziness-protocol-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/principle-redesign-from-first-principles/SKILL.md" ] && _ap_args+=(--principle-redesign-from-first-principles-skill-baked)
  # END GENERATED SKILL-BAKED PROBES
  # DRIVER_ARGV_MODEL_OMIT_EMPTY is the Driver registry's own model-slot gate,
  # and --devshell mirrors the devShell wrapping run_driver_in_env used to build
  # per call; both are baked into the handoff here (issue #2975). _use_dev_shell is
  # main's cross-phase sentinel, read via dynamic scoping.
  [ -n "${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}" ] && _ap_args+=(--argv-model-omit-empty)
  [ "$_use_dev_shell" = "1" ] && _ap_args+=(--devshell --devshell-name "${DEV_SHELL_NAME:-default}")

  local _prompt_out _agents_out _handoff_out _review_prompt_out
  _prompt_out="$(mktemp)"
  _agents_out="$(mktemp)"
  _handoff_out="$(mktemp)"
  _review_prompt_out="$(mktemp)"

  # Bare `driver-exec` off $PATH, the same convention the other verb call sites
  # use: the real in-box binary in production, the bats fake under test. A
  # nonzero exit propagates through `set -euo pipefail`, so no explicit error
  # handling is needed here.
  driver-exec assemble-prompt "${_ap_args[@]}" \
    --prompt-output "$_prompt_out" \
    --agents-json-output "$_agents_out" \
    --handoff-output "$_handoff_out" \
    --review-prompt-output "$_review_prompt_out"

  # $(...) strips the trailing newline exactly as the old `$(_subst ...)` chain
  # did, so the prompt stays byte-identical. $_agents_out is never read back into
  # a bash string: it survives on disk because it IS the file Handoff.AgentsFile
  # points to, read by driver-exec directly off --handoff-file.
  prompt="$(cat "$_prompt_out")"
  # Plain (non-local) assignment so _handoff escapes to run_driver_in_env and the
  # required-marker gates, which run outside this function's call frame (issue
  # #515, #2355). ORCHESTRATOR is the deliberate exception: see main's note on
  # the ORCHESTRATOR/Handoff.Invoker equivalence.
  _handoff="$(cat "$_handoff_out")"
  # The handoff file, and the agents/review-prompt files it names by path, must
  # survive the rest of the run: driver-exec reads Handoff.AgentsFile at
  # invocation time and the orchestrator reads Handoff.ReviewPromptFile at review
  # time, both after this function returns. So only _prompt_out is removed here
  # (run_driver_in_env always writes its own per-call --prompt-file).
  _handoff_file="$_handoff_out"
  # Test-only hook (issue #2395): no fake Driver ever receives SessionMode or
  # Invoker as CLI args, so this raw JSON is the only place a test can observe
  # them. A no-op in production, where this var is never set.
  [ -n "${DRIVER_HANDOFF_FILE:-}" ] && cp "$_handoff_out" "$DRIVER_HANDOFF_FILE"
  rm -f "$_prompt_out"
  # Test-only hook, same shape as DRIVER_HANDOFF_FILE above: once the cleanup
  # below removes $_review_prompt_out, nothing in production names that path
  # again, so a test proving the removal has no other way to learn it.
  [ -n "${DRIVER_REVIEW_PROMPT_TMP_FILE:-}" ] && printf '%s' "$_review_prompt_out" > "$DRIVER_REVIEW_PROMPT_TMP_FILE"
  # Assemble writes $_review_prompt_out only when it actually rendered a review
  # prompt; every other cell leaves it empty, so remove it rather than leak a
  # temp file for the life of the Box. An `if`, not a bare `[ ... ] &&`: as the
  # last statement in the function a false left-hand side would become its
  # return value, and `set -e` would then abort every run with no review prompt.
  if [ -z "$(printf '%s' "$_handoff" | jq -r '.ReviewPromptFile')" ]; then
    rm -f "$_review_prompt_out"
  fi
}

# _write_env_handoff writes a minimal Handoff descriptor JSON to $1 for the one
# Driver pass that runs before phase_prompt_assembly and so has no handoff file
# yet: phase_conflict_resolve's pre-work rebase fixup. driver-exec and the
# orchestrator both require --handoff-file. The roster, review fields, and
# PromptFile are left off deliberately and unmarshal to their zero values.
_write_env_handoff() {
  local _devshell_args=()
  [ "${_use_dev_shell:-0}" = "1" ] && _devshell_args=(--devshell --devshell-name "${DEV_SHELL_NAME:-default}")
  local _model_omit_empty_args=()
  [ -n "${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}" ] && _model_omit_empty_args=(--argv-model-omit-empty)
  # The verb rather than a `jq -n` blob (issue #2975): promptassembly.Handoff is
  # the one source of truth for the shape, and the Go flags parse MAX_BUDGET_*
  # leniently, where a malformed value used to fail jq and, under
  # `set -euo pipefail`, kill the whole box run before this pass finished.
  driver-exec env-handoff \
    --driver "$DRIVER_NAME" \
    --driver-bin "$DRIVER_BIN" \
    --driver-flags "$DRIVER_FLAGS_COMMON" \
    --model "${MODEL:-}" \
    --effort "${EFFORT:-}" \
    "${_devshell_args[@]}" \
    --issue "$ISSUE_NUMBER" \
    --heartbeat-log "${HEARTBEAT_LOG:-}" \
    --argv-prompt-style "$DRIVER_ARGV_PROMPT_STYLE" \
    --argv-prompt-flag "${DRIVER_ARGV_PROMPT_FLAG:-}" \
    --argv-model-flag "$DRIVER_ARGV_MODEL_FLAG" \
    "${_model_omit_empty_args[@]}" \
    --argv-agents-flag "${DRIVER_ARGV_AGENTS_FLAG:-}" \
    --argv-effort-flag "$DRIVER_ARGV_EFFORT_FLAG" \
    --argv-order "$DRIVER_ARGV_ORDER" \
    --max-budget-tokens "${MAX_BUDGET_TOKENS:-0}" \
    --max-budget-usd "${MAX_BUDGET_USD:-0}" \
    --handoff-output "$1"
}

# run_driver_in_env runs the Driver against $1 (the assembled prompt), $2 (an
# optional per-call handoff-file override; "" uses the shared $_handoff_file),
# $3 (session mode, forwarded verbatim to _driver_session_flags), and $4 (the raw
# Handoff JSON, used only to derive the invoker fork). Delegates to driver-exec
# (issue #626), which owns running the Driver, optionally inside the devShell.
run_driver_in_env() {
  local prompt="$1" handoff_file_override="$2" session_mode="$3" handoff_json="${4:-}"

  # An unrecognized session_mode (e.g. "" for the conflict-resolve pass, which
  # pins no session) falls through _driver_session_flags' case with no output,
  # leaving the session file below empty.
  local _driver_session_flags_rendered
  _driver_session_flags_rendered="$(_driver_session_flags "$session_mode")"

  # Prompt and session data cross into driver-exec as file paths: a compiled
  # binary, unlike the devShell wrapper, needs no quoting-hazard workaround. The
  # roster is not written here; it rides the Handoff descriptor's AgentsFile
  # field, read by buildDriverArgs straight off --handoff-file.
  local _prompt_file _session_file stream_log
  _prompt_file="$(mktemp)"
  printf '%s' "$prompt" > "$_prompt_file"
  _session_file="$(mktemp)"
  printf '%s' "$_driver_session_flags_rendered" > "$_session_file"

  # stream_log is driver-exec's teed copy of the Driver's raw stdout; the
  # launcher's own byte-exact capture is separate and untouched. It survives this
  # call because main()'s SPINDRIFT_PR_INTENT gate scans it later (see
  # _last_stream_log below).
  stream_log="$(mktemp)"

  # $_handoff_file is phase_prompt_assembly's cross-phase sentinel, read via
  # dynamic scoping. $handoff_file_override wins for this call only: the
  # required-marker gates' resumes use it to hand the invoker their own
  # ReviewPromptFile-stripped copy, keeping the nudge a narrow single pass
  # instead of re-entering the full review loop (issues #2065, #2975).
  local _run_handoff_file="${handoff_file_override:-${_handoff_file:-}}" _synthesized_handoff=""
  if [ -z "$_run_handoff_file" ]; then
    _run_handoff_file="$(mktemp)"
    _synthesized_handoff="$_run_handoff_file"
    _write_env_handoff "$_run_handoff_file"
  fi

  # Invoker comes from handoff_json's Invoker field when a Handoff exists (issue
  # #2355); the pre-Handoff conflict-resolve pass falls back to $ORCHESTRATOR,
  # which is mathematically identical to what that field would say. The fork only
  # swaps which binary receives the same flag set (issues #1996, #2047): the
  # orchestrator forwards the handoff to driver-exec for each of its own passes.
  local _driver_invoker=driver-exec
  if [ -n "$handoff_json" ]; then
    [ "$(_handoff_field "$handoff_json" Invoker)" = "orchestrator" ] && _driver_invoker=orchestrator
  else
    [ -n "$ORCHESTRATOR" ] && _driver_invoker=orchestrator
  fi

  # --manifest-path (issue #2983) is passed only when the invoker understands it
  # (driver-exec does not, and hard-fails on an unrecognized flag) and the outbox
  # is mounted host-side, otherwise the orchestrator would write to a path no one
  # can read back. Its contract treats an omitted path as "write no manifest, no
  # error", so leaving the flag off is correct rather than a missed feature.
  local -a _driver_argv=(
    --handoff-file "$_run_handoff_file"
    --prompt-file "$_prompt_file"
    --session-file "$_session_file"
    --log-path "$stream_log"
  )
  if [ "$_driver_invoker" = orchestrator ] && _needs_outbox; then
    # manifest.json here must match passmanifest.FileName.
    _driver_argv+=(--manifest-path "$OUTBOX_DIR/manifest.json")
  fi

  local claude_rc=0
  set +e
  "$_driver_invoker" "${_driver_argv[@]}"
  claude_rc=$?
  set -e
  rm -f "$_prompt_file" "$_session_file"
  [ -n "$_synthesized_handoff" ] && rm -f "$_synthesized_handoff"

  # The launcher greps '^SPINDRIFT_OUTCOME ' from the container log, but claude
  # buries it in a stream-json result event; _driver_extract_outcome re-prints it
  # as a bare line so that contract is unchanged. Captured rather than printed
  # directly so main's backstop (issue #593) can tell whether one was emitted.
  _last_outcome_line="$(_driver_extract_outcome "$stream_log")"
  # main()'s SPINDRIFT_PR_INTENT gate reads this path well after this call
  # returns and hands it to driver-exec marker-gate as --log-path, so the verb
  # owns that grammar instead of bash. This Box exits after one run, so an
  # undeleted per-pass stream log is not a real leak.
  _last_stream_log="$stream_log"
  # The unwrapped Driver result text, remembered the same dynamically-scoped way
  # as _last_stream_log above (issue #2978): main()'s SPINDRIFT_OUTCOME gate
  # hands this path to marker-gate so the verb scans for the marker itself,
  # rather than main() pre-extracting a near-miss line in bash.
  _last_driver_text_log="$(mktemp)"
  _driver_extract_result_text "$stream_log" > "$_last_driver_text_log"
  if [ -n "$_last_outcome_line" ]; then
    printf '%s\n' "$_last_outcome_line"
  fi

  return "$claude_rc"
}

# _stripped_review_handoff writes a throwaway copy of $_handoff_file with
# ReviewPromptFile cleared and prints its path (issue #2975), so each
# required-marker gate's corrective resume stays a narrow single pass instead of
# re-entering the full implement/review/fix loop (issue #2065). Left on disk:
# this Box exits after one run, and test tooling can inspect it afterwards.
_stripped_review_handoff() {
  local _stripped
  _stripped="$(mktemp)"
  jq '.ReviewPromptFile = ""' "$_handoff_file" > "$_stripped"
  printf '%s' "$_stripped"
}

# emit_outcome_backstop hands off to the driver-exec outcome-backstop verb, which
# preserves any committed work on BRANCH and prints one synthetic status=blocked
# SPINDRIFT_OUTCOME line. Called only when the Driver produced no parseable
# outcome, so the launcher always gets a terminal signal (issue #593). The whole
# decision lives in the verb (issue #2157, ADR 0036); this is exec glue.
emit_outcome_backstop() {
  local _recovery="${1:-}"
  # --run-state-file is a fixed path, not a launcher-set env var: it mirrors the
  # orchestrator's own --state-file default (issue #1997). A missing file, from a
  # non-orchestrator run or one that never reached a review pass, is handled by
  # the backstop's own graceful degrade (issue #2459).
  driver-exec outcome-backstop \
    --repo "$WORK_DIR" \
    --issue "$ISSUE_NUMBER" \
    --branch "$BRANCH" \
    --base "origin/${BASE_BRANCH:-}" \
    --dispatch-kind "${DISPATCH_KIND:-work}" \
    --host-mediated-remote "${BOX_HOST_MEDIATED_REMOTE:-}" \
    --outbox-relay-capable "${BOX_OUTBOX_RELAY_CAPABLE:-}" \
    --box-write-enabled "${BOX_WRITE_ENABLED:-}" \
    --recovery-attempted "$_recovery" \
    --max-attempts "$MAX_REBASE_ATTEMPTS" \
    --backoff-secs "$TRANSIENT_BACKOFF_SECS" \
    --jitter-secs "$HOLD_JITTER_SECS" \
    --run-state-file "/tmp/run-state.json"
}

main() {
  # Cross-phase sentinels: declared local here so bash's dynamic scoping lets
  # each phase function assign them by plain (non-local) assignment while
  # keeping them out of true global scope (issue #515).
  local _rebase_and_publish _had_rebase_conflict
  local _use_dev_shell _harness_path
  local prompt _handoff
  local _last_outcome_line _last_stream_log _last_driver_text_log
  # Initialized rather than left bare, unlike the siblings above: this one is
  # assigned only inside the backstop `if` below, and `set -u` treats a bare
  # `local x` as unbound, not empty (issue #2448).
  local _outcome_via_backstop=""
  local ORCHESTRATOR

  configure_env

  # Must run before any phase that could first invoke a cargo/npm/pnpm/yarn/Go/
  # Gradle build. Gradle's own binding is written by the same `driver-exec
  # bind-registry` call this phase already makes (issue #2934), not a separate
  # phase.
  phase_registry_proxy_bindings

  # ORCHESTRATOR (issue #2047, ADR 0035 amendment) is the single master-switch
  # gate, computed once here; the orchestrator-fork-well-formed check
  # (nix/checks/prompts.nix) pins this as the only non-comment
  # ORCHESTRATOR_ENABLED test in this file. Computed before
  # phase_conflict_resolve, whose pass predates any Handoff to read Invoker off.
  ORCHESTRATOR=""
  [ -n "${ORCHESTRATOR_ENABLED:-}" ] && ORCHESTRATOR=1

  if _is_self_contained; then
    # No repo to clone or explore (issue #2202): stand up an empty working
    # directory for the Driver, wire fj for a forgejo verdict post, and skip
    # every clone/branch/toolchain/devShell/prefetch phase.
    mkdir -p "$WORK_DIR"
    cd "$WORK_DIR"
    configure_forgejo_cli
    _use_dev_shell=0
  else
    clone_repo
    # In-tree binding runs right after clone_repo; the Go engine's row table
    # covers all four ecosystems (issues #2932, #2933). See the revert/re-apply
    # dance around phase_branch_recovery/phase_prework_rebase just below.
    intree_binding_apply
    # A research dispatch (ADR 0022, issue #640) explores the clone but never
    # lands code: no branch to cut, adopt, or rebase, so no dance either.
    if ! _is_research_kind; then
      intree_binding_revert
      phase_branch_recovery
      phase_prework_rebase
      intree_binding_apply
    fi
    phase_toolchain_nudge
    phase_devshell_probe
    phase_prefetch
  fi
  # phase_conflict_resolve runs before phase_prompt_assembly (issue #2354) so its
  # two early exits skip the assemble-prompt verb entirely.
  # _populate_driver_skills_dir must run before it too (issue #2706): that
  # phase's driver invocation and both early-exit paths otherwise ran ahead of
  # the only other call site, leaving a prompt naming a skill unable to find it.
  _populate_driver_skills_dir
  # _populate_home_agent_files runs at the same early position for the same
  # reason (issue #2843): under bwrap the baked hooks, settings.json, and
  # opencode agent files must already be in $HOME before phase_conflict_resolve
  # runs and before assemble-prompt rewrites them in place. A no-op under OCI.
  _populate_home_agent_files
  phase_conflict_resolve
  phase_prompt_assembly

  if _is_research_kind; then
    echo "==> claude researching issue #$ISSUE_NUMBER"
  else
    echo "==> claude implementing issue #$ISSUE_NUMBER on $BRANCH"
  fi
  local claude_rc=0
  run_driver_in_env "$prompt" "" "$(printf '%s' "$_handoff" | jq -r '.SessionMode')" "$_handoff" || claude_rc=$?

  # SPINDRIFT_OUTCOME required-marker gate (issues #1607, #2044, #2511, #2978): a
  # Driver pass that exits cleanly but leaves the marker missing most often just
  # ended its turn early (issue #1542), so resume the pinned session once with a
  # nudge the marker-gate verb renders, near-miss-quoting variant included (issue
  # #1900). Research pins no session and a non-zero exit is the launcher's path.
  local _outcome_gate_resumed=""
  if [ "$claude_rc" -eq 0 ] && ! _is_research_kind; then
    local _outcome_gate_json
    _outcome_gate_json="$(driver-exec marker-gate --phase nudge --marker outcome \
      --log-path "$_last_driver_text_log" \
      --issue "${ISSUE_NUMBER:-}" --landing "$BRANCH")"
    if [ "$(printf '%s' "$_outcome_gate_json" | jq -r '.should_nudge // false')" = "true" ]; then
      echo "==> required marker missing — resuming the session once with a nudge"
      _outcome_gate_resumed=1
      local _outcome_nudge_prompt
      _outcome_nudge_prompt="$(printf '%s' "$_outcome_gate_json" | jq -r '.prompt')"
      # This resume gets its own ReviewPromptFile-stripped handoff, not the
      # shared one, or it would re-enter the full implement/review/fix loop a
      # second time under the orchestrator (issues #2065, #2975).
      local _outcome_nudge_handoff_file
      _outcome_nudge_handoff_file="$(_stripped_review_handoff)"
      run_driver_in_env "$_outcome_nudge_prompt" "$_outcome_nudge_handoff_file" "resume" "$(printf '%s' "$_handoff" | jq -c '{Invoker}')" || claude_rc=$?
    fi
  fi

  # Only a driver that exited cleanly yet reported nothing gets the synthetic
  # backstop. A non-zero exit propagates untouched: the launcher's
  # ClassifyTransient/retry path owns that case, and forcing exit 0 here would
  # turn a retryable transient failure into a terminal blocked run (issue #593).
  if [ "$claude_rc" -eq 0 ] && [ -z "$_last_outcome_line" ]; then
    echo "==> driver produced no SPINDRIFT_OUTCOME line — emitting synthetic backstop"
    # Captured (issue #2448): the PR-intent nudge gate below reads it, and a bare
    # `emit_outcome_backstop` left it empty, silently skipping the nudge on every
    # backstopped run.
    _last_outcome_line="$(emit_outcome_backstop "$_outcome_gate_resumed")"
    printf '%s\n' "$_last_outcome_line"
    # This run's ready status was manufactured by the backstop, not reported by
    # the driver, so a later crash in the nudge's best-effort resume must not
    # undo the terminal verdict already committed to here (issue #2448).
    _outcome_via_backstop=1
    # A read-only Box holds no push token, so emit_outcome_backstop could not
    # push $BRANCH itself; it falls through to the bundle-out step below, which
    # relays the branch through the outbox (issue #2094). No longer a per-forge
    # branch (ADR 0039, issue #2252): every mode reaches the single exit at the
    # bottom of main(), and bundleout.Run is a no-op with nothing to relay.
  fi

  # SPINDRIFT_PR_INTENT required-marker gate (issues #2045, #2036, #2511, #2978):
  # a read-only github Box that reaches status=ready but never printed the marker
  # leaves the launcher's hostMediateDraftPR with nothing to relay. Scoped to a
  # genuine status=ready (issue #2448). The scan lives in the marker-gate verb,
  # $RUN_NONCE anchoring against a mid-sentence mention included (issue #1937).
  if [ "$claude_rc" -eq 0 ] && _is_readonly_outbox_relay; then
    local _pr_intent_gate_json
    _pr_intent_gate_json="$(driver-exec marker-gate --phase nudge --marker pr-intent \
      --nonce "${RUN_NONCE:-}" --original-outcome-line "$_last_outcome_line" \
      --log-path "$_last_stream_log")"
    if [ "$(printf '%s' "$_pr_intent_gate_json" | jq -r '.should_nudge // false')" = "true" ]; then
      local _original_ready_outcome_line="$_last_outcome_line"
      local _pr_intent_nudge_prompt
      _pr_intent_nudge_prompt="$(printf '%s' "$_pr_intent_gate_json" | jq -r '.prompt')"
      echo "==> PR-intent marker missing — resuming the session once with a nudge"
      # Same ReviewPromptFile-stripped handoff override as the SPINDRIFT_OUTCOME
      # gate's resume above: this corrective resume must stay a narrow single
      # pass too (issues #2065, #2975).
      local _pr_intent_nudge_handoff_file
      _pr_intent_nudge_handoff_file="$(_stripped_review_handoff)"
      run_driver_in_env "$_pr_intent_nudge_prompt" "$_pr_intent_nudge_handoff_file" "resume" "$(printf '%s' "$_handoff" | jq -c '{Invoker}')" || claude_rc=$?

      # Both logs were reassigned by the resume call above, so --log-path scans
      # the resumed pass's own raw log for a genuine PR-intent marker and
      # --resumed-driver-text-log hands the verb its unwrapped text log to scan
      # for a near-miss line itself, rather than main() pre-extracting one.
      local -a _resolve_args=(
        --phase resolve --marker pr-intent
        --attempts 1
        --nonce "${RUN_NONCE:-}"
        --log-path "$_last_stream_log"
        --resumed-outcome-line "$_last_outcome_line"
        --resumed-driver-text-log "$_last_driver_text_log"
        --original-outcome-line "$_original_ready_outcome_line"
        --resume-exit-code "$claude_rc"
      )
      [ -n "$_outcome_via_backstop" ] && _resolve_args+=(--outcome-via-backstop)
      local _resolve_json
      _resolve_json="$(driver-exec marker-gate "${_resolve_args[@]}")"

      # A crash in this best-effort nudge must never undo an already-terminal
      # backstop-declared ready run (issue #593's exit-0 guarantee, issue #2448).
      # ForceExitZero fires only when this run's ready status came from the
      # synthetic backstop and this resume itself exited non-zero.
      if [ "$(printf '%s' "$_resolve_json" | jq -r '.force_exit_zero // false')" = "true" ]; then
        echo "==> PR-intent nudge resume failed (rc=$claude_rc) after a backstop-declared ready outcome — staying terminal (issue #593, #2448)"
        claude_rc=0
      fi

      local _pr_intent_giveup_op
      _pr_intent_giveup_op="$(printf '%s' "$_resolve_json" | jq -r '.op_line // empty')"
      if [ -n "$_pr_intent_giveup_op" ]; then
        printf '%s\n' "$_pr_intent_giveup_op"
      fi

      # Restore bookkeeping (issue #2448): the resume left no valid outcome, so
      # the earlier line is still this run's final word. Reprinted only when the
      # resume shadowed it with a near-miss line, since the line must stay
      # emitted exactly once. A non-empty $_last_outcome_line here is the resumed
      # pass's own genuine verdict and must be left alone.
      if [ -z "$_last_outcome_line" ]; then
        _last_outcome_line="$_original_ready_outcome_line"
        local _pr_intent_restore_line
        _pr_intent_restore_line="$(printf '%s' "$_resolve_json" | jq -r '.outcome_line // empty')"
        if [ -n "$_pr_intent_restore_line" ]; then
          echo "==> resumed pass did not repeat the original SPINDRIFT_OUTCOME line — restoring it"
          printf '%s\n' "$_pr_intent_restore_line"
        fi
      fi
    fi
  fi

  # Settle-time lockfile scan (issue #3199), best-effort ahead of bundle-out: it
  # warns when a git-tracked lockfile still names the run's Forwarder URL, so a
  # stale pin does not ship silently. Unguarded by $claude_rc, since a crashed
  # run can still have committed one. Guarded on !_is_self_contained rather than
  # !_is_research_kind: only a self-contained dispatch has no clone to scan.
  if ! _is_self_contained; then
    lockfile_forwarder_scan
  fi

  # Harness-owned code-out (ADR 0033, issues #1808, #2082): the Harness, not the
  # Agent, bundles the seam after the Driver exits, for CODE_FORGE=local and for
  # a read-only Box that never pushed anything itself. Skipped for research,
  # which never cuts $BRANCH, so bundle-out could not resolve it. Left unguarded
  # under set -e on purpose: a bundle-out failure is a real container failure.
  if ! _is_research_kind && _needs_outbox; then
    driver-exec bundle-out \
      --repo "$WORK_DIR" \
      --base "origin/${BASE_BRANCH:-}" \
      --branch "$BRANCH" \
      --outbox "$OUTBOX_DIR" \
      --issue "$ISSUE_NUMBER" \
      --prior-outcome-line "$_last_outcome_line"
  fi

  echo "==> entrypoint complete for issue #$ISSUE_NUMBER"
  exit "$claude_rc"
}

main "$@"
