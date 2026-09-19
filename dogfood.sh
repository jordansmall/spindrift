#!/usr/bin/env bash
# Dogfood loop: spindrift building spindrift. The box's behaviour is baked at
# `nix run $NIX_APP -- build` time from $PWD, so a merged fix stays invisible
# until this loop checks out $BASE_BRANCH, pulls, and rebuilds. Launcher exit 4
# asks for that (under bwrap a stale image hot-swaps in place, ADR 0043, #2682).
set -euo pipefail

cd "$(dirname "$0")"

# REPO_SLUG and GH_TOKEN come from harness.env, a gitignored env file.
if [ -f harness.env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./harness.env
  set +a
fi
BASE_BRANCH="${BASE_BRANCH:-main}"      # must match env-schema.nix baseBranch.default
MAX_PARALLEL="${MAX_PARALLEL:-3}"      # must match env-schema.nix maxParallel.default
case "$MAX_PARALLEL" in
  *[!0-9]* | 0[0-9]*)
    echo "!! MAX_PARALLEL must be a non-negative integer, got: $MAX_PARALLEL" >&2
    exit 1
    ;;
esac
MAX_JOBS="${MAX_JOBS:-$MAX_PARALLEL}"
# env-schema.nix defaults continuousDispatch off; dogfood turns it on so the
# launcher refills each freed slot instead of draining one wave (#527, #528).
# `-` not `:-` so an operator can set CONTINUOUS_DISPATCH= in harness.env to opt out.
CONTINUOUS_DISPATCH="${CONTINUOUS_DISPATCH-1}"
: "${REPO_SLUG:?set REPO_SLUG=owner/repo in harness.env}"
# Both Dispatch kinds share the launcher's exit-code contract, so the loop below
# needs no other change to drive research instead of work (ADR 0022).
DOGFOOD_KIND="${DOGFOOD_KIND:-dispatch}"
case "$DOGFOOD_KIND" in
  dispatch | research) ;;
  *)
    echo "!! DOGFOOD_KIND must be 'dispatch' or 'research', got: $DOGFOOD_KIND" >&2
    exit 1
    ;;
esac

# NIX_APP is resolved once here so every `nix run` below shares one target (#2672).
DOGFOOD_RUNTIME="${DOGFOOD_RUNTIME:-podman}"
case "$DOGFOOD_RUNTIME" in
  podman) NIX_APP=".#" ;;
  bwrap) NIX_APP=".#dogfood-bwrap" ;;
  *)
    echo "!! DOGFOOD_RUNTIME must be 'podman' or 'bwrap', got: $DOGFOOD_RUNTIME" >&2
    exit 1
    ;;
esac

# bwrap is Linux-only (bubblewrap has no macOS build). Rejecting it here gives a
# clear message instead of an opaque failure deep inside the launcher (#2672).
if [ "$DOGFOOD_RUNTIME" = "bwrap" ] && [ "$(uname -s)" != "Linux" ]; then
  echo "!! DOGFOOD_RUNTIME=bwrap requires Linux (bubblewrap is not available on macOS)." >&2
  exit 1
fi

if [ -n "$(git status --porcelain)" ]; then
  echo "!! working tree is dirty — commit/stash before dogfooding (build reads \$PWD)." >&2
  exit 1
fi

# Converts a --memory value to MiB, to compare against `podman machine inspect`'s
# Resources.Memory. No suffix means bytes, matching podman's own --memory parsing.
_memory_limit_to_mib() {
  local limit="$1"
  case "$limit" in
    *[Gg]) echo $(( ${limit%[Gg]} * 1024 )) ;;
    *[Mm]) echo "${limit%[Mm]}" ;;
    *[Kk]) echo $(( ${limit%[Kk]} / 1024 )) ;;
    *) echo $(( limit / 1024 / 1024 )) ;;
  esac
}

# Preflight (#580, parallelism-aware per #712): on macOS/Windows podman runs
# containers in a VM with fixed RAM, so when MAX_PARALLEL containers want more
# than the machine has, the VM's OOM-killer fires before any single container's
# --memory cap bites (#565 killed an in-box `nix build`; #712 took down the whole
# VM). With no active machine, `podman machine inspect` errors or prints nothing.
check_podman_machine_memory() {
  # `-` not `:-`: MEMORY_LIMIT= is a deliberate opt-out (env-schema.nix
  # memoryLimit.default disables the limit on an empty string), distinct from unset.
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

  # The machine's own OS and daemon compete with the containers for that VM RAM.
  local -r VM_OVERHEAD_MIB=512
  local required_mib=$(( limit_mib * MAX_PARALLEL + VM_OVERHEAD_MIB ))

  if [ "$machine_mib" -lt "$required_mib" ]; then
    echo "!! podman machine has ${machine_mib}MiB RAM but MEMORY_LIMIT=$limit x MAX_PARALLEL=$MAX_PARALLEL needs ${required_mib}MiB (incl. ${VM_OVERHEAD_MIB}MiB VM overhead)." >&2
    echo "!! the VM's own OOM-killer fires before any single container's --memory cgroup cap ever bites." >&2
    echo "!! fix: lower MAX_PARALLEL, raise podman machine RAM (podman machine set --memory $required_mib; then restart the machine), or lower MEMORY_LIMIT." >&2
    exit 1
  fi
}
# bwrap is daemonless and has no VM, so this preflight cannot apply to it.
if [ "$DOGFOOD_RUNTIME" = "podman" ]; then
  check_podman_machine_memory
fi

# Signal this PID with USR1 or TERM (the devShell `dogfood-stop` alias) to exit
# after the current wave: bash defers the trap until the in-flight `nix run`
# returns. Ctrl-C stays the hard abort, though a backgrounded `nix build` from
# NixRealizer deliberately survives it (docs/reference.md). The pid file is
# untracked, so it is written after the dirty-tree check it would otherwise trip.
stop_requested=0
trap 'stop_requested=1; echo "==> dogfood: stop requested — will exit after the current wave"' USR1 TERM
mkdir -p .spindrift
echo $$ > .spindrift/dogfood.pid
trap 'rm -f .spindrift/dogfood.pid' EXIT

iteration=0

echo "==> dogfood: git checkout $BASE_BRANCH && git pull --ff-only"
# The build reads $PWD, so reset to the base branch first. A host left on a
# feature branch has no upstream to fast-forward and the pull would fail.
git checkout "$BASE_BRANCH"
git pull --ff-only

echo "==> dogfood: nix run $NIX_APP -- build"
nix run "$NIX_APP" -- build

while :; do
  echo "==> dogfood: nix run $NIX_APP -- $DOGFOOD_KIND --max-jobs $MAX_JOBS --continuous-dispatch=$CONTINUOUS_DISPATCH"
  nix_exit=0
  nix run "$NIX_APP" -- "$DOGFOOD_KIND" --max-jobs "$MAX_JOBS" --continuous-dispatch="$CONTINUOUS_DISPATCH" || nix_exit=$?

  if [ "$nix_exit" -eq 2 ]; then
    echo "==> dogfood: queue empty — done after $iteration iteration(s)."
    break
  fi

  if [ "$nix_exit" -eq 3 ]; then
    if [ "$stop_requested" -eq 1 ]; then
      echo "==> dogfood: graceful stop after $iteration iteration(s)."
      break
    fi

    # Pull anyway, as a backstop for staleness the freshness probe cannot see,
    # such as a label flip on the tracker. If HEAD does not move, the block is
    # genuine and needs human triage, typically a failed blocker to re-label.
    head_before="$(git rev-parse HEAD)"
    echo "==> dogfood: git checkout $BASE_BRANCH && git pull --ff-only"
    git checkout "$BASE_BRANCH"
    git pull --ff-only

    if [ "$(git rev-parse HEAD)" = "$head_before" ]; then
      echo "==> dogfood: open issues remain but none are dispatchable — triage needed (a blocked issue may need re-labeling)."
      break
    fi

    echo "==> dogfood: pull advanced HEAD past a prior none-dispatchable exit — rebuilding and retrying once"
    echo "==> dogfood: nix run $NIX_APP -- build"
    nix run "$NIX_APP" -- build
    continue
  fi

  if [ "$nix_exit" -eq 5 ]; then
    echo "==> dogfood: halting — non-converging (host-tainted) image divergence, a rebuild cannot fix this."
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

  echo "==> dogfood: nix run $NIX_APP -- build"
  nix run "$NIX_APP" -- build
done
