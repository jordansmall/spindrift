#!/usr/bin/env bash
# Marginal-cost eval probe runner (issue #5860): prints the function calls of
# nix/eval-probe.nix's `one` and `two` plus the second instance's marginal cost
# as JSON on stdout (see that file for why). Reported, never gated; an eval
# failure is still a real failure and exits non-zero.
#
# usage: eval-probe.sh [FLAKE_REF]    (default ".")
set -euo pipefail

flake=${1:-.}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

system=$(nix eval --impure --raw --expr builtins.currentSystem)

calls() {
  local n=$1
  # On a clean tree the eval cache lets a later run skip evaluating the flake's
  # outputs, so `one` and `two` would pay different fixed costs.
  NIX_SHOW_STATS=1 NIX_SHOW_STATS_PATH="$tmp/$n.json" \
    nix eval --no-eval-cache --json "$flake#legacyPackages.$system.evalProbe.$n" >/dev/null
  jq -e '.nrFunctionCalls' "$tmp/$n.json"
}

one=$(calls one)
two=$(calls two)

jq -n --argjson one "$one" --argjson two "$two" \
  '{oneInstanceFunctionCalls: $one, twoInstancesFunctionCalls: $two, marginalFunctionCalls: ($two - $one)}'
