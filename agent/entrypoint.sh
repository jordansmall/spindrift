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
[ -n "${BOX_FULLY_LOCAL:-}" ] && fully_local=true
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
[ "$fully_local" = true ] || [ "$no_repo" = true ] || : "${REPO_SLUG:?REPO_SLUG (owner/repo) is required}"
: "${ISSUE_NUMBER:?ISSUE_NUMBER is required}"
[ "$fully_local" = true ] || [ "$no_repo" = true ] || : "${GH_TOKEN:?GH_TOKEN is required}"
: "${GIT_USER_NAME:?GIT_USER_NAME is required}"
: "${GIT_USER_EMAIL:?GIT_USER_EMAIL is required}"

# BEGIN GENERATED OUTCOME STATUS WORDS -- nix run .#regen -- DO NOT EDIT
# shellcheck disable=SC2034 # consumed by _subst's envsubst allowlist, wired in a later slice (issue #2504)
RESEARCH_STATUS_ENUM="recommend|reject|unclear"
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
  BRANCH="${BRANCH_PREFIX:-}${ISSUE_NUMBER}"

  # Baked-in locations; overridable only so the harness can be exercised on the
  # host without a container.
  WORK_DIR="${WORK_DIR:-/work}"
  PROMPTS_DIR="${PROMPTS_DIR:-/agent/prompts}"
  # REPO_MOUNT_DIR is the read-only Accumulation-repo mount CODE_FORGE=local
  # clones from instead of a network remote (ADR 0033, issue #1697's /repo
  # mount); unused otherwise.
  REPO_MOUNT_DIR="${REPO_MOUNT_DIR:-/repo}"
  # OUTBOX_DIR is the writable mount driver-exec's bundle-out verb writes
  # CODE_FORGE=local's seam bundle into (ADR 0033, issue #1808); unused
  # otherwise.
  OUTBOX_DIR="${OUTBOX_DIR:-/outbox}"

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

  # DRIVER_NAME, DRIVER_BIN, DRIVER_FLAGS_COMMON, and DRIVER_SKILLS_DIR are
  # baked by the selected Driver's lib/drivers/<name>.nix registry entry (ADR
  # 0009, issue #624) via the nix-rendered preamble prepended ahead of this
  # file at image build time. No fallback literal lives here: a Box built
  # without that preamble dies loudly instead of silently impersonating the
  # claude Driver. Checked ahead of phase_prompt_assembly's driver-exec
  # assemble-prompt call (issue #2354) so a Box missing every nix-rendered
  # preamble dies naming the Driver preamble, not some unrelated failure deep
  # inside the verb (issue #2246).
  : "${DRIVER_BIN:?DRIVER_BIN not set -- the nix-rendered Driver preamble did not run}"
  : "${DRIVER_FLAGS_COMMON:?DRIVER_FLAGS_COMMON not set -- the nix-rendered Driver preamble did not run}"
  : "${DRIVER_SKILLS_DIR:?DRIVER_SKILLS_DIR not set -- the nix-rendered Driver preamble did not run}"
  : "${DRIVER_NAME:?DRIVER_NAME not set -- the nix-rendered Driver preamble did not run}"

  # The canonical SPINDRIFT_OUTCOME contract (issue #419), baked at a sibling
  # path to /agent/prompts so a SPINDRIFT_PROMPT_DIR mount -- which shadows only
  # /agent/prompts -- never hides it (issue #420).
  # Only the file-path default lives here; the driver-exec assemble-prompt
  # verb (issue #2354) reads the marker straight off the contract file's own
  # first line (injectSharedBlock, cmd/launcher/internal/promptassembly) so
  # it cannot drift from the block's canonical source-file heading.
  OUTCOME_CONTRACT_FILE="${OUTCOME_CONTRACT_FILE:-/agent/outcome-contract.md}"

  # The COMMS and CHECK/COMMIT blocks fix-prompt.md shares with issue-prompt.md
  # (issue #455 extends #419/#420's slice mechanism beyond the outcome contract):
  # baked and injected the same way, so a SPINDRIFT_PROMPT_DIR override of the
  # fix prompt gets the identical treatment.
  COMMS_CONTRACT_FILE="${COMMS_CONTRACT_FILE:-/agent/comms-contract.md}"
  CHECK_CONTRACT_FILE="${CHECK_CONTRACT_FILE:-/agent/check-contract.md}"

  # The research dispatch kind's own harness-owned outcome contract (ADR 0022,
  # issue #640): posting the verdict comment and emitting the outcome line.
  # Baked and injected the same way as the work contract above, so a
  # SPINDRIFT_PROMPT_DIR override of research-prompt.md gets it too.
  RESEARCH_OUTCOME_CONTRACT_FILE="${RESEARCH_OUTCOME_CONTRACT_FILE:-/agent/research-outcome-contract.md}"

  # The Conditional fragment registry as JSON (issue #622, #2354), baked at
  # the same sibling-of-/agent/prompts path as the contract files above, for
  # the `driver-exec assemble-prompt` verb's `--registry` flag.
  PROMPTASSEMBLY_REGISTRY_FILE="${PROMPTASSEMBLY_REGISTRY_FILE:-/agent/fragments-registry.json}"

  # lib/prompt-contract.nix's validateMarkers list as JSON (issue #2356),
  # baked at the same sibling-of-/agent/prompts path as the contract files
  # above, for the `driver-exec assemble-prompt` verb's
  # `--validate-markers-registry` flag.
  PROMPT_CONTRACT_REGISTRY_FILE="${PROMPT_CONTRACT_REGISTRY_FILE:-/agent/prompt-contract-registry.json}"

  # lib/prompt-contract.nix's forbiddenMarkers list as JSON (issue #2464),
  # baked at the same sibling-of-/agent/prompts path as the contract files
  # above, for the `driver-exec readonly-guards` verb's
  # `--forbidden-markers-registry` flag (issue #2513: assemble-prompt no
  # longer takes this flag).
  FORBIDDEN_MARKERS_REGISTRY_FILE="${FORBIDDEN_MARKERS_REGISTRY_FILE:-/agent/forbidden-markers-registry.json}"

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
# BOX_OUTBOX_RELAY_CAPABLE both unset, e.g. forgejo -- see
# cmd/launcher/backend.go's outboxRelayCapable knob) must never get that
# push blocked locally, since it has no other way to hand off its work; the
# command-shim guards carry no such risk -- a read-only Box should never be
# allowed to run `fj pr create`/`gh pr create` etc. regardless of how it
# hands off -- so they install unconditionally for every read-only Box.
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

# _is_readonly_github reports (via exit status) whether this is a read-only
# github Box: BOX_WRITE_ENABLED is unset (no push-capable token was ever
# issued, so a force-push can only 403) and the Box is outbox-relay-capable
# (today, true only for CODE_FORGE=github, per lib/backends/default.nix's
# registry -- forwarded as BOX_OUTBOX_RELAY_CAPABLE, issue #2267/#2527,
# rather than compared against the raw CODE_FORGE name here). Such a Box
# hands its branch off through the harness-owned outbox bundle seam rather
# than a push (issue #2094).
_is_readonly_github() {
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
# entirely rather than discarding its output. This function still sets
# prompt, agents_json, and _handoff (the raw Handoff descriptor JSON) --
# run_driver_in_env and the required-marker gates' corrective resumes now
# read session mode/invoker/review-prompt/review-model straight off $_handoff at
# their own call sites instead of from separately-extracted sentinels (issue
# #2355 drained _driver_session_mode/review_prompt_rendered/
# review_model_rendered onto the descriptor itself).
phase_prompt_assembly() {
  # Discover available skills at DRIVER_SKILLS_DIR and build a directive to
  # prefer them over the inline guidance where they apply -- filesystem I/O
  # only bash can do; the verb takes the result as its --skills-found flag.
  # Claude Code discovers a skill as a directory holding a SKILL.md
  # (DRIVER_SKILLS_DIR/<name>/SKILL.md), never a flat <name>.md file, so the
  # skill name advertised in SKILLS_FOUND is the directory basename.
  # Populate DRIVER_SKILLS_DIR by copying, not mounting (issue #2489):
  # baked skills first, then any operator override layered on top by
  # name, so an operator-supplied skill wins on a name collision but a
  # harness-owned skill the operator didn't override survives.
  mkdir -p "$DRIVER_SKILLS_DIR"
  if [ -d "$HARNESS_SKILLS_DIR" ]; then
    cp -r "$HARNESS_SKILLS_DIR"/. "$DRIVER_SKILLS_DIR"/
  fi
  if [ -d "$OPERATOR_SKILLS_DIR" ]; then
    cp -r "$OPERATOR_SKILLS_DIR"/. "$DRIVER_SKILLS_DIR"/
  fi

  local SKILLS_FOUND=""
  if [ -d "$DRIVER_SKILLS_DIR" ]; then
    local _sf _sn
    for _sf in "${DRIVER_SKILLS_DIR}/"*/SKILL.md; do
      [ -f "$_sf" ] || continue
      _sn="$(basename "$(dirname "$_sf")")"
      SKILLS_FOUND="${SKILLS_FOUND:+${SKILLS_FOUND}, }${_sn}"
    done
  fi

  # Build the assemble-prompt invocation. Every string flag maps 1:1 onto a
  # bash env var this Box already carries -- no gate computation here
  # anymore, just forwarding (assembleprompt_cmd.go re-derives every gate the
  # old inline precompute/fragment-loop/roster-injection blocks used to).
  # Boolean gates ride bare flags (`flag.Bool` on the verb side), appended
  # only when true; the verb's own zero-value default covers the rest, so an
  # unset knob here is indistinguishable from an explicit off.
  local -a _ap_args=(
    --registry "$PROMPTASSEMBLY_REGISTRY_FILE"
    --validate-markers-registry "$PROMPT_CONTRACT_REGISTRY_FILE"
    --agents-json-template "${AGENTS_JSON_TEMPLATE:-}"
    --issue-tracker "${ISSUE_TRACKER:-}"
    --code-forge "${CODE_FORGE:-}"
    --dispatch-kind "${DISPATCH_KIND:-}"
    --fix-pass "${FIX_PASS:-0}"
    --prompts-dir "$PROMPTS_DIR"
    --agents-prompt-files "${AGENTS_PROMPT_FILES:-}"
    --driver-agent-files-dir "${DRIVER_AGENT_FILES_DIR:-}"
    --comms-contract-file "$COMMS_CONTRACT_FILE"
    --check-contract-file "$CHECK_CONTRACT_FILE"
    --outcome-contract-file "$OUTCOME_CONTRACT_FILE"
    --research-outcome-contract-file "$RESEARCH_OUTCOME_CONTRACT_FILE"
    --skills-found "$SKILLS_FOUND"
    --issue-number "$ISSUE_NUMBER"
    --issue-title "$ISSUE_TITLE"
    --branch "$BRANCH"
    --base-branch "${BASE_BRANCH:-}"
    --in-progress-label "${IN_PROGRESS_LABEL:-}"
    --complete-label "${COMPLETE_LABEL:-}"
    --run-nonce "${RUN_NONCE:-}"
    --ci-failure-summary "${CI_FAILURE_SUMMARY:-}"
    --research-status-enum "${RESEARCH_STATUS_ENUM:-}"
  )
  # BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT
  [ -f "$DRIVER_SKILLS_DIR/caveman/SKILL.md" ] && _ap_args+=(--caveman-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/tdd/SKILL.md" ] && _ap_args+=(--tdd-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/commit/SKILL.md" ] && _ap_args+=(--commit-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/code-review/SKILL.md" ] && _ap_args+=(--code-review-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/auto-format/SKILL.md" ] && _ap_args+=(--auto-format-skill-baked)
  [ -f "$DRIVER_SKILLS_DIR/auto-lint/SKILL.md" ] && _ap_args+=(--auto-lint-skill-baked)
  # END GENERATED SKILL-BAKED PROBES
  # Reads $ORCHESTRATOR (main's early ORCHESTRATOR_ENABLED-derived cross-phase
  # sentinel, issue #2354 slice 3), not ORCHESTRATOR_ENABLED directly -- the
  # orchestrator-fork-well-formed check (nix/checks/prompts.nix) pins exactly
  # one raw ORCHESTRATOR_ENABLED test in this file (main's own early
  # computation); every fork downstream, this one included, reads that one
  # computed gate instead. This call happens before the driver-exec
  # assemble-prompt verb runs, so no Handoff exists yet to read Invoker from
  # regardless -- and unlike before issue #2355, nothing downstream ever
  # reassigns $ORCHESTRATOR from the resulting Handoff either: the two are
  # always mathematically identical, so main's one early computation is
  # $ORCHESTRATOR's value for this entire run, read as-is by every consumer
  # (this flag, and run_driver_in_env's own pre-Handoff conflict-resolve
  # fallback -- the reject/warn marker matrix these gates used to also feed
  # moved into the Go verb itself, issue #2356).
  [ -n "$ORCHESTRATOR" ] && _ap_args+=(--orchestrator-enabled)
  [ -n "${BOX_WRITE_ENABLED:-}" ] && _ap_args+=(--box-write-enabled)
  [ -n "${LOCAL_ISSUE_REFERENCE:-}" ] && _ap_args+=(--local-issue-reference)
  _is_self_contained && _ap_args+=(--self-contained)
  [ -n "${RESUME_AFTER_HOLD:-}" ] && _ap_args+=(--resume-after-hold)
  [ -n "${AUTO_FORMAT:-}" ] && _ap_args+=(--auto-format)
  [ -n "${AUTO_LINT:-}" ] && _ap_args+=(--auto-lint)

  local _prompt_out _agents_out _handoff_out
  _prompt_out="$(mktemp)"
  _agents_out="$(mktemp)"
  _handoff_out="$(mktemp)"

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
    --handoff-output "$_handoff_out"

  # $(...) strips the file's trailing newline exactly the way it always
  # stripped a command substitution's -- Assemble already trims one from its
  # own Prompt/AgentsJSON output the same way the old `$(_subst ...)`/
  # `$(printf ... | jq ...)` chains did, so this stays byte-identical.
  prompt="$(cat "$_prompt_out")"
  agents_json="$(cat "$_agents_out")"
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
  # Test-only hook (issue #2395 slice 1): when a bats test has exported
  # DRIVER_HANDOFF_FILE (tests/helper.bash), persist the raw Handoff JSON
  # there before the tempfile below is removed, for golden-fixture diffing
  # in tests/prompt-assembly-parity.bats. Unlike prompt/agents, which
  # tests/fakes/claude captures from inside the fake Driver, no fake ever
  # receives SessionMode/Invoker as CLI args, so this raw JSON is the only
  # place a test can observe them. A no-op in production, where this var is
  # never set.
  [ -n "${DRIVER_HANDOFF_FILE:-}" ] && cp "$_handoff_out" "$DRIVER_HANDOFF_FILE"
  rm -f "$_prompt_out" "$_agents_out" "$_handoff_out"
}

# run_driver_in_env runs the Driver against $1 (the assembled prompt), with
# $2 (--agents JSON, or "" to omit the flag), $3 (session mode, forwarded
# verbatim to the nix-supplied _driver_session_flags — "initial"/"resume" pin
# or resume the issue's session id; any other value, e.g. "" for the
# conflict-resolve pass, yields no session flags), and $4 (the raw Handoff
# descriptor JSON phase_prompt_assembly's driver-exec assemble-prompt call
# produced, or "" for the one pass that predates it -- phase_conflict_resolve's
# conflict-resolve call, which runs before any Handoff exists; the corrective
# resume each required-marker gate fires deliberately narrows $4 to
# `{"Invoker": ...}` only, carrying issue #2065's deliberate omission of the
# review fields forward). Below derives the invoker fork and the code-owned
# review pass's own rendered prompt text/model (issues #2037, #2277) straight
# from $4's Invoker/ReviewPromptFile/ReviewModel fields when $4 is non-empty;
# when $4 is empty (the pre-Handoff conflict-resolve pass) the invoker fork
# instead falls back to $ORCHESTRATOR, main's early ORCHESTRATOR_ENABLED-
# derived cross-phase sentinel, and the review fields stay empty. Issue
# #2355 drained the session-mode/invoker/review-prompt/review-model
# sentinels (_driver_session_mode/review_prompt_rendered/
# review_model_rendered) this function used to be handed as separate
# params/globals onto this one descriptor param instead; $ORCHESTRATOR alone
# survives, as the unavoidable pre-Handoff fallback -- see the
# ORCHESTRATOR/Handoff.Invoker equivalence note in main, near its early
# ORCHESTRATOR computation (line ~1185). Delegates to driver-exec
# (issue #626), the in-box Go unit that owns "run the Driver, optionally
# inside the Project devShell" as one code path: it takes the prompt/agents/
# session as file paths (a compiled binary crosses the devShell process
# boundary with a plain argv, so none of the temp-file/eval marshalling this
# function used to do is needed here), spawns the Driver directly or via
# `nix develop --command` when phase_devshell_probe found one
# (_use_dev_shell, read via bash's dynamic scoping like every other phase
# function), tees the stream to a log path, filters heartbeats in-process,
# and returns the Driver's exit status.
#
# The invoker fork (default off, issue #1996; canonicalized #2047) swaps
# which binary receives that exact flag set: off takes today's direct
# driver-exec path unchanged; on hands the same invocation to the in-box Go
# orchestrator, which forwards it to driver-exec itself for its own
# single-pass tracer bullet. Neither branch changes the flags built below --
# this is the reusable seam the orchestrator drives, so a later multi-pass
# loop only ever touches the orchestrator side of it.
run_driver_in_env() {
  local prompt="$1" agents_json="$2" session_mode="$3" handoff_json="${4:-}"

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

  # review_prompt/review_model/review_effort come straight from the Handoff
  # descriptor's own ReviewPromptFile/ReviewModel/ReviewEffort fields (issue
  # #2355; ReviewEffort added by issue #2512, mirroring ReviewModel exactly)
  # -- empty whenever handoff_json itself is empty (the pre-Handoff
  # conflict-resolve pass) or the keys are simply absent (a required-marker
  # gate's corrective resume narrows handoff_json to {"Invoker": ...} only,
  # issue #2065).
  local review_prompt="" review_model="" review_effort="" worker_prompt=""
  if [ -n "$handoff_json" ]; then
    review_prompt="$(_handoff_field "$handoff_json" ReviewPromptFile)"
    review_model="$(_handoff_field "$handoff_json" ReviewModel)"
    review_effort="$(_handoff_field "$handoff_json" ReviewEffort)"
    worker_prompt="$(_handoff_field "$handoff_json" WorkerPromptFile)"
  fi

  # worker_work_dir/worker_timeout have no Handoff descriptor field
  # (issue #2059, #2058) -- plain strings read straight off the
  # environment, no Go-side rendering needed.
  local worker_work_dir="${WORKER_WORK_DIR:-}"
  local worker_timeout="${WORKER_TIMEOUT:-}"
  local worker_max_parallel="${MAX_PARALLEL_WORKERS:-}"

  local _review_prompt_file=""
  if [ -n "$review_prompt" ]; then
    _review_prompt_file="$(mktemp)"
    printf '%s' "$review_prompt" > "$_review_prompt_file"
  fi

  local _worker_prompt_file=""
  if [ -n "$worker_prompt" ]; then
    _worker_prompt_file="$(mktemp)"
    printf '%s' "$worker_prompt" > "$_worker_prompt_file"
  fi

  # stream_log is driver-exec's teed copy of the Driver's raw stdout, read
  # below by _driver_extract_outcome -- the launcher's own capture of stdout
  # (.spindrift/logs/issue-<n>.log, byte-exact, unchanged) is separate and untouched.
  stream_log="$(mktemp)"

  local -a _devshell_flags=()
  if [ "$_use_dev_shell" = "1" ]; then
    _devshell_flags=(--devshell --devshell-name "${DEV_SHELL_NAME:-default}")
  fi

  # driver-exec/orchestrator both default --heartbeat-log to the shared,
  # host-wide /tmp/heartbeat.log -- fine for a real Box (one per container,
  # nothing else touches its /tmp), but a bats suite invokes this entrypoint
  # directly on the nix build host, where several derivations can build
  # concurrently as distinct sandbox users and collide on that one path
  # (issue #2320: a second builder's append to a file the first already
  # created hits EACCES). HEARTBEAT_LOG lets a caller opt into a
  # collision-free path; unset (the real-Box default) leaves the binaries'
  # own /tmp/heartbeat.log default untouched.
  local -a _heartbeat_flags=()
  if [ -n "${HEARTBEAT_LOG:-}" ]; then
    _heartbeat_flags=(--heartbeat-log "$HEARTBEAT_LOG")
  fi

  # Invoker comes from handoff_json's own Invoker field when a Handoff
  # exists (issue #2355); the one pass with no Handoff yet
  # (phase_conflict_resolve's pre-Handoff call) falls back to $ORCHESTRATOR,
  # main's early ORCHESTRATOR_ENABLED-derived cross-phase sentinel --
  # mathematically identical to what the Handoff's own Invoker field would
  # say once one existed.
  local _driver_invoker=driver-exec
  if [ -n "$handoff_json" ]; then
    [ "$(_handoff_field "$handoff_json" Invoker)" = "orchestrator" ] && _driver_invoker=orchestrator
  else
    [ -n "$ORCHESTRATOR" ] && _driver_invoker=orchestrator
  fi

  # --review-prompt-file only ever means something to the orchestrator
  # binary (driver-exec declares no such flag, and would hard-fail on an
  # unknown one) -- guarded on $_driver_invoker, itself already derived from
  # handoff_json (or $ORCHESTRATOR when no Handoff exists yet) just above,
  # rather than a second raw test of the gate.
  local -a _review_prompt_flags=()
  if [ "$_driver_invoker" = orchestrator ] && [ -n "$_review_prompt_file" ]; then
    _review_prompt_flags=(--review-prompt-file "$_review_prompt_file")
  fi

  # --review-model, same orchestrator-only shape as --review-prompt-file
  # just above (issue #2277): the reviewer's own configured model, threaded
  # through so the orchestrator's review pass uses it instead of falling
  # back to the coordinator model when it's unset.
  local -a _review_model_flags=()
  if [ "$_driver_invoker" = orchestrator ] && [ -n "$review_model" ]; then
    _review_model_flags=(--review-model "$review_model")
  fi

  # --review-effort, same orchestrator-only shape as --review-model just
  # above (issue #2512): the reviewer's own configured effort now has its
  # own Handoff descriptor field (ReviewEffort), mirroring ReviewModel's
  # shape exactly.
  local -a _review_effort_flags=()
  if [ "$_driver_invoker" = orchestrator ] && [ -n "$review_effort" ]; then
    _review_effort_flags=(--review-effort "$review_effort")
  fi

  # --worker-prompt-file, same orchestrator-only shape as --review-prompt-file
  # above (issue #2059, #2058): the parallel worker's own base prompt text,
  # threaded through so dispatchManifestIfPresent has a non-empty prompt to
  # dispatch a manifest slice against -- without it, worker dispatch is a
  # permanent no-op regardless of what the coordinator emits.
  local -a _worker_prompt_flags=()
  if [ "$_driver_invoker" = orchestrator ] && [ -n "$_worker_prompt_file" ]; then
    _worker_prompt_flags=(--worker-prompt-file "$_worker_prompt_file")
  fi

  # --worker-work-dir, same orchestrator-only shape as --review-effort above
  # (issue #2059, #2058): the directory holding each dispatched worker's own
  # quarantined log/heartbeat/result/sentinel files.
  local -a _worker_work_dir_flags=()
  if [ "$_driver_invoker" = orchestrator ] && [ -n "$worker_work_dir" ]; then
    _worker_work_dir_flags=(--worker-work-dir "$worker_work_dir")
  fi

  # --worker-timeout, same orchestrator-only shape as --review-effort above
  # (issue #2059, #2058): the per-worker join timeout for a parallel
  # dispatch.
  local -a _worker_timeout_flags=()
  if [ "$_driver_invoker" = orchestrator ] && [ -n "$worker_timeout" ]; then
    _worker_timeout_flags=(--worker-timeout "$worker_timeout")
  fi

  # --max-parallel-workers, same orchestrator-only shape as --worker-timeout
  # just above (issue #2059, #2495): the cap on how many manifest-dispatched
  # workers LaunchWorkers runs concurrently.
  local -a _worker_max_parallel_flags=()
  if [ "$_driver_invoker" = orchestrator ] && [ -n "$worker_max_parallel" ]; then
    _worker_max_parallel_flags=(--max-parallel-workers "$worker_max_parallel")
  fi

  local claude_rc=0
  set +e
  "$_driver_invoker" \
    --prompt-file "$_prompt_file" \
    --agents-file "$_agents_file" \
    --session-file "$_session_file" \
    --driver "$DRIVER_NAME" \
    --driver-bin "$DRIVER_BIN" \
    --driver-flags "$DRIVER_FLAGS_COMMON" \
    --model "${MODEL:-}" \
    --effort "${EFFORT:-}" \
    --issue "$ISSUE_NUMBER" \
    --log-path "$stream_log" \
    "${_devshell_flags[@]}" \
    "${_heartbeat_flags[@]}" \
    "${_review_prompt_flags[@]}" \
    "${_review_model_flags[@]}" \
    "${_review_effort_flags[@]}" \
    "${_worker_prompt_flags[@]}" \
    "${_worker_work_dir_flags[@]}" \
    "${_worker_timeout_flags[@]}" \
    "${_worker_max_parallel_flags[@]}"
  claude_rc=$?
  set -e
  rm -f "$_prompt_file" "$_agents_file" "$_session_file" "$_review_prompt_file" "$_worker_prompt_file"

  # The launcher greps '^SPINDRIFT_OUTCOME ' from the container log, but the
  # Driver's raw transcript format buries it (claude wraps it in a stream-json
  # result event); _driver_extract_outcome surfaces it as a bare line so that
  # contract is unchanged. Captured (rather than left to print directly) so
  # main's post-return backstop (issue #593) can tell whether the Driver
  # actually emitted one.
  _last_outcome_line="$(_driver_extract_outcome "$stream_log")"
  # The offending near-miss line, if any (issue #1900): a line that led with
  # the SPINDRIFT_OUTCOME token but lacked landing=/status=, captured off the
  # same still-on-disk stream_log so the recovery nudge below can quote it
  # back. Empty whenever _last_outcome_line above already found a valid line.
  _last_near_miss_line="$(_driver_extract_near_miss_outcome "$stream_log")"
  # _scan_pr_intent_in_log's own read, ahead of the stream_log removal below
  # -- the SPINDRIFT_PR_INTENT required-marker gate row (issue #2045) needs
  # to know whether this pass attempted the marker at all, the same way the
  # outcome capture just above feeds that gate's SPINDRIFT_OUTCOME row.
  _last_pr_intent_line="$(_scan_pr_intent_in_log "$stream_log")"
  rm -f "$stream_log"
  if [ -n "$_last_outcome_line" ]; then
    printf '%s\n' "$_last_outcome_line"
  fi

  return "$claude_rc"
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

# _scan_pr_intent_in_log reports (via stdout) the last line in $1 (a raw
# stream_log a driver-exec pass just teed, still on disk at the point
# run_driver_in_env calls this) that carries a genuine SPINDRIFT_PR_INTENT
# attempt, or nothing if none is present -- the SPINDRIFT_PR_INTENT row's
# scanner (issue #2045, the #2036 fix).
#
# Unlike _driver_extract_outcome, this needs no jq-filtered, markdown-
# stripped bare-line reprint: driver-exec already tees the raw stream
# verbatim to the container's own stdout (issue #626), and the launcher's
# own outcome.LastPRIntentInLog already tolerates the token embedded
# mid-JSON-string (see that function's doc comment) -- so nothing downstream
# needs a cleaned-up copy of the line the way OUTCOME's LastInLog does.
#
# A bash regex has no reliable way to tell a genuine base64 payload from
# ordinary prose by character class alone -- both are letters -- so instead
# of guessing at the payload's shape, this anchors on this run's own
# $RUN_NONCE (issue #1937's reasoning, applied in-box): a line that merely
# mentions the token in passing essentially never also carries this run's
# nonce verbatim, the same way an untrusted comment/issue-body author's echo
# of the token can't. An empty RUN_NONCE (e.g. a research dispatch, which
# never reaches this gate anyway -- the SPINDRIFT_OUTCOME required-marker
# gate's own _is_research_kind check short-circuits first) matches nothing,
# never everything.
_scan_pr_intent_in_log() {
  local log="$1" nonce="${RUN_NONCE:-}"
  [ -n "$nonce" ] || return 0
  grep -F -- "SPINDRIFT_PR_INTENT ${nonce} " "$log" 2>/dev/null \
    | grep -oE "SPINDRIFT_PR_INTENT[[:space:]]+[^[:space:]\"]+[[:space:]]+[^[:space:]\"]+" \
    | tail -1
}

main() {
  # Cross-phase sentinels: declared local here so bash's dynamic scoping lets
  # each phase function assign them by plain (non-local) assignment while
  # keeping them out of true global scope (issue #515).
  local _rebase_and_publish _had_rebase_conflict
  local _use_dev_shell _harness_path
  local prompt agents_json _handoff
  local _last_outcome_line _last_near_miss_line _last_pr_intent_line
  # Initialized empty here, unlike the sibling vars above (which are always
  # assigned unconditionally by run_driver_in_env before any read) -- this
  # one is only ever assigned inside the backstop `if` block below, and
  # `set -u` treats a bare `local x` with no value as unbound, not empty
  # (issue #2448 finding 3).
  local _outcome_via_backstop=""
  local ORCHESTRATOR

  configure_env

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
    # A research dispatch (ADR 0022, issue #640) explores the fresh clone but
    # never lands code: no branch to cut, adopt, or rebase.
    if ! _is_research_kind; then
      phase_branch_recovery
      phase_prework_rebase
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
  phase_conflict_resolve
  phase_prompt_assembly

  if _is_research_kind; then
    echo "==> claude researching issue #$ISSUE_NUMBER"
  else
    echo "==> claude implementing issue #$ISSUE_NUMBER on $BRANCH"
  fi
  local claude_rc=0
  run_driver_in_env "$prompt" "$agents_json" "$(printf '%s' "$_handoff" | jq -r '.SessionMode')" "$_handoff" || claude_rc=$?

  # SPINDRIFT_OUTCOME required-marker gate (issue #1607/#2044, verb-owned
  # decision issue #2511): a Driver pass that exits cleanly but leaves the
  # marker missing or unparseable most often just ended its turn early
  # (issue #1542) rather than actually failing, so resume the same pinned
  # session exactly once with a corrective nudge before any backstop runs.
  # A research dispatch pins no session worth resuming (ADR 0022); a
  # non-zero exit is the launcher's own ClassifyTransient/retry path to
  # handle (issue #593) -- neither reaches this gate. The nudge prompt
  # itself, including the near-miss-quoting variant (issue #1900), is
  # rendered by the driver-exec marker-gate verb
  # (cmd/launcher/internal/markergate), not hand-typed here.
  local _outcome_gate_resumed=""
  if [ "$claude_rc" -eq 0 ] && ! _is_research_kind && [ -z "$_last_outcome_line" ]; then
    echo "==> required marker missing — resuming the session once with a nudge"
    _outcome_gate_resumed=1
    local _outcome_nudge_prompt
    _outcome_nudge_prompt="$(driver-exec marker-gate --phase nudge --marker outcome \
      --near-miss-line "$_last_near_miss_line" \
      --issue "${ISSUE_NUMBER:-}" --landing "$BRANCH" | jq -r '.prompt')"
    run_driver_in_env "$_outcome_nudge_prompt" "$agents_json" "resume" "$(printf '%s' "$_handoff" | jq -c '{Invoker}')" || claude_rc=$?
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
  # decision issue #2511): a read-only github Box that reaches status=ready
  # but never printed SPINDRIFT_PR_INTENT leaves the launcher's
  # hostMediateDraftPR with nothing to relay. Scoped to read-only + github +
  # a genuine status=ready (a blocked/failed run never opens a PR, ready or
  # not via the synthetic backstop above -- issue #2448). Matched against
  # the line's own issue=/landing=/status= prefix only (before " note="),
  # since the free-text note field can itself contain the substring
  # "status=ready". The corrective prompt, the give-up heartbeat op, and the
  # restore-vs-leave-alone decision are all rendered/decided by the
  # driver-exec marker-gate verb (cmd/launcher/internal/markergate) -- see
  # its own doc comments for why give-up and restore are independent,
  # separately-gated outcomes rather than a strict three-way switch.
  if [ "$claude_rc" -eq 0 ]; then
    local _outcome_fields_before_note="${_last_outcome_line%% note=*}"
    if _is_readonly_github \
      && [[ " $_outcome_fields_before_note " == *" status=ready "* ]] \
      && [ -z "$_last_pr_intent_line" ]; then
      local _original_ready_outcome_line="$_last_outcome_line"
      local _pr_intent_nudge_prompt
      _pr_intent_nudge_prompt="$(driver-exec marker-gate --phase nudge --marker pr-intent \
        --nonce "${RUN_NONCE:-}" --original-outcome-line "$_original_ready_outcome_line" | jq -r '.prompt')"
      echo "==> PR-intent marker missing — resuming the session once with a nudge"
      run_driver_in_env "$_pr_intent_nudge_prompt" "$agents_json" "resume" "$(printf '%s' "$_handoff" | jq -c '{Invoker}')" || claude_rc=$?

      local -a _resolve_args=(
        --phase resolve --marker pr-intent
        --attempts 1
        --pr-intent-line "$_last_pr_intent_line"
        --resumed-outcome-line "$_last_outcome_line"
        --resumed-near-miss-line "$_last_near_miss_line"
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
    && { [ "${CODE_FORGE:-github}" = "local" ] \
      || _is_readonly_github; }; then
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
