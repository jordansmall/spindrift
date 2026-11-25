# Issue #6765: the root flake takes its module from the core entry point, so
# one unchanged Consumer config must evaluate identically through both, apart
# from the github and forgejo Connectors the root module adds for every
# Consumer (#7139, #6868). Equal drvPaths mean the core split moved no other
# Consumer-visible output.
# The core side goes through core/flake.nix's outputs, so the thin wrapper is
# exercised.
{
  pkgs,
  fixtures,
  system,
  rootFlakeModule,
  githubConnectorModule,
  forgejoConnectorModule,
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
      forgejoConnectorModule
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

  # The most common opted-in shape: root's module plus the Connector module
  # imported again through `lib.connector`. The module's `key` dedups the two;
  # without it evaluation fails with "registered twice".
  rootAndConnectorModuleDedup =
    {
      name,
      module,
      credentialKnob,
    }:
    pkgs.runCommand name { nativeBuildInputs = [ pkgs.jq ]; } ''
      ${fixtures.runInputDocOf
        (fixtures.moduleConsumerWith [
          rootFlakeModule
          module
        ]).apps.${system}.default.program
      }
      jq -e '[.backends[] | select(.credentialKnob == "${credentialKnob}")] | length == 1' "$doc"
      touch $out
    '';

  # A Consumer's jira input follows its `spindrift` input, so the Connector's
  # `core` is the batteries root; two `lib.connector` calls give two distinct
  # module values, which only the module's `key` can dedup.
  rootAsCore = {
    inherit (import ../../core) lib;
    flakeModules.default = rootFlakeModule;
  };
  jiraViaRoot = (import ../../connectors/jira).lib.connector { core = rootAsCore; };
  jiraViaRootAgain = (import ../../connectors/jira).lib.connector { core = rootAsCore; };
  githubViaRoot = (import ../../connectors/github).lib.connector { core = rootAsCore; };
in
{
  # Issue #7139: a Consumer importing the root's `flakeModules.default` still
  # gets the github backend row, carrying neither `primary` nor `shadow`.
  core-entry-root-module-carries-github-row =
    pkgs.runCommand "core-entry-root-module-carries-github-row" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf
          (fixtures.moduleConsumerVia rootFlakeModule).apps.${system}.default.program
        }
        jq -e '[.backends[] | select(.credentialKnob == "ghToken")]
          | length == 1 and (.[0] | (has("primary") | not) and (has("shadow") | not))' "$doc"
        touch $out
      '';

  # Issue #6868: the same for the forgejo row, which is the Code Forge for
  # every forgejo run and so must exist for a Consumer that never imported it.
  core-entry-root-module-carries-forgejo-row =
    pkgs.runCommand "core-entry-root-module-carries-forgejo-row" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf
          (fixtures.moduleConsumerVia rootFlakeModule).apps.${system}.default.program
        }
        jq -e '[.backends[] | select(.credentialKnob == "forgejoToken")]
          | length == 1 and (.[0] | (has("primary") | not) and (has("shadow") | not))' "$doc"
        touch $out
      '';

  core-entry-root-and-forgejo-connector-module-dedup = rootAndConnectorModuleDedup {
    name = "core-entry-root-and-forgejo-connector-module-dedup";
    module = forgejoConnectorModule;
    credentialKnob = "forgejoToken";
  };

  # Issue #7417: the batteries root does not carry jira, so a Consumer imports
  # it from its own flake input; importing it twice must not fail.
  core-entry-jira-connector-module-imported-twice =
    pkgs.runCommand "core-entry-jira-connector-module-imported-twice"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf
          (fixtures.moduleConsumerWith [
            rootFlakeModule
            jiraViaRoot.flakeModule
            jiraViaRootAgain.flakeModule
          ]).apps.${system}.default.program
        }
        jq -e '[.backends[] | select(.credentialKnob == "jiraToken")] | length == 1' "$doc"
        touch $out
      '';

  # A data-form Connector imports the `core` it was handed, so with the batteries
  # root as `core` its module alone still brings the root's forgejo row.
  core-entry-github-connector-module-imports-handed-core =
    pkgs.runCommand "core-entry-github-connector-module-imports-handed-core"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf
          (fixtures.moduleConsumerWith [ githubViaRoot.flakeModule ]).apps.${system}.default.program
        }
        jq -e '[.backends[] | select(.credentialKnob == "forgejoToken")] | length == 1' "$doc"
        jq -e '[.backends[] | select(.credentialKnob == "ghToken")] | length == 1' "$doc"
        touch $out
      '';

  core-entry-root-and-connector-module-dedup = rootAndConnectorModuleDedup {
    name = "core-entry-root-and-connector-module-dedup";
    module = githubConnectorModule;
    credentialKnob = "ghToken";
  };

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
