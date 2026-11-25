# Issue #7562: a Connector registers only as data (`spindrift.connectorData`),
# and core refuses a second registration of a name however it is expressed.
# Each hostile registration below must fail evaluation of the full flake
# module, and the message it fails with is pinned on the pure function that
# words it: builtins.tryEval cannot read a thrown message.
{
  pkgs,
  fixtures,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    hasInfix
    mkForce
    mkIf
    mkMerge
    mkOverride
    ;
  names = import ../../lib/connector-names.nix;
  connectorData = import ../../lib/connector-data.nix;
  core = import ../../core;
  coreModule = ../../lib/flakeModule.nix;
  githubConnector = (import ../../connectors/github).lib.connector { inherit core; };
  forgejoConnector = (import ../../connectors/forgejo).lib.connector { inherit core; };
  githubData = githubConnector.data;
  forgejoData = forgejoConnector.data;

  consumerWith = modules: fixtures.connectorConsumerVia ([ coreModule ] ++ modules);
  configOf = consumer: consumer.debug.config.spindrift;
  fails = v: !(builtins.tryEval (builtins.deepSeq v true)).success;
  rowsFail = modules: fails (configOf (consumerWith modules)).connectors;
  inFile = file: mod: mod // { _file = file; };
  registers = data: { spindrift.connectorData = [ data ]; };

  # What a hostile module would set on github's row to replace it.
  hostileRow = {
    knobs.evil = {
      env = "EVIL_ENV";
      group = "dispatch";
      doc = "knob a hostile module smuggles into another Connector's row";
      flakeOption = false;
      boxEnv = false;
    };
  };
  hasAll = msg: builtins.all (s: hasInfix s msg);
in
{
  # Hole 1: a whole-row mkForce discarded the lower-priority definitions
  # before the duplicate-row check saw them; so did an override of the whole
  # row set, which dropped every other Connector's row.
  connector-registration-refuses-row-override =
    let
      both = [
        (registers githubData)
        (registers forgejoData)
      ];
      keepsBoth =
        override:
        builtins.attrNames
          (configOf (consumerWith (both ++ [ (inFile "hostile.nix" override) ]))).connectors == [
          "forgejo"
          "github"
        ];
      override = row: [
        githubConnector.flakeModule
        (inFile "hostile.nix" { spindrift.connectors.github = mkForce row; })
      ];
      message = names.rowOutsideDataError "github" "hostile.nix" [ "knobs" ];
      dischargedSwitch = both ++ [ { spindrift.connectors.nosuch = mkIf false { primary = true; }; } ];
    in
    assert assertMsg (
      !(rowsFail [ githubConnector.flakeModule ])
    ) "control: the github Connector alone must evaluate";
    assert assertMsg (rowsFail (
      override hostileRow
    )) "a whole-row mkForce on another Connector's row must fail evaluation";
    assert assertMsg (rowsFail (
      override (connectorData.row pkgs.lib githubData)
    )) "a whole-row mkForce copying the Connector's own row must fail evaluation";
    assert assertMsg (!(rowsFail (override { }))) "control: a mkForce setting no field must evaluate";
    assert assertMsg (
      (configOf (consumerWith (override { }))).connectors.github.routes == githubData.routes
    ) "a mkForce setting no field must not discard the data row";
    assert assertMsg (hasAll message [
      "Connector 'github'"
      "hostile.nix"
      "'knobs'"
      "registers only as data"
      "spindrift.connectorData"
    ]) "the row refusal must name the Connector, the file, the keys and the rule; got ${message}";
    assert assertMsg (keepsBoth {
      spindrift.connectors = mkForce { github.primary = true; };
    }) "a mkForce of the whole row set must not discard another Connector's row";
    assert assertMsg (keepsBoth {
      spindrift = mkForce { connectors = { }; };
    }) "a mkForce of the whole spindrift set must not discard the rows";
    assert assertMsg (keepsBoth {
      spindrift.connectors = mkForce { };
    }) "a mkForce of an empty row set must not discard the rows";
    assert assertMsg (rowsFail (
      both ++ [ { spindrift.connectors = mkForce { github = hostileRow; }; } ]
    )) "a whole-row-set mkForce carrying a hostile row must fail evaluation";
    assert assertMsg (rowsFail (
      both ++ [ { spindrift.connectors.nosuch.primary = true; } ]
    )) "a switch on a name no data registers must fail evaluation, not make a row";
    assert assertMsg (
      !(rowsFail dischargedSwitch)
    ) "a switch on an unregistered name under a false mkIf must evaluate, as it does for attrsOf";
    assert assertMsg (
      !(rowsFail (
        both ++ [ { spindrift.connectors.nosuch = mkMerge [ (mkIf false { primary = true; }) ]; } ]
      ))
    ) "a switch on an unregistered name under a false mkMerge must evaluate";
    assert assertMsg (
      builtins.attrNames (configOf (consumerWith dischargedSwitch)).connectors == [
        "forgejo"
        "github"
      ]
    ) "a discharged switch on an unregistered name must make no row";
    assert assertMsg (rowsFail (
      both ++ [ { spindrift.connectors.nosuch = mkIf true { primary = true; }; } ]
    )) "a live switch on an unregistered name under mkIf must still fail evaluation";
    assert assertMsg (rowsFail (
      both ++ [ { spindrift.connectors.nosuch = mkForce { primary = true; }; } ]
    )) "an element-level mkForce on an unregistered name must fail evaluation";
    assert assertMsg (rowsFail (
      both ++ [ { spindrift.connectors.nosuch = mkOverride 10 { primary = true; }; } ]
    )) "an element-level mkOverride on an unregistered name must fail evaluation";
    assert assertMsg (rowsFail (
      both ++ [ { spindrift.connectors.nosuch = mkForce (mkIf false { primary = true; }); } ]
    )) "an element-level mkForce over a false mkIf is still a defined row and must fail evaluation";
    assert assertMsg (
      names.strayDefiner [
        {
          file = "a.nix";
          value.nosuch = mkForce (mkIf false { primary = true; });
        }
      ] "nosuch" == "a.nix"
    ) "an override is live: the module system never discharges through it";
    assert assertMsg (keepsBoth {
      spindrift.connectors = mkForce { nosuch = mkIf false { primary = true; }; };
    }) "a whole-set mkForce of a discharged switch must evaluate and keep both rows";
    assert assertMsg (
      names.strayDefiner [
        {
          file = "a.nix";
          value.nosuch = mkIf false { primary = true; };
        }
        {
          file = "b.nix";
          value.nosuch.primary = true;
        }
      ] "nosuch" == "b.nix"
    ) "a stray name is blamed on the def that keeps it, not on one that discharged";
    assert assertMsg (hasAll (names.unregisteredRowError "nosuch" "a.nix") [
      "Connector 'nosuch'"
      "a.nix"
      "no Connector 'nosuch' is registered"
      "spindrift.connectorData"
    ]) "the unregistered refusal must name the Connector, the file and the rule";
    pkgs.runCommand "connector-registration-refuses-row-override" { } "touch $out";

  # Hole 2: a module reusing another Connector's key. The module system
  # drops whichever module comes later in the import graph, so a hostile
  # module imported first replaces the real one's registration.
  connector-registration-refuses-key-reuse =
    let
      reuser = row: {
        key = "spindrift-connector-github";
        spindrift.connectors.github = row;
      };
    in
    assert assertMsg (rowsFail [
      (reuser hostileRow)
      githubConnector.flakeModule
    ]) "a module reusing another Connector's key must not replace its row";
    assert assertMsg (rowsFail [
      (reuser hostileRow)
    ]) "a module reusing another Connector's key must not register a row of its own";
    assert assertMsg (
      !(rowsFail [
        githubConnector.flakeModule
        githubConnector.flakeModule
      ])
    ) "control: importing one Connector's module twice dedups by its key";
    pkgs.runCommand "connector-registration-refuses-key-reuse" { } "touch $out";

  connector-registration-refuses-two-data-registrations =
    let
      twice = [
        (inFile "a.nix" (registers githubData))
        (inFile "b.nix" (registers githubData))
      ];
      message = names.duplicateError "github" [
        { file = "a.nix"; }
        { file = "b.nix"; }
      ];
    in
    assert assertMsg (
      !(rowsFail [
        (registers githubData)
        (registers forgejoData)
      ])
    ) "control: two Connectors registered as data must evaluate";
    assert assertMsg (rowsFail twice) "two data registrations of one name must fail evaluation";
    assert assertMsg (rowsFail [
      (registers githubData)
      githubConnector.flakeModule
      (inFile "b.nix" (registers githubData))
    ]) "a data registration beside the Connector's own module must fail evaluation";
    assert assertMsg (rowsFail [
      {
        spindrift.connectorData = [
          githubData
          githubData
        ];
      }
    ]) "one list naming a Connector twice must fail evaluation";
    assert assertMsg (hasAll message [
      "Connector 'github'"
      "registered twice"
      "a.nix"
      "b.nix"
      "a name has one definer"
    ]) "the duplicate message must name the Connector, both files and the rule; got ${message}";
    assert assertMsg (
      names.duplicateError "github" [ { file = "a.nix"; } ] == null
    ) "a single registration must not be reported as a duplicate";
    pkgs.runCommand "connector-registration-refuses-two-data-registrations" { } "touch $out";

  # A Connector's export is its own row: data naming any other spindrift
  # option has no key to carry it.
  connector-registration-refuses-foreign-option =
    let
      foreign = key: githubData // { ${key} = { }; };
      message = connectorData.problem (foreign "hooks");
    in
    assert assertMsg (!(rowsFail [ (registers githubData) ])) "control: well-formed data must evaluate";
    assert assertMsg (rowsFail [
      (registers (foreign "hooks"))
    ]) "data setting another spindrift option must fail evaluation";
    assert assertMsg (rowsFail [
      (registers (foreign "spindrift"))
    ]) "data nesting a `spindrift` set must fail evaluation";
    assert assertMsg (hasAll message [
      "Connector 'github'"
      "'hooks'"
      "declares only its own row"
    ]) "the refusal must name the Connector, the key and the rule; got ${message}";
    pkgs.runCommand "connector-registration-refuses-foreign-option" { } "touch $out";

  # Another definer's registration would vanish with the list.
  connector-registration-refuses-data-override =
    let
      message = names.dataOverrideError [ "github" ];
    in
    assert assertMsg (rowsFail [
      (registers forgejoData)
      { spindrift.connectorData = mkForce [ githubData ]; }
    ]) "mkForce on spindrift.connectorData must fail evaluation";
    assert assertMsg (rowsFail [
      { spindrift.connectorData = mkOverride 90 [ githubData ]; }
    ]) "mkOverride below the default priority on spindrift.connectorData must fail evaluation";
    assert assertMsg (rowsFail [
      (registers forgejoData)
      { spindrift.connectorData = mkForce [ ]; }
    ]) "mkForce of an empty list must fail evaluation";
    assert assertMsg (hasAll message [
      "'github'"
      "mkForce"
      "no definition may discard another's registration"
    ]) "the override refusal must name the surviving Connectors and the rule; got ${message}";
    pkgs.runCommand "connector-registration-refuses-data-override" { } "touch $out";
}
