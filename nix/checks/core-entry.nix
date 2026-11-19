# Issue #6765: the root flake takes its module from the core entry point, so
# one unchanged Consumer config must evaluate identically through both, apart
# from the github Connector the root module adds for every Consumer (#7139).
# Equal drvPaths mean the core split moved no other Consumer-visible output.
# The core side goes through core/flake.nix's outputs, so the thin wrapper is
# exercised.
{
  pkgs,
  fixtures,
  system,
  rootFlakeModule,
  githubConnectorModule,
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
  coreEntry = (import ../../core/flake.nix).outputs { };
  viaCore = drvPathsVia {
    imports = [
      coreEntry.flakeModules.default
      githubConnectorModule
    ];
  };
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
  # Issue #7139: a Consumer importing the root's `flakeModules.default` and
  # setting neither `primary` nor `shadow` still gets the github backend row.
  core-entry-root-module-carries-github-row =
    pkgs.runCommand "core-entry-root-module-carries-github-row" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf
          (fixtures.moduleConsumerVia rootFlakeModule).apps.${system}.default.program
        }
        jq -e '[.backends[] | select(.credentialKnob == "githubConnectorToken")]
          | length == 1 and (.[0] | (has("primary") | not) and (has("shadow") | not))' "$doc"
        touch $out
      '';

  # The most common opted-in shape: root's module plus the github module
  # imported again through `lib.connector`. The module's `key` dedups the two;
  # without it evaluation fails with "registered twice".
  core-entry-root-and-connector-module-dedup =
    pkgs.linkFarm "core-entry-root-and-connector-module-dedup"
      (
        builtins.mapAttrs
          (
            mode: _:
            pkgs.runCommand "core-entry-root-and-connector-module-dedup-${mode}"
              { nativeBuildInputs = [ pkgs.jq ]; }
              ''
                ${fixtures.runInputDocOf
                  (fixtures.moduleConsumerWith [
                    rootFlakeModule
                    githubConnectorModule
                    { spindrift.connectors.github.${mode} = true; }
                  ]).apps.${system}.default.program
                }
                jq -e '[.backends[] | select(.credentialKnob == "githubConnectorToken")]
                  | length == 1 and .[0].${mode} == true' "$doc"
                touch $out
              ''
          )
          {
            shadow = null;
            primary = null;
          }
      );

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
