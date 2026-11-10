# Issue #6765: the root flake takes its module from the core entry point, so
# one unchanged Consumer config must evaluate identically through both. Equal
# drvPaths mean the core split moved no Consumer-visible output. The core side
# goes through core/flake.nix's outputs, so the thin wrapper is exercised.
{
  pkgs,
  fixtures,
  system,
  rootFlakeModule,
  ...
}:
let
  inherit (pkgs.lib) assertMsg filterAttrs isDerivation;
  drvPathsVia =
    module:
    builtins.mapAttrs (_: p: p.drvPath) (
      filterAttrs (_: isDerivation) (fixtures.moduleConsumerVia module).packages.${system}
    );
  viaRoot = drvPathsVia rootFlakeModule;
  viaCore = drvPathsVia ((import ../../core/flake.nix).outputs { }).flakeModules.default;
  differing = builtins.filter (n: viaRoot.${n} or null != viaCore.${n} or null) (
    builtins.attrNames (viaRoot // viaCore)
  );
  rootLock = builtins.fromJSON (builtins.readFile ../../flake.lock);
  coreLock = builtins.fromJSON (builtins.readFile ../../core/flake.lock);
  staleLockNodes = builtins.filter (
    n: (rootLock.nodes.${n}.locked or null) != coreLock.nodes.${n}.locked
  ) (builtins.filter (n: n != "root") (builtins.attrNames coreLock.nodes));
in
{
  core-entry-consumer-evaluates-identically =
    assert assertMsg (differing == [ ])
      "an unchanged Consumer must evaluate identically through the root and core modules; differing packages: ${toString differing}";
    pkgs.runCommand "core-entry-consumer-evaluates-identically" { } "touch $out";

  # core/flake.lock pins are copied from the root lock; a root bump must not
  # leave core's lock stale.
  core-flake-lock-matches-root =
    assert assertMsg (
      staleLockNodes == [ ]
    ) "core/flake.lock differs from the root flake.lock for nodes: ${toString staleLockNodes}";
    pkgs.runCommand "core-flake-lock-matches-root" { } "touch $out";
}
