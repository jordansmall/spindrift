# Eval-level checks for the multi-Driver consumer flake (ADR 0064, issue
# #5052): the `driverValued` marking on the knob schema rows, the pure resolver
# in lib/driver-map.nix, and the flake module's Driver-map wiring -- default-
# Driver equivalence, the must-fail evals, the option surface, per-Driver
# outputs, and what each Driver's Harness bakes.
{
  pkgs,
  nixpkgs,
  system,
  flake-parts,
  config,
  fixtures,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    mapAttrs
    recursiveUpdate
    filter
    hasInfix
    hasPrefix
    attrNames
    concatStringsSep
    sort
    splitString
    ;
  driverMap = import ../../lib/driver-map.nix;
  # These fixtures test store-closure behaviour (agent-closure output, no OCI
  # image).
  storeClosureRuntime = fixtures.bwrapRuntime;
  # FLAKE_IMAGE_ATTR names the image's Linux twin, not a darwin host.
  linuxSystem = (import ../../lib/nixpkgs-shared.nix).linuxTwin.${system};
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
  # A model-/effort-/bash-timeout-shaped env row is Driver-valued unless named
  # in the exemption list, which is for rows that match the shape but are
  # genuinely shared across Drivers, or match it only by name (piModelsContent
  # carries pi's models.json file, not a model id).
  driverValuedShape = ".*([Mm]odel|[Ee]ffort|BashTimeout).*";
  driverValuedExempt = [ "piModelsContent" ];
  unmarkedShaped = filter (
    n:
    builtins.match driverValuedShape n != null
    && !(builtins.elem n driverValuedExempt)
    && !(schema.${n}.driverValued or false)
  ) (attrNames schema);
  marked = rows: sort (a: b: a < b) (filter (n: rows.${n} ? driverValued) (attrNames rows));
  isBool = builtins.isBool;

  has = msg: needles: builtins.all (n: hasInfix n msg) needles;
  lacks = msg: needles: !(builtins.any (n: hasInfix n msg) needles);
  entry = { };
  # opencode rejects the schema's bare claude ids (issue #5355), so every
  # opencode Driver here names its models; the worker is opted out, as in the
  # dogfood.
  ocDefaultModel = fixtures.opencodeDefaultModel;
  ocRoster = (import ../../lib/roster.nix { inherit (pkgs) lib; }).defaultRoster {
    models = {
      scout = fixtures.opencodeNamespacedDefaults.scoutModel;
      filer = fixtures.opencodeNamespacedDefaults.filerModel;
      reviewer = fixtures.opencodeNamespacedDefaults.reviewModel;
      worker = "";
    };
  };
  ocModels = {
    models.default = ocDefaultModel;
    models.roster = ocRoster;
  };
  resolve = args: driverMap.resolve ({ topLevel = [ ]; } // args);
  top = paths: map (path: { inherit path; }) paths;

  evalModule =
    spindrift:
    flake-parts.lib.mkFlake
      {
        inputs = {
          inherit nixpkgs;
          self.outPath = ../../.;
        };
      }
      {
        systems = [ system ];
        imports = [ ../../lib/flakeModule.nix ];
        perSystem.spindrift = recursiveUpdate { packages = p: [ p.hello ]; } spindrift;
      };
  direct =
    args:
    import ../../lib/mkHarness.nix (
      {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
      }
      // args
    );
  # The opencode Harness of the two-Driver config, shared by the outputs and
  # baked checks.
  ocArgs = {
    driver = "opencode";
    multiDriver = true;
    roster = ocRoster;
    defaults = {
      model = ocDefaultModel;
      daemonApp = ".#opencode";
      daemonSelfApp = ".#opencode-daemon";
    };
  };
  oc = direct (ocArgs // { imageAttrName = "agent-image-opencode"; });
  # flake-parts `debug = true` exposes the per-system module eval, so a check
  # can read the hidden spindrift.driverDeprecations option a real eval emits.
  emittedDeprecations =
    spindrift:
    (flake-parts.lib.mkFlake
      {
        inputs = {
          inherit nixpkgs;
          self.outPath = ../../.;
        };
      }
      {
        systems = [ system ];
        debug = true;
        imports = [ ../../lib/flakeModule.nix ];
        perSystem.spindrift = recursiveUpdate { infra.image.packages = p: [ p.hello ]; } spindrift;
      }
    ).allSystems.${system}.config.spindrift.driverDeprecations;

  # The consumer starter's own perSystem eval: its `outputs` is called with a
  # mkFlake that adds `debug = true`, which exposes the per-system config the
  # plain outputs attrset hides.
  templateDeprecations =
    ((import ../../templates/default/flake.nix).outputs {
      inherit nixpkgs;
      flake-parts = flake-parts // {
        lib = flake-parts.lib // {
          mkFlake =
            args: module:
            flake-parts.lib.mkFlake args {
              imports = [ module ];
              debug = true;
            };
        };
      };
      self.outPath = ../../templates/default;
      spindrift = {
        flakeModules.default = ../../lib/flakeModule.nix;
        lib.mkHarness = import ../../lib/mkHarness.nix;
      };
    }).allSystems.${system}.config.spindrift.driverDeprecations;

  # One realistic value per single-Driver option; `set` nests the value at
  # its path.
  singleDriverSettings =
    map
      (e: {
        inherit (e) path;
        set = pkgs.lib.setAttrByPath (pkgs.lib.splitString "." e.path) e.value;
      })
      [
        {
          path = "agents.driver";
          value = "claude";
        }
        {
          path = "agents.models.default";
          value = "x";
        }
        {
          path = "agents.models.worker";
          value = "x";
        }
        {
          path = "agents.models.scout";
          value = "x";
        }
        {
          path = "agents.models.review";
          value = "x";
        }
        {
          path = "agents.models.filer";
          value = "x";
        }
        {
          path = "agents.models.effort";
          value = "high";
        }
        {
          path = "agents.models.reviewEffort";
          value = "high";
        }
        {
          path = "agents.bashTimeoutMs";
          value = 1000;
        }
        {
          path = "agents.models.roster";
          value = [
            {
              name = "worker";
              model = "m";
            }
          ];
        }
        {
          path = "agents.models.byName";
          value.scout.model = "y";
        }
      ];
  outputs = spindrift: (evalModule spindrift).packages.${system};
  appsOf = spindrift: (evalModule spindrift).apps.${system};
  evaluates = spindrift: (builtins.tryEval (outputs spindrift ? spindrift)).success;
  drvPaths = mapAttrs (_: p: p.drvPath);
  programs = mapAttrs (_: a: a.program);
  bakedSettingCheck = ''
    check() {
      got=$(grep -o "\"$2\":\"[^\"]*\"" "$3" | sed -e 's/.*:"//' -e 's/"$//')
      if [ "$got" != "$1" ]; then
        echo "baked $2=$got, expected $1" >&2
        exit 1
      fi
    }
  '';

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
  expectErrLacking =
    name: result: needles:
    assert assertMsg (result.error != null && lacks result.error needles)
      "driver-map-resolve: ${name}: expected an error free of ${builtins.toJSON needles}, got ${builtins.toJSON result}";
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
    assert assertMsg (unmarkedShaped == [ ])
      "lib/env-schema.nix: ${concatStringsSep ", " unmarkedShaped} look Driver-valued (model/effort/bash-timeout shaped) -- mark each with driverValued = true, or add it to driverValuedExempt in nix/checks/driver-map.nix if it genuinely is not Driver-valued";
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
    # placeEntry keys on the entry's own flag, not on a path table.
    assert assertMsg (
      driverMap.placeEntry "claude" { driverValued = true; } [
        "agents"
        "models"
        "default"
      ] == [
        "agents"
        "drivers"
        "claude"
        "models"
        "default"
      ]
      &&
        driverMap.placeEntry "claude" { driverValued = false; } [
          "agents"
          "prompt"
        ] == [
          "agents"
          "prompt"
        ]
      &&
        driverMap.placeEntry "claude" { } [
          "infra"
          "runtime"
        ] == [
          "infra"
          "runtime"
        ]
    ) "lib/driver-map.nix: placeEntry must place only driverValued entries under the Driver";
    pkgs.runCommand "driver-valued-marking" { } "touch $out";

  driver-map-placed-path =
    assert assertMsg
      (
        driverMap.placedPath "<name>" "model" schema.model == "agents.drivers.<name>.models.default"
        &&
          driverMap.placedPath "<name>" "driverBashTimeoutMs" schema.driverBashTimeoutMs
          == "agents.drivers.<name>.bashTimeoutMs"
        && driverMap.placedPath "<name>" "autoFormat" schema.autoFormat == "agents.format.enable"
      )
      "lib/driver-map.nix: placedPath must place Driver-valued keys under the Driver and leave other keys on their resolveNixPath path";
    pkgs.runCommand "driver-map-placed-path" { } "touch $out";

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
        "perSystem.spindrift.agents.defaultDriver"
        "\"claude\""
        "perSystem.spindrift.agents.drivers.claude"
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
        "perSystem.spindrift.agents.defaultDriver"
        "claude"
        "opencode"
      ];
    assert expectErr "undeclared default, non-empty drivers"
      (resolve {
        drivers.claude = entry;
        defaultDriver = "opencode";
      })
      [
        "perSystem.spindrift.agents.defaultDriver"
        "\"opencode\""
        "claude"
        "perSystem.spindrift.agents.drivers.opencode"
        "or set perSystem.spindrift.agents.defaultDriver"
      ];
    assert expectErr "both forms: agents.driver plus a map"
      (resolve {
        drivers.claude = entry;
        defaultDriver = null;
        topLevel = top [ "agents.driver" ];
      })
      [
        "perSystem.spindrift.agents.driver"
        "perSystem.spindrift.agents.drivers"
        "drop perSystem.spindrift.agents.driver"
      ];
    assert expectErr "both forms: a timeout without agents.driver"
      (resolve {
        drivers.claude = entry;
        defaultDriver = null;
        topLevel = top [ "agents.bashTimeoutMs" ];
      })
      [
        "perSystem.spindrift.agents.bashTimeoutMs"
        "perSystem.spindrift.agents.drivers.<name>.bashTimeoutMs"
      ];
    assert expectErrLacking "both forms without agents.driver omit the drop clause"
      (resolve {
        drivers.claude = entry;
        defaultDriver = null;
        topLevel = top [ "agents.bashTimeoutMs" ];
      })
      [
        "drop"
        "attribute name is the Driver"
      ];
    assert expectErr "both forms: one Driver plus a top-level model"
      (resolve {
        drivers.claude = entry;
        defaultDriver = null;
        topLevel = top [ "agents.models.default" ];
      })
      [
        "perSystem.spindrift.agents.models.default"
        "perSystem.spindrift.agents.drivers.<name>.models.default"
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
        "perSystem.spindrift.agents.bashTimeoutMs"
        "perSystem.spindrift.agents.drivers.<name>.bashTimeoutMs"
      ];
    assert expectErr "nested path under a Driver-valued knob"
      (resolve {
        drivers = {
          claude = entry;
          opencode = entry;
        };
        defaultDriver = "claude";
        topLevel = top [ "agents.models.byName" ];
      })
      [
        "perSystem.spindrift.agents.models.byName"
        "perSystem.spindrift.agents.drivers.<name>.models.byName"
      ];
    pkgs.runCommand "driver-map-resolve" { } "touch $out";

  # The single-Driver form warns once per setting, naming the exact new path.
  # The text is pinned here; flakemodule-driver-map-deprecations pins that a
  # real module eval emits exactly these messages, and
  # flakemodule-driver-map-errors that each setting is accepted alone and
  # rejected beside a Driver map.
  driver-map-deprecations =
    let
      oldPaths = [
        "agents.driver"
      ]
      ++ map (segs: "agents." + concatStringsSep "." segs) (builtins.attrValues driverMap.paths);
      # Line by line, so hasPrefix anchors each row's first cell.
      migratingLines = splitString "\n" (builtins.readFile ../../MIGRATING.md);
      # The cell ends at its closing backtick, or at ` = ` for agents.driver's
      # `agents.driver = "<name>"`, so a longer path sharing the prefix or a
      # right-hand cell never stands in for a missing row.
      missingRows = filter (
        p:
        let
          cell = "| `perSystem.spindrift.${p}";
        in
        !builtins.any (l: hasPrefix "${cell}` |" l || hasPrefix "${cell} = " l) migratingLines
      ) oldPaths;
      msgsFor =
        topLevel:
        driverMap.deprecations {
          name = "opencode";
          inherit topLevel;
        };
      one =
        p:
        let
          msgs = msgsFor (top [ p ]);
          msg = builtins.head msgs;
          newPath =
            "perSystem.spindrift.agents.drivers.opencode"
            + pkgs.lib.optionalString (p != "agents.driver") ".${pkgs.lib.removePrefix "agents." p}";
        in
        assert assertMsg (builtins.length msgs == 1) "deprecations: ${p}: expected exactly one message";
        assert assertMsg (has msg [
          "perSystem.spindrift.${p} "
          newPath
        ]) "deprecations: ${p}: message must name the old and new path, got ${msg}";
        assert assertMsg (
          hasPrefix "perSystem.spindrift." msg && !(hasInfix "\n" msg)
        ) "deprecations: ${p}: message must be one line starting with perSystem.spindrift.: ${msg}";
        true;
    in
    assert assertMsg (builtins.length oldPaths == 11)
      "expected 11 single-Driver paths (agents.driver plus the 10 entries in driverMap.paths), got ${toString (builtins.length oldPaths)}";
    assert assertMsg (builtins.all one oldPaths) "unreachable";
    assert assertMsg (
      builtins.length (msgsFor (top oldPaths)) == builtins.length oldPaths
    ) "deprecations: setting every path must warn once per path";
    # MIGRATING.md's table is hand-written; every old option must have a row.
    assert assertMsg (missingRows == [ ])
      "MIGRATING.md: the issue #5055 table must map every single-Driver option, missing: ${concatStringsSep ", " missingRows}";
    assert assertMsg (msgsFor [ ] == [ ]) "deprecations: no settings, no warnings";
    assert assertMsg (has (builtins.head (msgsFor (top [ "agents.driver" ]))) [
      "perSystem.spindrift.agents.defaultDriver"
    ]) "deprecations: agents.driver must point at agents.defaultDriver for several Drivers";
    pkgs.runCommand "driver-map-deprecations" { } "touch $out";

  # The default Driver's harness is the same whichever form spells it
  # (ADR 0064): a Driver-map entry equals the single-Driver form's top-level
  # settings, and equals the direct mkHarness call carrying the same values.
  flakemodule-driver-map-equivalence =
    let
      oldForm = {
        agents = {
          driver = "claude";
          models.default = "x";
          models.byName.scout.model = "y";
          bashTimeoutMs = 1000;
        };
      };
      mapForm = {
        agents.drivers.claude = {
          models.default = "x";
          models.byName.scout.model = "y";
          bashTimeoutMs = 1000;
        };
      };
      oldRoster = {
        agents.driver = "claude";
        agents.models.roster = [
          {
            name = "worker";
            model = "m";
          }
        ];
      };
      mapRoster.agents.drivers.claude.models.roster = [
        {
          name = "worker";
          model = "m";
        }
      ];
      directForm = direct {
        driver = "claude";
        defaults = {
          model = "x";
          driverBashTimeoutMs = 1000;
        };
        byName.scout.model = "y";
      };
      directRoster = direct {
        driver = "claude";
        roster = [
          {
            name = "worker";
            model = "m";
          }
        ];
      };
      same =
        a: b: drvPaths (outputs a) == drvPaths (outputs b) && programs (appsOf a) == programs (appsOf b);
    in
    assert assertMsg (same oldForm mapForm)
      "agents.drivers.claude must resolve to the same default harness as the single-Driver form";
    assert assertMsg (same oldRoster mapRoster)
      "agents.drivers.claude.models.roster must resolve to the same default harness as agents.models.roster";
    assert assertMsg (
      (outputs mapForm).spindrift.drvPath == directForm.spindrift.drvPath
    ) "a Driver-map entry must equal the direct mkHarness call with the same values";
    assert assertMsg (
      (outputs mapRoster).spindrift.drvPath == directRoster.spindrift.drvPath
    ) "a Driver-map roster must equal the direct mkHarness call with the same roster";
    assert assertMsg (
      (outputs oldForm).spindrift.drvPath != (outputs { }).spindrift.drvPath
    ) "anti-vacuity: the Driver-valued settings must change the harness";
    # The shorthand keeps today's outputs and gains the default Driver's aliases.
    assert assertMsg (
      (outputs { }).spindrift.drvPath == (direct { }).spindrift.drvPath
    ) "the shorthand must equal the direct mkHarness call";
    assert assertMsg (
      (appsOf { }).claude == (appsOf { }).default && (appsOf { }).claude-daemon == (appsOf { }).daemon
    ) "the default Driver must alias apps.default and apps.daemon under its name";
    assert assertMsg (
      !pkgs.stdenv.hostPlatform.isLinux
      || (outputs { }).agent-image-claude.drvPath == (outputs { }).agent-image.drvPath
    ) "the default Driver's image alias must be the same derivation as agent-image";
    assert assertMsg (
      (appsOf mapForm).claude == (appsOf mapForm).default
    ) "a Driver-map default must alias its own name";
    pkgs.runCommand "flakemodule-driver-map-equivalence"
      {
        doc = directForm.internals.runInputDocumentFile;
      }
      ''
        grep -q '"MODEL":"x"' "$doc" \
          || { echo "model x not baked in the run input document" >&2; exit 1; }
        touch $out
      '';

  # Both forms, several Drivers without a default, and an undeclared default
  # must fail module eval; each arm has a control that evaluates, so a
  # rejection cannot be an unrelated failure. The message text is pinned by
  # driver-map-resolve.
  flakemodule-driver-map-errors =
    let
      rejects = name: spindrift: assertMsg (!(evaluates spindrift)) "${name} must fail eval";
      accepts = name: spindrift: assertMsg (evaluates spindrift) "control: ${name} must evaluate";
    in
    assert accepts "one Driver, inferred" { agents.drivers.claude.models.default = "x"; };
    assert accepts "two Drivers with defaultDriver" {
      agents.drivers.claude = { };
      agents.drivers.opencode = ocModels;
      agents.defaultDriver = "opencode";
    };
    assert accepts "the shorthand" { agents.models.default = "x"; };
    assert rejects "agents.driver beside agents.drivers" {
      agents.driver = "claude";
      agents.drivers.claude = { };
    };
    assert rejects "a one-Driver map beside a top-level agents.models.default" {
      agents.models.default = "x";
      agents.drivers.claude = { };
    };
    assert rejects "several Drivers beside a top-level agents.models.default" {
      agents.models.default = "x";
      agents.drivers.claude = { };
      agents.drivers.opencode = ocModels;
      agents.defaultDriver = "claude";
    };
    assert rejects "several Drivers without defaultDriver" {
      agents.drivers.claude = { };
      agents.drivers.opencode = ocModels;
    };
    assert rejects "an undeclared defaultDriver" {
      agents.drivers.claude = { };
      agents.defaultDriver = "opencode";
    };
    assert rejects "defaultDriver with an empty map" { agents.defaultDriver = "claude"; };
    # Every single-Driver setting evaluates alone and conflicts beside a
    # Driver map. The values are realistic for each option's type.
    assert builtins.all (
      s:
      accepts "the single-Driver setting ${builtins.toJSON s} alone" s
      && rejects "the single-Driver setting ${builtins.toJSON s} beside agents.drivers" (
        recursiveUpdate s { agents.drivers.claude = { }; }
      )
    ) (map (e: e.set) singleDriverSettings);
    pkgs.runCommand "flakemodule-driver-map-errors" { } "touch $out";

  # A real module eval emits the deprecation text: the single-Driver form
  # surfaces exactly driverMap.deprecations (one message per setting), the
  # Driver map and a bare shorthand surface none. Deleting the warn fold in
  # lib/flakeModule.nix empties the list and fails this.
  flakemodule-driver-map-deprecations =
    let
      expected =
        paths:
        driverMap.deprecations {
          name = "claude";
          topLevel = top paths;
        };
      allPaths = map (e: e.path) singleDriverSettings;
    in
    assert assertMsg (builtins.all (
      e:
      let
        got = emittedDeprecations e.set;
      in
      assertMsg (
        got == expected [ e.path ] && builtins.length got == 1
      ) "${e.path} alone must emit exactly its one deprecation, got ${builtins.toJSON got}"
    ) singleDriverSettings) "unreachable";
    assert assertMsg (
      # The module emits in its own order (env rows, roster, byName), so compare as sets.
      sort (a: b: a < b) (
        emittedDeprecations (builtins.foldl' recursiveUpdate { } (map (e: e.set) singleDriverSettings))
      ) == sort (a: b: a < b) (expected allPaths)
    ) "every single-Driver setting together must emit one message per setting";
    assert assertMsg (emittedDeprecations { } == [ ]) "a bare shorthand must emit nothing";
    # A legacy shim warns with its own message; it must not also earn an ADR
    # 0064 warning naming a top-level path the Consumer never wrote.
    assert assertMsg (
      emittedDeprecations {
        settings.models.workerModel = "x";
        driver = "claude";
      } == [ ]
    ) "legacy shims must not emit a Driver-map deprecation for a path they did not set";
    assert assertMsg (
      emittedDeprecations { agents.drivers.claude.models.default = "x"; } == [ ]
    ) "a Driver map must emit nothing";
    # The two Consumer configs spindrift ships must stay on the Driver map (or
    # bare shorthand) form: the dogfood's own perSystem config, and the
    # starter template a Consumer copies.
    assert assertMsg (config.spindrift.driverDeprecations == [ ])
      "the dogfood flake must emit no Driver-map deprecation, got ${builtins.toJSON config.spindrift.driverDeprecations}";
    assert assertMsg (templateDeprecations == [ ])
      "templates/default must emit no Driver-map deprecation, got ${builtins.toJSON templateDeprecations}";
    pkgs.runCommand "flakemodule-driver-map-deprecations" { } "touch $out";

  # agents.drivers.<name> is built from the driverValued marking, not a second
  # hand-kept list: its leaves are exactly lib/driver-map.nix's paths.
  flakemodule-driver-map-options =
    let
      spindriftOptions =
        (flake-parts.lib.evalFlakeModule
          {
            inputs = {
              inherit nixpkgs;
              self.outPath = ../../.;
            };
          }
          {
            systems = [ system ];
            imports = [ ../../lib/flakeModule.nix ];
          }
        ).options.perSystem.type.getSubOptions
          [ "perSystem" ];
      agentOptions = spindriftOptions.spindrift.agents.type.getSubOptions [ "agents" ];
      entryOptions = agentOptions.drivers.type.getSubOptions [
        "drivers"
        "<name>"
      ];
      # buildTree namespaces are submodules; every other option is a leaf, even
      # byName, whose own submodule is a value shape rather than a namespace.
      leaves =
        prefix: opts:
        builtins.concatMap (
          n:
          let
            o = opts.${n};
          in
          if pkgs.lib.isOption o && o.type.name == "submodule" then
            leaves (prefix ++ [ n ]) (o.type.getSubOptions o.loc)
          else
            [ (concatStringsSep "." (prefix ++ [ n ])) ]
        ) (filter (n: n != "_module") (attrNames opts));
      expected = sort builtins.lessThan (
        map (concatStringsSep ".") (builtins.attrValues driverMap.paths)
      );
    in
    assert assertMsg (sort builtins.lessThan (leaves [ ] entryOptions) == expected)
      "agents.drivers.<name> leaves ${
        builtins.toJSON (leaves [ ] entryOptions)
      } must equal lib/driver-map.nix's paths ${builtins.toJSON expected}";
    assert assertMsg (
      !(entryOptions ? driver)
    ) "a Driver entry has no driver field: the attribute name is the Driver";
    assert assertMsg (
      agentOptions.defaultDriver.type.name == "nullOr" && agentOptions.defaultDriver.default == null
    ) "agents.defaultDriver must be a nullOr string defaulting to null";
    pkgs.runCommand "flakemodule-driver-map-options" { } "touch $out";

  # Every declared Driver gets its own apps and image/closure attribute; the
  # default's are aliases of its one Harness, and building a second Driver
  # must not change what the first builds (ADR 0064).
  flakemodule-driver-map-outputs =
    let
      isLinux = pkgs.stdenv.hostPlatform.isLinux;
      claude = {
        agents.drivers.claude.models.default = "x";
        agents.defaultDriver = "claude";
      };
      two = recursiveUpdate claude {
        agents.drivers.opencode = ocModels;
      };
      names = x: sort builtins.lessThan (attrNames x);
      twoApps = appsOf two;
      twoPkgs = outputs two;
      onePkgs = outputs claude;
      bwrapPkgs = outputs (recursiveUpdate two { infra.runtime = storeClosureRuntime; });
    in
    assert assertMsg
      (
        names twoApps == [
          "claude"
          "claude-daemon"
          "daemon"
          "default"
          "opencode"
          "opencode-daemon"
        ]
      )
      "two Drivers must expose exactly default, daemon, and each Driver's two apps, got ${builtins.toJSON (names twoApps)}";
    assert assertMsg (
      twoApps.claude == twoApps.default && twoApps.claude-daemon == twoApps.daemon
    ) "the default Driver's apps must alias default and daemon";
    assert assertMsg (
      twoApps.opencode.program != twoApps.claude.program
      && twoApps.opencode-daemon.program != twoApps.claude-daemon.program
    ) "the second Driver's apps must be its own Harness's, not the default's";
    # A second Driver turns on harness.<driver>.env layering, which lives in
    # the wrapper, so the default's apps change; a lone Driver's do not.
    assert assertMsg (
      twoApps.default.program != (appsOf claude).default.program
      && twoApps.daemon.program != (appsOf claude).daemon.program
    ) "declaring a second Driver must turn on the default Driver's harness.<driver>.env layering";
    assert assertMsg (
      programs (appsOf claude) == programs (appsOf {
        agents.drivers.claude.models.default = "x";
      })
    ) "a one-entry Driver map must not enable harness.<driver>.env layering";
    # Only the image attribute is contributed; base packages stay the default's.
    assert assertMsg
      (
        names twoPkgs == names (onePkgs // pkgs.lib.optionalAttrs isLinux { agent-image-opencode = null; })
      )
      "a non-default Driver must contribute only its image attribute, got ${builtins.toJSON (names twoPkgs)}";
    assert assertMsg
      (
        drvPaths (
          removeAttrs twoPkgs [
            "agent-image-opencode"
            "spindrift"
          ]
        ) == drvPaths (removeAttrs onePkgs [ "spindrift" ])
      )
      "the default Driver's packages, bar the spindrift wrapper, must not change when another Driver is declared";
    assert assertMsg
      (!isLinux || twoPkgs.agent-image-opencode.drvPath == oc.packages.agent-image-opencode.drvPath)
      "agent-image-opencode must be a direct opencode mkHarness call's image, not merely differ from claude's";
    assert assertMsg (
      !isLinux
      || (
        bwrapPkgs ? agent-closure-opencode
        && bwrapPkgs ? agent-closure-claude
        && !(bwrapPkgs ? agent-image-opencode)
      )
    ) "under bwrap each Driver gets an agent-closure-<driver> attribute";
    pkgs.runCommand "flakemodule-driver-map-outputs" { } "touch $out";

  # The module exposes only packages and apps, so a non-default Harness's
  # baked run input document is reached by proving the module's app programs
  # equal a direct mkHarness call with the expected arguments.
  flakemodule-driver-map-baked =
    let
      base = {
        agents.drivers.claude.models.default = "x";
        agents.defaultDriver = "claude";
        agents.drivers.opencode = ocModels;
      };
      withTop = recursiveUpdate base { dispatch.daemonApp = ".#custom"; };
      bwrap = recursiveUpdate base { infra.runtime = storeClosureRuntime; };
      ocBwrap = direct (
        ocArgs
        // {
          runtime = storeClosureRuntime;
          imageAttrName = "agent-closure-opencode";
        }
      );
      sameApps =
        a: h:
        a.opencode.program == h.apps.default.program && a.opencode-daemon.program == h.apps.daemon.program;
      claudeDirect =
        defaults:
        direct {
          driver = "claude";
          multiDriver = true;
          defaults = {
            model = "x";
          }
          // defaults;
        };
    in
    assert assertMsg (sameApps (appsOf base) oc)
      "the module's opencode apps must equal a direct mkHarness call with its own image attribute and daemon refs";
    assert assertMsg (sameApps (appsOf withTop) oc)
      "a top-level daemonApp must not reach a non-default Driver";
    assert assertMsg (
      (appsOf withTop).default.program != (appsOf base).default.program
      &&
        (appsOf withTop).default.program == (claudeDirect { daemonApp = ".#custom"; }).apps.default.program
    ) "a top-level daemonApp must reach the default Driver";
    assert assertMsg (
      !pkgs.stdenv.hostPlatform.isLinux || sameApps (appsOf bwrap) ocBwrap
    ) "under bwrap the opencode Harness must probe agent-closure-opencode";
    pkgs.runCommand "flakemodule-driver-map-baked"
      {
        ocDoc = oc.internals.runInputDocumentFile;
        bwrapDoc = ocBwrap.internals.runInputDocumentFile;
        ocProgram = (appsOf base).opencode.program;
        claudeProgram = (appsOf base).default.program;
        ocDaemonProgram = (appsOf base).opencode-daemon.program;
        claudeDaemonProgram = (appsOf base).daemon.program;
        defaultDoc = (claudeDirect { }).internals.runInputDocumentFile;
        customDoc = (claudeDirect { daemonApp = ".#custom"; }).internals.runInputDocumentFile;
      }
      ''
        ${bakedSettingCheck}
        check '.#packages.${linuxSystem}.agent-image-opencode' FLAKE_IMAGE_ATTR "$ocDoc"
        check '.#opencode' DAEMON_APP "$ocDoc"
        check '.#opencode-daemon' DAEMON_SELF_APP "$ocDoc"
        check '.#packages.${linuxSystem}.agent-closure-opencode' FLAKE_IMAGE_ATTR "$bwrapDoc"
        check '.#packages.${linuxSystem}.agent-image' FLAKE_IMAGE_ATTR "$defaultDoc"
        check '.#' DAEMON_APP "$defaultDoc"
        check '.#daemon' DAEMON_SELF_APP "$defaultDoc"
        check '.#custom' DAEMON_APP "$customDoc"
        for p in "$ocProgram" "$ocDaemonProgram"; do
          grep -qF '. "$PWD/harness.opencode.env"' "$p" \
            || { echo "$p does not source harness.opencode.env" >&2; exit 1; }
        done
        for p in "$claudeProgram" "$claudeDaemonProgram"; do
          grep -qF '. "$PWD/harness.claude.env"' "$p" \
            || { echo "$p does not source harness.claude.env" >&2; exit 1; }
        done
        touch $out
      '';

  # A Consumer declaring agents.drivers.pi gets pi's own apps and an image
  # named spindrift-pi; models are Provider-namespaced as under opencode.
  flakemodule-driver-map-pi =
    let
      isLinux = pkgs.stdenv.hostPlatform.isLinux;
      withPi = {
        agents.drivers.claude.models.default = "x";
        agents.defaultDriver = "claude";
        agents.drivers.pi = ocModels;
      };
      apps = appsOf withPi;
      pkgsOut = outputs withPi;
    in
    assert assertMsg (apps ? pi && apps ? pi-daemon)
      "a Consumer declaring agents.drivers.pi must publish .#pi and .#pi-daemon, got ${builtins.toJSON (attrNames apps)}";
    assert assertMsg (
      apps.pi.program != apps.claude.program
    ) "the pi app must be its own Harness's, not the default's";
    assert assertMsg (
      !isLinux || pkgsOut.agent-image-pi.imageName == "spindrift-pi"
    ) "the pi Driver's image must be named spindrift-pi";
    pkgs.runCommand "flakemodule-driver-map-pi" { } "touch $out";
}
