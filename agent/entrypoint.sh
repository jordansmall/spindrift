#!/usr/bin/env bash
# Runs INSIDE the disposable container (one per issue): clones the target repo
# fresh — zero shared host filesystem — cuts a branch, then hands off to a
# headless Claude Code agent that implements the issue and opens a PR.
#
# Baked into the image at /agent/entrypoint.sh (see lib/mkHarness.nix); prompt
# templates at /agent/prompts, overridable via SPINDRIFT_PROMPT_DIR.
#
# --dangerously-skip-permissions is safe here precisely because the container
# IS the isolation boundary: the agent can do anything, but only to a throwaway
# clone with a scoped token and no host access.
set -euo pipefail

# Fully-local mode (CODE_FORGE=local AND ISSUE_TRACKER=local) talks to no real
# forge, so REPO_SLUG and GH_TOKEN have nothing to resolve against. Read the
# launcher's forwarded verdict rather than re-deriving it from the raw
# CODE_FORGE/ISSUE_TRACKER names.
fully_local=false
if [ -n "${BOX_FULLY_LOCAL:-}" ]; then
  fully_local=true
fi
# Self-contained research clones no repo, so REPO_SLUG/GH_TOKEN have nothing to
# resolve against either. SELF_CONTAINED stays a raw per-dispatch input; the
# local-issue-tracker half reads the launcher's forwarded signal instead of a
# raw ISSUE_TRACKER=local comparison.
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

# configure_env sets up the config/env-derived globals every later phase reads.
configure_env() {
  # Self-test mode (ADR 0018) trades hermeticity for in-box `nix flake check`
  # feedback, so it must be loud at Box start.
  if [ "${NIX_STORE_WRITABLE:-false}" = "true" ]; then
    echo "==> WARNING: /nix/store is writable (self-test mode) — this Box is not hermetic; do not use for untrusted issues"
  fi

  # BASE_BRANCH, BRANCH_PREFIX, MODEL, DEV_SHELL_NAME et al. are injected by the
  # nix-rendered defaults preamble prepended at image-build time
  # (env-schema.nix); the :- expansions below keep set -u happy for standalone
  # use.
  export BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"

  # Runtime mount points; overridable only so the harness can be exercised on
  # the host without a container.
  WORK_DIR="${WORK_DIR:-/work}"
  # Read-only Accumulation-repo mount CODE_FORGE=local clones from instead of a
  # network remote (ADR 0033); unused otherwise.
  REPO_MOUNT_DIR="${REPO_MOUNT_DIR:-/repo}"
  # Writable mount bundle-out writes CODE_FORGE=local's seam bundle into (ADR
  # 0033); unused otherwise.
  OUTBOX_DIR="${OUTBOX_DIR:-/outbox}"
  # Fixed in-box path the registry proxy's unix socket mount lands at (ADR
  # 0044); mirrors mount.go's registryProxySocketTarget exactly. Overridable
  # only so bats tests need not touch the real filesystem root.
  REGISTRY_PROXY_SOCKET_PATH="${REGISTRY_PROXY_SOCKET_PATH:-/registry-proxy.sock}"

  # HARNESS_SKILLS_DIR holds baked skills; OPERATOR_SKILLS_DIR is where
  # SPINDRIFT_SKILLS_DIR's runtime override mounts -- deliberately a path
  # distinct from DRIVER_SKILLS_DIR, since mounting onto DRIVER_SKILLS_DIR would
  # replace its entire contents and hide the baked skills. Both are copied into
  # DRIVER_SKILLS_DIR below: copying merges, mounting replaces.
  HARNESS_SKILLS_DIR="${HARNESS_SKILLS_DIR:-/agent/skills}"
  OPERATOR_SKILLS_DIR="${OPERATOR_SKILLS_DIR:-/operator-skills}"

  # Where bwrap stages baked /home/agent content (hooks, settings.json, agent
  # files) read-only. A fresh top-level path, not nested under /agent: /agent is
  # already bound read-only by then, and bwrap cannot fabricate a mountpoint
  # inside an existing read-only bind. The OCI image bakes the same content
  # directly, writable, at the real /home/agent, so under bwrap it must be
  # copied in at startup instead.
  HARNESS_HOME_AGENT_DIR="${HARNESS_HOME_AGENT_DIR:-/home-agent-staged}"

  # DRIVER_NAME, DRIVER_BIN, DRIVER_FLAGS_COMMON, and DRIVER_SKILLS_DIR are
  # baked by the selected Driver's registry entry (ADR 0009) via the
  # nix-rendered preamble -- deliberately no fallback literal or runtime guard;
  # nix/checks/image.nix catches an omitted Driver preamble at build time.

  # The *_CONTRACT_FILE and *_REGISTRY_FILE paths, like PROMPTS_DIR, are baked
  # by the nix-rendered agent-paths preamble; see lib/agent-paths.nix for what
  # each is and which driver-exec verb reads it.

  # _driver_extract_outcome and _driver_session_flags are defined by the Driver
  # registry (lib/drivers/<name>.nix), prepended via driverPreamble; the bats
  # harness sources the same rendered bodies via DRIVER_PREAMBLE_FILE.
}

# configure_forgejo_cli wires FORGEJO_TOKEN into fj so the agent's `fj issue`/
# `fj pr` commands run non-interactively. A no-op when fj isn't baked or no
# token is set (a read-only Box, which is never told to run an fj write).
configure_forgejo_cli() {
  command -v fj >/dev/null 2>&1 || return 0
  [ -n "${FORGEJO_TOKEN:-}" ] || return 0
  local _fj_base="${FORGEJO_BASE_URL:-https://codeberg.org}"
  _fj_base="${_fj_base%/}"
  # The trailing-slash strip above keeps the host fj keys the token under
  # identical to the one clone_repo derives from the same _fj_base.
  # The token is fed on stdin, not argv, so it never lands in `ps`. `auth
  # add-key` (NAME positional, token on stdin) is the forgejo-cli 0.5.0
  # spelling baked into the image; a nixpkgs bump that renames it must update
  # this call in lockstep, or fj would store GIT_USER_NAME as the token.
  printf '%s' "$FORGEJO_TOKEN" | fj -H "$_fj_base" auth add-key "${GIT_USER_NAME:-spindrift-agent}" >/dev/null
}

# clone_repo authenticates, clones the target repo into WORK_DIR, sets the
# repo-local git identity, and fetches the latest refs.
clone_repo() {
  # Neither the local mount nor a Forgejo remote (ADR 0038) is github.com, so
  # gh's credential helper has nothing to apply -- and running it would fail a
  # forgejo Box that carries no GH_TOKEN. Skipping it makes both paths a genuine
  # no-github-credential-helper guarantee, not merely "the clone happens not to
  # use it".
  case "${CODE_FORGE:-github}" in
  local | forgejo) ;;
  *)
    export GH_TOKEN
    gh auth setup-git
    ;;
  esac

  # REPO_SLUG still resolves the Issue Tracker regardless of forge. Gated on the
  # exact CODE_FORGE value so a stray CODE_FORGE_REMOTE_URL left in the
  # environment can't silently redirect a CODE_FORGE=github deployment to the
  # wrong remote.
  local CLONE_URL="https://github.com/${REPO_SLUG}.git"
  if [ "${CODE_FORGE:-github}" = "git" ]; then
    CLONE_URL="${CODE_FORGE_REMOTE_URL:?CODE_FORGE_REMOTE_URL is required when CODE_FORGE=git}"
  elif [ "${CODE_FORGE:-github}" = "forgejo" ]; then
    # FORGEJO_TOKEN rides as the remote URL's userinfo -- the same shape the
    # launcher's forgejoGitRemoteURL builds host-side, so this Box's push and
    # the launcher's later Merge clone target one remote.
    : "${FORGEJO_TOKEN:?FORGEJO_TOKEN is required when CODE_FORGE=forgejo}"
    local _fj_base="${FORGEJO_BASE_URL:-https://codeberg.org}"
    _fj_base="${_fj_base%/}"
    CLONE_URL="${_fj_base%%://*}://${FORGEJO_TOKEN}@${_fj_base#*://}/${REPO_SLUG}.git"
  elif [ "${CODE_FORGE:-github}" = "local" ]; then
    CLONE_URL="$REPO_MOUNT_DIR"
    # Under rootless podman the Box's mapped uid never matches the host-owned
    # bind mount's uid, so git's dubious-ownership guard rejects
    # $REPO_MOUNT_DIR before the clone copies a single object (#1720). A
    # standing global config entry, not a one-shot flag on the clone, since
    # both paths outlive the clone step. This is the one CODE_FORGE=local
    # exception to #404's empty-global-git-config invariant below.
    git config --global --add safe.directory "$REPO_MOUNT_DIR"
    git config --global --add safe.directory "$WORK_DIR"
  fi
  # Redact any embedded userinfo (<token>@) before echoing: CODE_FORGE=forgejo
  # always carries FORGEJO_TOKEN there, and CODE_FORGE_REMOTE_URL commonly
  # carries embedded credentials too, so echoing verbatim would leak a secret
  # to Box stdout. Mirrors the launcher's forge.RedactURLCredentials.
  echo "==> cloning $(printf '%s' "$CLONE_URL" | sed -E 's#://[^/@[:space:]]+@#://#')"
  git clone "$CLONE_URL" "$WORK_DIR"
  cd "$WORK_DIR"
  # Identity is repo-local, not global (#404): CI's hermetic check environment
  # has no global git config, so a global identity here would let git-shelling
  # tests observe config the Box has but CI doesn't.
  git config user.name "$GIT_USER_NAME"
  git config user.email "$GIT_USER_EMAIL"
  # Fetch the absolute latest refs so the pre-work rebase positions the branch
  # on current origin/BASE_BRANCH, not the state captured at clone time.
  git fetch origin
  # After the repo-local identity above, so GIT_USER_NAME is available for fj's
  # (cosmetic) key label.
  configure_forgejo_cli
  install_readonly_guards
}

# install_readonly_guards installs both runtime read-only guards -- the git
# push hook and the gh/fj command shims -- via the `driver-exec readonly-guards`
# verb, which renders every guard named by the forbiddenMarkers registry
# (lib/prompt-contract.nix) so no rejection wording is hand-copied into shell.
# The verb resolves each argv0 on PATH first, so a Box that bakes no `fj`
# (github) or no `gh` (forgejo) is unaffected.
#
# The two guard families are gated separately. A read-only Box whose hand-off IS
# a real `git push` (neither BOX_HOST_MEDIATED_REMOTE nor
# BOX_OUTBOX_RELAY_CAPABLE set) must never get that push blocked locally -- it
# has no other way to hand off its work. No backend registered today leaves both
# unset, but the branch stays live for a future one that does. The command shims
# carry no such risk and install unconditionally.
#
# When the git-hook guard does install, it repoints `origin`'s *push* URL
# (leaving fetch untouched) at a throwaway bare decoy repo -- never $WORK_DIR
# itself. Every real dispatch push targets the branch already checked out here,
# so a pushurl pointed at $WORK_DIR would resolve as "Everything up-to-date" and
# exit 0 without invoking any hook: a silent fake success, worse than the 403
# this replaces. A bare decoy's refs never match $WORK_DIR's, so every push to
# it is a genuine ref update and its pre-receive hook always fires,
# `--no-verify` included.
#
# The hook is installed in BOTH decoy/hooks (--repo-dir) and
# $WORK_DIR/.git/hooks (--extra-repo-dir): the decoy's pre-receive fires only
# for a push that goes through origin's repointed pushurl, so a push to an
# explicit URL or a non-origin remote would otherwise reach the real forge and
# 403 there -- exactly the round trip issue #2463 exists to prevent.
# $WORK_DIR's pre-push catches that case regardless of destination.
install_readonly_guards() {
  if [ -n "${BOX_WRITE_ENABLED:-}" ]; then
    return 0
  fi
  # Deterministic, not mktemp: the PATH mutation below never survives back to a
  # caller inspecting the Box, so the install location must be predictable.
  # $HOME rather than $WORK_DIR's parent -- production /work's parent is
  # root-owned while the Box runs as uid 1000, so a $WORK_DIR-derived path
  # fails the verb's mkdir with EACCES and `set -e` kills the Box mid-clone.
  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  local -a _rg_args=(
    readonly-guards
    --forbidden-markers-registry "$FORBIDDEN_MARKERS_REGISTRY_FILE"
    --shim-dir "$shim_dir"
  )
  if [ -n "${BOX_HOST_MEDIATED_REMOTE:-}" ] || [ -n "${BOX_OUTBOX_RELAY_CAPABLE:-}" ]; then
    # Outside $WORK_DIR so it never shows up in `git status`/`git add -A`.
    # A real bare repo, not just a path, so the local-filesystem transport's
    # ref listing succeeds and the pre-receive hook fires.
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

# phase_branch_recovery adopts prior work on an open PR or force-resets a
# stale branch with no open PR, so this Box always starts from a clean base.
# Sets _rebase_and_publish, read by phase_prework_rebase and
# phase_conflict_resolve.
phase_branch_recovery() {
  _rebase_and_publish=""

  # CODE_FORGE=local has no PR concept and no writable origin (ADR 0033), so a
  # stale refs/remotes/origin/$BRANCH is simply superseded by a fresh checkout:
  # nothing to adopt via a gh call that would violate the
  # no-forge-network-calls guarantee, and no remote branch to force-push.
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
# the stale pre-rebase state. A read-only Box holds no push-capable token (a
# direct force-push 403s before the agent ever runs), so it relays via the
# outbox bundle instead, behind the same BOX_WRITE_ENABLED fail-closed gate the
# OPEN A PULL REQUEST contract's own outbox step uses.
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
  echo "==> rebasing $BRANCH onto latest origin/${BASE_BRANCH:-}"
  _had_rebase_conflict=""
  git rebase "origin/${BASE_BRANCH:-}" || _had_rebase_conflict=1
  # Only needed in the adoption path, where the rebase rewrote history already
  # on the remote. On conflict, publication defers until after the
  # conflict-resolve agent runs below.
  if [ -z "${_had_rebase_conflict:-}" ] && [ -n "${_rebase_and_publish:-}" ]; then
    echo "==> publishing rebased $BRANCH"
    publish_rebased_branch "$BRANCH" || {
      echo "==> publishing rebased branch failed after pre-work rebase on $BRANCH"
      exit 1
    }
  fi
}

# REGISTRY_PROXY_FORWARDER_PORT is the fixed localhost TCP port the in-Box
# Forwarder (ADR 0044) listens on, forwarding to the registry proxy's mounted
# unix socket. An implementation-internal contract, not a user-facing knob;
# chosen to collide with nothing a typical Target devShell binds (unlike the
# common 3000/8000/8080 dev-server range).
REGISTRY_PROXY_FORWARDER_PORT="${REGISTRY_PROXY_FORWARDER_PORT:-27182}"

# phase_registry_proxy_bindings ensures the in-Box Forwarder (ADR 0044) is up
# and wires cargo/npm/pnpm/yarn/Go at it, via `driver-exec bind-registry`'s
# bindings mode. A silent no-op when REGISTRY_PROXY_SOCKET_PATH isn't mounted
# (the registry proxy is off by default). See bindregistry.CargoConfigTOML and
# NpmFamilyBindings for the cargo table-valued-config and npm env-precedence
# reasoning behind exactly what gets bound.
#
# Must run before clone_repo, phase_prefetch, phase_devshell_probe, or any
# driver invocation -- every place a cargo build or npm install could first
# happen.
phase_registry_proxy_bindings() {
  local _bindings_env_out _bind_registry_rc=0 _source_rc=0
  # A verb failure here (mktemp, unwritable output path, verb crash) must never
  # take the whole box run down.
  if ! _bindings_env_out="$(mktemp)"; then
    echo "==> WARNING: mktemp failed — skipping registry proxy bindings"
    return 0
  fi

  # See phase_toolchain_nudge's own matching trap for why this is a RETURN
  # trap that unsets itself, not a plain `rm -f` at each return site.
  trap 'rm -f "$_bindings_env_out"; trap - RETURN' RETURN

  driver-exec bind-registry \
    --registry-proxy-socket "$REGISTRY_PROXY_SOCKET_PATH" \
    --forwarder-port "$REGISTRY_PROXY_FORWARDER_PORT" \
    --bindings-env-output "$_bindings_env_out" \
    || _bind_registry_rc=$?

  if [ "$_bind_registry_rc" -ne 0 ]; then
    echo "==> WARNING: driver-exec bind-registry failed (exit ${_bind_registry_rc}) — skipping registry proxy bindings"
    return 0
  fi

  # rc-captured: an unguarded `source` failure would abort the whole entrypoint
  # mid-phase under errexit (see phase_toolchain_nudge's matching comment).
  # shellcheck disable=SC1090  # dynamic path (tempfile), sourced by design: the verb's own env-file output
  source "$_bindings_env_out" || _source_rc=$?
  if [ "$_source_rc" -ne 0 ]; then
    echo "==> WARNING: sourcing driver-exec bind-registry's env output failed (exit ${_source_rc}) — skipping registry proxy bindings"
    return 0
  fi
}

# intree_binding_apply wraps `driver-exec bind-registry`'s in-tree apply mode
# for every ecosystem row's in-tree config rewrite under $WORK_DIR; the rewrite
# logic lives in the Go engine (ApplyInTreeBinding), whose doc comments carry
# the cargo#5416/ADR 0044 rationale and crash-recovery behavior. On failure it
# also runs intree_binding_revert as best-effort cleanup, since otherwise a
# failed apply's partial state would sit on disk until some later, conditional
# cleanup path happened to run.
intree_binding_apply() {
  local _intree_apply_rc=0 _cargo_bindings_env_out _source_rc=0
  # A verb failure here must never take the whole box run down.
  if ! _cargo_bindings_env_out="$(mktemp)"; then
    echo "==> WARNING: mktemp failed — skipping cargo registry placeholder bindings"
    return 0
  fi

  # See phase_registry_proxy_bindings's own matching trap for why this is a
  # RETURN trap that unsets itself, not a plain `rm -f` at each return site.
  trap 'rm -f "$_cargo_bindings_env_out"; trap - RETURN' RETURN

  driver-exec bind-registry \
    --intree-action apply \
    --intree-work-dir "$WORK_DIR" \
    --registry-proxy-socket "$REGISTRY_PROXY_SOCKET_PATH" \
    --forwarder-port "$REGISTRY_PROXY_FORWARDER_PORT" \
    --intree-bindings-env-output "$_cargo_bindings_env_out" \
    || _intree_apply_rc=$?
  if [ "$_intree_apply_rc" -ne 0 ]; then
    echo "==> WARNING: driver-exec bind-registry (in-tree apply) failed (exit ${_intree_apply_rc}) — skipping in-tree registry binding"
    intree_binding_revert
    return 0
  fi

  # rc-captured: an unguarded `source` failure would abort the whole entrypoint
  # mid-phase under errexit.
  # shellcheck disable=SC1090  # dynamic path (tempfile), sourced by design: the verb's own env-file output
  source "$_cargo_bindings_env_out" || _source_rc=$?
  if [ "$_source_rc" -ne 0 ]; then
    echo "==> WARNING: sourcing driver-exec bind-registry's cargo placeholder env output failed (exit ${_source_rc}) — skipping cargo registry placeholder bindings"
    return 0
  fi
}

# intree_binding_revert wraps `driver-exec bind-registry`'s in-tree revert mode,
# undoing intree_binding_apply's rewrite (see RevertInTreeBinding). See
# phase_conflict_resolve's call site for the one case where the revert
# legitimately fails and this warns rather than aborting.
intree_binding_revert() {
  local _intree_revert_rc=0
  driver-exec bind-registry --intree-action revert --intree-work-dir "$WORK_DIR" \
    || _intree_revert_rc=$?
  if [ "$_intree_revert_rc" -ne 0 ]; then
    echo "==> WARNING: driver-exec bind-registry (in-tree revert) failed (exit ${_intree_revert_rc})"
  fi
}

# phase_toolchain_nudge emits a one-time hint for a cold run with a
# recognized dependency-manifest file and no prefetch configured.
phase_toolchain_nudge() {
  # The verb runs unconditionally -- not gated on PREFETCH or the registry
  # proxy's own on/off state -- so the env file is always available; only the
  # hint's emission stays gated on PREFETCH.
  local _nudge_env_out _bind_registry_rc=0 _source_rc=0 _nudge_ecosystem=""
  # This phase is cosmetic-hint-only -- a verb failure must not take the whole
  # box run down under set -euo pipefail. mktemp is wrapped in the same guard:
  # it is the same "verb tooling" this phase must survive losing.
  if ! _nudge_env_out="$(mktemp)"; then
    if [ -z "${PREFETCH:-}" ]; then
      echo "==> WARNING: mktemp failed — skipping toolchain nudge"
    fi
    return 0
  fi

  # Registered as soon as mktemp has produced a path, so every exit from here on
  # removes the tempfile once instead of repeating `rm -f` at each return site.
  # The handler unsets itself the moment it fires: a bash RETURN trap is
  # process-global, not function-scoped, so leaving it registered would fire on
  # the next unrelated function's return and dereference a `local` that no
  # longer exists.
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

  # rc-captured rather than left to fail into errexit: an unguarded `source`
  # failure (e.g. a malformed env file) would abort the whole entrypoint
  # mid-phase, which the RETURN trap above can never clean up after -- errexit
  # unwinds the process without "returning" from any function on the way out.
  # shellcheck disable=SC1090  # dynamic path (tempfile), sourced by design: the verb's own env-file output
  source "$_nudge_env_out" || _source_rc=$?
  if [ "$_source_rc" -ne 0 ]; then
    if [ -z "${PREFETCH:-}" ]; then
      echo "==> WARNING: sourcing driver-exec bind-registry's env output failed (exit ${_source_rc}) — skipping toolchain nudge"
    fi
    return 0
  fi
  # NUDGE_ECOSYSTEM is the env file's own on-disk variable name, not this
  # shell's naming convention, so it must not survive past sourcing.
  _nudge_ecosystem="${NUDGE_ECOSYSTEM:-}"
  unset NUDGE_ECOSYSTEM

  if [ -z "${PREFETCH:-}" ] && [ -n "$_nudge_ecosystem" ]; then
    echo "==> hint: ${_nudge_ecosystem} project detected; set 'prefetch' to warm dependency caches per run, or 'packages' to bake a toolchain into the image"
  fi
}

# phase_devshell_probe detects a Nix devShell in the cloned repo. Sets
# _use_dev_shell (read by phase_prefetch and run_driver_in_env's --devshell
# switch) and _harness_path (read by phase_prefetch only -- run_driver_in_env
# delegates devShell PATH handling to driver-exec, issue #626).
phase_devshell_probe() {
  # Detect a Nix devShell in the cloned repo. When found the prefetch hook and
  # Driver run inside `nix develop` so the agent operates in the Target's exact
  # When found, the prefetch hook and Driver run inside `nix develop` so the
  # agent operates in the Target's exact pinned environment.
  # DEV_SHELL_PROBE_TIMEOUT is nix-baked so a heavy consumer devShell eval
  # cannot stall the box.
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
  # No-op when unset. Runs inside the devShell when one is available, so the
  # hook sees the Target's exact toolchain and env vars.
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
# snippets, etc.) survives. The fragment registry (lib/fragments.nix)
# contributes the rest via the nix-rendered _FRAGMENT_SUBST_VARS array: a
# fragment can reference only what its registry row declares, so a forgotten
# allowlist entry is impossible by construction.
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

# _is_research_kind reports (via exit status) whether this dispatch is the
# advise-only research kind (ADR 0022); an unset DISPATCH_KIND means work.
_is_research_kind() {
  [ "${DISPATCH_KIND:-work}" = "research" ]
}

# _is_self_contained reports (via exit status) whether this is the research
# kind's no-repo sub-mode: the launcher forwards SELF_CONTAINED=1, so the Box
# clones no repo and explores none. Unset defaults to off.
_is_self_contained() {
  [ "${SELF_CONTAINED:-}" = "1" ]
}

# _is_readonly_outbox_relay reports (via exit status) whether this is a
# read-only, outbox-relay-capable Box: no push-capable token was ever issued (so
# a force-push can only 403) and the backend is outbox-relay-capable per
# lib/backends/default.nix, forwarded as BOX_OUTBOX_RELAY_CAPABLE rather than
# compared against the raw CODE_FORGE name here. Such a Box hands its branch off
# through the harness-owned outbox bundle seam rather than a push.
_is_readonly_outbox_relay() {
  [ -z "${BOX_WRITE_ENABLED:-}" ] && [ -n "${BOX_OUTBOX_RELAY_CAPABLE:-}" ]
}

# _handoff_field extracts field $2 from the raw Handoff descriptor JSON $1,
# defaulting to empty when the field is absent or null.
_handoff_field() {
  printf '%s' "$1" | jq -r ".$2 // empty"
}

# _populate_driver_skills_dir copies HARNESS_SKILLS_DIR then OPERATOR_SKILLS_DIR
# into DRIVER_SKILLS_DIR, so an operator-supplied skill wins on a name collision
# but a harness-owned skill the operator didn't override survives. This is the
# one seam that makes a skill actually invocable -- the driver discovers skills
# only at DRIVER_SKILLS_DIR (e.g. Claude Code's $HOME/.claude/skills), never at
# the source dirs directly -- so every call site that spawns a driver invocation
# whose prompt may reference a skill must run this first. Cheap and idempotent.
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
# the real $HOME, by copying rather than mounting -- the same reasoning as
# _populate_driver_skills_dir above. A no-op under the OCI runner, which never
# creates that path since lib/image.nix bakes /home/agent directly, writable, at
# the real location. Under bwrap the same baked content is ro-bound at a
# distinct source path, so it must be copied in before anything reads $HOME.
#
# The follow-up chmod is required, not optional: `cp -r` from a read-only source
# (bwrap's ro-bind, or a Nix store path) preserves the source's read-only mode
# bits, so a hook or settings.json copied in here would land unwritable.
# Directories are made writable too, since the box creates new content under
# arbitrary copied-in directories (e.g. `gh` doing `mkdir ~/.config/gh`) and
# there is no way to enumerate ahead of time which ones will need it.
#
# Exactly ONE directory must be excluded: lib/image.nix pre-creates an empty
# placeholder for the driver's sessionCacheDirRelative, and bwrap uses that same
# HOME-relative path as a read-write bind target for a directory that lives on
# the HOST filesystem. Chmod'ing it would reach out of the sandbox and mutate
# the host bind-mount's permission bits (and, under `set -euo pipefail`, a
# failure there aborts box startup entirely). Hence the
# $DRIVER_SESSION_CACHE_DIR guard below.
_populate_home_agent_files() {
  local _src _target
  if [ -d "$HARNESS_HOME_AGENT_DIR" ]; then
    cp -r "$HARNESS_HOME_AGENT_DIR"/. "$HOME"/
    while IFS= read -r _src; do
      _target="$HOME/${_src#"$HARNESS_HOME_AGENT_DIR"/}"
      if [ -d "$_src" ] && [ -n "${DRIVER_SESSION_CACHE_DIR:-}" ] && [ "$_target" = "$DRIVER_SESSION_CACHE_DIR" ]; then
        continue
      fi
      chmod u+w "$_target"
    done < <(find "$HARNESS_HOME_AGENT_DIR" -mindepth 1)
  fi
}

# _scan_skills_found echoes a comma-joined list of skill names discoverable
# under "$1" -- a directory holding a SKILL.md (<dir>/<name>/SKILL.md), never a
# flat <name>.md file, matching how Claude Code itself discovers a skill.
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
# resolve-only dispatch mode. Reads _had_rebase_conflict and
# _rebase_and_publish. Called from main() before phase_prompt_assembly, so its
# two early-exit paths fire before the assemble-prompt verb is ever invoked.
phase_conflict_resolve() {
  if [ -n "${_had_rebase_conflict:-}" ]; then
    echo "==> pre-work rebase conflict detected — invoking conflict-resolve agent"
    # This prompt renders through the bash-only `_subst` path below, not the
    # driver-exec assemble-prompt verb, so nothing else ever populates these
    # fragment vars for this call site -- left unset, `_subst`'s `${!v:-}`
    # indirect expansion would substitute them as permanently empty. They are
    # `local` here and reach `_subst` via bash dynamic scoping (issue #515).
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
    # Unconditional, unlike its two siblings above: resolving a rebase conflict
    # always edits code, so the comment-discipline rule always applies here.
    local CODE_COMMENTS_STEP
    # shellcheck disable=SC2034 # consumed by _subst's envsubst allowlist via ${!v:-} indirection
    CODE_COMMENTS_STEP="$(_subst "${PROMPTS_DIR}/fragments/code-comments.md")"$'\n\n'
    local _cr_prompt
    _cr_prompt="$(_subst "${PROMPTS_DIR}/conflict-resolve-prompt.md")"
    # No agents config or session to pin/resume for this pass; its exit status
    # isn't checked here either — success is read off the rebase state below.
    # Shadows _use_dev_shell to 0 for this call only: this pass runs outside
    # the devShell, and only the main run enters it.
    local _use_dev_shell=0
    run_driver_in_env "$_cr_prompt" "" "" "" || true
    if [ -d ".git/rebase-merge" ] || [ -d ".git/rebase-apply" ]; then
      # Best-effort revert before the abort below re-checks out HEAD (ADR
      # 0044). When an in-tree config file (.cargo/config.toml, .npmrc, ...) is
      # itself among the unmerged conflicting paths, git refuses to check it
      # out and intree_binding_revert prints its own warning -- harmless, since
      # the `git rebase --abort` right below cleans up regardless.
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
  # branch onto current main.  Exit after resolution without running the main agent.
  if [ -n "${CONFLICT_RESOLVE_PR_URL:-}" ]; then
    echo "==> CONFLICT_RESOLVE_PR_URL: conflict resolved — exiting without main agent"
    exit 0
  fi
}

# phase_prompt_assembly delegates prompt/roster assembly to the driver-exec
# assemble-prompt verb (ADR 0036, ADR 0007's thin-exec-glue tier): it collects
# the handful of facts only bash can supply (filesystem-derived skill discovery
# at DRIVER_SKILLS_DIR), forwards the already-available env knobs as flags, and
# reads back the three files the verb writes.
# cmd/launcher/internal/promptassembly owns the gate/fragment/base-prompt/
# roster-injection logic.
#
# Sets `prompt`, `_handoff` (the raw Handoff descriptor JSON, read for
# SessionMode/Invoker at each call site), and `_handoff_file` -- the on-disk
# path to that same descriptor, which run_driver_in_env hands the invoker as
# --handoff-file by default: the whole driver/model/effort/argv-shape/devshell/
# caps fact set lives inside that file, sourced by driver-exec/orchestrator
# themselves rather than rebuilt into per-call flags. A required-marker gate's
# corrective resume instead passes its own throwaway ReviewPromptFile-stripped
# copy, leaving this file untouched.
phase_prompt_assembly() {
  # Skill discovery is filesystem I/O only bash can do; the verb takes the
  # result as its --skills-found flag. main() already populated
  # DRIVER_SKILLS_DIR before phase_conflict_resolve; repeating it here keeps
  # this function self-contained if invoked in isolation -- cheap and
  # idempotent.
  _populate_driver_skills_dir

  local SKILLS_FOUND
  SKILLS_FOUND="$(_scan_skills_found "$DRIVER_SKILLS_DIR")"

  # Only paths, the skill-baked probes, and Handoff passthrough are forwarded as
  # flags; every other Box env var is read straight off os.Getenv by the verb
  # (promptassembly.EnvFromEnviron). Boolean gates ride bare flags, appended
  # only when true, so an unset knob here is indistinguishable from an explicit
  # off.
  local -a _ap_args=(
    --registry "$PROMPTASSEMBLY_REGISTRY_FILE"
    --validate-markers-registry "$PROMPT_CONTRACT_REGISTRY_FILE"
    --prompts-dir "$PROMPTS_DIR"
    --agents-prompt-files "${AGENTS_PROMPT_FILES:-}"
    --driver-agent-files-dir "${DRIVER_AGENT_FILES_DIR:-}"
    --comms-contract-file "$COMMS_CONTRACT_FILE"
    --check-contract-file "$CHECK_CONTRACT_FILE"
    --code-comments-contract-file "$CODE_COMMENTS_CONTRACT_FILE"
    --outcome-contract-file "$OUTCOME_CONTRACT_FILE"
    --research-outcome-contract-file "$RESEARCH_OUTCOME_CONTRACT_FILE"
    --skills-found "$SKILLS_FOUND"
    # Driver-invocation passthrough: assemble-prompt layers these onto the
    # handoff JSON verbatim, and driver-exec/orchestrator read them straight off
    # the handoff file at run time.
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
  # END GENERATED SKILL-BAKED PROBES
  # Bare passthrough gates. _use_dev_shell is main's cross-phase sentinel, set
  # by phase_devshell_probe or the self-contained branch before this phase and
  # read via dynamic scoping.
  [ -n "${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}" ] && _ap_args+=(--argv-model-omit-empty)
  [ "$_use_dev_shell" = "1" ] && _ap_args+=(--devshell --devshell-name "${DEV_SHELL_NAME:-default}")

  local _prompt_out _agents_out _handoff_out _review_prompt_out
  _prompt_out="$(mktemp)"
  _agents_out="$(mktemp)"
  _handoff_out="$(mktemp)"
  _review_prompt_out="$(mktemp)"

  # Bare `driver-exec`, resolved via $PATH: the real in-box binary in prod, the
  # bats fake under test. A nonzero exit propagates straight through
  # `set -euo pipefail`, so no explicit error handling is needed.
  driver-exec assemble-prompt "${_ap_args[@]}" \
    --prompt-output "$_prompt_out" \
    --agents-json-output "$_agents_out" \
    --handoff-output "$_handoff_out" \
    --review-prompt-output "$_review_prompt_out"

  # $_agents_out is never read back into a bash string: it survives on disk
  # because it IS the file Handoff.AgentsFile points to, read by driver-exec/
  # orchestrator directly off --handoff-file.
  prompt="$(cat "$_prompt_out")"
  # Plain (non-local) assignment, so this escapes to run_driver_in_env and the
  # required-marker gates as one of main's cross-phase sentinels (issue #515) --
  # those callers run outside this function's own call frame.
  _handoff="$(cat "$_handoff_out")"
  # Same cross-phase escape as _handoff above. The handoff file, and the
  # agents/review-prompt files it references by path, must survive for the rest
  # of the run: driver-exec reads Handoff.AgentsFile at driver-invocation time
  # and the orchestrator reads Handoff.ReviewPromptFile at review-pass time,
  # both after this function returns. So only _prompt_out is removed here --
  # run_driver_in_env always writes its own per-call --prompt-file.
  _handoff_file="$_handoff_out"
  # Test-only hook: no fake Driver ever receives SessionMode/Invoker as CLI
  # args, so this raw JSON is the only place a test can observe them. A no-op in
  # production, where this var is never set.
  [ -n "${DRIVER_HANDOFF_FILE:-}" ] && cp "$_handoff_out" "$DRIVER_HANDOFF_FILE"
  rm -f "$_prompt_out"
  # Test-only hook: once the cleanup below removes $_review_prompt_out on a
  # no-render cell, nothing else ever surfaces that path, so a test proving the
  # removal happened has no other way to learn it.
  [ -n "${DRIVER_REVIEW_PROMPT_TMP_FILE:-}" ] && printf '%s' "$_review_prompt_out" > "$DRIVER_REVIEW_PROMPT_TMP_FILE"
  # $_review_prompt_out is written only when Assemble actually rendered a review
  # prompt; every other cell leaves it empty and nothing else references its
  # path, so remove it rather than leaking a temp file for the life of the Box.
  # An `if`, not a bare `[ ... ] &&`: this is the function's last statement, so
  # a false `&&` left-hand side would become its return value -- `set -e` would
  # then abort the whole run on every cell that renders no review prompt.
  if [ -z "$(printf '%s' "$_handoff" | jq -r '.ReviewPromptFile')" ]; then
    rm -f "$_review_prompt_out"
  fi
}

# _write_env_handoff writes, to path $1, a minimal Handoff descriptor JSON built
# straight from the same env knobs phase_prompt_assembly forwards to
# assemble-prompt. It exists solely for the one Driver pass that runs before
# phase_prompt_assembly and so has no handoff file yet: phase_conflict_resolve's
# pre-work rebase fixup. driver-exec/orchestrator both require --handoff-file,
# so that pass cannot run without one. The roster, review fields, and PromptFile
# are deliberately left off -- that pass provisions no subagents, runs no review
# pass, and is always handed an explicit --prompt-file -- so they unmarshal to
# their zero values.
#
# Delegates to `driver-exec env-handoff` rather than restating the
# Handoff/ArgvShape/Caps grammar in a `jq -n` blob here: promptassembly.Handoff
# is the single source of truth for that shape, and the Go flag parses
# MAX_BUDGET_TOKENS/MAX_BUDGET_USD leniently (degrading a malformed value to 0)
# where the old `jq --argjson` call would fail and, under `set -euo pipefail`,
# kill the whole box run before this pass ever finished.
_write_env_handoff() {
  local _devshell_args=()
  [ "${_use_dev_shell:-0}" = "1" ] && _devshell_args=(--devshell --devshell-name "${DEV_SHELL_NAME:-default}")
  local _model_omit_empty_args=()
  [ -n "${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}" ] && _model_omit_empty_args=(--argv-model-omit-empty)
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

# run_driver_in_env runs the Driver against $1 (the assembled prompt), with:
#   $2 - handoff-file override; "" uses the shared $_handoff_file unchanged.
#        Each required-marker gate's corrective resume passes its own throwaway
#        copy with ReviewPromptFile cleared, so the nudge-and-retry reaches the
#        invoker as a narrow single pass rather than re-entering the full
#        implement/review/fix loop a second time (issue #2065).
#   $3 - session mode, forwarded verbatim to the nix-supplied
#        _driver_session_flags: "initial"/"resume" pin or resume the issue's
#        session id; any other value yields no session flags.
#   $4 - the raw Handoff descriptor JSON, or "" for the one pass that predates
#        it. Used only to derive the invoker fork; every other
#        driver/model/effort/argv-shape/devshell/caps fact lives inside the
#        handoff FILE, read by driver-exec/orchestrator themselves rather than
#        rebuilt into per-call CLI flags here.
#
# The one pass with no $_handoff_file yet -- phase_conflict_resolve's pre-work
# rebase fixup -- gets a minimal env-derived handoff synthesized here
# (_write_env_handoff), and its invoker fork falls back to $ORCHESTRATOR.
#
# Delegates to driver-exec, the in-box Go unit that owns "run the Driver,
# optionally inside the Project devShell" as one code path: it takes the
# prompt/session as file paths (a compiled binary crosses the devShell process
# boundary with a plain argv), tees the stream to a log path, filters
# heartbeats in-process, and returns the Driver's exit status.
#
# The invoker fork (default off) swaps which binary receives that same flag set:
# off takes the direct driver-exec path; on hands the invocation to the in-box
# Go orchestrator, which forwards the handoff to driver-exec for each of its own
# passes.
run_driver_in_env() {
  local prompt="$1" handoff_file_override="$2" session_mode="$3" handoff_json="${4:-}"

  # An unrecognized session_mode (e.g. "" for the conflict-resolve pass, which
  # pins/resumes no session) falls through _driver_session_flags' case with no
  # output, so the session file below ends up empty — same as before.
  local _driver_session_flags_rendered
  _driver_session_flags_rendered="$(_driver_session_flags "$session_mode")"

  # Prompt/session data crosses into driver-exec as file paths -- a compiled
  # binary, unlike the devShell wrapper, needs no quoting-hazard workaround for
  # the prompt. The roster rides the Handoff descriptor's AgentsFile field,
  # read by buildDriverArgs straight off --handoff-file.
  local _prompt_file _session_file stream_log
  _prompt_file="$(mktemp)"
  printf '%s' "$prompt" > "$_prompt_file"
  _session_file="$(mktemp)"
  printf '%s' "$_driver_session_flags_rendered" > "$_session_file"

  # driver-exec's teed copy of the Driver's raw stdout, read below by
  # _driver_extract_outcome; the launcher's own stdout capture is separate and
  # untouched. It also survives this call -- main()'s SPINDRIFT_PR_INTENT gate
  # scans it later (see _last_stream_log below).
  stream_log="$(mktemp)"

  # $handoff_file_override wins over phase_prompt_assembly's $_handoff_file
  # sentinel for this call only -- the required-marker gates' corrective
  # resumes use it to hand the invoker their own ReviewPromptFile-stripped
  # copy. The single pass that runs before phase_prompt_assembly finds neither
  # set, so synthesize a minimal handoff: driver-exec/orchestrator require
  # --handoff-file and cannot run without one.
  local _run_handoff_file="${handoff_file_override:-${_handoff_file:-}}" _synthesized_handoff=""
  if [ -z "$_run_handoff_file" ]; then
    _run_handoff_file="$(mktemp)"
    _synthesized_handoff="$_run_handoff_file"
    _write_env_handoff "$_run_handoff_file"
  fi

  # The one pass with no Handoff yet falls back to $ORCHESTRATOR, main's early
  # ORCHESTRATOR_ENABLED-derived sentinel -- mathematically identical to what
  # the Handoff's own Invoker field would say once one existed.
  local _driver_invoker=driver-exec
  if [ -n "$handoff_json" ]; then
    [ "$(_handoff_field "$handoff_json" Invoker)" = "orchestrator" ] && _driver_invoker=orchestrator
  else
    [ -n "$ORCHESTRATOR" ] && _driver_invoker=orchestrator
  fi

  local claude_rc=0
  set +e
  "$_driver_invoker" \
    --handoff-file "$_run_handoff_file" \
    --prompt-file "$_prompt_file" \
    --session-file "$_session_file" \
    --log-path "$stream_log"
  claude_rc=$?
  set -e
  rm -f "$_prompt_file" "$_session_file"
  [ -n "$_synthesized_handoff" ] && rm -f "$_synthesized_handoff"

  # The launcher greps '^SPINDRIFT_OUTCOME ' from the container log, but the
  # Driver's raw transcript buries it in a stream-json result event;
  # _driver_extract_outcome surfaces it as a bare line so that contract is
  # unchanged. Captured rather than printed directly so main's post-return
  # backstop (issue #593) can tell whether the Driver emitted one.
  _last_outcome_line="$(_driver_extract_outcome "$stream_log")"
  # Left on disk and remembered by dynamic-scoping assignment: main()'s
  # SPINDRIFT_PR_INTENT gate hands this path to driver-exec marker-gate well
  # after this call returns, so the verb scans it itself rather than
  # re-implementing that grammar in bash. This Box exits after one run, so an
  # undeleted per-pass stream log is not a real leak.
  _last_stream_log="$stream_log"
  # The plain-text, unwrapped Driver result -- no landing=/status=
  # classification, just the jq-unwrapped/markdown-stripped text. Remembered the
  # same dynamically-scoped way as _last_stream_log above: main()'s
  # SPINDRIFT_OUTCOME gate hands this path to driver-exec marker-gate so the
  # verb scans for the marker itself.
  _last_driver_text_log="$(mktemp)"
  _driver_extract_result_text "$stream_log" > "$_last_driver_text_log"
  if [ -n "$_last_outcome_line" ]; then
    printf '%s\n' "$_last_outcome_line"
  fi

  return "$claude_rc"
}

# _stripped_review_handoff writes a throwaway copy of $_handoff_file with
# ReviewPromptFile cleared and prints its path -- shared by both required-marker
# gates' corrective resumes, so that nudge-and-retry reaches the invoker as a
# narrow single pass rather than re-entering the full implement/review/fix loop
# (issue #2065). Only called after phase_prompt_assembly has set $_handoff_file,
# so no unset guard is needed. The printed file is left on disk: this Box exits
# after one run, and leaving it lets test/debug tooling inspect it.
_stripped_review_handoff() {
  local _stripped
  _stripped="$(mktemp)"
  jq '.ReviewPromptFile = ""' "$_handoff_file" > "$_stripped"
  printf '%s' "$_stripped"
}

# emit_outcome_backstop hands off to the driver-exec outcome-backstop verb,
# which best-effort preserves any committed work on BRANCH and prints a single
# synthetic status=blocked SPINDRIFT_OUTCOME line -- called from main only when
# the Driver's run produced no parseable outcome line, so the launcher always
# gets a terminal signal to classify (issue #593) instead of a silent gap.
#
# The whole backstop decision lives in the verb (ADR 0036); this function is
# linear exec glue. BOX_HOST_MEDIATED_REMOTE and BOX_OUTBOX_RELAY_CAPABLE are
# backend-capability facts the launcher already resolved host-side, not
# re-derived in-box from CODE_FORGE's name.
emit_outcome_backstop() {
  local _recovery="${1:-}"
  # --run-state-file is a fixed path, not forwarded from any launcher-set env
  # var: it mirrors the orchestrator's own --state-file default, the path the
  # orchestrator process already writes its run-state handoff artifact to. A
  # missing file (a non-orchestrator run, or one that never reached a review
  # pass) is handled by the backstop's own graceful degrade.
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
  # Initialized empty, unlike the siblings above (always assigned before any
  # read): this one is only assigned inside the backstop `if` block below, and
  # `set -u` treats a bare `local x` with no value as unbound, not empty.
  local _outcome_via_backstop=""
  local ORCHESTRATOR

  configure_env

  # Must run before any phase that could first invoke a cargo/npm/pnpm/yarn/Go/
  # Gradle build (clone_repo's devShell/prefetch phases below, the driver
  # itself) -- see its own doc comment. Gradle's binding rides the same
  # `driver-exec bind-registry` call, not a separate phase.
  phase_registry_proxy_bindings

  # ORCHESTRATOR (ADR 0035 amendment): the single canonical master-switch gate,
  # computed exactly once from the raw ORCHESTRATOR_ENABLED env var -- the
  # orchestrator-fork-well-formed check (nix/checks/prompts.nix) pins this as
  # the one non-comment ORCHESTRATOR_ENABLED test allowed in this file.
  # Computed before phase_conflict_resolve so that pass -- which predates any
  # Handoff, and so cannot read Invoker off one -- has a value to fall back to.
  # Handoff.Invoker == "orchestrator" iff ORCHESTRATOR_ENABLED is set
  # (gates.go), so the two are always mathematically identical.
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
    # In-tree binding for all four ecosystems runs right after clone_repo; see
    # the revert/re-apply dance around the branch-recovery/rebase pair below.
    intree_binding_apply
    # A research dispatch (ADR 0022, issue #640) explores the fresh clone but
    # never lands code: no branch to cut, adopt, or rebase -- and so never
    # needs the revert/re-apply dance either.
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
  # phase_conflict_resolve runs before phase_prompt_assembly, unconditionally
  # for every dispatch kind, so its two early-exit paths
  # (CONFLICT_RESOLVE_PR_URL's resolve-only dispatch, and an unresolvable
  # pre-work rebase conflict) skip the assemble-prompt verb entirely instead of
  # running it and discarding the result.
  #
  # Both populate helpers must run before phase_conflict_resolve, not just
  # before phase_prompt_assembly (issues #2706, #2843): that phase's own driver
  # invocation and both its early-exit paths otherwise run before
  # DRIVER_SKILLS_DIR is populated -- leaving a prompt that tells the agent to
  # invoke a skill structurally unable to find it -- and, under bwrap, before
  # the baked hooks/settings.json/agent files are copied into $HOME.
  _populate_driver_skills_dir
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

  # SPINDRIFT_OUTCOME required-marker gate (issue #1607/#2044): a Driver pass
  # that exits cleanly but leaves the marker missing or unparseable most often
  # just ended its turn early rather than actually failing, so resume the same
  # pinned session exactly once with a corrective nudge before any backstop
  # runs. A research dispatch pins no session worth resuming (ADR 0022); a
  # non-zero exit is the launcher's own ClassifyTransient/retry path to handle
  # -- neither reaches this gate. The driver-exec marker-gate verb both decides
  # whether to nudge and renders the nudge prompt, scanning
  # $_last_driver_text_log itself.
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
      # Its own ReviewPromptFile-stripped handoff, not the shared $_handoff_file
      # the main pass just used -- otherwise the resume re-enters the full
      # implement/review/fix loop under the orchestrator (issue #2065).
      local _outcome_nudge_handoff_file
      _outcome_nudge_handoff_file="$(_stripped_review_handoff)"
      run_driver_in_env "$_outcome_nudge_prompt" "$_outcome_nudge_handoff_file" "resume" "$(printf '%s' "$_handoff" | jq -c '{Invoker}')" || claude_rc=$?
    fi
  fi

  # Only a driver that exited cleanly yet told us nothing gets the synthetic
  # backstop. A non-zero exit propagates untouched -- the launcher's own
  # ClassifyTransient/retry path already owns that case, and forcing exit 0 here
  # would silently turn a retryable transient failure into a terminal synthetic
  # status=blocked (issue #593).
  if [ "$claude_rc" -eq 0 ] && [ -z "$_last_outcome_line" ]; then
    echo "==> driver produced no SPINDRIFT_OUTCOME line — emitting synthetic backstop"
    # Captured, not bare: the PR-intent nudge gate below reads it, and a bare
    # call left it empty, silently skipping the nudge on every backstopped run.
    _last_outcome_line="$(emit_outcome_backstop "$_outcome_gate_resumed")"
    printf '%s\n' "$_last_outcome_line"
    # Remembered for the PR-intent nudge gate below: this run's ready status was
    # manufactured by the backstop, not reported by the driver, so a later crash
    # in the nudge's best-effort resume must not undo the terminal verdict
    # already committed to here.
    _outcome_via_backstop=1
    # A read-only github Box holds no push token, so emit_outcome_backstop could
    # not push $BRANCH itself. Fall through unconditionally to the bundle-out
    # step below, which relays the branch through the outbox seam exactly as a
    # read-only status=ready hand-off does. Not a per-forge branch (ADR 0039):
    # every forge/mode falls through the same way, and bundleout.Run is a safe
    # no-op when there is nothing to relay.
  fi

  # SPINDRIFT_PR_INTENT required-marker gate (issue #2045/#2036): a read-only
  # github Box that reaches status=ready but never printed SPINDRIFT_PR_INTENT
  # leaves the launcher's hostMediateDraftPR with nothing to relay. Scoped to
  # read-only + github + a genuine status=ready -- a blocked/failed run never
  # opens a PR, ready or not via the synthetic backstop above. The scan (the
  # only-before-" note=" matching and the $RUN_NONCE anchoring that tells a
  # genuine attempt apart from a mid-sentence mention), the corrective prompt,
  # the give-up heartbeat op, and the restore-vs-leave-alone decision all live
  # in the driver-exec marker-gate verb -- see its own doc comments for why
  # give-up and restore are independent, separately-gated outcomes.
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
      # gate's resume above -- this resume must stay a narrow single pass too.
      local _pr_intent_nudge_handoff_file
      _pr_intent_nudge_handoff_file="$(_stripped_review_handoff)"
      run_driver_in_env "$_pr_intent_nudge_prompt" "$_pr_intent_nudge_handoff_file" "resume" "$(printf '%s' "$_handoff" | jq -c '{Invoker}')" || claude_rc=$?

      # Both log sentinels were just reassigned by the resume call above, so
      # --log-path scans the resumed pass's own raw log for a genuine PR-intent
      # marker, and --resumed-driver-text-log hands the verb the resumed pass's
      # unwrapped text log so it can scan for a near-miss
      # SPINDRIFT_OUTCOME-shaped line itself.
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

      # A crash in this best-effort nudge must never retroactively undo an
      # already-terminal backstop-declared ready run (issue #593's exit-0
      # guarantee). ForceExitZero fires only when this run's ready status came
      # from the synthetic backstop and this resume itself exited non-zero.
      if [ "$(printf '%s' "$_resolve_json" | jq -r '.force_exit_zero // false')" = "true" ]; then
        echo "==> PR-intent nudge resume failed (rc=$claude_rc) after a backstop-declared ready outcome — staying terminal (issue #593, #2448)"
        claude_rc=0
      fi

      local _pr_intent_giveup_op
      _pr_intent_giveup_op="$(printf '%s' "$_resolve_json" | jq -r '.op_line // empty')"
      if [ -n "$_pr_intent_giveup_op" ]; then
        printf '%s\n' "$_pr_intent_giveup_op"
      fi

      # The resume left no valid outcome of its own, so the earlier
      # genuine/backstop line is still this run's final word for the bundle-out
      # step below -- reprinted into the container log only when the resume
      # shadowed it with a near-miss line of its own (AC5 requires the line stay
      # emitted exactly once). A non-empty $_last_outcome_line here is the
      # resumed pass's own genuine verdict and must be left alone.
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

  # CODE_FORGE=local's harness-owned code-out (ADR 0033): the Harness, not the
  # Agent, bundles the seam after the Driver exits. An empty base..BRANCH range
  # against a claimed status=ready prints a corrective SPINDRIFT_OUTCOME line,
  # picked up by the launcher's own last-line-wins log scan. A read-only
  # CODE_FORGE=github Box is harness-owned code-out too: BOX_WRITE_ENABLED unset
  # means the Box never pushed anything itself. Guarded on !_is_research_kind --
  # a research dispatch never cuts $BRANCH, so a bundle-out attempt would fail
  # resolving it. Deliberately left unguarded under set -e otherwise: a
  # bundle-out failure is a genuine container failure belonging on the
  # launcher's ClassifyTransient/retry path. Reached for any $claude_rc value
  # (ADR 0039): a driver that crashed non-zero can still have left real commits
  # worth relaying, and bundleout.Run is a safe no-op when there is nothing to
  # bundle.
  if ! _is_research_kind \
    && { [ -n "${BOX_HOST_MEDIATED_REMOTE:-}" ] \
      || _is_readonly_outbox_relay; }; then
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
