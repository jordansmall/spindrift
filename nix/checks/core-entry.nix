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
  githubConnectorData,
  forgejoConnectorData,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    filterAttrs
    isDerivation
    ;
  names = import ../../lib/connector-names.nix;
  drvPathsVia =
    module:
    builtins.mapAttrs (_: p: p.drvPath) (
      filterAttrs (_: isDerivation) (fixtures.moduleConsumerVia module).packages.${system}
    );
  viaRoot = drvPathsVia rootFlakeModule;
  coreEntry = (import ../../core/flake.nix).outputs { };
  viaCore = drvPathsVia {
    imports = [ coreEntry.flakeModules.default ];
    spindrift.connectorData = [
      githubConnectorData
      forgejoConnectorData
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

  # The root module already registers github and forgejo, so a Consumer
  # registering either again is refused, not deduplicated (issue #7562): a name
  # has one definer. The message is pinned on its pure function, as
  # builtins.tryEval cannot read a thrown message.
  rootConfigWith =
    modules: (fixtures.connectorConsumerVia ([ rootFlakeModule ] ++ modules)).debug.config.spindrift;
  inherit (fixtures) fails hasAll;
  rootAndConnectorDataRefused =
    {
      name,
      data,
    }:
    let
      message = names.duplicateError data.name [
        { file = rootFlakeModule._file; }
        { file = "consumer.nix"; }
      ];
    in
    assert assertMsg (
      !(fails (rootConfigWith [ ]).connectors)
    ) "control: the root module alone must evaluate";
    assert assertMsg (fails
      (rootConfigWith [
        {
          _file = "consumer.nix";
          spindrift.connectorData = [ data ];
        }
      ]).connectors
    ) "the root module plus a second registration of ${data.name} must fail evaluation";
    assert assertMsg (hasAll message [
      "registered twice"
      rootFlakeModule._file
      "consumer.nix"
    ]) "the duplicate message must name both definers; got ${message}";
    pkgs.runCommand name { } "touch $out";

  # A Consumer's jira input follows its `spindrift` input; the batteries root
  # does not carry jira, so the Consumer registers it once from its own input.
  jiraData = ((import ../../connectors/jira).lib.connector { core = import ../../core; }).data;
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

  core-entry-root-and-forgejo-connector-data-refused = rootAndConnectorDataRefused {
    name = "core-entry-root-and-forgejo-connector-data-refused";
    data = forgejoConnectorData;
  };

  # Issue #7417: the batteries root does not carry jira, so a Consumer registers
  # it from its own flake input; registering it twice fails evaluation.
  core-entry-jira-connector-data-registered-once =
    assert assertMsg (
      !(fails (rootConfigWith [ { spindrift.connectorData = [ jiraData ]; } ]).connectors)
    ) "control: jira registered once beside the root module must evaluate";
    assert assertMsg (fails
      (rootConfigWith [
        { spindrift.connectorData = [ jiraData ]; }
        { spindrift.connectorData = [ jiraData ]; }
      ]).connectors
    ) "jira registered twice must fail evaluation";
    pkgs.runCommand "core-entry-jira-connector-data-registered-once" { } "touch $out";

  core-entry-root-and-connector-data-refused = rootAndConnectorDataRefused {
    name = "core-entry-root-and-connector-data-refused";
    data = githubConnectorData;
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
