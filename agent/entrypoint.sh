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
  # IN_PROGRESS_LABEL, COMPLETE_LABEL, DEV_SHELL_NAME and DEV_SHELL_PROBE_TIMEOUT
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

  # The Driver registry (lib/drivers/<name>.nix) renders the DRIVER_* variables
  # and the _driver_extract_* helpers; a nix-built image prepends them via
  # driverPreamble (lib/mkHarness.nix), and the bats harness sources the same
  # bytes via DRIVER_PREAMBLE_FILE (issue #433). Session flags are rendered in
  # Go by the Driver strategy's SessionFlags.
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
# ADR 0044 rationale in ApplyInTreeBinding). The same call renders
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
# _use_dev_shell (read by phase_prefetch and main's box exec) and
# _harness_path (read by phase_prefetch only; driver-exec handles the Driver's
# devShell PATH, issue #626).
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
      # The wrapper evals $PREFETCH so shell constructs in the hook are
      # interpreted; like the non-devShell path, it runs in a child bash and
      # failures are non-fatal. $PATH and $PREFETCH stay literal in the
      # generated script.
      # shellcheck disable=SC2016
      printf '#!/bin/bash\nexport PATH="%s:$PATH"\neval "$PREFETCH"\n' \
        "$_harness_path" > "$_pf_wrapper"
      chmod +x "$_pf_wrapper"
      # Prefetch failures are non-fatal, so the nix exit status is ignored.
      WORK_DIR="$WORK_DIR" nix develop ".#${DEV_SHELL_NAME:-default}" --command bash "$_pf_wrapper" || true
      rm -f "$_pf_wrapper"
    else
      # A child bash, not eval (a failure/cd/set/exit would hit this shell) nor
      # a ( subshell ) (inherits -u/pipefail) -- issue #3943.
      WORK_DIR="$WORK_DIR" bash -c "$PREFETCH" || true
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

# _populate_driver_skills_dir copies HARNESS_SKILLS_DIR then OPERATOR_SKILLS_DIR
# into DRIVER_SKILLS_DIR (issue #2489), so an operator skill wins a name
# collision but a harness skill the operator didn't override survives. The driver
# discovers skills only at DRIVER_SKILLS_DIR, so every call site that spawns a
# driver invocation naming a skill must run this first. Idempotent (issue #2706)
# only because each cp is followed by chmod -R u+w: cp -r carries a read-only
# source's mode bits (bwrap ro-binds /agent from the Nix store), so an unwritable
# copy would break the very next cp over it (issue #3941). A blanket chmod -R is safe here, unlike in _populate_home_agent_files:
# nothing is bind-mounted under DRIVER_SKILLS_DIR.
_populate_driver_skills_dir() {
  local _src
  mkdir -p "$DRIVER_SKILLS_DIR"
  for _src in "$HARNESS_SKILLS_DIR" "$OPERATOR_SKILLS_DIR"; do
    [ -d "$_src" ] || continue
    cp -r "$_src"/. "$DRIVER_SKILLS_DIR"/
    chmod -R u+w "$DRIVER_SKILLS_DIR"
  done
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
      # on failure, abort box startup under set -e. assertShape now rejects a
      # malformed sessionCacheDirRelative at eval time (#3348); the trailing-
      # slash strip here stays as defense in depth.
      if [ -d "$_src" ] && [ -n "${DRIVER_SESSION_CACHE_DIR:-}" ] && [ "${_target%/}" = "${DRIVER_SESSION_CACHE_DIR%/}" ]; then
        continue
      fi
      chmod u+w "$_target"
    done < <(find "$HARNESS_HOME_AGENT_DIR" -mindepth 1)
  fi
}

# _scan_skills_found echoes a comma-joined list of skill names under "$1". A
# skill is a directory holding a SKILL.md, never a flat <name>.md file, matching
# how Claude Code itself discovers one. Used by phase_conflict_resolve; box
# scans in Go.
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
# main() calls it before box assembles the prompt (issue #2354) so its two
# early-exit paths fire before assembly runs for nothing.
phase_conflict_resolve() {
  # Escalate to exit 1 only when the agent genuinely cannot resolve.
  if [ -n "${_had_rebase_conflict:-}" ]; then
    echo "==> pre-work rebase conflict detected — invoking conflict-resolve agent"
    # Precompute the fragments conflict-resolve-prompt.md references (issue
    # #2706): this prompt renders through the bash-only `_subst` path, not the
    # box's prompt assembly, so nothing else populates these vars; left unset,
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
    local _cr_prompt
    _cr_prompt="$(_subst "${PROMPTS_DIR}/conflict-resolve-prompt.md")"
    # No session to pin or resume for this pass (empty session file), and its
    # exit status is not checked: success is read off the rebase state below.
    # Shadows _use_dev_shell to 0 for the synthesized handoff only (dynamic
    # scoping, issue #515), since only the main run enters the devShell.
    local _use_dev_shell=0
    local _cr_prompt_file _cr_session_file _cr_log _cr_handoff
    _cr_prompt_file="$(mktemp)"
    printf '%s' "$_cr_prompt" > "$_cr_prompt_file"
    _cr_session_file="$(mktemp)"
    _cr_log="$(mktemp)"
    _cr_handoff="$(mktemp)"
    _write_env_handoff "$_cr_handoff"
    local -a _cr_argv=(
      --handoff-file "$_cr_handoff"
      --prompt-file "$_cr_prompt_file"
      --session-file "$_cr_session_file"
      --log-path "$_cr_log"
    )
    # manifest.json here must match passmanifest.FileName.
    if _needs_outbox; then
      _cr_argv+=(--manifest-path "$OUTBOX_DIR/manifest.json")
    fi
    orchestrator "${_cr_argv[@]}" || true
    rm -f "$_cr_prompt_file" "$_cr_session_file" "$_cr_log" "$_cr_handoff"
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

# _write_env_handoff writes a minimal Handoff descriptor JSON to $1 for the one
# Driver pass that runs before box assembles the prompt and so has no handoff file
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
    --issue "${ISSUE_NUMBER:-}" \
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
  local _use_dev_shell _harness_path
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

  # Must run before any phase that could first invoke a cargo/npm/pnpm/yarn/Go/
  # Gradle build. Gradle's own binding is written by the same `driver-exec
  # bind-registry` call this phase already makes (issue #2934), not a separate
  # phase.
  phase_registry_proxy_bindings

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
    # In-tree binding runs right after clone_repo; the Go engine's in-tree
    # rows cover npm, yarn and pnpm, cargo as intree_binding_apply's header
    # says (issues #2932, #2933). See the revert/re-apply dance around
    # phase_branch_recovery/phase_prework_rebase just below.
    intree_binding_apply
    # An advise-only dispatch (research today, ADR 0022, issue #640) explores
    # the clone but never lands code: no branch to cut, adopt, or rebase, so
    # no dance either.
    if ! _is_advise_only; then
      intree_binding_revert
      phase_branch_recovery
      phase_prework_rebase
      intree_binding_apply
    fi
    phase_toolchain_nudge
    phase_devshell_probe
    phase_prefetch
  fi
  # phase_conflict_resolve runs before box assembles the prompt (issue #2354)
  # so its two early exits skip assembly entirely.
  # _populate_driver_skills_dir must run before it too (issue #2706): that
  # phase's driver invocation and both early-exit paths otherwise ran ahead of
  # the skills copy, leaving a prompt naming a skill unable to find it. box
  # probes the same directory for the baked skills.
  _populate_driver_skills_dir
  # _populate_home_agent_files runs at the same early position for the same
  # reason (issue #2843): under bwrap the baked hooks, settings.json, and
  # opencode agent files must already be in $HOME before phase_conflict_resolve
  # runs and before box's prompt assembly rewrites them in place. A no-op under
  # OCI.
  _populate_home_agent_files
  phase_conflict_resolve

  # box assembles the prompt, then runs the first Driver run and everything
  # after it: the required-marker nudges, the synthetic outcome backstop, the
  # already-resolved demotion, the lockfile scan and bundle-out, and it exits
  # with the run's exit code (ADR 0058, issues #4292, #4293, #4294). The flags
  # carry the shell-local values assembly needs: exporting them would put them
  # in the Driver's environment. Every value rides a flag, bools as explicit
  # 0/1, because box rejects a missing flag instead of defaulting it.
  local _model_omit_empty=0 _devshell=0 _devshell_name=default
  [ -z "${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}" ] || _model_omit_empty=1
  # The name rides the handoff only for a devShell run, as the verb defaulted it.
  if [ "$_use_dev_shell" = "1" ]; then
    _devshell=1
    _devshell_name="${DEV_SHELL_NAME:-default}"
  fi
  exec box \
    --work-dir "$WORK_DIR" \
    --outbox-dir "$OUTBOX_DIR" \
    --registry "$PROMPTASSEMBLY_REGISTRY_FILE" \
    --validate-markers-registry "$PROMPT_CONTRACT_REGISTRY_FILE" \
    --driver-skills-dir "$DRIVER_SKILLS_DIR" \
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
    --devshell="$_devshell" \
    --devshell-name "$_devshell_name"
}

main "$@"
