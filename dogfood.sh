#!/usr/bin/env bash
# Dogfood loop: spindrift building spindrift.
#
# The box's behaviour — entrypoint, toolchain, and prompt — is baked into the OCI
# image at `nix run .#build` time (the merge gate itself lives in the launcher).
# When an agent merges a fix to the base branch, later issues stay blind to it
# until the image is rebuilt from an updated tree. This loop closes both
# staleness sources:
#
#   1. `git checkout $BASE_BRANCH && git pull --ff-only`
#                              — reset to the base branch and pull the just-merged
#                                change into the local tree, which is what
#                                `nix run .#build` reads from ($PWD).
#   2. `nix run .#build`     — re-bake the image from that updated tree.
#
# Each iteration fans out concurrently through the ready set. Concurrency is
# bounded by MAX_PARALLEL (default 3) and MAX_JOBS (default unlimited). The
# pull + rebuild happen between iterations so each wave sees any fix the
# previous wave landed.
#
# Termination is driven by the launcher's exit code — no separate gh probe:
#   exit 0 — dispatched work; loop continues after rebuilding from updated tree.
#   exit 2 — queue empty (no open issues with the dispatch label); loop exits.
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
: "${REPO_SLUG:?set REPO_SLUG=owner/repo in harness.env}"

if [ -n "$(git status --porcelain)" ]; then
  echo "!! working tree is dirty — commit/stash before dogfooding (build reads \$PWD)." >&2
  exit 1
fi

iteration=0

echo "==> dogfood: git checkout $BASE_BRANCH && git pull --ff-only"
# An agent's PR merges on $BASE_BRANCH, and the build reads $PWD — so reset to
# the base branch first. A host left on a feature branch (a merged PR's branch,
# a leftover checkout) has no upstream to fast-forward and would break the pull.
git checkout "$BASE_BRANCH"
git pull --ff-only

echo "==> dogfood: nix run .#build"
nix run .#build

while :; do
  echo "==> dogfood: nix run .#run"
  nix_exit=0
  nix run .#run || nix_exit=$?

  if [ "$nix_exit" -eq 2 ]; then
    echo "==> dogfood: queue empty — done after $iteration iteration(s)."
    break
  fi

  if [ "$nix_exit" -ne 0 ]; then
    echo "!! dogfood: launcher failed (exit $nix_exit)" >&2
    exit 1
  fi

  iteration=$((iteration + 1))
  echo "==> dogfood iteration $iteration complete"

  echo "==> dogfood: git checkout $BASE_BRANCH && git pull --ff-only"
  git checkout "$BASE_BRANCH"
  git pull --ff-only

  echo "==> dogfood: nix run .#build"
  nix run .#build
done
