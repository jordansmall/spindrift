# Aggregator: merges the per-concern check modules into the single attrset
# flake.nix wires into `perSystem.checks` (issue #454). The shared arg bundle
# is hoisted once here so every module sees the exact same fixtures/pkgs
# instead of each re-deriving its own.
{
  pkgs,
  config,
  fixtures,
  nixpkgs,
  system,
  flake-parts,
}:
let
  common = {
    inherit
      pkgs
      config
      fixtures
      nixpkgs
      system
      flake-parts
      ;
  };
in
(import ./bats.nix common)
// (import ./equivalence.nix common)
// (import ./prompts.nix common)
// (import ./schema-drift.nix common)
// (import ./dispatch-labels.nix common)
// (import ./changelog.nix common)
// (import ./go.nix common)
// pkgs.lib.optionalAttrs pkgs.stdenv.isLinux (import ./image.nix common)
