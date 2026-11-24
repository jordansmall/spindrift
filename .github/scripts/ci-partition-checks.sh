#!/usr/bin/env bash
# Check partitioner (issue #7523): prints the image set or the main set of
# checks.<system> as installables for `nix build --stdin`, so a workflow can
# build the checks that realise an OCI image in a job of their own. The split
# is by derivation closure, not a hand-kept list: a check is in the image set
# when its build-time requisites include an image derivation, and every other
# check is in the main set. A check that fails to evaluate fails the script,
# so it can never fall out of both sets.
#
# usage: ci-partition-checks.sh SYSTEM image|main [FLAKE_REF]    (default ".")
# env: CI_PARTITION_WORKERS sets the nix-eval-jobs worker count (default 2).
set -euo pipefail

usage() {
  echo "usage: ci-partition-checks.sh SYSTEM image|main [FLAKE_REF]" >&2
  exit 2
}

{ [ $# -ge 2 ] && [ $# -le 3 ]; } || usage
system=$1
set_name=$2
flake=${3:-.}
case $set_name in image | main) ;; *) usage ;; esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# 12288 MiB matches nix/gate.sh's per-worker ceiling: one worker reaches ~10 GB
# over the whole check set; a lower ceiling forces restarts that re-pay
# shared fixtures.
nix-eval-jobs --flake "$flake#checks.$system" --workers "${CI_PARTITION_WORKERS:-2}" \
  --max-memory-size 12288 >"$tmp/jobs"

errors=$(jq -r 'select(has("error")) | "ci-partition-checks: eval error in \(.attr): \(.error)"' "$tmp/jobs")
if [ -n "$errors" ]; then
  printf '%s\n' "$errors" >&2
  exit 1
fi

# The layered image's tarball depends on its stream script, which depends on
# `<name>-customisation-layer` (nixpkgs dockerTools), so the layer alone marks
# all three. `.tar.gz.drv` would also match fetched sources, and `stream` is
# too generic a name. This assumes layered images (buildLayeredImage and
# streamLayeredImage, the only builders in lib/image.nix and
# lib/connector-sandbox-image.nix): a plain `dockerTools.buildImage` has no
# customisation layer and would land in main.
jq -r '.drvPath' "$tmp/jobs" |
  xargs -r nix-store --query --requisites |
  { grep -E -- '-customisation-layer\.drv$' || [ $? -eq 1 ]; } |
  sort -u >"$tmp/layers"

: >"$tmp/dependents"
if [ -s "$tmp/layers" ]; then
  mapfile -t layers <"$tmp/layers"
  nix-store --query --referrers-closure "${layers[@]}" >"$tmp/dependents"
fi

jq -r '[.attr, .drvPath] | @tsv' "$tmp/jobs" |
  prefix="$flake#checks.$system." awk -F'\t' -v set="$set_name" -v deps="$tmp/dependents" '
    BEGIN { while ((getline d < deps) > 0) dep[d] }
    (($2 in dep) ? "image" : "main") == set { print ENVIRON["prefix"] $1 }
  ' |
  sort
