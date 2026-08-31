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

# Fully-local mode (CODE_FORGE=local AND ISSUE_TRACKER=local) talks to no real
# forge, so REPO_SLUG and GH_TOKEN have nothing to resolve against. The
# launcher's own validate() (cmd/launcher/main.go) already made this call and
# forwards it as BOX_FULLY_LOCAL (issue #2527) -- read it rather than
# re-deriving it from the raw CODE_FORGE/ISSUE_TRACKER names.
fully_local=false
if [ -n "${BOX_FULLY_LOCAL:-}" ]; then
  fully_local=true
fi
# Self-contained research (issue #2202) supplies its content from a local
# issue tracker and clones no repo, so REPO_SLUG/GH_TOKEN have nothing to
# resolve against either -- mirrors the launcher validate()'s noRepoResearch
# permit. SELF_CONTAINED itself stays a raw runtime input (a genuinely
# per-dispatch Dispatch-kind knob, not a nix-resolved capability signal), but
# the local-issue-tracker half is now the forwarded BOX_IN_BOX_UNREACHABLE_TRACKER
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
  export BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"

  # Baked-in locations; overridable only so the harness can be exercised on the
  # host without a container. PROMPTS_DIR moved to the nix-rendered agent-paths
  # preamble (lib/preambles.nix's renderAgentPathsPreamble, issue #2531) --
  # WORK_DIR/REPO_MOUNT_DIR/OUTBOX_DIR are true runtime mount points, not baked
  # artifacts, so they keep their own literal defaults here.
  WORK_DIR="${WORK_DIR:-/work}"
  # REPO_MOUNT_DIR is the read-only Accumulation-repo mount CODE_FORGE=local
  # clones from instead of a network remote (ADR 0033, issue #1697's /repo
  # mount); unused otherwise.
  REPO_MOUNT_DIR="${REPO_MOUNT_DIR:-/repo}"
  # OUTBOX_DIR is the writable mount driver-exec's bundle-out verb writes
  # CODE_FORGE=local's seam bundle into (ADR 0033, issue #1808); unused
  # otherwise.
  OUTBOX_DIR="${OUTBOX_DIR:-/outbox}"
  # REGISTRY_PROXY_SOCKET_PATH is the fixed in-box path the registry proxy's
  # unix socket mount lands at when REGISTRY_PROXY_UPSTREAM_URL is set (ADR
  # 0044, issue #2849) -- mirrors mount.go's registryProxySocketTarget
  # exactly. Overridable only so phase_registry_proxy_bindings can be
  # exercised in bats tests without touching the real host filesystem root.
  REGISTRY_PROXY_SOCKET_PATH="${REGISTRY_PROXY_SOCKET_PATH:-/registry-proxy.sock}"

  # HARNESS_SKILLS_DIR is where harness-owned and build-time
  # Consumer-configured skills are baked (lib/image.nix), a sibling of
  # PROMPTS_DIR. OPERATOR_SKILLS_DIR is where SPINDRIFT_SKILLS_DIR's
  # runtime override is mounted (issue #2489) -- a fixed path distinct
  # from DRIVER_SKILLS_DIR itself, since a mount directly onto
  # DRIVER_SKILLS_DIR would replace its entire contents and hide the
  # harness-owned skill(s) baked at HARNESS_SKILLS_DIR. Both get copied
  # into DRIVER_SKILLS_DIR below instead of being mounted there
  # directly, since copying merges and mounting replaces.
  HARNESS_SKILLS_DIR="${HARNESS_SKILLS_DIR:-/agent/skills}"
  OPERATOR_SKILLS_DIR="${OPERATOR_SKILLS_DIR:-/operator-skills}"

  # HARNESS_HOME_AGENT_DIR is where bwrap.go's homeAgentStagingDir ro-bind
  # stages baked /home/agent content (Claude hooks, settings.json, opencode
  # agent files) read-only (issue #2843). It is a fresh top-level path, not
  # nested under /agent: /agent is already bound read-only by the time that
  # mount is added, and bwrap cannot fabricate a new mountpoint inside an
  # existing read-only bind. The OCI image instead bakes this content
  # directly, writable, at the real /home/agent (lib/image.nix's
  # fakeRootCommands), so under bwrap it must be copied into the real
  # (tmpfs) /home/agent/$HOME at startup instead -- mirrors
  # HARNESS_SKILLS_DIR's copy-not-mount pattern above.
  HARNESS_HOME_AGENT_DIR="${HARNESS_HOME_AGENT_DIR:-/home-agent-staged}"

  # DRIVER_NAME, DRIVER_BIN, DRIVER_FLAGS_COMMON, and DRIVER_SKILLS_DIR are
  # baked by the selected Driver's lib/drivers/<name>.nix registry entry (ADR
  # 0009, issue #624) via the nix-rendered preamble prepended ahead of this
  # file at image build time -- no fallback literal and no runtime guard
  # here; nix/checks/image.nix's driver-preamble-baked-into-image check
  # catches an omitted Driver preamble at build time instead (issue #2531).

  # OUTCOME_CONTRACT_FILE, COMMS_CONTRACT_FILE, CHECK_CONTRACT_FILE,
  # CODE_COMMENTS_CONTRACT_FILE, RESEARCH_OUTCOME_CONTRACT_FILE,
  # PROMPTASSEMBLY_REGISTRY_FILE, PROMPT_CONTRACT_REGISTRY_FILE, and
  # FORBIDDEN_MARKERS_REGISTRY_FILE -- like PROMPTS_DIR above -- are
  # baked by the nix-rendered agent-paths preamble (lib/preambles.nix's
  # renderAgentPathsPreamble); no fallback literal or per-var commentary
  # lives here anymore. See lib/agent-paths.nix for what each path is, why
  # it lives outside PROMPTS_DIR, and which driver-exec verb/flag reads it
  # (issue #2531).

  # _driver_extract_outcome and _driver_session_flags are defined by the Driver
  # registry (lib/drivers/<name>.nix); a nix-built image prepends them via
  # driverPreamble (lib/mkHarness.nix), and the bats harness sources the same
  # registry-rendered bodies via DRIVER_PREAMBLE_FILE (issue #433).
}

# configure_forgejo_cli wires FORGEJO_TOKEN into fj (forgejo-cli) so the
# agent's `fj issue`/`fj pr` commands (ADR-0038-adjacent, issue #1963) run
# non-interactively. A no-op when fj isn't baked (non-forgejo images) or no
# token is set (a read-only Box: see the read-only ISSUE_TRACKER_FORGEJO
# gates in phase_prompt_assembly, which never surface an fj write command in
# that case anyway).
configure_forgejo_cli() {
  command -v fj >/dev/null 2>&1 || return 0
  [ -n "${FORGEJO_TOKEN:-}" ] || return 0
  local _fj_base="${FORGEJO_BASE_URL:-https://codeberg.org}"
  _fj_base="${_fj_base%/}"
  # Strip any trailing slash so the host fj keys the token under (below)
  # matches the host clone_repo derives from the same _fj_base; a
  # FORGEJO_BASE_URL set with a trailing slash would otherwise key the token
  # under a host the stripped git remote never resolves to.
  # fj stores the token in ~/.local/share/forgejo-cli/keys.json keyed by the
  # bare host; the name argument is just a cosmetic label. The token is fed
  # on stdin, not argv, so it never lands in `ps`/argv snooping. Offline --
  # writes the keys file only, no network call. `auth add-key` (NAME
  # positional, token on stdin) is the forgejo-cli 0.5.0 spelling baked into
  # the image; a nixpkgs bump that renames it (add-key -> add-token) must
  # update this call in lockstep, or fj would store GIT_USER_NAME as the token.
  printf '%s' "$FORGEJO_TOKEN" | fj -H "$_fj_base" auth add-key "${GIT_USER_NAME:-spindrift-agent}" >/dev/null
}

# clone_repo authenticates, clones the target repo into WORK_DIR, sets the
# repo-local git identity, and fetches the latest refs.
clone_repo() {
  # CODE_FORGE=local clones from a local filesystem mount, and CODE_FORGE=forgejo
  # clones from and pushes to a Forgejo instance over a FORGEJO_TOKEN-authenticated
  # URL (ADR 0038); neither target is github.com, so gh's github credential helper
  # has nothing to apply -- and running it would fail a forgejo Box that carries no
  # GH_TOKEN (ISSUE_TRACKER=forgejo). Skipping it keeps both paths a genuine
  # no-github-credential-helper guarantee rather than merely "the actual clone
  # happens not to use it."
  case "${CODE_FORGE:-github}" in
  local | forgejo) ;;
  *)
    export GH_TOKEN
    gh auth setup-git
    ;;
  esac

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
  elif [ "${CODE_FORGE:-github}" = "forgejo" ]; then
    # CODE_FORGE=forgejo clones from and pushes to a Forgejo/Gitea instance
    # (ADR 0038), authenticating with FORGEJO_TOKEN carried as the remote URL's
    # userinfo -- the same https://<token>@<host>/<owner>/<repo>.git shape the
    # launcher's forgejoGitRemoteURL builds host-side, so the branch this Box
    # pushes here and the branch the launcher's Merge later clones target one
    # remote.
    : "${FORGEJO_TOKEN:?FORGEJO_TOKEN is required when CODE_FORGE=forgejo}"
    local _fj_base="${FORGEJO_BASE_URL:-https://codeberg.org}"
    _fj_base="${_fj_base%/}"
    CLONE_URL="${_fj_base%%://*}://${FORGEJO_TOKEN}@${_fj_base#*://}/${REPO_SLUG}.git"
  elif [ "${CODE_FORGE:-github}" = "local" ]; then
    CLONE_URL="$REPO_MOUNT_DIR"
    # Under rootless podman the Box's mapped uid never matches the
    # host-owned Accumulation-repo bind mount's uid, so git's
    # dubious-ownership guard rejects $REPO_MOUNT_DIR before the clone
    # copies a single object (#1720). Trust both paths this Box's git
    # commands ever open under CODE_FORGE=local: the mount itself, which
    # git fetch origin below re-opens via local transport after the clone,
    # and WORK_DIR, the clone of it that the rest of the run (identity
    # config, commits, the Seam bundle) operates on. A standing global
    # config entry, not a one-shot flag on the clone command alone, since
    # both paths outlive the clone step. This is the one CODE_FORGE=local
    # exception to #404's empty-global-git-config invariant below; every
    # check suite that asserts that invariant runs a non-local forge, so
    # it stays untouched.
    git config --global --add safe.directory "$REPO_MOUNT_DIR"
    git config --global --add safe.directory "$WORK_DIR"
  fi
  # Redact any embedded userinfo (<token>@) before echoing: for
  # CODE_FORGE=forgejo CLONE_URL always carries FORGEJO_TOKEN as the URL's
  # userinfo (https://<token>@host/...), and CODE_FORGE_REMOTE_URL commonly
  # carries embedded credentials too, so echoing verbatim would leak a
  # secret to Box stdout. This mirrors the Go launcher's
  # forge.RedactURLCredentials: strip "://<userinfo>@" down to "://".
  echo "==> cloning $(printf '%s' "$CLONE_URL" | sed -E 's#://[^/@[:space:]]+@#://#')"
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
  # Configure fj (forgejo-cli) now that the repo-local identity above is set,
  # so GIT_USER_NAME is available for its (cosmetic) key label -- mirrors gh
  # auth setup-git's placement earlier in this function for the github case.
  configure_forgejo_cli
  install_readonly_guards
}

# install_readonly_guards installs both runtime read-only guards -- the git
# push hook (issue #2463) and the command shims for every command-shim argv0
# in the registry, gh and fj (issue #2465, #2509) -- via the
# `driver-exec readonly-guards` verb (issue #2509), the Go successor to this
# function's own former hand-rolled heredocs. The verb renders every guard
# named by the forbiddenMarkers registry (lib/prompt-contract.nix) in one
# pass: today that's exactly one git-hook row (pre-push/pre-receive, blocking
# `git push`), five command-shim rows sharing argv0 "gh" (`gh pr create`,
# `gh pr ready`, `gh pr merge`, `gh issue comment`, `gh issue create`, and
# `gh api` with a mutating method), and five command-shim rows sharing argv0
# "fj" (`fj pr create`, `fj pr ready`, `fj pr merge`, `fj issue comment`,
# `fj issue create`) -- so every rejection message's wording lives in the
# registry, never hand-copied shell this file's own shellcheck sweep
# couldn't reach. readonlyguards.installCommandShims resolves each argv0 on
# PATH before shimming it and skips gracefully when absent, so a Box whose
# image never bakes one of these binaries (a github Box has no `fj`, a
# forgejo Box has no `gh`) is never affected by shimming a binary it
# doesn't have.
#
# The git-hook guard and the command-shim guards are gated separately below,
# via -skip-git-hook (readonlyguards.Config.SkipGitHook) -- only the
# git-hook guard's gate depends on outbox capability. A read-only Box whose
# hand-off IS a real `git push` (BOX_HOST_MEDIATED_REMOTE and
# BOX_OUTBOX_RELAY_CAPABLE both unset -- see lib/backends/default.nix's
# outboxRelayCapable/hostMediatedRemote knobs) must never get that push
# blocked locally, since it has no other way to hand off its work; no
# backend registered today (github, local, forgejo -- the three valid
# CODE_FORGE choices permitted under read-only) actually leaves both unset,
# since issue #2927 gave forgejo OutboxRelayCapable too, but the branch
# stays live for a future backend that lacks it. The command-shim guards
# carry no such risk -- a read-only Box should never be allowed to run `fj
# pr create`/`gh pr create` etc. regardless of how it hands off -- so they
# install unconditionally for every read-only Box.
#
# When the git-hook guard does install (the outbox-capable branch), it
# repoints `origin`'s *push* URL (leaving fetch untouched) at a throwaway
# bare decoy repo -- never $WORK_DIR itself. Every real dispatch push
# (publish_rebased_branch/pushWithRetry) pushes the exact branch name
# phase_branch_recovery already checked out here, so a pushurl pointed at
# $WORK_DIR would make that push resolve as "Everything up-to-date" (the
# destination ref is already at this exact commit) and exit 0 without ever
# invoking a hook -- `--no-verify` or not: a silent fake success, worse than
# the 403 this issue exists to replace, since the agent would believe its
# work landed when it went nowhere. A bare decoy's refs are never the same
# as $WORK_DIR's, so every push to it is a genuine ref update and its
# pre-receive hook -- installed under decoy/hooks by the verb call below,
# see readonlyguards.Config.RepoDir -- always fires, `--no-verify` included.
#
# The git-hook guard is installed in BOTH places, not just the decoy:
# decoy/hooks (via --repo-dir) AND $WORK_DIR/.git/hooks (via
# --extra-repo-dir, see readonlyguards.Config.ExtraRepoDirs). The decoy's
# pre-receive hook only ever fires for a push that actually goes through
# origin's repointed pushurl -- a plain `git push`/`git push origin ...`.
# A push to an explicit URL (`git push https://github.com/owner/repo
# HEAD:b`) or a non-origin remote never touches origin's pushurl at all, so
# the decoy alone never sees it and, without the second install, that push
# would reach the real forge and 403 there -- exactly the round trip issue
# #2463 exists to prevent. $WORK_DIR/.git/hooks/pre-push, installed by the
# --extra-repo-dir call, catches that case: it fires for every push
# attempted directly against $WORK_DIR's own checkout, regardless of
# destination URL or remote name, `--no-verify` included (pre-receive isn't
# bypassable by a client-side flag either way).
install_readonly_guards() {
  if [ -n "${BOX_WRITE_ENABLED:-}" ]; then
    return 0
  fi
  # A deterministic location under $HOME, not a mktemp path: the PATH
  # mutation below is local to this subprocess and never survives back to a
  # caller inspecting the Box after it exits, so the install location has to
  # be predictable from a value the caller already knows instead of
  # discovered. $HOME, not $WORK_DIR's parent: production WORK_DIR is /work,
  # whose parent is the root-owned `/`, while the Box runs as uid 1000 -- so
  # a $WORK_DIR-derived path fails the verb's mkdir with EACCES and `set -e`
  # kills the Box mid-clone. $HOME is /home/agent, which lib/image.nix chowns
  # to that same uid alongside /work.
  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  local -a _rg_args=(
    readonly-guards
    --forbidden-markers-registry "$FORBIDDEN_MARKERS_REGISTRY_FILE"
    --shim-dir "$shim_dir"
  )
  if [ -n "${BOX_HOST_MEDIATED_REMOTE:-}" ] || [ -n "${BOX_OUTBOX_RELAY_CAPABLE:-}" ]; then
    # A path outside $WORK_DIR (never inside the clone, so it never shows up
    # in `git status`/`git add -A`) that every push now targets instead of
    # the real remote -- see the function comment above for why. Created as
    # a real bare repo (not just a bare path) so the local-filesystem
    # transport's cheap, non-network ref listing succeeds and the
    # pre-receive hook fires.
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
      publish_rebased_branch "$BRANCH" || {
        echo "==> publishing reset branch failed on $BRANCH; concurrent Box may be ahead"
        exit 1
      }
    fi
  else
    git checkout -b "$BRANCH" "origin/${BASE_BRANCH:-}"
  fi
}

# publish_rebased_branch lands a just-rebased branch so a later step never
# sees the stale pre-rebase state: the agent's own incremental push in
# read-write mode, or CONFLICT_RESOLVE_PR_URL's no-agent exit in read-only
# mode, where this is the only chance to land it at all. A read-only Box
# holds no push-capable token (issue #1979: a direct force-push here 403s
# before the agent ever runs), so it relays via the outbox bundle instead --
# the same BOX_WRITE_ENABLED fail-closed gate the OPEN A PULL REQUEST
# contract's own outbox step uses (open-pr-push-outbox.md, issue #1918).
# Delegates the bundle itself to driver-exec's bundle-out verb (issue #1808)
# rather than a second hand-rolled `git bundle create` -- the same
# empty-range-is-a-no-op guard and outbox filename this Box's post-driver
# CODE_FORGE=local bundle-out call below already share, one implementation
# instead of two that could drift.
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
    publish_rebased_branch "$BRANCH" || {
      echo "==> publishing rebased branch failed after pre-work rebase on $BRANCH"
      exit 1
    }
  fi
}

# REGISTRY_PROXY_FORWARDER_PORT is the fixed localhost TCP port the
# in-Box Forwarder (spawned by `driver-exec bind-registry`'s bindings mode,
# ADR 0044, issue #2849) listens on, forwarding to the registry proxy's
# mounted unix socket. Mirrors
# mount.go's registryProxySocketTarget: an implementation-internal contract
# between this phase and the CARGO_* binding it sets up below, not a
# user-facing knob. Chosen to collide with nothing else this harness or a
# typical Target devShell binds (unlike the common 3000/8000/8080 dev-server
# range).
REGISTRY_PROXY_FORWARDER_PORT="${REGISTRY_PROXY_FORWARDER_PORT:-27182}"

# phase_registry_proxy_bindings ensures the in-Box Forwarder (ADR 0044, issue
# #2849) is up and wires cargo/npm/pnpm/yarn/Go at it, via `driver-exec
# bind-registry`'s bindings mode (ADR 0044, ADR 0036 amendment #6, issue
# #2931) rather than inline bash -- the spawn/readiness/env-rendering logic
# this replaced now has real Go unit tests instead of a bats suite driving a
# real socat process per case. A silent no-op when REGISTRY_PROXY_SOCKET_PATH
# isn't mounted (the registry proxy is off by default). See
# bindregistry.CargoConfigTOML/bindregistry.NpmFamilyBindings's own doc
# comments in cmd/launcher/internal/bindregistry/registrybindings.go for the
# cargo table-valued-config and npm env-precedence reasoning behind exactly
# what gets bound and how.
# Called from main() right after configure_env, before the
# _is_self_contained branch and thus before clone_repo, phase_prefetch,
# phase_devshell_probe, or any driver invocation -- every place a cargo
# build or npm install could first happen.
phase_registry_proxy_bindings() {
  local _bindings_env_out _bind_registry_rc=0 _source_rc=0
  # A verb failure here (mktemp, unwritable output path, verb crash) must
  # never take the whole box run down -- mirrors phase_toolchain_nudge's own
  # defensive rc-capture, minus its PREFETCH gate: unlike that cosmetic hint,
  # these bindings apply unconditionally, so their own failure warnings are
  # never suppressed either.
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

  # rc-captured rather than left to fail straight into set -euo pipefail's
  # errexit -- see phase_toolchain_nudge's matching comment for why an
  # unguarded `source` here would abort the whole entrypoint mid-phase.
  # shellcheck disable=SC1090  # dynamic path (tempfile), sourced by design: the verb's own env-file output
  source "$_bindings_env_out" || _source_rc=$?
  if [ "$_source_rc" -ne 0 ]; then
    echo "==> WARNING: sourcing driver-exec bind-registry's env output failed (exit ${_source_rc}) — skipping registry proxy bindings"
    return 0
  fi
}

# intree_binding_apply wraps `driver-exec bind-registry`'s in-tree apply mode
# for every ecosystem row's in-tree config rewrite under $WORK_DIR (cargo,
# npm, yarn, pnpm -- bindregistry.InTreeBindings()) -- the actual rewrite
# logic lives in the Go engine (ApplyInTreeBinding,
# cmd/launcher/internal/bindregistry/intreebinding.go; see its own doc
# comments for the cargo#5416/ADR 0044 rationale and the crash-recovery
# convergence behavior). Called twice, both from main() below: once right
# after clone_repo returns, and once again after the
# phase_branch_recovery/phase_prework_rebase re-apply pass, so a single
# warning (with exit code, unlike the two former inline call sites which
# dropped it) covers both. On failure, also runs
# intree_binding_revert as best-effort cleanup (issue #2932): otherwise a
# failed apply's partial state (rewritten-but-untracked content, or a stray
# skip-worktree bit) would sit on disk until some later, conditional cleanup
# path happened to run.
intree_binding_apply() {
  local _intree_apply_rc=0 _cargo_bindings_env_out _source_rc=0
  # A verb failure here (mktemp, unwritable output path, verb crash) must
  # never take the whole box run down -- mirrors phase_registry_proxy_bindings'
  # own defensive rc-capture above.
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

  # rc-captured rather than left to fail straight into set -euo pipefail's
  # errexit -- see phase_registry_proxy_bindings's matching comment for why
  # an unguarded `source` here would abort the whole entrypoint mid-phase.
  # shellcheck disable=SC1090  # dynamic path (tempfile), sourced by design: the verb's own env-file output
  source "$_cargo_bindings_env_out" || _source_rc=$?
  if [ "$_source_rc" -ne 0 ]; then
    echo "==> WARNING: sourcing driver-exec bind-registry's cargo placeholder env output failed (exit ${_source_rc}) — skipping cargo registry placeholder bindings"
    return 0
  fi
}

# intree_binding_revert wraps `driver-exec bind-registry`'s in-tree revert
# mode, undoing intree_binding_apply's rewrite (see RevertInTreeBinding in
# cmd/launcher/internal/bindregistry/intreebinding.go for the Go-side
# mechanics). Called from main()'s branch-recovery/prework-rebase re-apply
# dance and from phase_conflict_resolve's rebase-abort path -- see that call
# site's own comment for the one case (`.cargo/config.toml` itself among the
# unmerged conflicting paths) where the revert legitimately fails and this
# warns rather than aborting.
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
  # Cold-run toolchain nudge: classification now delegates to `driver-exec
  # bind-registry` (issue #2930), which reads the shared ecosystem table
  # (cmd/launcher/internal/registryproxy) instead of this function
  # re-deriving its own lockfile chain. The verb runs unconditionally --
  # not gated on PREFETCH or the registry proxy's own on/off state -- so the
  # env file is always available; only the hint's own emission stays gated
  # on PREFETCH, same as before.
  local _nudge_env_out _bind_registry_rc=0 _source_rc=0 _nudge_ecosystem=""
  # This phase is cosmetic-hint-only -- a verb failure (mktemp, unwritable
  # output path) must not take the whole box run down under
  # set -euo pipefail. Bail before sourcing a possibly-missing/garbage env
  # file rather than letting the failure propagate (mirrors
  # phase_devshell_probe's own rc-capture a few functions below). mktemp
  # itself is wrapped in the same guard: it runs the same "verb tooling"
  # this phase must survive losing.
  if ! _nudge_env_out="$(mktemp)"; then
    if [ -z "${PREFETCH:-}" ]; then
      echo "==> WARNING: mktemp failed — skipping toolchain nudge"
    fi
    return 0
  fi

  # Registered as soon as mktemp has actually produced a path, so every exit
  # out of this function from here on -- an early `return 0` below or
  # falling off the end -- removes the tempfile once, instead of repeating
  # an `rm -f` at each return site. The handler unsets itself the moment it
  # fires: a bash RETURN trap is a process-global setting, not truly
  # function-scoped, so leaving it registered would fire again on the next
  # unrelated function's return anywhere later in this script and dereference
  # a `local` that no longer exists.
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

  # rc-captured rather than left to fail straight into set -euo pipefail's
  # errexit: an unguarded `source` failure (e.g. a malformed env file) would
  # abort the whole entrypoint script mid-phase, which the RETURN trap above
  # can never clean up after -- errexit unwinds the process without
  # "returning" from any function on the way out.
  # shellcheck disable=SC1090  # dynamic path (tempfile), sourced by design: the verb's own env-file output
  source "$_nudge_env_out" || _source_rc=$?
  if [ "$_source_rc" -ne 0 ]; then
    if [ -z "${PREFETCH:-}" ]; then
      echo "==> WARNING: sourcing driver-exec bind-registry's env output failed (exit ${_source_rc}) — skipping toolchain nudge"
    fi
    return 0
  fi
  # Captured into a local, underscore-prefixed variable and unset immediately:
  # every sibling local in this function (and phase_devshell_probe right
  # below) is underscore-prefixed precisely so phase-local state never
  # outlives the phase -- NUDGE_ECOSYSTEM is the env file's own on-disk
  # variable name (cmd/launcher/driver-exec/bindregistry_cmd.go), not this
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
# advise-only research kind (ADR 0022, issue #640) rather than work/fix; the
# default is work, so an unset DISPATCH_KIND is never mistaken for research.
_is_research_kind() {
  [ "${DISPATCH_KIND:-work}" = "research" ]
}

# _is_self_contained reports (via exit status) whether this is the research
# kind's no-repo sub-mode (issue #2202): the launcher forwards SELF_CONTAINED=1,
# so the Box clones no repo and explores none. Unset defaults to off, so every
# repo-backed dispatch is unaffected.
_is_self_contained() {
  [ "${SELF_CONTAINED:-}" = "1" ]
}

# _is_readonly_outbox_relay reports (via exit status) whether this is a
# read-only, outbox-relay-capable Box: BOX_WRITE_ENABLED is unset (no
# push-capable token was ever issued, so a force-push can only 403) and the
# Box's backend is outbox-relay-capable per lib/backends/default.nix's
# registry -- github and forgejo both set outboxRelayCapable = true there
# (issue #2927 closed the asymmetry) -- forwarded as
# BOX_OUTBOX_RELAY_CAPABLE, issue #2267/#2527, rather than compared against
# the raw CODE_FORGE name here. Such a Box hands its branch off through the
# harness-owned outbox bundle seam rather than a push (issue #2094).
_is_readonly_outbox_relay() {
  [ -z "${BOX_WRITE_ENABLED:-}" ] && [ -n "${BOX_OUTBOX_RELAY_CAPABLE:-}" ]
}

# _handoff_field extracts field $2 from the raw Handoff descriptor JSON $1
# (phase_prompt_assembly's driver-exec assemble-prompt call produces it,
# issue #2355), defaulting to empty when the field is absent or null -- the
# `jq -r ".$2 // empty"` shape every call site below already used inline
# before this was pulled out as one shared helper.
_handoff_field() {
  printf '%s' "$1" | jq -r ".$2 // empty"
}

# _populate_driver_skills_dir copies HARNESS_SKILLS_DIR and OPERATOR_SKILLS_DIR
# into DRIVER_SKILLS_DIR, by copying rather than mounting (issue #2489):
# baked skills first, then any operator override layered on top by name, so
# an operator-supplied skill wins on a name collision but a harness-owned
# skill the operator didn't override survives. This is the one seam that
# makes a skill actually invocable -- the driver discovers skills only at
# DRIVER_SKILLS_DIR (e.g. Claude Code's $HOME/.claude/skills), never at
# HARNESS_SKILLS_DIR/OPERATOR_SKILLS_DIR directly -- so every call site that
# spawns a driver invocation whose prompt may reference a skill must run this
# first. Called once in main() before phase_conflict_resolve (issue #2706:
# that phase's own driver invocation and both its early-exit paths otherwise
# ran ahead of phase_prompt_assembly, the only other call site, leaving
# DRIVER_SKILLS_DIR unpopulated for a prompt that told the agent to invoke a
# skill from it), and again at the top of phase_prompt_assembly so that
# function stays self-contained if it's ever invoked in isolation -- cheap
# and idempotent, just an mkdir -p and two conditional `cp -r`s.
_populate_driver_skills_dir() {
  mkdir -p "$DRIVER_SKILLS_DIR"
  if [ -d "$HARNESS_SKILLS_DIR" ]; then
    cp -r "$HARNESS_SKILLS_DIR"/. "$DRIVER_SKILLS_DIR"/
  fi
  if [ -d "$OPERATOR_SKILLS_DIR" ]; then
    cp -r "$OPERATOR_SKILLS_DIR"/. "$DRIVER_SKILLS_DIR"/
  fi
}

# _populate_home_agent_files copies HARNESS_HOME_AGENT_DIR's staged content
# (issue #2843) into the real $HOME, by copying rather than mounting -- the
# same copy-not-mount reasoning as _populate_driver_skills_dir above. Guarded
# on [ -d "$HARNESS_HOME_AGENT_DIR" ], a no-op under the OCI runner: OCI never
# creates that path, since lib/image.nix already bakes /home/agent directly,
# writable, at the real location -- this must not fight or duplicate that.
# Under bwrap, bwrap.go ro-binds the same baked content at
# HARNESS_HOME_AGENT_DIR, a distinct source from the real (tmpfs) $HOME, so it
# must be copied in before anything reads it from $HOME. The follow-up chmod
# is required, not optional: a plain `cp -r` from a read-only source
# (bwrap's ro-bind, or a Nix store path) preserves the source's read-only
# mode bits, the same subtlety lib/image.nix's own fix hit (commit
# 8961b62e) -- without it, a hook or settings.json copied in here would land
# unwritable under $HOME.
#
# Every plain FILE enumerated under HARNESS_HOME_AGENT_DIR gets an
# unconditional `chmod u+w` on its mirrored $HOME path -- that only needs
# the file's own write bit, never its parent directory's, so it's always
# safe (round 3's review, issue #2843).
#
# A DIRECTORY is writable-by-default too, for the same reason: the box
# needs to create new content under arbitrary copied-in directories -- e.g.
# `gh` doing `mkdir ~/.config/gh` inside a copied-in (otherwise read-only)
# ~/.config -- and there's no way to enumerate ahead of time which
# directories will need that. An earlier round of this fix instead used a
# narrow DRIVER_AGENT_FILES_DIR-only allowlist, which under-covered this:
# every OTHER copied-in directory (~/.config, ~/.claude/hooks, ...) kept
# the Nix store's read-only r-xr-xr-x mode, which was the actual "opencode
# driver dispatch fails under bwrap" bug this issue is about.
#
# There is exactly ONE directory that must be excluded from this
# writable-by-default treatment: lib/image.nix (300-303) pre-creates an
# EMPTY placeholder directory inside the baked home/agent tree whenever the
# driver declares sessionCacheDirRelative (e.g. home/agent/.claude/projects
# for the claude driver) -- the exact same HOME-relative path bwrap also
# uses as the live --bind (read-write) mount target for the Driver's
# session-cache dir, a directory that lives on the HOST filesystem
# (cmd/launcher/internal/runner/mount.go). bwrap.go ro-binds the whole
# baked tree, placeholder included, at HARNESS_HOME_AGENT_DIR, so a naive
# per-path chmod loop -- even a non-recursive one that never uses `chmod
# -R` -- still walks into that placeholder path and lands directly on the
# HOST bind-mount's root directory, mutating its permission bits: a
# container process reaching out and modifying a directory outside the
# sandbox (and, under `set -euo pipefail`, a chmod failure there aborts box
# startup entirely).
#
# So every enumerated directory is chmod'd EXCEPT when its $HOME-relative
# target is exactly $DRIVER_SESSION_CACHE_DIR (guarded on it being
# set/non-empty) -- the env var lib/drivers/default.nix's renderPreamble
# exports into entrypoint.sh's environment whenever the selected driver
# declares sessionCacheDirRelative (e.g. /home/agent/.claude/projects for
# the claude driver; unset entirely for opencode, which declares no
# session-cache dir). This naturally makes DRIVER_AGENT_FILES_DIR itself
# get chmod'd too, since it's never the session-cache dir, so it needs no
# allowlist entry of its own anymore: it's the one directory that
# genuinely needs directory-level write access from this copy-in --
# cmd/launcher/internal/promptassembly/assemble.go's rewriteAgentFiles does
# `os.Remove` on a file inside it when the orchestrator is on, and removing
# a file needs write+execute on its *containing* directory, not just the
# file.
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
# under "$1" -- a directory holding a SKILL.md (<dir>/<name>/SKILL.md), never
# a flat <name>.md file, matches how Claude Code itself discovers a skill.
# Shared by phase_prompt_assembly and phase_conflict_resolve's precompute
# block below, which each build a SKILLS_FOUND directive off this same shape.
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
# _rebase_and_publish. Called from main(), right before phase_prompt_assembly
# (issue #2354 slice 3 hoisted the call site out of phase_prompt_assembly),
# so its two early-exit paths (CONFLICT_RESOLVE_PR_URL's `exit 0` and the
# unresolvable-conflict `exit 1`) fire before the driver-exec assemble-prompt
# verb is ever invoked, instead of after it wastefully ran and its output was
# discarded.
phase_conflict_resolve() {
  # When the pre-work rebase produced conflicts, spawn a conflict-resolve agent to
  # re-map the branch onto current main.  Only escalate to exit 1 if the agent
  # genuinely cannot resolve.
  if [ -n "${_had_rebase_conflict:-}" ]; then
    echo "==> pre-work rebase conflict detected — invoking conflict-resolve agent"
    # Precompute the CAVEMAN_STEP/SKILL_PREAMBLE fragments conflict-resolve-
    # prompt.md references (issue #2706): unlike phase_prompt_assembly, this
    # prompt renders through the bash-only `_subst` path below, not the
    # driver-exec assemble-prompt verb, so nothing else ever populates these
    # two vars for this call site -- left unset, `_subst`'s `${!v:-}`
    # indirect expansion would substitute them as permanently empty
    # regardless of whether caveman is baked. Scans DRIVER_SKILLS_DIR (main()
    # populates it via _populate_driver_skills_dir before this phase runs
    # now), not HARNESS_SKILLS_DIR alone, so an operator-supplied skill
    # override shows up here too, exactly like phase_prompt_assembly's own
    # scan below. Shadows CAVEMAN_STEP/SKILL_PREAMBLE the same way
    # _use_dev_shell is shadowed just below -- `local` vars in this
    # function's own scope, visible to `_subst` via bash dynamic scoping
    # (issue #515).
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
    # Unlike its two siblings above, CODE_COMMENTS_STEP is set unconditionally
    # -- it's not gated on caveman/skills being baked, because resolving a
    # rebase conflict always edits code, so the comment-discipline rule
    # (issue #2880) always applies here (reuses lib/fragments.nix's
    # already-registered code-comments.md var/fragment).
    local CODE_COMMENTS_STEP
    # shellcheck disable=SC2034 # consumed by _subst's envsubst allowlist via ${!v:-} indirection
    CODE_COMMENTS_STEP="$(_subst "${PROMPTS_DIR}/fragments/code-comments.md")"$'\n\n'
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
    run_driver_in_env "$_cr_prompt" "" "" "" || true
    if [ -d ".git/rebase-merge" ] || [ -d ".git/rebase-apply" ]; then
      # Defensive best-effort revert before the abort below re-checks out
      # HEAD (ADR 0044, issue #2851). In the rare case where one of the four
      # in-tree config files (.cargo/config.toml, .npmrc, .yarnrc.yml,
      # pnpm-workspace.yaml) is itself one of the unmerged conflicting
      # paths, this revert's `git checkout --` fails outright for that path
      # -- git refuses to check out an unmerged path -- so
      # intree_binding_revert prints its own warning here. Harmless: the
      # `git rebase --abort` right below cleans up regardless, so no state
      # is left corrupted, but don't mistake the warning for a real problem
      # in that specific case.
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
# assemble-prompt verb (ADR 0036, ADR 0007's thin-exec-glue tier, issue
# #2354): rather than bash computing every fragment gate, selecting the base
# prompt, and rewriting the roster inline, this collects the handful of facts
# only bash can supply (filesystem-derived skill discovery at DRIVER_SKILLS_DIR)
# and forwards every already-available env knob as a flag, then reads back the
# three files the verb writes. cmd/launcher/internal/promptassembly (issues
# #2349-#2353) owns the gate/fragment/base-prompt/roster-injection logic that
# used to live here -- this function is now the same bare-`driver-exec`-on-
# $PATH exec-glue shape publish_rebased_branch/emit_outcome_backstop already
# use for their own verbs, just with three output files instead of stdout.
# phase_conflict_resolve now runs from main(), before this function is ever
# called (issue #2354 slice 3), so its early exits skip the verb call
# entirely rather than discarding its output. This function sets prompt,
# _handoff (the raw Handoff descriptor JSON string, read for
# SessionMode/Invoker at each call site), and _handoff_file -- the on-disk
# path to that same descriptor, which run_driver_in_env hands the invoker as
# --handoff-file by default (issue #2975): the whole driver/model/effort/
# argv-shape/devshell/caps fact set now lives inside that file, sourced by
# driver-exec/orchestrator themselves rather than rebuilt into per-call flags
# (issue #2355 drained _driver_session_mode/review_prompt_rendered/
# review_model_rendered onto the descriptor; #2975 drained the remaining ~20
# invocation flags onto it too). A required-marker gate's corrective resume
# instead hands run_driver_in_env its own throwaway ReviewPromptFile-stripped
# copy via that call's $2 override, leaving this file untouched.
phase_prompt_assembly() {
  # Discover available skills at DRIVER_SKILLS_DIR and build a directive to
  # prefer them over the inline guidance where they apply -- filesystem I/O
  # only bash can do; the verb takes the result as its --skills-found flag.
  # Claude Code discovers a skill as a directory holding a SKILL.md
  # (DRIVER_SKILLS_DIR/<name>/SKILL.md), never a flat <name>.md file, so the
  # skill name advertised in SKILLS_FOUND is the directory basename.
  # main() already calls _populate_driver_skills_dir before phase_conflict_resolve
  # (issue #2706); call it again here so this function stays self-contained
  # if it's ever invoked in isolation (e.g. tests) -- cheap and idempotent.
  _populate_driver_skills_dir

  local SKILLS_FOUND
  SKILLS_FOUND="$(_scan_skills_found "$DRIVER_SKILLS_DIR")"

  # Build the assemble-prompt invocation. What remains here is paths, the
  # skill-baked probes, and Handoff passthrough (issue #2975, out of scope
  # for #2979) -- every other Box env var assembleprompt_cmd.go now reads
  # straight off os.Getenv itself (promptassembly.EnvFromEnviron, issue
  # #2979), so this array no longer forwards it as a flag.
  # Boolean gates ride bare flags (`flag.Bool` on the verb side), appended
  # only when true; the verb's own zero-value default covers the rest, so an
  # unset knob here is indistinguishable from an explicit off.
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
    # Driver-invocation passthrough (issue #2975): every fact run_driver_in_env
    # used to rebuild into ~20 per-call CLI flags now rides the Handoff
    # descriptor instead, sourced once here from the same env knobs and
    # forwarded to assemble-prompt, which layers them onto the handoff JSON
    # verbatim (assembleprompt_cmd.go, pure passthrough). driver-exec/
    # orchestrator read them straight off the handoff file at run time.
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
  # Driver-invocation passthrough gates (issue #2975), bare:
  # DRIVER_ARGV_MODEL_OMIT_EMPTY is the Driver registry's own model-slot gate,
  # and --devshell/--devshell-name mirror the devShell wrapping
  # run_driver_in_env's own _devshell_flags block used to build per-call --
  # both now baked into the handoff once here. _use_dev_shell is main's
  # cross-phase sentinel (phase_devshell_probe/self-contained both set it
  # before this phase), read via dynamic scoping the same way the rest of
  # this run's phases read it.
  [ -n "${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}" ] && _ap_args+=(--argv-model-omit-empty)
  [ "$_use_dev_shell" = "1" ] && _ap_args+=(--devshell --devshell-name "${DEV_SHELL_NAME:-default}")

  local _prompt_out _agents_out _handoff_out _review_prompt_out
  _prompt_out="$(mktemp)"
  _agents_out="$(mktemp)"
  _handoff_out="$(mktemp)"
  _review_prompt_out="$(mktemp)"

  # Bare `driver-exec`, resolved via $PATH -- the same convention
  # publish_rebased_branch's `driver-exec bundle-out` and
  # emit_outcome_backstop's `driver-exec outcome-backstop` calls already use:
  # the real in-box binary in prod, the bats fake's own assemble-prompt
  # branch (which itself execs the real Go binary, not a bash
  # reimplementation) under test. A nonzero exit here propagates straight
  # through `set -euo pipefail` at the top of this script -- no explicit
  # error handling needed, mirroring emit_outcome_backstop's own bare-call
  # shape.
  driver-exec assemble-prompt "${_ap_args[@]}" \
    --prompt-output "$_prompt_out" \
    --agents-json-output "$_agents_out" \
    --handoff-output "$_handoff_out" \
    --review-prompt-output "$_review_prompt_out"

  # $(...) strips the file's trailing newline exactly the way it always
  # stripped a command substitution's -- Assemble already trims one from its
  # own Prompt output the same way the old `$(_subst ...)` chain did, so this
  # stays byte-identical. $_agents_out itself is never read back into a bash
  # string (issue #2975 removed the last reader, run_driver_in_env's own $2):
  # it survives on disk untouched below because it IS the file Handoff.
  # AgentsFile points to, read by driver-exec/orchestrator directly off
  # --handoff-file.
  prompt="$(cat "$_prompt_out")"
  # _handoff is assigned here by plain (non-local) assignment, not `local`,
  # so it escapes to run_driver_in_env and the required-marker gates via
  # main's cross-phase sentinel -- the same dynamic-scoping shape _use_dev_shell
  # already uses (issue #515) -- since those callers run outside this
  # function's own call frame. Issue #2355 drains run_driver_in_env's
  # session-mode/invoker/review-prompt/review-model derivation onto this raw
  # descriptor directly, read at each call site, rather than pre-extracting
  # each field into its own cross-phase sentinel (_driver_session_mode/
  # review_prompt_rendered/review_model_rendered, all retired). ORCHESTRATOR
  # (issue #2047, ADR 0035 amendment) is the one exception, deliberately left
  # untouched here -- see the ORCHESTRATOR/Handoff.Invoker equivalence note
  # in main, near its early ORCHESTRATOR computation (line ~1185).
  _handoff="$(cat "$_handoff_out")"
  # _handoff_file is assigned by plain (non-local) assignment, like _handoff
  # above, so it escapes this function's own call frame to every
  # run_driver_in_env call this run makes (main's implement pass plus the two
  # required-marker corrective resumes) -- each now hands the invoker
  # --handoff-file "$_handoff_file" as its single static-config input (issue
  # #2975). The handoff file itself, and the agents/review-prompt files it
  # references by path, therefore must survive for the rest of the run:
  # driver-exec reads Handoff.AgentsFile at driver-invocation time
  # (buildDriverArgs) and the orchestrator reads Handoff.ReviewPromptFile at
  # review-pass time, both after this function returns -- so only _prompt_out
  # is removed here (run_driver_in_env always writes its own per-call
  # --prompt-file, so the handoff's PromptFile fallback is never read).
  _handoff_file="$_handoff_out"
  # Test-only hook (issue #2395 slice 1): when a bats test has exported
  # DRIVER_HANDOFF_FILE (tests/helper.bash), persist the raw Handoff JSON
  # there for golden-fixture diffing in tests/prompt-assembly-parity.bats.
  # Unlike prompt/agents, which tests/fakes/claude captures from inside the
  # fake Driver, no fake ever receives SessionMode/Invoker as CLI args, so
  # this raw JSON is the only place a test can observe them. A no-op in
  # production, where this var is never set.
  [ -n "${DRIVER_HANDOFF_FILE:-}" ] && cp "$_handoff_out" "$DRIVER_HANDOFF_FILE"
  rm -f "$_prompt_out"
  # Test-only hook, same shape/reasoning as DRIVER_HANDOFF_FILE just above:
  # once the cleanup below removes $_review_prompt_out on a no-render cell,
  # nothing in production ever surfaces that path again, so a bats test
  # proving the removal happened has no other way to learn it. A no-op in
  # production, where this var is never set.
  [ -n "${DRIVER_REVIEW_PROMPT_TMP_FILE:-}" ] && printf '%s' "$_review_prompt_out" > "$DRIVER_REVIEW_PROMPT_TMP_FILE"
  # $_review_prompt_out is only ever written to when Assemble actually
  # rendered a review prompt (orchestrator on, default fresh-work dispatch,
  # FixPass == 0 -- Handoff.ReviewPromptFile's own doc comment); every other
  # cell (research, FixPass, orchestrator off, ...) leaves it empty and
  # nothing else in this run references its path, unlike $_agents_out/
  # $_handoff_out above which the rest of the run reads by path regardless of
  # cell. Remove it here rather than leaving an empty temp file to leak for
  # the life of the Box. An `if`, not a bare `[ ... ] &&` (this is the last
  # statement in the function): a false `&&` left-hand side would make this
  # the function's own nonzero return value, and phase_prompt_assembly is
  # called as a bare statement in main -- `set -e` would abort the whole run
  # on every cell that renders no review prompt.
  if [ -z "$(printf '%s' "$_handoff" | jq -r '.ReviewPromptFile')" ]; then
    rm -f "$_review_prompt_out"
  fi
}

# _write_env_handoff writes, to path $1, a minimal Handoff descriptor JSON
# built straight from the same env knobs phase_prompt_assembly forwards to
# assemble-prompt as passthrough flags (issue #2975). It exists solely for the
# one Driver pass that runs before phase_prompt_assembly and so has no
# assemble-prompt-written handoff file yet: phase_conflict_resolve's pre-work
# rebase fixup. driver-exec/orchestrator both require --handoff-file and read
# every driver/model/effort/argv-shape/devshell/caps fact off it, so that pass
# cannot run without one. Only the fields a driver-exec-direct (or, under
# $ORCHESTRATOR, orchestrator) invocation actually consults are passed; the
# roster (AgentsFile), review fields, and PromptFile are deliberately left off
# -- the conflict-resolve pass provisions no subagents, runs no review pass,
# and is always handed an explicit --prompt-file -- so they unmarshal to their
# zero values on load. Devshell reads _use_dev_shell via dynamic scoping, the
# same cross-phase sentinel every other phase reads (phase_conflict_resolve
# shadows it to 0 for its own call, matching that pass's always-outside-the-
# devShell behavior).
#
# Delegates to `driver-exec env-handoff` (issue #2975 slice 3) instead of
# hand-restating the Handoff/ArgvShape/Caps grammar in a `jq -n` blob here:
# the real promptassembly.Handoff struct is the single source of truth for
# that shape now, and the Go flag parses MAX_BUDGET_TOKENS/MAX_BUDGET_USD
# leniently (degrading a malformed value to 0) rather than requiring valid
# JSON the way the old `jq --argjson` call did -- a malformed value there
# used to fail jq itself and, under entrypoint.sh's `set -euo pipefail`, kill
# the whole box run before this pass ever finished (blocking review finding
# A).
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

# run_driver_in_env runs the Driver against $1 (the assembled prompt), with
# $2 (an optional handoff-file override, issue #2975: "" uses the shared
# $_handoff_file below unchanged, same as every call site did before this arg
# existed; a non-empty value is used as the handoff file for this call only,
# leaving $_handoff_file itself untouched for any later call. Each
# required-marker gate's corrective resume passes its own throwaway copy of
# the shared handoff with ReviewPromptFile cleared, so the nudge-and-retry
# reaches the invoker as a narrow single pass rather than re-entering the
# full implement/review/fix loop a second time -- issue #2065's original
# design decision, restored here after a prior slice of this same issue's own
# work accidentally dropped it when the roster moved off this same arg), $3
# (session mode, forwarded verbatim to the
# nix-supplied _driver_session_flags — "initial"/"resume" pin or resume the
# issue's session id; any other value, e.g. "" for the conflict-resolve pass,
# yields no session flags), and $4 (the raw Handoff descriptor JSON string
# phase_prompt_assembly's driver-exec assemble-prompt call produced, or "" for
# the one pass that predates it -- phase_conflict_resolve's conflict-resolve
# call, which runs before any Handoff exists; the corrective resume each
# required-marker gate fires narrows $4 to `{"Invoker": ...}` only). $4 is used
# only to derive the invoker fork (Invoker field) at this call site; every
# other driver/model/effort/argv-shape/devshell/caps fact now lives inside the
# Handoff FILE ($2's override when given, else $_handoff_file,
# phase_prompt_assembly's cross-phase sentinel), read by driver-exec/
# orchestrator themselves rather than rebuilt into ~20 per-call CLI flags here
# (issue #2975 drained them all onto the file).
#
# The one pass that runs before phase_prompt_assembly exists -- and so has no
# $_handoff_file yet -- is phase_conflict_resolve's pre-work rebase fixup; it
# gets a minimal env-derived handoff synthesized here (_write_env_handoff),
# since driver-exec/orchestrator both require --handoff-file and cannot run
# without one. That pass's $4 is also "", so its invoker fork falls back to
# $ORCHESTRATOR, main's early ORCHESTRATOR_ENABLED-derived cross-phase sentinel
# -- see the ORCHESTRATOR/Handoff.Invoker equivalence note in main.
#
# Delegates to driver-exec (issue #626), the in-box Go unit that owns "run the
# Driver, optionally inside the Project devShell" as one code path: it takes
# the prompt/session as file paths (a compiled binary crosses the devShell
# process boundary with a plain argv), spawns the Driver directly or via `nix
# develop --command` when the handoff's Devshell field is set, tees the stream
# to a log path, filters heartbeats in-process, and returns the Driver's exit
# status.
#
# The invoker fork (default off, issue #1996; canonicalized #2047) swaps which
# binary receives that same --handoff-file/--prompt-file/--session-file/
# --log-path set: off takes the direct driver-exec path; on hands the same
# invocation to the in-box Go orchestrator, which forwards the handoff to
# driver-exec itself for each of its own passes. Neither branch changes the
# tiny flag set below -- this is the reusable seam the orchestrator drives.
run_driver_in_env() {
  local prompt="$1" handoff_file_override="$2" session_mode="$3" handoff_json="${4:-}"

  # An unrecognized session_mode (e.g. "" for the conflict-resolve pass, which
  # pins/resumes no session) falls through _driver_session_flags' case with no
  # output, so the session file below ends up empty — same as before.
  local _driver_session_flags_rendered
  _driver_session_flags_rendered="$(_driver_session_flags "$session_mode")"

  # The prompt/session data crosses into driver-exec as file paths -- a
  # compiled binary, unlike the devShell wrapper, needs no quoting-hazard
  # workaround for the prompt. The roster is no longer written here: it rides
  # the Handoff descriptor's AgentsFile field (already on disk at the path
  # assemble-prompt recorded there), read by buildDriverArgs straight off
  # --handoff-file.
  local _prompt_file _session_file stream_log
  _prompt_file="$(mktemp)"
  printf '%s' "$prompt" > "$_prompt_file"
  _session_file="$(mktemp)"
  printf '%s' "$_driver_session_flags_rendered" > "$_session_file"

  # stream_log is driver-exec's teed copy of the Driver's raw stdout, read
  # below by _driver_extract_outcome -- the launcher's own capture of stdout
  # (.spindrift/logs/issue-<n>.log, byte-exact, unchanged) is separate and
  # untouched. It also survives this call (see _last_stream_log below): the
  # SPINDRIFT_PR_INTENT required-marker gate in main() scans it later, via
  # the driver-exec marker-gate verb's own --log-path.
  stream_log="$(mktemp)"

  # $_handoff_file is phase_prompt_assembly's cross-phase sentinel, read via
  # dynamic scoping like _use_dev_shell/_handoff. $handoff_file_override
  # (issue #2975), when non-empty, wins over it for this call only -- the
  # required-marker gates' corrective resumes use this to hand the invoker
  # their own ReviewPromptFile-stripped copy instead of the shared handoff.
  # The single pass that runs before phase_prompt_assembly (phase_conflict_
  # resolve's pre-Handoff fixup) finds neither set, so synthesize a minimal
  # handoff from the same env knobs assemble-prompt would have received --
  # driver-exec/orchestrator require --handoff-file and cannot run without
  # one.
  local _run_handoff_file="${handoff_file_override:-${_handoff_file:-}}" _synthesized_handoff=""
  if [ -z "$_run_handoff_file" ]; then
    _run_handoff_file="$(mktemp)"
    _synthesized_handoff="$_run_handoff_file"
    _write_env_handoff "$_run_handoff_file"
  fi

  # Invoker comes from handoff_json's own Invoker field when a Handoff exists
  # (issue #2355); the one pass with no Handoff yet (phase_conflict_resolve's
  # pre-Handoff call) falls back to $ORCHESTRATOR, main's early
  # ORCHESTRATOR_ENABLED-derived cross-phase sentinel -- mathematically
  # identical to what the Handoff's own Invoker field would say once one
  # existed.
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
  # Driver's raw transcript format buries it (claude wraps it in a stream-json
  # result event); _driver_extract_outcome surfaces it as a bare line so that
  # contract is unchanged. Captured (rather than left to print directly) so
  # main's post-return backstop (issue #593) can tell whether the Driver
  # actually emitted one.
  _last_outcome_line="$(_driver_extract_outcome "$stream_log")"
  # Left on disk and remembered by dynamic-scoping assignment: main()'s
  # SPINDRIFT_PR_INTENT gate reads this path later, well after this call
  # returns, and hands it to driver-exec marker-gate's own --log-path so
  # the verb scans it itself (outcome.LastPRIntentInLog) rather than
  # re-implementing that grammar in bash. This Box exits after one run (see
  # _stripped_review_handoff's own doc comment above), so an undeleted
  # per-pass stream log surviving to process exit is not a real leak.
  _last_stream_log="$stream_log"
  # The plain-text, unwrapped Driver result -- no landing=/status=
  # classification, just the jq-unwrapped/markdown-stripped text. Written to
  # a fresh temp file and remembered the same dynamically-scoped way as
  # _last_stream_log above (issue #2978): main()'s SPINDRIFT_OUTCOME gate
  # hands this path to driver-exec marker-gate's own --log-path so the verb
  # scans for the marker itself, rather than main() pre-extracting a
  # near-miss line for it in bash.
  _last_driver_text_log="$(mktemp)"
  _driver_extract_result_text "$stream_log" > "$_last_driver_text_log"
  if [ -n "$_last_outcome_line" ]; then
    printf '%s\n' "$_last_outcome_line"
  fi

  return "$claude_rc"
}

# _stripped_review_handoff writes a throwaway copy of $_handoff_file with
# ReviewPromptFile cleared and prints its path -- shared by both
# required-marker gates' corrective-resume call sites in main (issue #2975),
# each of which hands the printed path to its own run_driver_in_env call as
# the handoff-file override, so that nudge-and-retry reaches the invoker as a
# narrow single pass rather than re-entering the full implement/review/fix
# loop a second time from whatever cap or park stopped the first attempt
# (issue #2065's original design decision). Only called from main, after
# phase_prompt_assembly has already set $_handoff_file, so no unset guard is
# needed here. Like $_handoff_file itself (see the "only _prompt_out is
# removed here" comment above _write_env_handoff), the printed file is left
# on disk rather than deleted once its own run_driver_in_env call returns:
# this Box exits after one run, so a single throwaway temp file surviving to
# process exit is not a real leak, and leaving it in place lets test/debug
# tooling inspect it after the fact (issue #2975).
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
# The whole backstop decision -- research short-circuit, dirty-tree salvage,
# no-work / CODE_FORGE=local / read-only-github-relay / writable-push-with-
# retry branching, and the single synthetic status=blocked SPINDRIFT_OUTCOME
# line -- now lives in the driver-exec `outcome-backstop` verb (issue #2157,
# ADR 0036). This function is just linear exec glue: it hands the verb the
# inputs it needs and lets it own the decision. BOX_HOST_MEDIATED_REMOTE and
# BOX_OUTBOX_RELAY_CAPABLE are the two backend-capability facts that decision
# keys off (issue #2267) -- forwarded here as presence flags the launcher's
# dispatch.buildBoxEnv already resolved host-side from the backend registry,
# not re-derived in-box from CODE_FORGE's name.
emit_outcome_backstop() {
  local _recovery="${1:-}"
  # --run-state-file is a fixed path, not forwarded from any launcher-set
  # env var: it mirrors the orchestrator's own --state-file default
  # (issue #1997), the same fixed path the orchestrator process running
  # alongside the Driver already writes its run-state handoff artifact to.
  # A missing file -- a non-orchestrator run, or a run that never reached a
  # review pass -- is handled by the backstop's own graceful degrade
  # (issue #2459), not by anything here.
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
  # Initialized empty here, unlike the sibling vars above (which are always
  # assigned unconditionally by run_driver_in_env before any read) -- this
  # one is only ever assigned inside the backstop `if` block below, and
  # `set -u` treats a bare `local x` with no value as unbound, not empty
  # (issue #2448 finding 3).
  local _outcome_via_backstop=""
  local ORCHESTRATOR

  configure_env

  # phase_registry_proxy_bindings must run before any phase that could first
  # invoke a cargo/npm/pnpm/yarn/Go/Gradle build (clone_repo's devShell/
  # prefetch phases below, the driver itself) -- see its own doc comment for
  # why this exact placement. Gradle's own binding (writing
  # $GRADLE_USER_HOME/init.d/spindrift-registry-proxy.init.gradle) is written
  # by the same `driver-exec bind-registry` call this phase already makes
  # (bindregistry.GradleInitScript, issue #2934), not a separate phase.
  phase_registry_proxy_bindings

  # ORCHESTRATOR (issue #2047, ADR 0035 amendment; issue #2354 slice 3 hoisted
  # phase_conflict_resolve here, ahead of phase_prompt_assembly): the single
  # canonical master-switch gate, computed exactly once from the raw
  # ORCHESTRATOR_ENABLED env var -- the orchestrator-fork-well-formed check
  # (nix/checks/prompts.nix) pins this as the one non-comment
  # ORCHESTRATOR_ENABLED test allowed in this file; every fork downstream
  # (phase_prompt_assembly's --orchestrator-enabled flag, and
  # run_driver_in_env's own pre-Handoff invoker fallback) reads this one
  # computed value instead of re-testing the env var. Computed here, before
  # phase_conflict_resolve, so that pass's run_driver_in_env call -- which
  # predates any Handoff, so it cannot read Invoker off one -- has a value to
  # fall back to. Issue #2355 retired phase_prompt_assembly's own
  # Handoff-sourced reassignment of this same variable: Handoff.Invoker ==
  # "orchestrator" iff ORCHESTRATOR_ENABLED is set (gates.go's
  # g["ORCHESTRATOR"] = e.OrchestratorEnabled), so the two were always
  # mathematically identical -- this one computation is $ORCHESTRATOR's value
  # for main()'s entire run, not just an early placeholder.
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
    # Cargo/npm/pnpm/yarn-berry's in-tree binding all run here, right after
    # clone_repo, via intree_binding_apply's `driver-exec bind-registry`
    # in-tree mode -- the Go engine's row table covers all four ecosystems
    # (issue #2932, issue #2933). See the revert/re-apply dance around
    # phase_branch_recovery/phase_prework_rebase just below.
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
  # phase_conflict_resolve runs before phase_prompt_assembly (issue #2354
  # slice 3), unconditionally for every dispatch kind -- self-contained,
  # research, and fresh work alike -- exactly as it did nested inside
  # phase_prompt_assembly before this hoist. Its two early-exit paths
  # (CONFLICT_RESOLVE_PR_URL's resolve-only dispatch, and an unresolvable
  # pre-work rebase conflict) now skip the driver-exec assemble-prompt verb
  # call entirely instead of running it and discarding the result.
  #
  # _populate_driver_skills_dir must run before phase_conflict_resolve, not
  # just before phase_prompt_assembly (issue #2706): phase_conflict_resolve's
  # own driver invocation, and both of its early-exit paths, otherwise ran
  # ahead of the only other call site that populated DRIVER_SKILLS_DIR,
  # leaving a conflict-resolve prompt that tells the agent to invoke a skill
  # (e.g. `/caveman`) structurally unable to find it.
  _populate_driver_skills_dir
  # _populate_home_agent_files runs alongside _populate_driver_skills_dir, at
  # the same early position, for the same reason (issue #2843): under bwrap,
  # phase_conflict_resolve's own driver invocation and both its early-exit
  # paths need the baked hooks/settings.json/opencode agent files already
  # copied into $HOME by the time they run, and phase_prompt_assembly's
  # driver-exec assemble-prompt rewrites .config/opencode/agents/*.md in
  # place under $HOME per DRIVER_AGENT_FILES_DIR, which also needs the real
  # files to exist under $HOME by then. A no-op under the OCI runner (see the
  # function's own doc comment).
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

  # SPINDRIFT_OUTCOME required-marker gate (issue #1607/#2044, verb-owned
  # decision issue #2511; verb-owned scan issue #2978): a Driver pass that
  # exits cleanly but leaves the marker missing or unparseable most often
  # just ended its turn early (issue #1542) rather than actually failing, so
  # resume the same pinned session exactly once with a corrective nudge
  # before any backstop runs. A research dispatch pins no session worth
  # resuming (ADR 0022); a non-zero exit is the launcher's own
  # ClassifyTransient/retry path to handle (issue #593) -- neither reaches
  # this gate. The nudge prompt itself, including the near-miss-quoting
  # variant (issue #1900), is rendered by the driver-exec marker-gate verb
  # (cmd/launcher/internal/markergate), not hand-typed here -- and, as of
  # issue #2978, the verb also decides whether to nudge at all
  # (should_nudge), scanning $_last_driver_text_log itself via
  # outcome.LastFieldedOutcomeLine/outcome.LastNearMissOutcomeLine rather
  # than main() pre-extracting a near-miss line for it in bash.
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
      # Issue #2975: hand this corrective resume its own ReviewPromptFile-
      # stripped handoff, not the shared $_handoff_file the main pass above
      # just used -- otherwise the resume re-enters the full
      # implement/review/fix loop a second time under the orchestrator
      # (issue #2065).
      local _outcome_nudge_handoff_file
      _outcome_nudge_handoff_file="$(_stripped_review_handoff)"
      run_driver_in_env "$_outcome_nudge_prompt" "$_outcome_nudge_handoff_file" "resume" "$(printf '%s' "$_handoff" | jq -c '{Invoker}')" || claude_rc=$?
    fi
  fi

  # Only a driver that exited cleanly yet told us nothing gets the synthetic
  # backstop. A non-zero exit is left to propagate untouched -- the
  # launcher's own ClassifyTransient/retry path (cmd/launcher/internal/dispatch)
  # already owns that case, and only runs when the container's own exit code
  # is non-zero; forcing exit 0 here would silently turn a retryable
  # transient failure into a terminal synthetic status=blocked (issue #593).
  if [ "$claude_rc" -eq 0 ] && [ -z "$_last_outcome_line" ]; then
    echo "==> driver produced no SPINDRIFT_OUTCOME line — emitting synthetic backstop"
    # Capture into $_last_outcome_line (issue #2448): the PR-intent nudge gate
    # below reads it, and a bare `emit_outcome_backstop` left it empty,
    # silently skipping the nudge on every backstopped run.
    _last_outcome_line="$(emit_outcome_backstop "$_outcome_gate_resumed")"
    printf '%s\n' "$_last_outcome_line"
    # Remembered for the PR-intent nudge gate below (issue #2448 finding 3):
    # this run's ready status was manufactured by the backstop, not reported
    # by the driver itself, so a later crash in the nudge's own best-effort
    # resume must not be allowed to undo the terminal verdict already
    # committed to here.
    _outcome_via_backstop=1
    # A read-only github Box (BOX_WRITE_ENABLED unset) holds no push token, so
    # emit_outcome_backstop could not push $BRANCH itself. Fall through
    # unconditionally to the harness-owned bundle-out step below, which
    # relays the branch through the outbox seam.bundle exactly as a
    # read-only status=ready hand-off does (issue #2094). This is no longer
    # a per-forge branch (ADR 0039 slice S1, issue #2252): every forge/mode
    # -- writable github, git, local, and read-only github alike -- falls
    # through the same way to the single exit at the bottom of main(), after
    # bundle-out has had a chance to run. bundleout.Run is a safe no-op when
    # there is nothing to relay (no commits, or a prior line that already
    # claimed something other than ready), so nothing further is needed here.
  fi

  # SPINDRIFT_PR_INTENT required-marker gate (issue #2045/#2036, verb-owned
  # decision issue #2511; verb-owned scan issue #2978): a read-only github
  # Box that reaches status=ready but never printed SPINDRIFT_PR_INTENT
  # leaves the launcher's hostMediateDraftPR with nothing to relay. Scoped to
  # read-only + github + a genuine status=ready (a blocked/failed run never
  # opens a PR, ready or not via the synthetic backstop above -- issue
  # #2448). The scan for a genuine SPINDRIFT_PR_INTENT line -- including the
  # only-before-" note="-field matching and the $RUN_NONCE anchoring that
  # tells a genuine attempt apart from a mid-sentence mention (issue #1937's
  # reasoning) -- is no longer reimplemented here: the driver-exec
  # marker-gate verb's own nudge phase decides whether to fire at all
  # (should_nudge), scanning $_last_stream_log itself via
  # outcome.LastPRIntentInLog (cmd/launcher/internal/markergate and
  # cmd/launcher/internal/outcome). The corrective prompt, the give-up
  # heartbeat op, and the restore-vs-leave-alone decision are all
  # rendered/decided by that same verb -- see its own doc comments for why
  # give-up and restore are independent, separately-gated outcomes rather
  # than a strict three-way switch.
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
      # Issue #2975: same ReviewPromptFile-stripped handoff override as the
      # SPINDRIFT_OUTCOME gate's resume above -- this corrective resume must
      # stay a narrow single pass too (issue #2065).
      local _pr_intent_nudge_handoff_file
      _pr_intent_nudge_handoff_file="$(_stripped_review_handoff)"
      run_driver_in_env "$_pr_intent_nudge_prompt" "$_pr_intent_nudge_handoff_file" "resume" "$(printf '%s' "$_handoff" | jq -c '{Invoker}')" || claude_rc=$?

      # $_last_stream_log and $_last_driver_text_log were both just
      # reassigned by the resume call above (their own dynamic-scoping
      # assignments in run_driver_in_env), so --log-path scans the resumed
      # pass's own raw log for a genuine PR-intent marker, and
      # --resumed-driver-text-log hands the resumed pass's own unwrapped
      # text log to the verb so it can scan for a near-miss
      # SPINDRIFT_OUTCOME-shaped line itself (outcome.LastNearMissOutcomeLine)
      # rather than main() pre-extracting one in bash.
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

      # issue #2448 finding 3: a crash in this best-effort nudge must never
      # retroactively undo an already-terminal backstop-declared ready run
      # (issue #593's exit-0 guarantee for a cleanly-exited, no-outcome
      # driver). ForceExitZero fires only when this run's ready status came
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

      # Restore bookkeeping (issue #2448 finding 2): the resume left no
      # valid outcome of its own, so the earlier genuine/backstop line is
      # still this run's final word for the bundle-out step below --
      # reprinted into the container log only when the resume actually
      # shadowed it with a near-miss line of its own (nothing to reprint
      # otherwise -- AC5 requires the line stay emitted exactly once). A
      # non-empty $_last_outcome_line here is the resumed pass's own
      # genuine, differently-shaped verdict and must be left completely
      # alone.
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

  # CODE_FORGE=local's harness-owned code-out (ADR 0033, issue #1808): the
  # Harness, not the Agent, bundles the seam after the Driver exits. An empty
  # base..BRANCH range against a claimed status=ready prints a corrective
  # SPINDRIFT_OUTCOME line, picked up by the launcher's own last-line-wins
  # log scan with no launcher changes. A read-only CODE_FORGE=github Box
  # (issue #2082) is harness-owned code-out too, for the same reason:
  # BOX_WRITE_ENABLED unset means the Box never pushed anything itself, so
  # nothing else bundles the seam. Same read-only detection as the
  # PR-intent nudge gate above. Guarded on !_is_research_kind, mirroring the
  # branch-recovery/rebase gate in main() above: a research dispatch never
  # cuts $BRANCH at all, so a bundle-out attempt there would fail resolving
  # it, not just find it empty. Deliberately left unguarded under set -e
  # otherwise: a bundle-out failure (e.g. a transient git error) is a
  # genuine container failure, not a judgment call, so it belongs on the
  # launcher's own ClassifyTransient/retry path like any other non-zero
  # exit here, rather than a caught-and-noted best-effort step. Reached for
  # any $claude_rc value, zero or non-zero (ADR 0039 slice S1, issue #2252):
  # a driver that crashed non-zero can still have left real commits on
  # $BRANCH worth relaying, and bundleout.Run is a safe no-op when there is
  # nothing to bundle.
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
