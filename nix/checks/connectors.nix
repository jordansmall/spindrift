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
  inherit (pkgs.lib)
    assertMsg
    filterAttrs
    isDerivation
    ;
  connectorNames = import ../../lib/connector-names.nix;
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
  claudeConnector = (import ../../connectors/claude).lib.connector { inherit core; };
  withClaude = fixtures.connectorConsumerVia [
    coreModule
    claudeConnector.flakeModule
  ];
  drivers = import ../../lib/drivers { inherit (pkgs) lib; };
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
  fails = v: !(builtins.tryEval (builtins.deepSeq v true)).success;
  drvPathsOf = pkgs': builtins.mapAttrs (_: p: p.drvPath) (filterAttrs (_: isDerivation) pkgs');

  programsOf = builtins.mapAttrs (_: a: a.program);

  fixtureKnob =
    (import ../fixtures/connector/module.nix { inherit core; }).spindrift.connectors.fixture.knobs;
  knob = connector: key: env: { inherit connector key env; };
  oneFresh = [ (knob "clash" "clashing" "CLASH_FRESH") ];
  twoFresh = [
    (knob "one" "first" "ONE_ENV")
    (knob "two" "second" "TWO_ENV")
  ];
  collisionsOf =
    knobs:
    (import ../../lib/row-knobs.nix).collisions (
      import ../../lib/connector-knobs.nix (
        map (k: {
          name = k.connector;
          knobs.${k.key} = { inherit (k) env; };
        }) knobs
      )
    );
  consumerOf =
    knobs:
    fixtures.connectorConsumerVia ([ coreModule ] ++ map (k: knobModule k.connector k.key k.env) knobs);
  # A rejection pin: the pure fold names both sources and a Consumer carrying
  # the same knobs cannot evaluate envSchema, while `control` (same shape,
  # distinct key and env) can, so the failure is the collision and nothing else.
  collisionCase =
    name:
    {
      knobs,
      expected,
      control,
    }:
    let
      got = collisionsOf knobs;
    in
    assert assertMsg (
      got == expected
    ) "${name}: expected ${builtins.toJSON expected}; got ${builtins.toJSON got}";
    assert assertMsg (collisionsOf control == [ ]) "${name}: control must not collide";
    assert assertMsg (fails (configOf (consumerOf knobs))
      .envSchema) "${name}: a Consumer with the colliding knobs must fail to evaluate envSchema";
    assert assertMsg (
      !(fails (configOf (consumerOf control)).envSchema)
    ) "${name}: control Consumer must evaluate envSchema";
    pkgs.runCommand name { } "touch $out";
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

  # Issue #6796: the claude Connector contributes claude's row, so the
  # Descriptor facts and the in-box preamble it renders match core's.
  connectors-claude-contributes-driver-row =
    let
      row = (configOf withClaude).connectors.claude.driver or null;
    in
    assert assertMsg (row != null) "the claude Connector must contribute a Driver row";
    assert assertMsg (
      drivers.descriptorFacts row == drivers.descriptorFacts drivers.entries.claude
    ) "the claude Connector's row must carry core's claude Descriptor facts";
    assert assertMsg (
      drivers.renderPreamble row == drivers.renderPreamble drivers.entries.claude
    ) "the claude Connector's row must render core's claude preamble";
    pkgs.runCommand "connectors-claude-contributes-driver-row" { } "touch $out";

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

  connectors-duplicate-name-fails =
    let
      sourced = file: mod: mod // { _file = file; };
      message = connectorNames.duplicateError "dup" [
        { file = "a.nix"; }
        { file = "b.nix"; }
      ];
      threeMessage = connectorNames.duplicateError "dup" [
        { file = "a.nix"; }
        { file = "b.nix"; }
        { file = "c.nix"; }
      ];
      twoModules = fixtures.connectorConsumerVia [
        coreModule
        (sourced "a.nix" (knobModule "dup" "a" "DUP_A"))
        (sourced "b.nix" (knobModule "dup" "b" "DUP_B"))
      ];
      oneModule = fixtures.connectorConsumerVia [
        coreModule
        (sourced "a.nix" (knobModule "dup" "a" "DUP_A"))
      ];
      # The type the module system rebuilds through substSubModules; a lost
      # override would merge two definitions instead of throwing.
      rewrapped = (connectorNames.uniqueRow (pkgs.lib.types.submodule { })).substSubModules [ ];
      mergeFrom =
        files:
        rewrapped.merge [ "connectors" "dup" ] (
          map (file: {
            inherit file;
            value = { };
          }) files
        );
    in
    assert assertMsg (
      message == "perSystem.spindrift.connectors.dup is registered twice: by a.nix and by b.nix"
    ) "the duplicate-name message must name the option path and both sources; got ${toString message}";
    assert assertMsg (
      threeMessage
      == "perSystem.spindrift.connectors.dup is registered 3 times: by a.nix and by b.nix and by c.nix"
    ) "the duplicate-name message must count three definitions; got ${toString threeMessage}";
    assert assertMsg (
      connectorNames.duplicateError "dup" [ { file = "a.nix"; } ] == null
    ) "a single definition must not be reported as a duplicate";
    assert assertMsg (fails (mergeFrom [
      "a.nix"
      "b.nix"
    ])) "uniqueRow must keep throwing on a duplicate after substSubModules rebuilds the type";
    assert assertMsg (
      !(fails (mergeFrom [ "a.nix" ]))
    ) "control: uniqueRow after substSubModules must merge a single definition";
    assert assertMsg (
      (configOf oneModule).connectors.dup.knobs.a.env == "DUP_A"
    ) "control: one module registering the row must evaluate";
    assert assertMsg (fails (configOf twoModules)
      .connectors.dup) "two modules registering one Connector row name must fail to evaluate, not merge";
    pkgs.runCommand "connectors-duplicate-name-fails" { } "touch $out";
}
// builtins.mapAttrs collisionCase {
  connectors-env-collision-names-connector = {
    knobs = [
      (knob "clash" "clashing" "LABEL")
    ];
    expected = [
      "env LABEL is declared twice: by lib/env-schema.nix entry label and by spindrift.connectors.clash knob clashing"
    ];
    control = oneFresh;
  };

  connectors-key-collision-names-connector = {
    knobs = [
      (knob "clash" "label" "CLASH_FRESH")
    ];
    expected = [
      "key label is declared twice: by lib/env-schema.nix entry label and by spindrift.connectors.clash knob label"
    ];
    control = oneFresh;
  };

  connectors-core-row-env-collision-names-connector = {
    knobs = [
      (knob "clash" "clashing" "PODMAN_NETWORK")
    ];
    expected = [
      "env PODMAN_NETWORK is declared twice: by lib/runners/oci.nix knob podmanNetwork and by spindrift.connectors.clash knob clashing"
    ];
    control = oneFresh;
  };

  connectors-core-row-key-collision-names-connector = {
    knobs = [
      (knob "clash" "podmanNetwork" "CLASH_FRESH")
    ];
    expected = [
      "key podmanNetwork is declared twice: by lib/runners/oci.nix knob podmanNetwork and by spindrift.connectors.clash knob podmanNetwork"
    ];
    control = oneFresh;
  };

  connectors-connector-env-collision-names-both = {
    knobs = [
      (knob "one" "first" "SHARED_ENV")
      (knob "two" "second" "SHARED_ENV")
    ];
    expected = [
      "env SHARED_ENV is declared twice: by spindrift.connectors.one knob first and by spindrift.connectors.two knob second"
    ];
    control = twoFresh;
  };

  connectors-connector-key-collision-names-both = {
    knobs = [
      (knob "one" "shared" "ONE_ENV")
      (knob "two" "shared" "TWO_ENV")
    ];
    expected = [
      "key shared is declared twice: by spindrift.connectors.one knob shared and by spindrift.connectors.two knob shared"
    ];
    control = twoFresh;
  };
}
