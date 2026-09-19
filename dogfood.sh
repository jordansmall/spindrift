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
# `spindrift doctor`'s podman-machine-memory row (#3537) runs the same
# arithmetic; VM_OVERHEAD_MIB below is duplicated in
# cmd/launcher/podmanmachine_doctor_checks.go because this loop must refuse to
# start before any launcher binary is built.
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

# Signal this PID with USR1 or TERM (the devShell `dogfood-stop` alias) to wind
# the loop down: under CONTINUOUS_DISPATCH the trap forwards SIGTERM to the
# in-flight launcher, which drains the Boxes it already claimed and exits 7 (a
# clean stop below). That refill loop keeps the launcher busy until the whole
# queue is gone, so latching stop_requested alone would defer the stop
# indefinitely; stop_requested remains only as the backstop for a launcher that
# exits on its own before the signal reaches it. Under the opt-out nothing is
# forwarded: the launcher installs its SIGTERM handler (installStopSignal,
# cmd/launcher/main.go) only on the continuous path, so a forwarded TERM would
# take Go's default disposition and kill it mid-wave with its Boxes and
# registry proxies still up — and without refill the wave it is draining is
# itself the boundary the backstop waits for. Ctrl-C stays the hard abort: see
# `abort` below. The pid file is untracked, so it is written after the
# dirty-tree check it would otherwise trip.

# Mirrors how the launcher reads a `--<flag>=<value>` bool (cmd/launcher/flags.go):
# empty, `0`, and `false` are the only off-values.
bool_is_on() {
  case "$1" in
    "" | 0 | false) return 1 ;;
    *) return 0 ;;
  esac
}
stop_requested=0
launcher_pid=""
wait_interrupted=0
request_stop() {
  stop_requested=1
  wait_interrupted=1
  if [ -n "$launcher_pid" ] && bool_is_on "$CONTINUOUS_DISPATCH"; then
    echo "==> dogfood: stop requested — forwarding SIGTERM to the launcher, which drains its in-flight Boxes before exiting"
    kill -TERM "$launcher_pid" 2>/dev/null || true
  else
    echo "==> dogfood: stop requested — will exit after the current wave"
  fi
}
# Prints, one per line, the descendants of $1 that share its process group,
# walked breadth-first from a single `ps` snapshot. Prints nothing where `ps`
# is unavailable, leaving the caller's own signalling to carry on.
same_group_descendants() {
  local root="$1" snapshot pid ppid pgid root_pgid="" frontier next
  local found=()
  snapshot="$(ps -eo pid=,ppid=,pgid= 2>/dev/null)" || return 0
  while read -r pid ppid pgid; do
    if [ "$pid" = "$root" ]; then root_pgid="$pgid"; fi
  done <<<"$snapshot"
  [ -n "$root_pgid" ] || return 0
  frontier=" $root "
  while [ -n "${frontier// /}" ]; do
    next=""
    while read -r pid ppid pgid; do
      if [[ "$frontier" == *" $ppid "* ]]; then
        next+=" $pid "
        if [ "$pgid" = "$root_pgid" ]; then found+=("$pid"); fi
      fi
    done <<<"$snapshot"
    frontier="$next"
  done
  if [ "${#found[@]}" -gt 0 ]; then printf '%s\n' "${found[@]}"; fi
}
abort() {
  # Ctrl-C has to reach the launcher's children too: `podman run` is started
  # without `--rm` (cmd/launcher/internal/runner/oci.go), so a surviving client leaves a
  # container behind, and the SIG_IGN noted above the backgrounded `nix run`
  # below makes the terminal's own SIGINT a no-op throughout that whole tree.
  # TERM is what still lands there, and `podman run`'s signal proxy forwards
  # it to the container's PID 1. The pgid filter is load-bearing: NixRealizer
  # forks its background `nix build` into its own process group (Setpgid,
  # cmd/launcher/internal/runner/nixrealize.go) precisely so a Ctrl-C aimed at this loop's
  # group spares it (docs/reference.md, "Background realize process
  # isolation"), and matching on pgid honours that by construction. The
  # launcher itself gets HUP, not TERM: TERM is this script's *drain* request
  # (request_stop above) whereas Ctrl-C is a hard abort, and HUP is the one
  # signal bash leaves at its default disposition in an async command.
  # Deliberately no `wait`: before the launcher was backgrounded, Ctrl-C
  # reached shell and launcher at once and the shell exited immediately.
  local pid
  if [ -n "$launcher_pid" ]; then
    for pid in $(same_group_descendants "$launcher_pid"); do
      kill -TERM "$pid" 2>/dev/null || true
    done
    kill -HUP "$launcher_pid" 2>/dev/null || true
  fi
  exit 130
}
trap request_stop USR1 TERM
trap abort INT
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
  # Backgrounded so the USR1/TERM trap above can forward SIGTERM to it. `<&0`
  # is load-bearing: bash redirects an async command's stdin from /dev/null
  # "in the absence of any explicit redirections", which would hide the
  # terminal from the launcher's isInteractiveTTY (flags.go) and so from any
  # SPINDRIFT_GH_TOKEN_CMD vault-unlock prompt. No `set -m` (no job control):
  # that keeps the launcher in this loop's own process group, which is the
  # terminal's foreground group, so a vault prompt's tty read stays a legal
  # foreground read instead of raising SIGTTIN and stopping the launcher
  # (`ps` state T) — the bug this replaces. The cost is that bash hard-ignores
  # SIGINT/SIGQUIT (SIG_IGN) in an async command with job control off, an
  # ignore inherited through `nix run` into the launcher, so `abort` above
  # relays SIGHUP instead of SIGINT.
  nix run "$NIX_APP" -- "$DOGFOOD_KIND" --max-jobs "$MAX_JOBS" --continuous-dispatch="$CONTINUOUS_DISPATCH" <&0 &
  launcher_pid=$!
  while :; do
    wait_interrupted=0
    nix_exit=0
    wait "$launcher_pid" || nix_exit=$?
    # A trapped signal makes `wait` return 128+signum without reaping the
    # launcher, so that status is not the launcher's own — wait again for the
    # real one. bash keeps a reaped child's status in its jobs table, so the
    # re-wait still lands it even when the launcher exited inside the handler;
    # a `kill -0` liveness probe would see nothing there and keep 128+signum.
    [ "$wait_interrupted" -eq 1 ] && [ "$nix_exit" -gt 128 ] || break
  done
  launcher_pid=""

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

  if [ "$nix_exit" -eq 7 ]; then
    echo "==> dogfood: stopped on request — launcher drained and exited after a SIGTERM."
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
