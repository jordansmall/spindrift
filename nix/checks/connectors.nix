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
  forgejoConnector = (import ../../connectors/forgejo).lib.connector { inherit core; };
  withForgejo = fixtures.connectorConsumerVia [
    coreModule
    forgejoConnector.flakeModule
  ];
  githubConnector = (import ../../connectors/github).lib.connector { inherit core; };
  withGithub = fixtures.connectorConsumerVia [
    coreModule
    githubConnector.flakeModule
  ];
  piConnector = (import ../../connectors/pi).lib.connector { inherit core; };
  withPi = fixtures.connectorConsumerVia [
    coreModule
    piConnector.flakeModule
  ];
  jiraConnector = (import ../../connectors/jira).lib.connector { inherit core; };
  withJira = fixtures.connectorConsumerVia [
    coreModule
    jiraConnector.flakeModule
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

  # Issue #6875: the pi Connector contributes pi's row, so the Descriptor facts
  # and the in-box preamble it renders match core's, and it carries a module.
  connectors-pi-contributes-driver-row =
    let
      row = (configOf withPi).connectors.pi.driver or null;
    in
    assert assertMsg (row != null) "the pi Connector must contribute a Driver row";
    assert assertMsg (
      drivers.descriptorFacts row == drivers.descriptorFacts drivers.entries.pi
    ) "the pi Connector's row must carry core's pi Descriptor facts";
    assert assertMsg (
      drivers.renderPreamble row == drivers.renderPreamble drivers.entries.pi
    ) "the pi Connector's row must render core's pi preamble";
    assert assertMsg (row ? module) "the pi Connector's row must carry a `module`";
    pkgs.runCommand "connectors-pi-contributes-driver-row" { } "touch $out";

  # The github Connector's module registers its Route table on the row, so
  # the table a Consumer's row carries is the one routes.nix declares.
  connectors-github-contributes-routes =
    let
      routes = (configOf withGithub).connectors.github.routes or null;
    in
    assert assertMsg (routes != null) "the github Connector must register spindrift.connectors.github";
    assert assertMsg (
      routes == import ../../connectors/github/routes.nix
    ) "the github Connector's row must carry the Route table in connectors/github/routes.nix";
    pkgs.runCommand "connectors-github-contributes-routes" { } "touch $out";

  # Issue #7139: the github Connector registers its row and token knob
  # unconditionally, so every Consumer that imports it has them.
  connectors-github-row-unconditional =
    assert assertMsg (
      (configOf withGithub).envSchema ? githubConnectorToken
    ) "the env schema must carry githubConnectorToken";
    assert assertMsg (
      (configOf withGithub).connectors.github.knobs ? githubConnectorToken
      && (configOf withGithub).connectors.github.backend != null
    ) "the github row must carry the githubConnectorToken knob and its backend";
    pkgs.runCommand "connectors-github-row-unconditional" { } "touch $out";

  # Issue #7456: github, jira and forgejo are Connector-only, so a backend row
  # has no native backend to shadow or replace and a Consumer setting
  # `shadow`, `shadowEvery` or `primary` on one is refused. `shadowEvery` has a
  # default, so a row leaving it alone must still evaluate.
  connectors-backend-rows-refuse-switches =
    let
      evaluatesWith =
        connector: modules:
        (builtins.tryEval (
          builtins.deepSeq
            (fixtures.connectorConsumerVia (
              [
                coreModule
                connector.flakeModule
              ]
              ++ modules
            )).apps.${system}.default.program
            true
        )).success;
      rows = {
        github = githubConnector;
        jira = jiraConnector;
        forgejo = forgejoConnector;
      };
      switches = {
        shadow = true;
        shadowEvery = 3;
        primary = true;
      };
      refused =
        name: switch:
        !(evaluatesWith rows.${name} [
          { spindrift.connectors.${name}.${switch} = switches.${switch}; }
        ]);
      # Read off the option itself: `refused` alone would pass on any throw.
      definedWith =
        name: modules:
        (configOf (
          fixtures.connectorConsumerVia (
            [
              coreModule
              rows.${name}.flakeModule
            ]
            ++ modules
          )
        )).connectors.${name}.definedSwitches;
    in
    assert assertMsg (
      connectorNames.backendSwitchError "github" [ ] == null
    ) "a row setting no switch must not be refused";
    assert assertMsg (
      connectorNames.backendSwitchError "github" [ "shadow" ]
      == "Connector 'github': `shadow` cannot be set on a backend row: github is Connector-only, with no native backend to shadow or replace. Delete the setting from spindrift.connectors.github."
    ) "the refusal must name the row and the switch and tell the Consumer to delete it";
    assert assertMsg (
      connectorNames.backendSwitchError "jira" [
        "shadowEvery"
        "primary"
      ]
      == "Connector 'jira': `shadowEvery`, `primary` cannot be set on a backend row: jira is Connector-only, with no native backend to shadow or replace. Delete the setting from spindrift.connectors.jira."
    ) "the refusal must name every switch the row sets";
    assert assertMsg (builtins.all (name: evaluatesWith rows.${name} [ ]) (
      builtins.attrNames rows
    )) "control: a backend row leaving every switch alone must evaluate";
    assert assertMsg (builtins.all (name: builtins.all (refused name) (builtins.attrNames switches)) (
      builtins.attrNames rows
    )) "every backend row must refuse each of `shadow`, `shadowEvery` and `primary`";
    assert assertMsg (builtins.all (name: definedWith name [ ] == [ ]) (
      builtins.attrNames rows
    )) "a row setting no switch must define none: the `shadowEvery` default does not count";
    assert assertMsg (builtins.all (
      name:
      builtins.all (
        switch:
        definedWith name [ { spindrift.connectors.${name}.${switch} = switches.${switch}; } ] == [ switch ]
      ) (builtins.attrNames switches)
    ) (builtins.attrNames rows)) "setting one switch must define exactly that switch";
    assert assertMsg (
      definedWith "github" [ { spindrift.connectors.github.primary = false; } ] == [ "primary" ]
    ) "an explicit `primary = false` still counts as set";
    pkgs.runCommand "connectors-backend-rows-refuse-switches" { } "touch $out";

  connectors-forgejo-contributes-routes =
    let
      routes = (configOf withForgejo).connectors.forgejo.routes or null;
    in
    assert assertMsg (
      routes != null
    ) "the forgejo Connector must register spindrift.connectors.forgejo";
    assert assertMsg (
      routes == import ../../connectors/forgejo/routes.nix
    ) "the forgejo Connector's row must carry the Route table in connectors/forgejo/routes.nix";
    pkgs.runCommand "connectors-forgejo-contributes-routes" { } "touch $out";

  # Issue #6868: the forgejo Connector registers its row and token knob
  # unconditionally; it is the Code Forge for every forgejo run.
  connectors-forgejo-row-unconditional =
    assert assertMsg (
      (configOf withForgejo).envSchema ? forgejoConnectorToken
    ) "the env schema must still carry forgejoConnectorToken";
    assert assertMsg (
      (configOf withForgejo).connectors.forgejo.knobs ? forgejoConnectorToken
      && (configOf withForgejo).connectors.forgejo.backend != null
    ) "the forgejo row must carry the forgejoConnectorToken knob and its backend";
    pkgs.runCommand "connectors-forgejo-row-unconditional" { } "touch $out";

  connectors-jira-contributes-routes =
    let
      routes = (configOf withJira).connectors.jira.routes or null;
      widened = [
        {
          method = "PUT";
          path = "/rest/api/2/issue";
        }
        {
          method = "POST";
          path = "/rest/api/2/issue";
        }
        {
          method = "GET";
          path = "/rest/api/2/label";
        }
      ];
    in
    assert assertMsg (routes != null) "the jira Connector must register spindrift.connectors.jira";
    assert assertMsg (
      routes == import ../../connectors/jira/routes.nix
    ) "the jira Connector's row must carry the Route table in connectors/jira/routes.nix";
    # The writes and the label list are the Routes the Connector's tracker
    # write path needs, so the table must name them.
    assert assertMsg (builtins.all
      (
        r:
        let
          route = r // {
            host = "{base}";
          };
        in
        builtins.elem route routes
      )
      widened
    ) "the jira Route table must carry PUT and POST /rest/api/2/issue and GET /rest/api/2/label";
    pkgs.runCommand "connectors-jira-contributes-routes" { } "touch $out";

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
      shadowable = connectorNames.uniqueRow (
        pkgs.lib.types.submodule {
          options.shadow = pkgs.lib.mkOption {
            type = pkgs.lib.types.bool;
            default = false;
          };
          options.shadowEvery = pkgs.lib.mkOption {
            type = pkgs.lib.types.int;
            default = 10;
          };
        }
      );
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
      message == "spindrift.connectors.dup is registered twice: by a.nix and by b.nix"
    ) "the duplicate-name message must name the option path and both sources; got ${toString message}";
    assert assertMsg (
      threeMessage == "spindrift.connectors.dup is registered 3 times: by a.nix and by b.nix and by c.nix"
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
    # A Consumer's `spindrift.connectors.<name>.shadow = true` is a switch on
    # the registered row, not a second registration.
    assert assertMsg (
      !(fails (
        shadowable.merge
          [ "connectors" "dup" ]
          [
            {
              file = "a.nix";
              value = { };
            }
            {
              file = "b.nix";
              value = {
                shadow = true;
              };
            }
          ]
      ))
    ) "a definition setting only `shadow` must not count as a second registration";
    assert assertMsg (
      !(fails (
        shadowable.merge
          [ "connectors" "dup" ]
          [
            {
              file = "a.nix";
              value = { };
            }
            {
              file = "b.nix";
              value = {
                shadow = true;
                shadowEvery = 3;
              };
            }
          ]
      ))
    ) "a definition setting only `shadow` and `shadowEvery` must not count as a second registration";
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
