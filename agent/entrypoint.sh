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
# forge, so REPO_SLUG and GH_TOKEN have nothing to resolve against — mirrors
# the launcher's own validate() (cmd/launcher/main.go).
fully_local=false
if [ "${CODE_FORGE:-}" = local ] && [ "${ISSUE_TRACKER:-}" = local ]; then
  fully_local=true
fi
# Self-contained research (issue #2202) supplies its content from a local
# issue tracker and clones no repo, so REPO_SLUG/GH_TOKEN have nothing to
# resolve against either -- mirrors the launcher validate()'s noRepoResearch
# permit, which is likewise scoped to a local issue tracker.
no_repo=false
if [ "${SELF_CONTAINED:-}" = 1 ] && [ "${ISSUE_TRACKER:-}" = local ]; then
  no_repo=true
fi
[ "$fully_local" = true ] || [ "$no_repo" = true ] || : "${REPO_SLUG:?REPO_SLUG (owner/repo) is required}"
: "${ISSUE_NUMBER:?ISSUE_NUMBER is required}"
[ "$fully_local" = true ] || [ "$no_repo" = true ] || : "${GH_TOKEN:?GH_TOKEN is required}"
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
  # OUTBOX_DIR is the writable mount driver-exec's bundle-out verb writes
  # CODE_FORGE=local's seam bundle into (ADR 0033, issue #1808); unused
  # otherwise.
  OUTBOX_DIR="${OUTBOX_DIR:-/outbox}"

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
# issued, so a force-push can only 403) and the Code Forge is github. Such a
# Box hands its branch off through the harness-owned outbox bundle seam rather
# than a push (issue #2094). The github default mirrors every other
# ${CODE_FORGE:-github} read in this file.
_is_readonly_github() {
  [ -z "${BOX_WRITE_ENABLED:-}" ] && [ "${CODE_FORGE:-github}" = "github" ]
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
# run_driver_in_env and required_marker_gate's corrective resume now read
# session mode/invoker/review-prompt/review-model straight off $_handoff at
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
  )
  [ -f "${DRIVER_SKILLS_DIR}/caveman/SKILL.md" ] && _ap_args+=(--caveman-skill-baked)
  [ -f "${DRIVER_SKILLS_DIR}/tdd/SKILL.md" ] && _ap_args+=(--tdd-skill-baked)
  [ -f "${DRIVER_SKILLS_DIR}/commit/SKILL.md" ] && _ap_args+=(--commit-skill-baked)
  [ -f "${DRIVER_SKILLS_DIR}/code-review/SKILL.md" ] && _ap_args+=(--code-review-skill-baked)
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
  # so it escapes to run_driver_in_env and required_marker_gate via main's
  # cross-phase sentinel -- the same dynamic-scoping shape _use_dev_shell
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
  # there before the tempfile below is removed -- mirrors how
  # DRIVER_PROMPT_FILE/DRIVER_AGENTS_FILE already let tests/fakes/claude
  # capture the prompt/agents JSON verbatim for golden-fixture diffing. A
  # no-op in production, where this var is never set.
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
# resume required_marker_gate fires deliberately narrows $4 to
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

  # review_prompt/review_model come straight from the Handoff descriptor's
  # own ReviewPromptFile/ReviewModel fields (issue #2355) -- empty whenever
  # handoff_json itself is empty (the pre-Handoff conflict-resolve pass) or
  # the keys are simply absent (required_marker_gate's corrective resume
  # narrows handoff_json to {"Invoker": ...} only, issue #2065).
  local review_prompt="" review_model=""
  if [ -n "$handoff_json" ]; then
    review_prompt="$(_handoff_field "$handoff_json" ReviewPromptFile)"
    review_model="$(_handoff_field "$handoff_json" ReviewModel)"
  fi

  local _review_prompt_file=""
  if [ -n "$review_prompt" ]; then
    _review_prompt_file="$(mktemp)"
    printf '%s' "$review_prompt" > "$_review_prompt_file"
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
    "${_review_model_flags[@]}"
  claude_rc=$?
  set -e
  rm -f "$_prompt_file" "$_agents_file" "$_session_file" "$_review_prompt_file"

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
  driver-exec outcome-backstop \
    --repo "$WORK_DIR" \
    --issue "$ISSUE_NUMBER" \
    --branch "$BRANCH" \
    --base "origin/${BASE_BRANCH:-}" \
    --dispatch-kind "${DISPATCH_KIND:-work}" \
    --host-mediated-remote "${BOX_HOST_MEDIATED_REMOTE:-}" \
    --outbox-relay-capable "${BOX_OUTBOX_RELAY_CAPABLE:-}" \
    --box-write-enabled "${BOX_WRITE_ENABLED:-}" \
    --recovery-attempted "${_recovery_attempted:-}" \
    --max-attempts "$MAX_REBASE_ATTEMPTS" \
    --backoff-secs "$TRANSIENT_BACKOFF_SECS" \
    --jitter-secs "$HOLD_JITTER_SECS"
}

# required_marker_gate is the reusable shape issue #1607 hardwired to
# SPINDRIFT_OUTCOME: a Driver pass that exits cleanly but leaves a required
# marker missing most often just ended its turn early (issue #1542) rather
# than actually failing, so before any caller-owned backstop runs, resume the
# same pinned session exactly once with a corrective nudge and re-scan. Never
# loops past that one resume -- a second miss falls through for the caller to
# handle. A research dispatch pins no session worth resuming (ADR 0022), so
# it always falls through untouched. Registering a second marker (e.g.
# SPINDRIFT_PR_INTENT, issue #2036) means one more call at the call site
# below, not a second copy of this function (issue #2044).
#
# Args:
#   $1 - name of a scanner function: called with no arguments, echoes the
#        marker's current value (or empty) by inspecting whatever state the
#        just-completed pass left behind
#   $2 - corrective prompt to resume the session with, sent only on a miss
#   $3 - name of a required-predicate function: called with the scanned
#        value as $1, returns success if the gate is already satisfied
#
# Reads/writes claude_rc, agents_json, _handoff, and _recovery_attempted by
# dynamic scoping from main, the same idiom every other phase function here
# uses (issue #515) -- there is no separate return value; a caller re-scans its
# own marker after this returns to see whether the resume changed anything.
required_marker_gate() {
  local _scanner="$1" _corrective_prompt="$2" _predicate="$3"

  if [ "$claude_rc" -ne 0 ] || _is_research_kind; then
    return
  fi
  if "$_predicate" "$("$_scanner")"; then
    return
  fi

  echo "==> required marker missing — resuming the session once with a nudge"
  _recovery_attempted=1
  # Narrows handoff_json to {"Invoker": ...} only (jq's `{Invoker}` object-
  # construction shorthand) -- no ReviewPromptFile/ReviewModel key at all, so
  # run_driver_in_env's own `// empty` jq defaults leave both empty,
  # preserving issue #2065's deliberate single-pass-nudge omission (see the
  # comment on the primary run_driver_in_env call in main for why).
  run_driver_in_env "$_corrective_prompt" "$agents_json" "resume" "$(printf '%s' "$_handoff" | jq -c '{Invoker}')" || claude_rc=$?
}

# The SPINDRIFT_OUTCOME row on the required-marker gate above -- the scanner
# reads back main's captured outcome line, and the predicate is bare
# presence, the same condition the pre-#2044 inline check tested.
#
# Both are only ever invoked indirectly, via required_marker_gate's
# "$_scanner"/"$_predicate" parameters (see its body above), never by their
# literal names. shellcheck's own indirect-invocation credit for that
# pattern stops working once main() ends with an unconditional
# `exit "$claude_rc"` (ADR 0039 slice S1, issue #2252) -- a shellcheck
# limitation, not a real dead-code finding; both are called from main() well
# before that trailing exit.
# shellcheck disable=SC2329
_scan_outcome() { printf '%s' "$_last_outcome_line"; }
# shellcheck disable=SC2329
_require_nonempty() { [ -n "$1" ]; }

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
# never reaches this gate anyway -- required_marker_gate's own
# _is_research_kind check short-circuits first) matches nothing, never
# everything.
_scan_pr_intent_in_log() {
  local log="$1" nonce="${RUN_NONCE:-}"
  [ -n "$nonce" ] || return 0
  grep -F -- "SPINDRIFT_PR_INTENT ${nonce} " "$log" 2>/dev/null \
    | grep -oE "SPINDRIFT_PR_INTENT[[:space:]]+[^[:space:]\"]+[[:space:]]+[^[:space:]\"]+" \
    | tail -1
}

# The SPINDRIFT_PR_INTENT row on the required-marker gate above (issue
# #2045, the #2036 fix): the scanner reads back main's captured PR-intent
# line, reusing _require_nonempty as its predicate -- the same bare-presence
# condition the SPINDRIFT_OUTCOME row above already uses.
#
# Same shellcheck note as _scan_outcome above: only ever invoked indirectly
# via required_marker_gate's "$_scanner" parameter.
# shellcheck disable=SC2329
_scan_pr_intent() { printf '%s' "$_last_pr_intent_line"; }

# _emit_pr_intent_giveup_op prints a single heartbeat "decision" op (issue
# #2046) recording that the read-only PR-intent nudge gave up after $1
# attempt(s) without ever yielding a usable SPINDRIFT_PR_INTENT line. It is an
# ordinary stream-json line on the Box's own stdout -- the very stream
# driver-exec and the orchestrator already write their own #2027 spindrift_op
# events onto -- so the host heartbeat Writer (cmd/launcher/internal/driver/
# claude) parses and renders it identically, giving an operator a visible
# reason the run ended blocked rather than the unexplained state behind #2036.
# The JSON mirrors that package's Event/SpindriftOp shape; a "decision" op
# with decision=stop is exactly how the orchestrator's own give-up decisions
# are encoded. The reason text deliberately omits the literal
# SPINDRIFT_PR_INTENT token so no downstream scan mistakes it for a genuine
# marker attempt (_scan_pr_intent_in_log and the launcher's
# outcome.LastPRIntentInLog both key off the bare token).
_emit_pr_intent_giveup_op() {
  local attempts="$1"
  printf '{"type":"spindrift_op","spindrift_op":{"op":"decision","decision":"stop","reason":"read-only PR-intent nudge exhausted after %s attempt; no marker line, handing off blocked"}}\n' "$attempts"
}

main() {
  # Cross-phase sentinels: declared local here so bash's dynamic scoping lets
  # each phase function assign them by plain (non-local) assignment while
  # keeping them out of true global scope (issue #515).
  local _rebase_and_publish _had_rebase_conflict
  local _use_dev_shell _harness_path
  local prompt agents_json _handoff
  local _last_outcome_line _last_near_miss_line _last_pr_intent_line _recovery_attempted
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
  _recovery_attempted=""
  run_driver_in_env "$prompt" "$agents_json" "$(printf '%s' "$_handoff" | jq -r '.SessionMode')" "$_handoff" || claude_rc=$?

  # Resume-once-then-fall-through via the required-marker gate (issue
  # #2044). The same --agents JSON as the first pass rides along on any
  # resume -- the run may still need to reach the scout/filer step it never
  # got to, and the pinned session has no other way to learn about them.
  # required_marker_gate's own corrective-resume call narrows its
  # run_driver_in_env handoff_json argument to `{"Invoker": ...}` only (jq's
  # `{Invoker}` object-construction shorthand) -- no ReviewPromptFile or
  # ReviewModel key at all, so run_driver_in_env's own `// empty` jq defaults
  # leave both empty: under the orchestrator, this one corrective nudge
  # stays a narrow single-pass resume rather than re-entering the full
  # implement/review/fix loop (issue #2037) a second time from whatever cap
  # or park stopped the first attempt. Issue #2065
  # reviewed this deliberate downgrade and kept the single-pass fallback:
  # each run_driver_in_env call spawns a fresh orchestrator process whose
  # --max-review-rounds/--max-slices budgets reset to their binary defaults
  # (entrypoint.sh threads neither), so re-attaching --review-prompt-file
  # here would hand the last-resort nudge a brand-new full review budget and
  # re-trigger the exact bounded-but-large loop the original attempt just
  # exhausted -- the deterministic checks/gates the recovery_prompt already
  # asks for still run, only the code-owned review pass is skipped. A
  # regression test pins this omission (tests/entrypoint-orchestrator-handoff.bats).
  # When the just-completed pass left a near-miss outcome line -- the token
  # present but the line unparseable (issue #1900) -- quote that offending
  # text back and restate the canonical grammar and the allowed status
  # values, mirroring the PR-intent nudge's grammar-rich shape below, rather
  # than the bare "print the required line" wording. A bare absence (no
  # near-miss line captured) still gets the generic nudge unchanged.
  local recovery_prompt
  if [ -n "$_last_near_miss_line" ]; then
    recovery_prompt="Your last message printed a line that looks like a SPINDRIFT_OUTCOME marker but does not parse, so the run has no usable outcome: ${_last_near_miss_line}
Print the required line exactly once as your final message, using this grammar -- one line, space-delimited fields: SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER:-} landing=<landing-ref> status=<status> note=<short reason>. The only valid status values are ready and blocked. Run any remaining checks/gates in the foreground first, then print that line."
  else
    recovery_prompt="The run ended without printing a SPINDRIFT_OUTCOME line. Finish the workflow: run any remaining checks/gates in the foreground, then print the required SPINDRIFT_OUTCOME line as your final message."
  fi
  required_marker_gate _scan_outcome "$recovery_prompt" _require_nonempty

  # Only a driver that exited cleanly yet told us nothing gets the synthetic
  # backstop. A non-zero exit is left to propagate untouched -- the
  # launcher's own ClassifyTransient/retry path (cmd/launcher/internal/dispatch)
  # already owns that case, and only runs when the container's own exit code
  # is non-zero; forcing exit 0 here would silently turn a retryable
  # transient failure into a terminal synthetic status=blocked (issue #593).
  if [ "$claude_rc" -eq 0 ] && [ -z "$_last_outcome_line" ]; then
    echo "==> driver produced no SPINDRIFT_OUTCOME line — emitting synthetic backstop"
    emit_outcome_backstop
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

  # The SPINDRIFT_PR_INTENT row on the required-marker gate (issue #2045,
  # the #2036 fix): a read-only github Box that reaches status=ready but
  # never printed a PR-intent line leaves the launcher's hostMediateDraftPR
  # with nothing to relay -- it posts "merge blocked" and strands the
  # otherwise-finished branch. Scoped tighter than the SPINDRIFT_OUTCOME row
  # above: only when read-only (BOX_WRITE_ENABLED absent, so a push-capable
  # token was never issued and this marker is the Box's only way to hand off
  # a PR), the github Code Forge (git/local never reach OPEN A PULL REQUEST
  # at all, so they never emit this marker either -- ADR 0034), and the
  # outcome itself parsed as status=ready (a blocked/failed run never opens
  # a PR, so a missing PR-intent there is expected, not a bug to nudge).
  # $_last_outcome_line may already be the SPINDRIFT_OUTCOME row's own
  # resumed line, not the first pass's -- read after that gate above, same
  # as every other post-gate use of it in this function. Matched against the
  # line's own issue=/landing=/status= prefix only (everything up to the
  # first " note="), not the whole line -- the free-text note field can
  # itself contain the substring "status=ready" (e.g. an agent explaining
  # why a status=blocked run couldn't reach status=ready), and a bare
  # grep across the full line would false-positive on that.
  #
  # This whole block only makes sense on a genuine status=ready reached by a
  # cleanly-exited driver. It used to rely on the now-deleted
  # `[ "$claude_rc" -eq 0 ] || exit "$claude_rc"` line above for that
  # guarantee; the explicit guard below (ADR 0039 slice S1, issue #2252)
  # replaces it now that a non-zero claude_rc falls through instead of
  # exiting immediately.
  if [ "$claude_rc" -eq 0 ]; then
    local _outcome_fields_before_note="${_last_outcome_line%% note=*}"
    if _is_readonly_github \
      && [[ " $_outcome_fields_before_note " == *" status=ready "* ]]; then
      # Carries this run's nonce (so the resumed pass can emit a line
      # _scan_pr_intent_in_log will actually match) and repeats the exact
      # SPINDRIFT_OUTCOME line already captured above verbatim: run_driver_in_env
      # recaptures $_last_outcome_line from whatever this resumed pass prints,
      # so without an instruction to repeat it, a resume that prints only the
      # PR-intent line would blank that var out and trip the no-outcome
      # backstop above on a run that already genuinely finished ready.
      #
      # The grammar spelled out below is free-text LLM instruction, not a
      # machine-parsed contract -- only the bare SPINDRIFT_PR_INTENT token
      # (outcome.PRIntentToken) is load-bearing for _scan_pr_intent_in_log and
      # the launcher's own outcome.LastPRIntentInLog, and that literal is what
      # TestPromptMarkersMatchScanner pins against
      # open-pr-create-outbox.md/if-blocked-pr-outbox.md. A reworded sentence
      # here is harmless as long as it still leads the agent to print the
      # token, the nonce, and a base64 payload in that order.
      local _original_ready_outcome_line="$_last_outcome_line"
      local pr_intent_recovery_prompt="Your last message ended with a status=ready SPINDRIFT_OUTCOME line but printed no SPINDRIFT_PR_INTENT line, so the launcher has no draft PR to open. Print exactly one SPINDRIFT_PR_INTENT line, grammar: SPINDRIFT_PR_INTENT ${RUN_NONCE:-} <base64-encoded title, a blank line, then the body>, built by joining the PR title, a blank line, and the PR body, then base64-encoding the result into one unbroken token with no embedded newlines or spaces. Then repeat this exact line as your final message: ${_last_outcome_line}"
      required_marker_gate _scan_pr_intent "$pr_intent_recovery_prompt" _require_nonempty
      # The nudge is exhausted: required_marker_gate resumed the session once
      # and the resumed pass still left no usable PR-intent line, so settle's
      # host-mediated draft-PR step will find nothing to relay and report this
      # run merge-blocked. Record that give-up as a heartbeat op (issue #2046)
      # so the terminal state is visibly explained -- an operator sees the nudge
      # ran and gave up, rather than an unexplained blocked hand-off (the #2036
      # confusion). Fires only on the genuinely-exhausted path: a resume that
      # supplied the marker leaves $_last_pr_intent_line non-empty and skips it,
      # and this whole block is already gated on read-only + github +
      # status=ready, so a run that landed a PR never reaches here. The single
      # attempt is required_marker_gate's own one-resume contract.
      if [ -z "$_last_pr_intent_line" ]; then
        _emit_pr_intent_giveup_op 1
      fi
      # Belt-and-braces beyond the "repeat this exact line" instruction above:
      # the resumed pass is still a fresh LLM turn that could garble or drop
      # the outcome line despite being told not to, and unlike the
      # SPINDRIFT_OUTCOME row's own resume (which only ever replaces "nothing"
      # with something), this row's resume has a known-good line to fall back
      # on. Restoring it here fixes both entrypoint.sh's own bookkeeping (so
      # the no-outcome backstop below never fires on a run that already
      # genuinely finished ready) and the container log the launcher's own
      # last-line-wins outcome.LastInLog scans (so a garbled resumed line
      # never shadows the good one there either).
      if [ "$_last_outcome_line" != "$_original_ready_outcome_line" ]; then
        echo "==> resumed pass did not repeat the original SPINDRIFT_OUTCOME line — restoring it"
        _last_outcome_line="$_original_ready_outcome_line"
        printf '%s\n' "$_last_outcome_line"
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
