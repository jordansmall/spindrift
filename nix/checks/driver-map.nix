# Eval-level checks for the multi-Driver consumer flake (ADR 0064, issue
# #5052 slice 1): the `driverValued` marking on the knob schema rows and the
# pure resolver in lib/driver-map.nix.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    filter
    hasInfix
    hasPrefix
    attrNames
    concatStringsSep
    sort
    ;
  driverMap = import ../../lib/driver-map.nix;
  schema = import ../../lib/env-schema.nix;
  structuralDoc = import ../../lib/structural-options-doc.nix;

  expectedEnv = [
    "driverBashTimeoutMs"
    "effort"
    "filerModel"
    "model"
    "reviewEffort"
    "reviewModel"
    "scoutModel"
    "workerModel"
  ];
  expectedStructural = [
    "byName"
    "roster"
  ];
  marked = rows: sort (a: b: a < b) (filter (n: rows.${n} ? driverValued) (attrNames rows));
  isBool = builtins.isBool;

  has = msg: needles: builtins.all (n: hasInfix n msg) needles;
  entry = { };
  resolve = args: driverMap.resolve ({ topLevel = [ ]; } // args);
  top = paths: map (path: { inherit path; }) paths;

  expectErr =
    name: result: needles:
    assert assertMsg (result.error != null && has result.error needles)
      "driver-map-resolve: ${name}: expected an error containing ${builtins.toJSON needles}, got ${builtins.toJSON result}";
    assert assertMsg (hasPrefix "perSystem.spindrift." result.error)
      "driver-map-resolve: ${name}: message must start with the perSystem.spindrift. path context: ${result.error}";
    assert assertMsg (
      !(hasInfix "\n" result.error)
    ) "driver-map-resolve: ${name}: message must be one line";
    true;
  expectOk =
    name: result: default:
    assert assertMsg (result.error == null && result.default == default)
      "driver-map-resolve: ${name}: expected default ${builtins.toJSON default} with no error, got ${builtins.toJSON result}";
    true;
in
{
  driver-valued-marking =
    let
      envPaths = map (n: driverMap.paths.${n}) expectedEnv;
      badEnv = filter (
        n:
        let
          e = schema.${n};
        in
        !((e.flakeOption or false) && e.group == "agents")
      ) (marked schema);
    in
    assert assertMsg (marked schema == expectedEnv)
      "lib/env-schema.nix: the driverValued rows drifted from the pinned set (ADR 0064) -- edit nix/checks/driver-map.nix deliberately: ${concatStringsSep ", " (marked schema)}";
    assert assertMsg (marked structuralDoc == expectedStructural)
      "lib/structural-options-doc.nix: the driverValued rows drifted from the pinned set (ADR 0064): ${concatStringsSep ", " (marked structuralDoc)}";
    assert assertMsg (badEnv == [ ])
      "lib/env-schema.nix: driverValued requires flakeOption = true and group = \"agents\": ${concatStringsSep ", " badEnv}";
    assert assertMsg (builtins.all (
      n: isBool schema.${n}.driverValued
    ) expectedEnv) "lib/env-schema.nix: driverValued must be a bool";
    assert assertMsg (builtins.all (
      n: isBool structuralDoc.${n}.driverValued
    ) expectedStructural) "lib/structural-options-doc.nix: driverValued must be a bool";
    assert assertMsg (
      filter (n: schema.${n}.driverValued) expectedEnv == expectedEnv
    ) "lib/env-schema.nix: driverValued, when present, must be true";
    # The resolver's single source mirrors the marking.
    assert assertMsg (
      driverMap.driverValued == {
        env = expectedEnv;
        structural = expectedStructural;
      }
    ) "lib/driver-map.nix: driverValued export diverged from the schema markings";
    assert assertMsg (
      driverMap.paths.model == [
        "models"
        "default"
      ]
      && driverMap.paths.driverBashTimeoutMs == [ "bashTimeoutMs" ]
      &&
        driverMap.paths.roster == [
          "models"
          "roster"
        ]
      &&
        driverMap.paths.byName == [
          "models"
          "byName"
        ]
      && builtins.length envPaths == 8
    ) "lib/driver-map.nix: paths must give each Driver-valued key's agents-relative segments";
    pkgs.runCommand "driver-valued-marking" { } "touch $out";

  driver-map-resolve =
    assert expectOk "shorthand" (resolve {
      drivers = { };
      defaultDriver = null;
    }) null;
    assert expectOk "shorthand with top-level driver and model" (resolve {
      drivers = { };
      defaultDriver = null;
      topLevel = top [
        "agents.driver"
        "agents.models.default"
      ];
    }) null;
    assert expectErr "undeclared default, empty drivers"
      (resolve {
        drivers = { };
        defaultDriver = "claude";
      })
      [
        "agents.defaultDriver"
        "\"claude\""
        "agents.drivers.claude"
      ];
    assert expectOk "one Driver inferred" (resolve {
      drivers.claude = entry;
      defaultDriver = null;
    }) "claude";
    assert expectOk "one Driver explicit" (resolve {
      drivers.claude = entry;
      defaultDriver = "claude";
    }) "claude";
    assert expectOk "several Drivers with default" (resolve {
      drivers = {
        claude = entry;
        opencode = entry;
      };
      defaultDriver = "opencode";
    }) "opencode";
    assert expectErr "several Drivers, no default"
      (resolve {
        drivers = {
          claude = entry;
          opencode = entry;
        };
        defaultDriver = null;
      })
      [
        "agents.defaultDriver"
        "claude"
        "opencode"
      ];
    assert expectErr "undeclared default, non-empty drivers"
      (resolve {
        drivers.claude = entry;
        defaultDriver = "opencode";
      })
      [
        "agents.defaultDriver"
        "\"opencode\""
        "claude"
        "agents.drivers.opencode"
      ];
    assert expectErr "both forms: agents.driver plus a map"
      (resolve {
        drivers.claude = entry;
        defaultDriver = null;
        topLevel = top [ "agents.driver" ];
      })
      [
        "agents.driver"
        "agents.drivers"
      ];
    assert expectErr "both forms: one Driver plus a top-level model"
      (resolve {
        drivers.claude = entry;
        defaultDriver = null;
        topLevel = top [ "agents.models.default" ];
      })
      [
        "agents.models.default"
        "agents.drivers.<name>.models.default"
      ];
    assert expectErr "several Drivers, top-level Driver-valued knob"
      (resolve {
        drivers = {
          claude = entry;
          opencode = entry;
        };
        defaultDriver = "claude";
        topLevel = top [
          "agents.bashTimeoutMs"
          "agents.driver"
        ];
      })
      [
        "agents.bashTimeoutMs"
        "agents.drivers.<name>.bashTimeoutMs"
      ];
    assert expectErr "nested path under a Driver-valued knob" (resolve {
      drivers = {
        claude = entry;
        opencode = entry;
      };
      defaultDriver = "claude";
      topLevel = top [ "agents.models.byName" ];
    }) [ "agents.models.byName" ];
    pkgs.runCommand "driver-map-resolve" { } "touch $out";
}
