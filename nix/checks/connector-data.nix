# A Connector registers as data: a plain attribute set that core alone turns
# into its `spindrift.connectors.<name>` row (lib/connector-data.nix), so the
# Connector carries no flake-parts module of its own. Every Consumer here gets
# the in-tree core.
{
  pkgs,
  fixtures,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg hasInfix;
  core = import ../../core;
  connectorData = import ../../lib/connector-data.nix;
  wasm = "${fixtures.connectorWasm}/network.wasm";

  knob = env: secret: {
    inherit env secret;
    group = "dispatch";
    doc = "knob of the data fixture Connector in the connector-data checks";
    flakeOption = false;
    boxEnv = false;
  };
  fixtureRow =
    (import ../fixtures/connector/module.nix { inherit core; }).spindrift.connectors.fixture.driver;
  jiraFragments = import ../../connectors/jira/fragments.nix;
  routes = [
    {
      method = "GET";
      host = "api.datafixture.test";
      path = "/repos";
    }
  ];
  good = {
    name = "datafixture";
    module = _: wasm;
    inherit routes;
    credential = "dataFixtureToken";
    knobs = {
      dataFixtureToken = knob "DATAFIXTURE_TOKEN" true;
      dataFixtureGreeting = (knob "DATAFIXTURE_GREETING" false) // {
        default = "hello";
      };
    };
    fragments = {
      name = "datafixture";
      inherit (jiraFragments) dir shipped;
    };
    driver = fixtureRow // {
      name = "datafixture";
      module = _: wasm;
    };
  };

  consumerWith = modules: fixtures.connectorConsumerVia ([ ../../lib/flakeModule.nix ] ++ modules);
  viaData = consumerWith [ (core.lib.connectorModule core good) ];
  configOf = consumer: consumer.debug.config.spindrift;
  programOf = c: c.apps.${system}.default.program;
  throws = v: !(builtins.tryEval (builtins.deepSeq v v)).success;
  evaluates = c: (builtins.tryEval (builtins.deepSeq (programOf c) true)).success;
  row = (configOf viaData).connectors.datafixture;

  # The same Connector as a hand-written flake-parts module, in the shape every
  # Connector registered in before it could be data.
  handWritten = {
    key = "spindrift-connector-datafixture";
    imports = [ ../../lib/flakeModule.nix ];
    spindrift.connectors.datafixture = {
      inherit (good) knobs fragments driver;
      inherit routes;
      backend = {
        module = _: wasm;
        inherit routes;
        credential = "dataFixtureToken";
      };
    };
  };

  # Each bad data set with the substrings its message must carry: the
  # Connector, the offending key or field, and the types.
  refusals = [
    {
      data = good // {
        shadow = true;
      };
      says = [
        "datafixture"
        "'shadow'"
      ];
    }
    {
      data = good // {
        primary = true;
      };
      says = [ "'primary'" ];
    }
    {
      data = good // {
        shadowEvery = 3;
      };
      says = [ "'shadowEvery'" ];
    }
    {
      data = good // {
        backend = { };
      };
      says = [ "'backend'" ];
    }
    {
      data = good // {
        knbos = { };
      };
      says = [ "'knbos'" ];
    }
    {
      data = good // {
        knobs = [ ];
      };
      says = [
        "datafixture"
        "'knobs'"
        "set"
        "list"
      ];
    }
    {
      data = good // {
        routes = "GET /repos";
      };
      says = [
        "'routes'"
        "list"
        "string"
      ];
    }
    {
      data = good // {
        credential = 1;
      };
      says = [
        "'credential'"
        "string"
        "int"
      ];
    }
    {
      data = good // {
        module = "network.wasm";
      };
      says = [
        "'module'"
        "function"
        "string"
      ];
    }
    {
      data = good // {
        driver = "claude";
      };
      says = [
        "'driver'"
        "set"
        "string"
      ];
    }
    {
      data = good // {
        fragments = [ ];
      };
      says = [
        "'fragments'"
        "list"
      ];
    }
    {
      data = good // {
        gitRemote = 1;
      };
      says = [
        "'gitRemote'"
        "int"
      ];
    }
    {
      data = good // {
        name = 3;
      };
      says = [
        "'name'"
        "string"
        "int"
      ];
    }
    {
      data = good // {
        name = "";
      };
      says = [ "'name'" ];
    }
    {
      data = removeAttrs good [ "module" ];
      says = [
        "datafixture"
        "'routes'"
        "'credential'"
        "'module'"
      ];
    }
    {
      data = removeAttrs good [ "routes" ];
      says = [
        "datafixture"
        "'routes'"
      ];
    }
    {
      data = removeAttrs good [ "credential" ];
      says = [
        "datafixture"
        "'credential'"
      ];
    }
  ];
  refusedWithMessage =
    r:
    let
      msg = connectorData.problem r.data;
    in
    msg != null && builtins.all (s: hasInfix s msg) r.says;
in
{
  connector-data-registers-row =
    assert assertMsg (row.knobs == good.knobs) "a data Connector's knobs must reach its row unchanged";
    assert assertMsg (
      row.fragments.name == "datafixture" && row.fragments.shipped == jiraFragments.shipped
    ) "a data Connector's fragments must reach its row";
    assert assertMsg (row.driver.name == "datafixture") "a data Connector's driver must reach its row";
    assert assertMsg (
      row.routes == routes && row.backend.routes == routes
    ) "a data Connector's routes must reach the row and its backend";
    assert assertMsg (
      row.backend.credential == "dataFixtureToken" && row.backend.scheme == "bearer"
    ) "a data Connector's backend must carry its credential and default to bearer";
    assert assertMsg (
      (configOf viaData).envSchema.dataFixtureToken.env == "DATAFIXTURE_TOKEN"
      && (configOf viaData).envSchema.dataFixtureGreeting.default == "hello"
    ) "a data Connector's knobs must reach the env schema";
    assert assertMsg (evaluates viaData) "a Consumer importing a data Connector must evaluate";
    pkgs.runCommand "connector-data-registers-row" { } "touch $out";

  connector-data-reaches-document =
    pkgs.runCommand "connector-data-reaches-document" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf (programOf viaData)}
        jq -e '[.backends[] | select(.name == "datafixture")] | length == 1
          and (.[0].credentialEnv == "DATAFIXTURE_TOKEN")
          and (.[0].routes == [{method: "GET", host: "api.datafixture.test", path: "/repos"}])
          and (.[0].module | endswith("/network.wasm"))' "$doc"
        jq -e '[.drivers[].name] | index("datafixture") != null' "$doc"
        touch $out
      '';

  # The data path is a shorthand for the module, not a second registration
  # route: both produce the same program from the same row.
  connector-data-equals-flake-module =
    assert assertMsg (
      programOf viaData == programOf (consumerWith [ handWritten ])
    ) "a data Connector must build the program its hand-written flake module builds";
    pkgs.runCommand "connector-data-equals-flake-module" { } "touch $out";

  # The module keeps the key a hand-written Connector module carries, so a
  # Consumer importing it twice dedups instead of failing "registered twice".
  connector-data-module-dedups =
    assert assertMsg (
      core.lib.connectorModule core good ? key
      && (core.lib.connectorModule core good).key == "spindrift-connector-datafixture"
    ) "the module must carry the key `spindrift-connector-<name>`";
    assert assertMsg (evaluates (consumerWith [
      (core.lib.connectorModule core good)
      (core.lib.connectorModule core good)
    ])) "importing a data Connector twice must dedup, not fail with 'registered twice'";
    pkgs.runCommand "connector-data-module-dedups" { } "touch $out";

  # A minimal row is just a name and, optionally, knobs: no backend, driver or
  # fragments appear on it.
  connector-data-minimal-row =
    let
      c = consumerWith [
        (core.lib.connectorModule core {
          name = "knobonly";
          knobs.knobOnlyGreeting = knob "KNOBONLY_GREETING" false;
        })
      ];
      r = (configOf c).connectors.knobonly;
    in
    assert assertMsg (
      r.backend == null && r.driver == null && r.fragments == null && r.routes == [ ]
    ) "a data Connector with no module, driver or fragments must register none";
    assert assertMsg (
      (configOf c).envSchema ? knobOnlyGreeting
    ) "a knob-only data Connector's knob must reach the env schema";
    assert assertMsg (evaluates c) "a knob-only data Connector must evaluate";
    pkgs.runCommand "connector-data-minimal-row" { } "touch $out";

  # `throw` text is out of tryEval's reach, so the messages are pinned on the
  # pure `problem` the validator throws, and the throw itself on tryEval.
  connector-data-refusals =
    assert assertMsg (connectorData.problem good == null) "control: well-formed data has no problem";
    assert assertMsg (connectorData.validate good == good) "control: validate returns good data";
    assert assertMsg (builtins.all refusedWithMessage refusals)
      "every refusal must name the Connector, the offending key or field and its types";
    assert assertMsg (builtins.all (
      r: throws (connectorData.validate r.data)
    ) refusals) "validate must throw on every refused data set";
    assert assertMsg (builtins.all (
      r: throws (core.lib.connectorModule core r.data)
    ) refusals) "core.lib.connectorModule must throw on every refused data set";
    assert assertMsg (
      !(evaluates (consumerWith [ (core.lib.connectorModule core (good // { shadow = true; })) ]))
    ) "a Consumer importing a data Connector with an unknown key must fail evaluation";
    assert assertMsg (hasInfix "attribute set" (
      connectorData.problem [ ]
    )) "data that is no set is refused";
    pkgs.runCommand "connector-data-refusals" { } "touch $out";
}
