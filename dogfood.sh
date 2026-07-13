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
export MAX_JOBS="${MAX_JOBS:-$MAX_PARALLEL}"
# env-schema.nix continuousDispatch.default is off (empty); dogfood overrides
# it to on so the loop drives slot-refill dispatch instead of one wave and
# exit (#528). `-` (not `:-`) preserves an operator setting CONTINUOUS_DISPATCH=
# (empty) in harness.env to opt back out.
export CONTINUOUS_DISPATCH="${CONTINUOUS_DISPATCH-1}"
: "${REPO_SLUG:?set REPO_SLUG=owner/repo in harness.env}"

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

# Preflight (#580): on macOS/Windows, podman runs containers inside a VM
# ("podman machine") with its own fixed RAM, independent of the per-container
# --memory cap (MEMORY_LIMIT). When the machine has less RAM than that cap,
# the VM's own Linux OOM-killer fires before the cgroup cap ever bites — it
# silently killed an in-box `nix build` under agent-issue-565 (EXIT:137,
# #565). Skips cleanly when there's no active machine (native Linux, or a
# non-podman runtime): `podman machine inspect` then errors or prints nothing.
check_podman_machine_memory() {
  local limit="${MEMORY_LIMIT:-4g}"
  [ -z "$limit" ] && return 0
  command -v podman >/dev/null 2>&1 || return 0

  local info
  info="$(podman machine inspect 2>/dev/null)" || return 0
  [ -z "$info" ] && return 0

  local machine_mib
  machine_mib="$(printf '%s' "$info" | jq -r '.[0].Resources.Memory // empty' 2>/dev/null)"
  [ -z "$machine_mib" ] && return 0

  local limit_mib
  limit_mib="$(_memory_limit_to_mib "$limit")"

  if [ "$machine_mib" -lt "$limit_mib" ]; then
    echo "!! podman machine has ${machine_mib}MiB RAM but MEMORY_LIMIT=$limit needs ${limit_mib}MiB per container." >&2
    echo "!! the VM's own OOM-killer will fire before the --memory cgroup cap ever bites." >&2
    echo "!! fix: podman machine set --memory $limit_mib (then restart the machine)." >&2
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
  echo "==> dogfood: nix run .# -- dispatch"
  nix_exit=0
  nix run .# -- dispatch || nix_exit=$?

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
