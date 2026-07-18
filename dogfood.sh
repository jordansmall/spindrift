#!/usr/bin/env bash
# Dogfood loop: spindrift building spindrift.
#
# The box's behaviour — entrypoint, toolchain, and prompt — is baked into the OCI
# image at `nix run .# -- build` time (the merge gate itself lives in the launcher).
# When an agent merges a fix to the base branch, later issues stay blind to it
# until the image is rebuilt from an updated tree. This loop closes both
# staleness sources:
#
#   1. `git checkout $BASE_BRANCH && git pull --ff-only`
#                              — reset to the base branch and pull the just-merged
#                                change into the local tree, which is what
#                                `nix run .# -- build` reads from ($PWD).
#   2. `nix run .# -- build` — re-bake the image from that updated tree.
#
# Each invocation runs CONTINUOUS_DISPATCH's slot-refill loop (#527): as each
# Box finishes, the launcher re-discovers the queue and refills the freed slot
# immediately, gated by the image-freshness probe, instead of draining one
# bounded batch and returning. Concurrency is bounded by MAX_PARALLEL (default
# 3); MAX_JOBS defaults to MAX_PARALLEL. An operator can override MAX_JOBS
# explicitly to run a larger or unbounded slot pool, or unset
# CONTINUOUS_DISPATCH to fall back to the older one-wave-and-exit shape.
# The freshness probe, not this loop, decides when a rebuild is due: it fires
# only once a merge actually changed the image hash, not on every iteration.
# When it does, the launcher exits 4 and this loop pulls, rebuilds, and
# re-invokes so later refills launch on the fresh image.
#
# Termination is driven by the launcher's exit code — no separate gh probe:
#   exit 0 — dispatched work; loop continues after rebuilding from updated tree.
#   exit 2 — queue empty (no open issues with the dispatch label); loop exits.
#   exit 3 — open issues exist but none are dispatchable; loop stops and asks
#             for human triage (typically a failed blocker needing re-label).
#   exit 4 — CONTINUOUS_DISPATCH mode: the image-freshness probe found the
#             loaded image stale; in-flight Boxes finished, no new ones
#             launched. Loop pulls, rebuilds, and re-invokes, like exit 0.
set -euo pipefail

cd "$(dirname "$0")"

# REPO_SLUG (and GH_TOKEN) come from harness.env, the gitignored env file
# sourced into the harness. BASE_BRANCH defaults to main if not set there.
if [ -f harness.env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./harness.env
  set +a
fi
BASE_BRANCH="${BASE_BRANCH:-main}"      # must match env-schema.nix baseBranch.default
MAX_PARALLEL="${MAX_PARALLEL:-3}"      # must match env-schema.nix maxParallel.default
case "$MAX_PARALLEL" in
  '' | *[!0-9]* | 0[0-9]*)
    echo "!! MAX_PARALLEL must be a positive integer, got: $MAX_PARALLEL" >&2
    exit 1
    ;;
esac
MAX_JOBS="${MAX_JOBS:-$MAX_PARALLEL}"
# env-schema.nix continuousDispatch.default is off (empty); dogfood overrides
# it to on so the loop drives slot-refill dispatch instead of one wave and
# exit (#528). `-` (not `:-`) preserves an operator setting CONTINUOUS_DISPATCH=
# (empty) in harness.env to opt back out.
CONTINUOUS_DISPATCH="${CONTINUOUS_DISPATCH-1}"
: "${REPO_SLUG:?set REPO_SLUG=owner/repo in harness.env}"
# Selects which Dispatch kind (ADR 0022) the loop drives: "dispatch" (default,
# work) or "research". Both share the launcher's exit-code contract (2 empty
# queue, 3 none dispatchable, 4 stale image), so the loop logic below needs no
# other change to drive research instead.
DOGFOOD_KIND="${DOGFOOD_KIND:-dispatch}"
case "$DOGFOOD_KIND" in
  dispatch | research) ;;
  *)
    echo "!! DOGFOOD_KIND must be 'dispatch' or 'research', got: $DOGFOOD_KIND" >&2
    exit 1
    ;;
esac

if [ -n "$(git status --porcelain)" ]; then
  echo "!! working tree is dirty — commit/stash before dogfooding (build reads \$PWD)." >&2
  exit 1
fi

# Converts a --memory value (e.g. "4g", "512m") to MiB for comparison against
# `podman machine inspect`'s Resources.Memory (already MiB). No suffix is
# treated as bytes, matching podman/docker's own --memory parsing.
_memory_limit_to_mib() {
  local limit="$1"
  case "$limit" in
    *[Gg]) echo $(( ${limit%[Gg]} * 1024 )) ;;
    *[Mm]) echo "${limit%[Mm]}" ;;
    *[Kk]) echo $(( ${limit%[Kk]} / 1024 )) ;;
    *) echo $(( limit / 1024 / 1024 )) ;;
  esac
}

# Preflight (#580, parallelism-aware per #712): on macOS/Windows, podman runs
# containers inside a VM ("podman machine") with its own fixed RAM,
# independent of the per-container --memory cap (MEMORY_LIMIT). When the
# machine has less RAM than MAX_PARALLEL containers each want, the VM's own
# Linux OOM-killer fires before any single container's cgroup cap ever bites
# — it silently killed an in-box `nix build` under agent-issue-565
# (EXIT:137, #565) and, at higher concurrency, took down the whole VM under
# agent-issue-640 (#712: global_oom, no in-box 137, no clean result). Skips
# cleanly when there's no active machine (native Linux, or a non-podman
# runtime): `podman machine inspect` then errors or prints nothing.
check_podman_machine_memory() {
  # `-` (not `:-`): MEMORY_LIMIT="" is a deliberate opt-out (env-schema.nix
  # memoryLimit.default's doc: "empty string disables the limit"), distinct
  # from unset. Same reasoning as CONTINUOUS_DISPATCH above.
  local limit="${MEMORY_LIMIT-5g}"
  [ -z "$limit" ] && return 0
  command -v podman >/dev/null 2>&1 || return 0

  local info
  info="$(podman machine inspect 2>/dev/null)" || return 0
  [ -z "$info" ] && return 0

  local machine_mib
  machine_mib="$(printf '%s' "$info" | jq -r '.[0].Resources.Memory // empty' 2>/dev/null)" || return 0
  [ -z "$machine_mib" ] && return 0

  local limit_mib
  limit_mib="$(_memory_limit_to_mib "$limit")"

  # VM_OVERHEAD_MIB covers the podman machine's own OS/daemon footprint,
  # which competes with the containers for the same fixed VM RAM.
  local -r VM_OVERHEAD_MIB=512
  local required_mib=$(( limit_mib * MAX_PARALLEL + VM_OVERHEAD_MIB ))

  if [ "$machine_mib" -lt "$required_mib" ]; then
    echo "!! podman machine has ${machine_mib}MiB RAM but MEMORY_LIMIT=$limit x MAX_PARALLEL=$MAX_PARALLEL needs ${required_mib}MiB (incl. ${VM_OVERHEAD_MIB}MiB VM overhead)." >&2
    echo "!! the VM's own OOM-killer fires before any single container's --memory cgroup cap ever bites." >&2
    echo "!! fix: lower MAX_PARALLEL, raise podman machine RAM (podman machine set --memory $required_mib; then restart the machine), or lower MEMORY_LIMIT." >&2
    exit 1
  fi
}
check_podman_machine_memory

# Graceful stop: signal this PID with USR1 or TERM (the devShell `dogfood-stop`
# alias does this) to exit after the current wave instead of aborting it. Bash
# defers a trapped signal until the in-flight `nix run` returns, so the wave
# always finishes cleanly; the loop then breaks at the next boundary. Ctrl-C
# (SIGINT to the whole process group) stays the hard-abort escape hatch.
# Written after the dirty-tree check above: the pid file is untracked, and
# writing it first would trip that very check.
stop_requested=0
trap 'stop_requested=1; echo "==> dogfood: stop requested — will exit after the current wave"' USR1 TERM
echo $$ > .dogfood.pid
trap 'rm -f .dogfood.pid' EXIT

iteration=0

echo "==> dogfood: git checkout $BASE_BRANCH && git pull --ff-only"
# An agent's PR merges on $BASE_BRANCH, and the build reads $PWD — so reset to
# the base branch first. A host left on a feature branch (a merged PR's branch,
# a leftover checkout) has no upstream to fast-forward and would break the pull.
git checkout "$BASE_BRANCH"
git pull --ff-only

echo "==> dogfood: nix run .# -- build"
nix run .# -- build

while :; do
  echo "==> dogfood: nix run .# -- $DOGFOOD_KIND --max-jobs $MAX_JOBS --continuous-dispatch $CONTINUOUS_DISPATCH"
  nix_exit=0
  nix run .# -- "$DOGFOOD_KIND" --max-jobs "$MAX_JOBS" --continuous-dispatch "$CONTINUOUS_DISPATCH" || nix_exit=$?

  if [ "$nix_exit" -eq 2 ]; then
    echo "==> dogfood: queue empty — done after $iteration iteration(s)."
    break
  fi

  if [ "$nix_exit" -eq 3 ]; then
    echo "==> dogfood: open issues remain but none are dispatchable — triage needed (a blocked issue may need re-labeling)."
    break
  fi

  if [ "$nix_exit" -eq 4 ]; then
    echo "==> dogfood: image stale — rebuilding and re-invoking"
  elif [ "$nix_exit" -ne 0 ]; then
    echo "!! dogfood: launcher failed (exit $nix_exit)" >&2
    exit 1
  fi

  iteration=$((iteration + 1))
  echo "==> dogfood iteration $iteration complete"

  if [ "$stop_requested" -eq 1 ]; then
    echo "==> dogfood: graceful stop after $iteration iteration(s)."
    break
  fi

  echo "==> dogfood: git checkout $BASE_BRANCH && git pull --ff-only"
  git checkout "$BASE_BRANCH"
  git pull --ff-only

  echo "==> dogfood: nix run .# -- build"
  nix run .# -- build
done
