# Pins agent-setup's restore-only nix cache key to ci.yml's x86_64-linux save
# key: a mismatch never errors, it silently degrades every agent run to a cold
# build (issue #3937).
{ pkgs, ... }:
let
  inherit (pkgs.lib) assertMsg hasInfix replaceStrings;
  setupSrc = builtins.readFile ../../.github/actions/agent-setup/action.yml;
  ciSrc = builtins.readFile ../../.github/workflows/ci.yml;

  # One template for both sides: ci.yml carries it raw, agent-setup its
  # x86_64-linux render.
  ciKey = "nix-\${{ matrix.nix-system }}-\${{ hashFiles('**/*.nix', '**/flake.lock', '**/go.sum') }}";
  ciPrefix = "nix-\${{ matrix.nix-system }}-";
  render = replaceStrings [ "\${{ matrix.nix-system }}" ] [ "x86_64-linux" ];

  ciHasCacheKey = hasInfix "primary-key: ${ciKey}" ciSrc;
  ciHasCachePrefix = hasInfix "restore-prefixes-first-match: ${ciPrefix}" ciSrc;
  ciHasMatrixLeg = hasInfix "nix-system: x86_64-linux" ciSrc;

  setupHasCacheKey = hasInfix "primary-key: ${render ciKey}" setupSrc;
  setupHasCachePrefix = hasInfix "restore-prefixes-first-match: ${render ciPrefix}" setupSrc;
in
{
  agent-setup-nix-cache-key =
    assert assertMsg ciHasCacheKey
      "ci.yml's cache save primary-key no longer matches the expected templated form ('${ciKey}') — agent-setup's restore key would no longer match (issue #3937).";
    assert assertMsg ciHasCachePrefix
      "ci.yml's cache save restore-prefixes-first-match no longer matches the expected templated form ('${ciPrefix}') — agent-setup's restore prefix would no longer match (issue #3937).";
    assert assertMsg ciHasMatrixLeg
      "ci.yml's matrix no longer has an x86_64-linux leg — agent-setup restores against a cache ci.yml never saves (issue #3937).";
    assert assertMsg setupHasCacheKey
      "agent-setup/action.yml's restore primary-key no longer matches ci.yml's x86_64-linux save key — restore-only misses every run and every agent job silently degrades to a cold build (issue #3937).";
    assert assertMsg setupHasCachePrefix
      "agent-setup/action.yml's restore-prefixes-first-match is no longer '${render ciPrefix}' — it no longer matches ci.yml's x86_64-linux save key (issue #3937).";
    pkgs.runCommand "agent-setup-nix-cache-key" { } "touch $out";
}
