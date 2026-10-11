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
    ;
  driverMap = import ../../lib/driver-map.nix;
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
  # genuinely shared across Drivers.
  driverValuedShape = ".*([Mm]odel|[Ee]ffort|BashTimeout).*";
  driverValuedExempt = [ ];
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
      agents.drivers.opencode = { };
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
      agents.drivers.opencode = { };
      agents.defaultDriver = "claude";
    };
    assert rejects "several Drivers without defaultDriver" {
      agents.drivers.claude = { };
      agents.drivers.opencode = { };
    };
    assert rejects "an undeclared defaultDriver" {
      agents.drivers.claude = { };
      agents.defaultDriver = "opencode";
    };
    assert rejects "defaultDriver with an empty map" { agents.defaultDriver = "claude"; };
    pkgs.runCommand "flakemodule-driver-map-errors" { } "touch $out";

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
        agents.drivers.opencode.models.default = "anthropic/claude-sonnet-5";
      };
      names = x: sort builtins.lessThan (attrNames x);
      twoApps = appsOf two;
      twoPkgs = outputs two;
      onePkgs = outputs claude;
      bwrapPkgs = outputs (recursiveUpdate two { infra.runtime = "bwrap"; });
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
    assert assertMsg (
      programs (appsOf claude) == removeAttrs (programs twoApps) [
        "opencode"
        "opencode-daemon"
      ]
    ) "the default Driver's apps must not change when another Driver is declared";
    # Only the image attribute is contributed; base packages stay the default's.
    assert assertMsg
      (
        names twoPkgs == names (onePkgs // pkgs.lib.optionalAttrs isLinux { agent-image-opencode = null; })
      )
      "a non-default Driver must contribute only its image attribute, got ${builtins.toJSON (names twoPkgs)}";
    assert assertMsg (
      drvPaths (removeAttrs twoPkgs [ "agent-image-opencode" ]) == drvPaths onePkgs
    ) "the default Driver's packages must not change when another Driver is declared";
    assert assertMsg (
      !isLinux || twoPkgs.agent-image-opencode.drvPath != twoPkgs.agent-image-claude.drvPath
    ) "the two Drivers' images must differ";
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
        agents.drivers.opencode.models.default = "anthropic/claude-sonnet-5";
      };
      withTop = recursiveUpdate base { dispatch.daemonApp = ".#custom"; };
      bwrap = recursiveUpdate base { infra.runtime = "bwrap"; };
      ocArgs = {
        driver = "opencode";
        defaults = {
          model = "anthropic/claude-sonnet-5";
          daemonApp = ".#opencode";
          daemonSelfApp = ".#opencode-daemon";
        };
      };
      oc = direct (ocArgs // { imageAttrName = "agent-image-opencode"; });
      ocBwrap = direct (
        ocArgs
        // {
          runtime = "bwrap";
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
        touch $out
      '';
}
