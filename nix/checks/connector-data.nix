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
  fixtureRow = (import ../fixtures/connector/data.nix { inherit core; }).driver;
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
  programOf = c: c.apps.${system}.connectors-accept.program;
  throws = v: !(builtins.tryEval (builtins.deepSeq v v)).success;
  evaluates = c: (builtins.tryEval (builtins.deepSeq (programOf c) true)).success;
  row = (configOf viaData).connectors.datafixture;

  # The same Connector registered straight into the option the module sets.
  direct = {
    spindrift.connectorData = [ good ];
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
  ];
  refusedWithMessage =
    r:
    let
      msg = connectorData.problem r.data;
    in
    msg != null && builtins.all (s: hasInfix s msg) r.says;
  # A `lib.connector { core }` entry in each of its two forms: the data one,
  # and the entry-level one a Connector not yet moved to data still returns.
  # `module` is a lambda that throws when applied, and `driver.module` throws when
  # forced, so a helper that applied the one or forced the other fails.
  trap = _: throw "a helper applied a wasm module";
  dataEntry = {
    data = good // {
      module = trap;
      driver = good.driver // {
        module = throw "a helper forced driver.module";
      };
    };
  };
  entryForm = {
    inherit (good) fragments;
    driverRow = _lib: good.driver;
    flakeModule =
      { lib }:
      {
        spindrift.connectors = lib.mapAttrs (_: _: { inherit (good) knobs; }) { datafixture = null; };
      };
  };
  badNames = [
    (removeAttrs good [ "name" ])
    (good // { name = 3; })
  ];
  bare = {
    flakeModule = _: { };
  };
  # A Driver row built from core's own rows, as the opencode, pi and claude
  # Connectors do: it needs nixpkgs `lib`, so `driver` is a function of it.
  coreDerivedData =
    { core }:
    {
      name = "corederived";
      knobs = {
        coreDerivedToken = knob "COREDERIVED_TOKEN" true;
      };
      module = _: wasm;
      inherit routes;
      driver =
        lib:
        (core.lib.driverEntries { inherit lib; }).opencode
        // {
          name = "corederived";
          module = _: wasm;
        };
    };
  coreDerivedViaModule = consumerWith [
    (core.lib.connectorModule core (coreDerivedData {
      inherit core;
    }))
  ];
  coreDerivedRow = (configOf coreDerivedViaModule).connectors.corederived;
  # What nix/connector-knobs.nix does: a stub core `{ }`, then the backend filter.
  stubRows = pkgs.lib.filterAttrs (_: r: r ? backend) (
    connectorData.rowsOf { inherit (pkgs) lib; } { data = coreDerivedData { core = { }; }; }
  );
in
{
  connector-data-readers-both-forms =
    assert assertMsg (
      connectorData.fragmentsOf dataEntry == good.fragments
    ) "fragmentsOf must read a data entry's fragments";
    assert assertMsg (
      connectorData.fragmentsOf entryForm == good.fragments
    ) "fragmentsOf must read an entry-form Connector's fragments";
    assert assertMsg (
      connectorData.fragmentsOf bare == null
    ) "fragmentsOf must be null for a Connector with no fragments";
    assert assertMsg (
      connectorData.fragmentsOf { data.name = "knobonly"; } == null
    ) "fragmentsOf must be null for data with no fragments";
    assert assertMsg (
      (connectorData.driverRowOf pkgs.lib dataEntry).name == "datafixture"
    ) "driverRowOf must read a data entry's driver";
    assert assertMsg (
      (connectorData.driverRowOf pkgs.lib entryForm).name == "datafixture"
    ) "driverRowOf must read an entry-form Connector's driverRow";
    assert assertMsg (
      connectorData.driverRowOf pkgs.lib bare == null
    ) "driverRowOf must be null for a Connector with no Driver row";
    assert assertMsg (
      connectorData.driverRowOf pkgs.lib { data.name = "knobonly"; } == null
    ) "driverRowOf must be null for data with no driver";
    assert assertMsg (
      (connectorData.rowsOf { inherit (pkgs) lib; } entryForm).datafixture.knobs == good.knobs
    ) "rowsOf must read an entry-form Connector's module row, given its module args";
    assert assertMsg (
      connectorData.rowsOf { inherit (pkgs) lib; } bare == { }
    ) "rowsOf must be empty for a module that registers no row";
    assert assertMsg (
      (connectorData.rowsOf { } dataEntry).datafixture.fragments == good.fragments
    ) "rowsOf must read a data entry's set-valued row, without module args";
    assert assertMsg (builtins.seq (connectorData.driverRowOf pkgs.lib dataEntry).name true)
      "no reader may apply a data entry's wasm module or force its driver.module";
    pkgs.runCommand "connector-data-readers-both-forms" { } "touch $out";

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
      row.backend.credential == null && row.backend.scheme == "bearer"
    ) "a data Connector's backend must name no credential and default to bearer";
    assert assertMsg (
      connectorData.problem (good // { credential = "dataFixtureToken"; }) == null
    ) "data may still name a credential, for a first-party row";
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
          and (.[0].credentialKnob == "dataFixtureToken")
          and (.[0].routes == [{method: "GET", host: "api.datafixture.test", path: "/repos"}])
          and (.[0].module | endswith("/network.wasm"))' "$doc"
        jq -e '[.drivers[].name] | index("datafixture") != null' "$doc"
        touch $out
      '';

  # The module is a shorthand for the option, not a second registration
  # route: both produce the same program from the same data.
  connector-data-equals-option =
    assert assertMsg (
      programOf viaData == programOf (consumerWith [ direct ])
    ) "a data Connector must build the same program through its module and through the option";
    pkgs.runCommand "connector-data-equals-option" { } "touch $out";

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
    assert assertMsg (hasInfix "'name'" (
      connectorData.problem (removeAttrs good [ "name" ])
    )) "data with no name is refused by name";
    assert assertMsg (builtins.all (
      d: throws (connectorData.rowsOf { } { data = d; })
    ) badNames) "rowsOf must throw on a bad name";
    assert assertMsg (
      map connectorData.problem badNames == [
        "Connector data needs a 'name': a non-empty string, the row's name in spindrift.connectors"
        "Connector data needs a 'name': a non-empty string, the row's name in spindrift.connectors ('name' is int)"
      ]
    ) "a bad name must be refused with the named problem, not a raw Nix error";
    pkgs.runCommand "connector-data-refusals" { } "touch $out";

  # A Driver row derived from core (`driverEntries`) needs `lib`: it must read
  # through the knob reader's stub core, and build the same row through the
  # module system and the entry-level reader.
  connector-data-core-derived-driver =
    let
      entry = {
        data = coreDerivedData { inherit core; };
      };
      opencode = (core.lib.driverEntries { inherit (pkgs) lib; }).opencode;
    in
    assert assertMsg (
      (builtins.tryEval (builtins.deepSeq (builtins.attrNames stubRows) true)).success
      && builtins.attrNames stubRows == [ "corederived" ]
    ) "a core-derived driver must not force the stub core when only the backend filter reads the row";
    assert assertMsg (
      let
        d = connectorData.driverRowOf pkgs.lib entry;
      in
      d.name == "corederived" && d.bin == opencode.bin && d.argvShape == opencode.argvShape
    ) "driverRowOf must apply a function driver to the lib it is given";
    assert assertMsg (
      coreDerivedRow.driver.name == "corederived" && coreDerivedRow.driver.bin == opencode.bin
    ) "a function driver must reach the row through the flake module";
    assert assertMsg (evaluates coreDerivedViaModule)
      "a Consumer importing a core-derived data Connector must evaluate";
    pkgs.runCommand "connector-data-core-derived-driver" { } "touch $out";
}
