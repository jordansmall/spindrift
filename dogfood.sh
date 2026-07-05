#!/usr/bin/env bash
# Dogfood loop: spindrift building spindrift, one issue at a time.
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
# It runs MAX_JOBS=1 so each `nix run .#run` drains exactly one issue; the pull +
# rebuild happen between issues. Strictly serial by design — that is the point:
# it guarantees issue N+1 runs against the image issue N produced.
set -euo pipefail

cd "$(dirname "$0")"

# REPO_SLUG (and GH_TOKEN, for the readiness query) come from harness.env, the
# gitignored env file sourced into the harness. LABEL falls back to the default.
if [ -f harness.env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./harness.env
  set +a
fi
LABEL="${LABEL:-ready-for-agent}"
BASE_BRANCH="${BASE_BRANCH:-main}"
: "${REPO_SLUG:?set REPO_SLUG=owner/repo in harness.env}"

if [ -n "$(git status --porcelain)" ]; then
  echo "!! working tree is dirty — commit/stash before dogfooding (build reads \$PWD)." >&2
  exit 1
fi

iteration=0
while :; do
  remaining="$(gh issue list --repo "$REPO_SLUG" --state open \
    --label "$LABEL" --json number --jq 'length')"
  if [ "$remaining" -eq 0 ]; then
    echo "==> dogfood: no '$LABEL' issues left — done after $iteration iteration(s)."
    break
  fi
  iteration=$((iteration + 1))
  echo "==> dogfood iteration $iteration: $remaining '$LABEL' issue(s) remaining"

  echo "==> dogfood: git checkout $BASE_BRANCH && git pull --ff-only"
  # An agent's PR merges on $BASE_BRANCH, and the build reads $PWD — so reset to
  # the base branch first. A host left on a feature branch (a merged PR's branch,
  # a leftover checkout) has no upstream to fast-forward and would break the pull.
  git checkout "$BASE_BRANCH"
  git pull --ff-only

  echo "==> dogfood: nix run .#build"
  nix run .#build

  echo "==> dogfood: nix run .#run (MAX_JOBS=1)"
  MAX_JOBS=1 nix run .#run
done
