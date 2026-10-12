# Pins agent-setup's restore-only nix cache key to ci.yml's x86_64-linux save
# key: a mismatch never errors, it silently degrades every agent run to a cold
# build (issue #3937). Also pins what keeps ci.yml's saved store worth
# restoring, since each of those failures degrades silently too.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    hasInfix
    length
    replaceStrings
    splitString
    ;
  setupSrc = builtins.readFile ../../.github/actions/agent-setup/action.yml;
  ciSrc = builtins.readFile ../../.github/workflows/ci.yml;

  # One template for both sides: ci.yml carries it raw, agent-setup its
  # x86_64-linux render. Keyed per commit because a cache entry is immutable
  # and a primary-key hit skips the save, so a content-hash key froze the
  # store at the first main commit to reach each hash.
  ciKey = "nix-\${{ matrix.nix-system }}-\${{ github.sha }}";
  ciPrefix = "nix-\${{ matrix.nix-system }}-";
  render = replaceStrings [ "\${{ matrix.nix-system }}" ] [ "x86_64-linux" ];

  ciHasCacheKey = hasInfix "primary-key: ${ciKey}" ciSrc;
  ciHasCachePrefix = hasInfix "restore-prefixes-first-match: ${ciPrefix}" ciSrc;
  ciHasMatrixLeg = hasInfix "nix-system: x86_64-linux" ciSrc;
  # The docs-only lane (issue #5155) restores the same cache outside the matrix.
  ciDocsLaneHasCacheKey = hasInfix "primary-key: ${render ciKey}" ciSrc;
  ciDocsLaneHasCachePrefix = hasInfix "restore-prefixes-first-match: ${render ciPrefix}" ciSrc;

  setupHasCacheKey = hasInfix "primary-key: ${render ciKey}" setupSrc;
  setupHasCachePrefix = hasInfix "restore-prefixes-first-match: ${render ciPrefix}" setupSrc;

  occurrences = needle: length (splitString needle ciSrc) - 1;
  # Both saving jobs: the Linux matrix and macOS.
  savingJobs = 2;
in
{
  agent-setup-nix-cache-key =
    assert assertMsg ciHasCacheKey
      "ci.yml's cache save primary-key no longer matches the expected templated form ('${ciKey}') — agent-setup's restore key would no longer match (issue #3937).";
    assert assertMsg ciHasCachePrefix
      "ci.yml's cache save restore-prefixes-first-match no longer matches the expected templated form ('${ciPrefix}') — agent-setup's restore prefix would no longer match (issue #3937).";
    assert assertMsg ciHasMatrixLeg
      "ci.yml's matrix no longer has an x86_64-linux leg — agent-setup restores against a cache ci.yml never saves (issue #3937).";
    assert assertMsg ciDocsLaneHasCacheKey
      "ci.yml's docs lane restore primary-key no longer matches the x86_64-linux save key ('${render ciKey}') — every docs-only PR silently builds cold (issue #5155).";
    assert assertMsg ciDocsLaneHasCachePrefix
      "ci.yml's docs lane restore-prefixes-first-match is no longer '${render ciPrefix}' — every docs-only PR silently builds cold (issue #5155).";
    assert assertMsg setupHasCacheKey
      "agent-setup/action.yml's restore primary-key no longer matches ci.yml's x86_64-linux save key — restore-only misses every run and every agent job silently degrades to a cold build (issue #3937).";
    assert assertMsg setupHasCachePrefix
      "agent-setup/action.yml's restore-prefixes-first-match is no longer '${render ciPrefix}' — it no longer matches ci.yml's x86_64-linux save key (issue #3937).";
    pkgs.runCommand "agent-setup-nix-cache-key" { } "touch $out";

  ci-nix-cache-keeps-check-outputs =
    # `nix flake check` registers no GC roots, so cache-nix-action's pre-save
    # `nix store gc --max` deleted every check output and each PR rebuilt
    # ~900 derivations from a store holding only toolchains.
    assert assertMsg
      (occurrences "| nix build --stdin --out-link \"$RUNNER_TEMP/check-roots/check\"" == savingJobs)
      "ci.yml no longer roots the check outputs in both saving jobs before the cache save — the pre-save GC deletes them and every PR rebuilds every check.";
    # Per-commit keys never collide, so without a purge each main push adds a
    # full store generation until the 10 GB repo cap LRU-evicts at random.
    assert assertMsg (occurrences "purge-primary-key: never" == savingJobs)
      "ci.yml no longer purges superseded nix store caches in both saving jobs — per-commit keys pile up past the 10 GB repo cap.";
    assert assertMsg (hasInfix "actions: write" ciSrc)
      "ci.yml no longer grants `actions: write` — cache-nix-action's purge needs it to delete superseded caches.";
    pkgs.runCommand "ci-nix-cache-keeps-check-outputs" { } "touch $out";
}
