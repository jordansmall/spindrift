# Issue #6766: Connector rows register through `spindrift.connectors.<name>`
# and their row-owned knobs fold into the env schema exactly as a core row's
# do (ADR 0073, ADR 0068). Every Consumer here gets the in-tree core, never
# the fixture flake.nix's standalone github pin.
{
  pkgs,
  fixtures,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg filterAttrs isDerivation;
  coreSchema = import ../../lib/env-schema.nix;
  core = import ../../core;
  fixtureConnector = (import ../fixtures/connector).lib.connector { inherit core; };
  coreModule = ../../lib/flakeModule.nix;
  template = (import ../fixtures/connector/flake.nix).outputs { inherit core; };

  withFixture = fixtures.connectorConsumerVia [
    coreModule
    fixtureConnector.flakeModule
  ];
  withNone = fixtures.connectorConsumerVia [ coreModule ];
  withTemplate = fixtures.connectorConsumerVia [
    coreModule
    template.flakeModules.default
  ];
  configOf = consumer: consumer.debug.config.spindrift;
  knobModule = name: key: env: {
    spindrift.connectors.${name}.knobs.${key} = {
      inherit env;
      group = "dispatch";
      doc = "test knob";
      flakeOption = false;
      boxEnv = false;
    };
  };
  duplicateRow = fixtures.connectorConsumerVia [
    coreModule
    (knobModule "dup" "a" "DUP_A")
    (knobModule "dup" "b" "DUP_B")
  ];
  envClash = fixtures.connectorConsumerVia [
    coreModule
    (knobModule "clash" "clashing" "LABEL")
  ];
  fails = v: !(builtins.tryEval (builtins.deepSeq v true)).success;
  drvPathsOf = pkgs': builtins.mapAttrs (_: p: p.drvPath) (filterAttrs (_: isDerivation) pkgs');

  programsOf = builtins.mapAttrs (_: a: a.program);

  fixtureKnob =
    (import ../fixtures/connector/module.nix { inherit core; }).spindrift.connectors.fixture.knobs;
  collisions = (import ../../lib/row-knobs.nix).collisions {
    shared = coreSchema;
    sharedSource = "lib/env-schema.nix";
    registries = [
      {
        sourceOf = name: "spindrift.connectors.${name}";
        rows = [
          {
            name = "clash";
            knobs.clashing = {
              env = "LABEL";
            };
          }
        ];
      }
    ];
  };
in
{
  connectors-fixture-row-registered =
    let
      row = (configOf withFixture).connectors.fixture or null;
    in
    assert assertMsg (
      row != null
    ) "the fixture Connector's module must register spindrift.connectors.fixture";
    assert assertMsg (row.name == "fixture") "the row's name must default to its attr name";
    assert assertMsg (row.knobs == fixtureKnob) "the registered row must carry the module's knobs";
    assert assertMsg (
      ((configOf withTemplate).connectors.fixture or { }).knobs or null == fixtureKnob
    ) "the copyable flake.nix template's flakeModules.default must register the same fixture row";
    pkgs.runCommand "connectors-fixture-row-registered" { } "touch $out";

  connectors-fixture-knob-in-env-schema =
    assert assertMsg (
      (configOf withFixture).envSchema == coreSchema // fixtureKnob
    ) "a Connector row's knobs must fold into spindrift.envSchema on top of the unchanged core schema";
    pkgs.runCommand "connectors-fixture-knob-in-env-schema" { } "touch $out";

  connectors-none-evaluates-as-before =
    assert assertMsg (
      (configOf withNone).connectors == { }
    ) "no Connector must leave spindrift.connectors empty";
    assert assertMsg (
      (configOf withNone).envSchema == coreSchema
    ) "no Connector must leave spindrift.envSchema equal to lib/env-schema.nix";
    assert assertMsg (
      drvPathsOf withNone.packages.${system} == drvPathsOf fixtures.consumerPkgs
    ) "a Consumer without a Connector must evaluate as the plain module Consumer does";
    assert assertMsg (
      withNone.formatter.${system}.drvPath == fixtures.consumerFormatter.drvPath
    ) "a Consumer without a Connector must keep the plain module Consumer's formatter";
    assert assertMsg (
      programsOf withNone.apps.${system} == programsOf fixtures.moduleConsumer.apps.${system}
    ) "a Consumer without a Connector must keep the plain module Consumer's apps";
    assert assertMsg (
      drvPathsOf (withNone.devShells.${system} or { })
      == drvPathsOf (fixtures.moduleConsumer.devShells.${system} or { })
    ) "a Consumer without a Connector must keep the plain module Consumer's devShells";
    pkgs.runCommand "connectors-none-evaluates-as-before" { } "touch $out";

  connectors-fixture-takes-in-tree-core =
    assert assertMsg (
      builtins.functionArgs (import ../fixtures/connector).lib.connector == {
        core = false;
      }
    ) "the fixture entry point must take exactly the core it is handed";
    assert assertMsg (
      fixtureConnector.flakeModule.imports == [ core.flakeModules.default ]
    ) "the fixture module must import the core it was handed";
    pkgs.runCommand "connectors-fixture-takes-in-tree-core" { } "touch $out";

  connectors-env-collision-names-connector =
    assert assertMsg
      (
        collisions == [
          "env LABEL is declared twice: by lib/env-schema.nix entry label and by spindrift.connectors.clash knob clashing"
        ]
      )
      "a Connector knob reusing a core env must collide, naming the Connector; got ${builtins.toJSON collisions}";
    assert assertMsg (fails (configOf envClash)
      .envSchema) "a Connector knob reusing a core env must fail to evaluate spindrift.envSchema";
    pkgs.runCommand "connectors-env-collision-names-connector" { } "touch $out";

  connectors-duplicate-name-fails =
    assert assertMsg (fails (configOf duplicateRow)
      .connectors.dup) "two modules registering one Connector row name must fail to evaluate, not merge";
    pkgs.runCommand "connectors-duplicate-name-fails" { } "touch $out";
}
