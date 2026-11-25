# Issue #6796: a Connector's Driver row (spindrift.connectors.<name>.driver)
# reaches mkHarness as `driverRows`. A row outside the core registry travels in
# the launcher input document and the Box's driver preamble; every row is held
# to the registry's own shape rules, and fails closed where a Go-side or
# generated list would silently disagree with it.
{
  pkgs,
  config,
  fixtures,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;
  registry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  core = import ../../core;
  mkHarness =
    extra:
    import ../../lib/mkHarness.nix (
      {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
      }
      // extra
    );
  # The fixture Driver Connector's own row: a full lib/drivers row plus
  # `module`, with a credential env already in core's generated scrub set.
  fixtureConnector = (import ../fixtures/connector).lib.connector { inherit core; };
  fixtureRow = (import ../fixtures/connector/data.nix { inherit core; }).driver;
  withRows = rows: mkHarness { driverRows = rows; };
  withFixture = mkHarness {
    driver = "fixture";
    driverRows.fixture = fixtureRow;
  };
  plain = mkHarness { };
  # The dogfood's primary Connectors are Drivers, named in mkHarness's
  # `primaryDrivers`; a backend row carries no `primary` (issue #7456).
  driverPrimaries = fixtures.dogfoodDriverPrimaries;
  claudeConnector = (import ../../connectors/claude).lib.connector { inherit core; };
  githubConnector = (import ../../connectors/github).lib.connector { inherit core; };
  jiraConnector = (import ../../connectors/jira).lib.connector { inherit core; };
  githubProgramWith =
    modules:
    (fixtures.connectorConsumerVia (
      [
        ../../lib/flakeModule.nix
        githubConnector.flakeModule
      ]
      ++ modules
    )).apps.${system}.default.program;
  forgejoConnector = (import ../../connectors/forgejo).lib.connector { inherit core; };
  claudeRow = claudeConnector.data.driver pkgs.lib;
  # opencode rejects the schema's bare claude ids (issue #5355), so each
  # Harness selecting it names its models; the worker's is dropped silently.
  opencodeDefaults = fixtures.opencodeNamespacedDefaults;
  opencodeModels = {
    models.default = fixtures.opencodeDefaultModel;
    models.roster = (import ../../lib/roster.nix { inherit (pkgs) lib; }).defaultRoster {
      models = {
        scout = opencodeDefaults.scoutModel;
        filer = opencodeDefaults.filerModel;
        reviewer = opencodeDefaults.reviewModel;
        worker = "";
      };
    };
  };
  # claude has no native Strategy (issue #6799), so shadow and primary mode
  # are exercised on a native Driver, opencode, whose Connector row is core's
  # row plus a wasm: the fixture's, which is not the batteries build.
  opencodeRow = registry.entries.opencode // {
    module = fixtureRow.module;
  };
  shadowOn = mkHarness {
    driver = "opencode";
    defaults = opencodeDefaults;
    driverRows.opencode = opencodeRow;
    shadowDrivers = [ "opencode" ];
  };
  plainOpencode = mkHarness {
    driver = "opencode";
    defaults = opencodeDefaults;
  };
  shadowOff = mkHarness {
    driver = "opencode";
    defaults = opencodeDefaults;
    driverRows.opencode = opencodeRow;
  };
  shadowOther = mkHarness {
    driver = "fixture";
    driverRows = {
      opencode = opencodeRow;
      fixture = fixtureRow;
    };
    shadowDrivers = [ "opencode" ];
  };
  # A Consumer module wiring opencode's Connector row, as a Connector's own
  # flake module would.
  opencodeConnectorModule = extra: {
    spindrift.connectors.opencode = {
      driver = opencodeRow;
    }
    // extra;
    perSystem.spindrift.agents.drivers.opencode = opencodeModels;
  };
  # Forces the validation chain without a build.
  evaluates =
    h: (builtins.tryEval (builtins.seq h.internals.runInputDocumentFile.drvPath true)).success;
  preambleEvaluates =
    h: (builtins.tryEval (builtins.seq h.internals.driverPreambleFile.drvPath true)).success;
  noModule = removeAttrs fixtureRow [ "module" ];
  foreignEnv = fixtureRow // {
    credentials = [
      {
        env = [ "NOT_A_CORE_CREDENTIAL_ENV" ];
        remedy = "set it";
      }
    ];
  };
  driftedClaude = registry.entries.claude // {
    credentials = [
      {
        env = [ "ANTHROPIC_API_KEY" ];
        remedy = "drifted";
      }
    ];
  };
in
{
  connector-driver-rows-outside-row-needs-module =
    assert assertMsg (evaluates (withRows {
      fixture = fixtureRow;
    })) "control: a well-formed outside row with a module must evaluate";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = noModule;
      }))
    ) "an outside Driver row without a module must fail closed";
    pkgs.runCommand "connector-driver-rows-outside-row-needs-module" { } "touch $out";

  # The Box loads a Driver Connector in-process only when its wire row carries
  # the first-party mark, and mkHarness alone derives it: a row renders it iff
  # its module is the wasm the batteries flake builds under that name. A row
  # setting the attribute fails closed (tryEval cannot read the message, so the
  # unmarked controls pin that the same rows evaluate without it).
  connector-driver-rows-first-party-mark =
    let
      markedWith =
        mark:
        withRows {
          fixture = fixtureRow // {
            firstParty = mark;
          };
        };
      # The batteries row's own wasm swapped for another build, and the
      # batteries row under a name of the Consumer's choosing.
      swapped = mkHarness {
        driverRows.claude = claudeRow // {
          module = fixtureRow.module;
        };
      };
      renamed = withRows {
        renamed = claudeRow // {
          name = "renamed";
        };
      };
      claudeWasmDir = builtins.unsafeDiscardStringContext (builtins.dirOf (claudeRow.module pkgs));
      # A Connector registered as data reaches mkHarness through core's
      # connectorModule, so a claim made there meets the same row validation
      # (issue #7559). Forcing the app program's length reaches it.
      dataEvaluates =
        data:
        let
          program =
            (fixtures.connectorConsumerVia [
              ../../lib/flakeModule.nix
              (core.lib.connectorModule core data)
            ]).apps.${system}.default.program;
        in
        (builtins.tryEval (builtins.stringLength program)).success;
      # Handles the fixture's attrset `driver` and claude's lib-taking function
      # `driver`, as resolveDriver in lib/connector-data.nix does.
      claiming =
        data:
        data
        // {
          driver =
            lib:
            (if builtins.isFunction data.driver then data.driver lib else data.driver)
            // {
              firstParty = true;
            };
        };
    in
    assert assertMsg (evaluates (withRows {
      fixture = fixtureRow;
    })) "control: the unmarked outside row must evaluate";
    assert assertMsg (
      !(evaluates (markedWith true))
    ) "a Consumer row claiming `firstParty = true` must fail closed";
    assert assertMsg (
      !(evaluates (markedWith ../fixtures/connector))
    ) "a Consumer row claiming a path as `firstParty` must fail closed";
    assert assertMsg (
      !(evaluates (markedWith claudeWasmDir))
    ) "a Consumer row claiming a string as `firstParty` must fail closed";
    assert assertMsg (
      !(evaluates (mkHarness {
        driverRows.claude = claudeRow // {
          firstParty = true;
        };
      }))
    ) "the batteries row carrying `firstParty` must fail closed: the mark is derived, never claimed";
    assert assertMsg (dataEvaluates claudeConnector.data)
      "control: the batteries Connector registered as data must evaluate";
    assert assertMsg (
      !(dataEvaluates (claiming claudeConnector.data))
    ) "a batteries Connector registered as data claiming `firstParty` must fail closed";
    assert assertMsg (dataEvaluates fixtureConnector.data)
      "control: a third-party Connector registered as data must evaluate";
    assert assertMsg (
      !(dataEvaluates (claiming fixtureConnector.data))
    ) "a third-party Connector registered as data claiming `firstParty` must fail closed";
    assert assertMsg (preambleEvaluates (mkHarness {
      driverRows.claude = claudeRow;
    })) "control: the batteries claude row must evaluate";
    assert assertMsg (preambleEvaluates swapped)
      "control: a batteries row with another module must evaluate";
    assert assertMsg (evaluates renamed) "control: a renamed batteries row must evaluate";
    pkgs.runCommand "connector-driver-rows-first-party-mark" { nativeBuildInputs = [ pkgs.jq ]; } ''
      # The batteries claude row, which every Harness renders, is the one
      # first-party row.
      jq -e 'length == 1 and .[0].name == "claude" and .[0].firstParty == true' ${plain.internals.driverRowsFile}

      # The batteries row's name with a Consumer's wasm, and the batteries wasm
      # under a Consumer's name, are not first-party.
      jq -e 'length == 1 and .[0].name == "claude" and (.[0] | has("firstParty") | not)' ${swapped.internals.driverRowsFile}
      jq -e '[.[] | select(.name == "renamed")] | length == 1 and (.[0] | has("firstParty") | not)' ${renamed.internals.driverRowsFile}
      jq -e '[.drivers[] | select(.name == "renamed") | has("firstParty")] | any | not' ${renamed.internals.runInputDocumentFile}

      # A third-party Connector's row stays unmarked, in the document too.
      jq -e '[.[] | select(.name == "fixture")] | length == 1 and (.[0] | has("firstParty") | not)' ${withFixture.internals.driverRowsFile}
      jq -e '[.drivers[] | select(.name == "fixture") | has("firstParty")] | any | not' ${withFixture.internals.runInputDocumentFile}
      touch $out
    '';

  connector-driver-rows-core-name-must-match-core =
    assert assertMsg (evaluates (withRows {
      claude = claudeRow;
    })) "control: a Connector row equal to core's claude row must evaluate";
    assert assertMsg (
      !(evaluates (withRows {
        claude = claudeRow // {
          inherit (driftedClaude) credentials;
        };
      }))
    ) "a Connector row naming a core Driver with different facts must fail closed";
    pkgs.runCommand "connector-driver-rows-core-name-must-match-core" { } "touch $out";

  connector-driver-rows-credential-env-in-core-set =
    assert assertMsg (evaluates (withRows {
      fixture = fixtureRow;
    })) "control: an outside row using a core credential env must evaluate";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = foreignEnv;
      }))
    ) "an outside row naming a credential env outside core's generated set must fail closed";
    pkgs.runCommand "connector-driver-rows-credential-env-in-core-set" { } "touch $out";

  connector-driver-rows-outside-row-reaches-document-and-box =
    let
      h = withFixture.internals;
    in
    assert assertMsg (
      h.driverEntry.name == "fixture"
    ) "the Harness's own Driver must resolve through the Connector row";
    pkgs.runCommand "connector-driver-rows-outside-row-reaches-document-and-box"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        doc=${h.runInputDocumentFile}
        jq -e '[.drivers[].name] == ["fixture", "claude"]' "$doc"
        wasm=$(jq -r '.drivers[0].module' "$doc")
        test -f "$wasm"
        case "$wasm" in */driver.wasm) ;; *) echo "unexpected module $wasm" >&2; exit 1 ;; esac

        line=$(grep '^DRIVER_ROWS_FILE=' ${h.driverPreambleFile})
        eval "$line"
        jq -e --arg w "$wasm" '.[0] == {name: "fixture", credentials: [{env: ["ANTHROPIC_API_KEY"], remedy: "set ANTHROPIC_API_KEY"}], sessionFlags: ["--session-id"], module: $w}
          and [.[].name] == ["fixture", "claude"]' "$DRIVER_ROWS_FILE"
        touch $out
      '';

  # The flake module hands the Connector row's `driver` to mkHarness, and only
  # when some Connector declares one.
  connector-driver-rows-flake-module-forwards-row =
    let
      consumerWith = modules: fixtures.connectorConsumerVia ([ ../../lib/flakeModule.nix ] ++ modules);
      withRow = consumerWith [ fixtureConnector.flakeModule ];
      without = consumerWith [ ];
      programOf = c: c.apps.${system}.default.program;
    in
    assert assertMsg (
      programOf without == fixtures.moduleConsumer.apps.${system}.default.program
    ) "with no Connector Driver row the module's app must be unchanged";
    pkgs.runCommand "connector-driver-rows-flake-module-forwards-row"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf (programOf withRow)}
        jq -e '[.drivers[].name] == ["fixture", "claude"]' "$doc"
        touch $out
      '';

  # Issue #6797: shadow mode. A Connector row naming a core Driver and carrying
  # a `module` can ask for its wasm to replay each Pass log beside the native
  # Strategy, which stays authoritative. Only the Box sees the shadow row.
  connector-driver-rows-shadow-reaches-box-only =
    let
      on = shadowOn.internals;
      facts = pkgs.writeText "opencode-facts.json" (
        builtins.toJSON (registry.descriptorFacts registry.entries.opencode)
      );
    in
    pkgs.runCommand "connector-driver-rows-shadow-reaches-box-only" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        line=$(grep '^DRIVER_ROWS_FILE=' ${on.driverPreambleFile})
        eval "$line"
        # claude's always-rendered row precedes the shadow row.
        jq -e --slurpfile facts ${facts} '
          [.[].name] == ["claude", "opencode"]
          and (.[1] | .shadow == true
            and (.module | endswith("/driver.wasm"))
            and (has("firstParty") | not)
            and (del(.module, .shadow)) == $facts[0])
          and (.[0] | has("shadow") | not)
        ' "$DRIVER_ROWS_FILE"
        test -f "$(jq -r '.[1].module' "$DRIVER_ROWS_FILE")"

        # The host never runs the shadow, so its document carries claude's row
        # alone.
        jq -e '[.drivers[].name] == ["claude"]' ${on.runInputDocumentFile}
        touch $out
      '';

  connector-driver-rows-shadow-off-is-unchanged =
    assert assertMsg (
      shadowOff.internals.driverPreambleFile.drvPath == plainOpencode.internals.driverPreambleFile.drvPath
    ) "a Connector row without `shadow` must leave the Box preamble unchanged";
    assert assertMsg (
      shadowOff.internals.runInputDocumentFile.drvPath
      == plainOpencode.internals.runInputDocumentFile.drvPath
    ) "a Connector row without `shadow` must leave the input document unchanged";
    pkgs.runCommand "connector-driver-rows-shadow-off-is-unchanged" { } "touch $out";

  # opencode is shadowed but the Harness runs the fixture Driver: its rows file
  # carries no shadow row, so the image never bakes the shadowed wasm.
  connector-driver-rows-shadow-only-for-selected-driver =
    pkgs.runCommand "connector-driver-rows-shadow-only-for-selected-driver"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq -e '[.[].name] == ["fixture", "claude"] and all(.[]; has("shadow") | not)' \
          ${shadowOther.internals.driverRowsFile}
        touch $out
      '';

  connector-driver-rows-shadow-needs-core-driver-with-module =
    assert assertMsg (preambleEvaluates shadowOn) "control: a core row with a module must shadow";
    assert assertMsg (
      !(preambleEvaluates (mkHarness {
        shadowDrivers = [ "opencode" ];
        driverRows.opencode = registry.entries.opencode;
      }))
    ) "a shadow row without a module must fail closed";
    assert assertMsg (
      !(preambleEvaluates (mkHarness {
        shadowDrivers = [ "fixture" ];
        driverRows.fixture = fixtureRow;
      }))
    ) "a shadow row outside the core registry has no native Strategy and must fail closed";
    assert assertMsg (
      !(preambleEvaluates (mkHarness {
        shadowDrivers = [ "opencode" ];
      }))
    ) "shadowing a Connector with no row must fail closed";
    pkgs.runCommand "connector-driver-rows-shadow-needs-core-driver-with-module" { } "touch $out";

  # The flake option is the switch; mkHarness only hears of it when on, and the
  # Connector's own module may be the only other writer of its row.
  connector-driver-rows-flake-module-shadow-option =
    let
      consumerWith = modules: fixtures.connectorConsumerVia ([ ../../lib/flakeModule.nix ] ++ modules);
      shadowed = consumerWith [ (opencodeConnectorModule { shadow = true; }) ];
      programOf = c: c.apps.${system}.default.program;
    in
    assert assertMsg (
      programOf shadowed != programOf (consumerWith [ (opencodeConnectorModule { }) ])
    ) "`shadow` on a native Driver's Connector must reach mkHarness";
    pkgs.runCommand "connector-driver-rows-flake-module-shadow-option" { } "touch $out";

  # claude's host half is its Connector (issue #6799): the Consumer switches
  # `shadow` and `primary` have no native Strategy to compare against or
  # replace, so each fails evaluation, in mkHarness and through the module.
  connector-driver-rows-claude-refuses-shadow-and-primary =
    let
      consumerWith =
        modules:
        fixtures.connectorConsumerVia (
          [
            ../../lib/flakeModule.nix
            claudeConnector.flakeModule
          ]
          ++ modules
        );
      evaluatesWith =
        modules:
        (builtins.tryEval (builtins.deepSeq (consumerWith modules).apps.${system}.default.program true))
        .success;
    in
    assert assertMsg (evaluatesWith [ ]) "control: the claude Connector with no switch must evaluate";
    assert assertMsg (
      !(evaluatesWith [ { spindrift.connectors.claude.shadow = true; } ])
    ) "`shadow` on the claude Connector must fail evaluation";
    assert assertMsg (
      !(evaluatesWith [ { spindrift.connectors.claude.primary = true; } ])
    ) "`primary` on the claude Connector must fail evaluation";
    assert assertMsg (
      !(preambleEvaluates (mkHarness {
        driverRows.claude = claudeRow;
        shadowDrivers = [ "claude" ];
      }))
    ) "mkHarness must refuse `shadowDrivers = [ \"claude\" ]`";
    assert assertMsg (
      !(preambleEvaluates (mkHarness {
        shadowDrivers = [ "claude" ];
      }))
    ) "mkHarness must refuse `shadowDrivers = [ \"claude\" ]` with no row too";
    assert assertMsg (
      !(evaluates (mkHarness {
        driverRows.claude = claudeRow;
        primaryDrivers = [ "claude" ];
      }))
    ) "mkHarness must refuse `primaryDrivers = [ \"claude\" ]`";
    assert assertMsg (
      !(evaluates (mkHarness {
        primaryDrivers = [ "claude" ];
      }))
    ) "mkHarness must refuse `primaryDrivers = [ \"claude\" ]` with no row too";
    pkgs.runCommand "connector-driver-rows-claude-refuses-shadow-and-primary" { } "touch $out";

  # Issue #6799: claude's Connector row is rendered for every Consumer, with no
  # `driverRows` at all and whichever Driver is selected. The row is the
  # batteries wasm, marked first-party, and neither shadow nor primary, in both
  # the launcher input document and the Box's rows file.
  connector-driver-rows-claude-row-always-rendered =
    let
      row = pkgs.lib.removeSuffix "\n" ''
        jq -e --arg w '${builtins.unsafeDiscardStringContext (claudeRow.module pkgs)}' '
          [.[] | select(.name == "claude")]
          | length == 1
            and (.[0] | .module == $w and .firstParty == true
              and (has("shadow") | not) and (has("primary") | not))'
      '';
      docRow = pkgs.lib.removeSuffix "\n" ''
        jq -e --arg w '${builtins.unsafeDiscardStringContext (claudeRow.module pkgs)}' '
          [.drivers[] | select(.name == "claude")]
          | length == 1
            and (.[0] | .module == $w and .firstParty == true
              and (has("shadow") | not) and (has("primary") | not))'
      '';
      other = mkHarness {
        driver = "opencode";
        defaults = opencodeDefaults;
      };
    in
    pkgs.runCommand "connector-driver-rows-claude-row-always-rendered"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${row} ${plain.internals.driverRowsFile}
        ${docRow} ${plain.internals.runInputDocumentFile}
        ${row} ${other.internals.driverRowsFile}
        ${docRow} ${other.internals.runInputDocumentFile}
        jq -e 'length == 1' ${plain.internals.driverRowsFile}
        grep -q '^DRIVER_ROWS_FILE=' ${plain.internals.driverPreambleFile}
        grep -q '^DRIVER_ROWS_FILE=' ${other.internals.driverPreambleFile}
        touch $out
      '';

  # Issue #6799: claude's host half is its Connector, so mkHarness needs a row
  # for it with a module that evaluates for the host, from the Consumer's
  # `driverRows` or else the batteries flake. checkConnectorRow admits a
  # core-named row without `module` (its facts equal core's) and `module` is
  # lazy, so each guard in `hostedRowOf` is reached and must still throw.
  connector-driver-rows-claude-hosted-row-needs-evaluating-module =
    let
      overridden = claudeRow // {
        module = _: "/nix/store/consumer-override-claude.wasm";
      };
    in
    assert assertMsg (evaluates (withRows {
      claude = claudeRow;
    })) "control: a claude row with a module must evaluate";
    assert assertMsg (evaluates plain)
      "control: no driverRows must fall back to the batteries flake's claude row";
    assert assertMsg (
      !(evaluates (withRows {
        claude = registry.entries.claude;
      }))
    ) "a claude row with no module must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        claude = claudeRow // {
          module = _: throw "claude module does not evaluate";
        };
      }))
    ) "a claude row whose module does not evaluate must fail evaluation";
    pkgs.runCommand "connector-driver-rows-claude-hosted-row-needs-evaluating-module"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq -e '[.[] | select(.name == "claude")]
          | length == 1 and .[0].module == "/nix/store/consumer-override-claude.wasm"' \
          ${(withRows { claude = overridden; }).internals.driverRowsFile}
        touch $out
      '';

  # Issues #6797, #6810, #6998, #6999, #6799, #6888, #6874, #6876: the dogfood
  # runs no backend as primary (github has no native adapter left) and shadows
  # opencode and pi; claude's Connector row is always rendered but, with no
  # native Strategy, is neither shadow nor primary. The real flake's default app
  # (flake.nix's `spindrift.connectors` through the module) must equal the
  # direct mirror's, whose Box rows file and input document carry `primary` on
  # the primaries only; the default Harness carries no `shadow`, and the
  # opencode and pi Harnesses carry it on their own rows. The app program embeds
  # both, so dropping flake.nix's wiring changes it. The mirror alone would not notice: it reads the
  # nix/dogfood-*.nix lists itself.
  connector-driver-rows-dogfood-shadows =
    assert assertMsg (config.apps.default.program == fixtures.harness.apps.default.program)
      "flake.nix must wire the nix/dogfood-shadow.nix and nix/dogfood-primary.nix Connectors: the dogfood app differs from the mirror (fixtures.harness)";
    pkgs.runCommand "connector-driver-rows-dogfood-shadows" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e --argjson names '${builtins.toJSON driverPrimaries}' '
        all(.[]; has("shadow") | not)
          and ([.[] | select(.name as $n | $names | index($n))] as $rows
            | ($rows | length) == ($names | length)
              and all($rows[]; .primary == true and .firstParty == true and has("module")))
      ' ${fixtures.harness.internals.driverRowsFile}
      jq -e '[.[] | select(.name == "claude")]
        | length == 1 and (.[0] | .firstParty == true and has("module")
          and (has("shadow") | not) and (has("primary") | not))' ${fixtures.harness.internals.driverRowsFile}
      jq -e '[.[] | select(.name == "opencode")]
        | length == 1 and (.[0] | .shadow == true and .firstParty == true and has("module"))' \
        ${fixtures.dogfoodOpencodeHarness.internals.driverRowsFile}
      jq -e '[.[] | select(.name == "pi")]
        | length == 1 and (.[0] | .shadow == true and .firstParty == true and has("module"))' \
        ${fixtures.dogfoodPiHarness.internals.driverRowsFile}
      ${fixtures.runInputDocOf config.apps.default.program}
      jq -e --argjson names '${builtins.toJSON driverPrimaries}' '
        ([.drivers[]? | select(.name as $n | $names | index($n))] as $rows
          | ($rows | length) == ($names | length)
            and all($rows[]; .primary == true and has("module")))
          and all((.drivers // [])[]; has("shadow") | not)
      ' "$doc"
      jq -e '[.drivers[] | select(.name == "claude")]
        | length == 1 and (.[0] | has("module") and (has("shadow") | not) and (has("primary") | not))' "$doc"
      jq -e 'all(.backends[]; has("shadow") or has("shadowEvery") or has("primary") | not)' "$doc"
      touch $out
    '';

  # Doctor's `connector-routes-widened[<name>]` reads each backend row's
  # `routes` against its `acceptedRoutes`. The forgejo row is registered but
  # neither primary nor shadowed, so only its place in the dogfood document
  # (routes == baseline, host from FORGEJO_BASE_URL) is pinned here.
  connector-driver-rows-dogfood-forgejo-route-table =
    pkgs.runCommand "connector-driver-rows-dogfood-forgejo-route-table"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf config.apps.default.program}
        jq -e --arg n forgejo '[.backends[] | select(.name == $n)]
          | length == 1 and (.[0]
            | (.routes | length) > 0
              and .routes == .acceptedRoutes
              and all(.routes[]; .host == "{base}")
              and .baseEnv == "FORGEJO_BASE_URL"
              and .credentialKnob == "forgejoToken"
              and (has("primary") | not) and (has("shadow") | not))' "$doc"
        touch $out
      '';

  # Issue #7139: the github Connector registers its backend row whatever
  # `primary` says. The document carries one row with its Route table and the
  # accepted baseline doctor diffs that table against, and neither flag.
  connector-driver-rows-github-row-unconditional =
    pkgs.runCommand "connector-driver-rows-github-row-unconditional"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf (githubProgramWith [ ])}
        jq -e --slurpfile b ${../../spindrift-connector-routes.json} '
          .backends | length == 1 and (.[0]
          | .credentialKnob == "ghToken"
            and (has("primary") | not) and (has("shadow") | not)
            and (.routes | length) > 0
            and .acceptedRoutes == ($b[0].github | if type == "array" then . else .routes // [] end))' "$doc"
        touch $out
      '';

  # Issue #6999: `primary` on a `driver` row reaches both sides. The launcher
  # input document (host) and the Box's rows file each carry the opencode row
  # with `primary` and its module (never the first-party mark: the fixture's
  # wasm is not the batteries build). Off, no `primary` key appears anywhere,
  # so the Consumer's outputs are unchanged. claude is not eligible
  # (connector-driver-rows-claude-refuses-shadow-and-primary).
  connector-driver-rows-primary-driver =
    let
      consumerWith = modules: fixtures.connectorConsumerVia ([ ../../lib/flakeModule.nix ] ++ modules);
      programOf = c: c.apps.${system}.default.program;
      primaryBox = mkHarness {
        driverRows.opencode = opencodeRow;
        primaryDrivers = [ "opencode" ];
      };
      plainBox = mkHarness { driverRows.opencode = opencodeRow; };
      bothBox = mkHarness {
        driverRows.opencode = opencodeRow;
        shadowDrivers = [ "opencode" ];
        primaryDrivers = [ "opencode" ];
      };
      outsideBox = mkHarness {
        driverRows.fixture = fixtureRow;
        primaryDrivers = [ "fixture" ];
      };
      moduleless = mkHarness {
        driverRows.opencode = registry.entries.opencode;
        primaryDrivers = [ "opencode" ];
      };
    in
    assert assertMsg (evaluates primaryBox) "control: a primary opencode row must evaluate";
    assert assertMsg (
      !(evaluates bothBox)
    ) "a row both shadowed and primary must fail closed in mkHarness";
    assert assertMsg (
      !(evaluates outsideBox)
    ) "`primary` on a Driver outside the core registry must fail closed";
    assert assertMsg (!(evaluates moduleless)) "`primary` on a row without a module must fail closed";
    pkgs.runCommand "connector-driver-rows-primary-driver" { nativeBuildInputs = [ pkgs.jq ]; } ''
      ${fixtures.runInputDocOf (
        programOf (consumerWith [ (opencodeConnectorModule { primary = true; }) ])
      )}
      jq -e '[.drivers[] | select(.name == "opencode")]
        | length == 1 and all(.[]; .primary == true and has("module") and (has("shadow") | not))' "$doc"
      jq -e '[.drivers[] | select(.name == "claude")] | length == 1 and (.[0] | has("primary") | not)' "$doc"
      jq -e '[.[] | select(.name == "opencode")]
        | length == 1 and all(.[]; .primary == true and has("module") and (has("firstParty") | not) and (has("shadow") | not))' \
        ${primaryBox.internals.driverRowsFile}
      ${fixtures.runInputDocOf (programOf (consumerWith [ (opencodeConnectorModule { }) ]))}
      jq -e 'all((.drivers // [])[]; has("primary") | not)' "$doc"
      jq -e 'all(.[]; has("primary") | not)' ${plainBox.internals.driverRowsFile}
      touch $out
    '';

  # Every refusal names the row; tryEval cannot read a thrown message, so the
  # text is pinned on lib/connector-names.nix's primaryError and the module
  # path is pinned to fail.
  connector-driver-rows-primary-refusals =
    let
      names = import ../../lib/connector-names.nix;
      consumerWith =
        modules:
        fixtures.connectorConsumerVia (
          [
            ../../lib/flakeModule.nix
            githubConnector.flakeModule
            claudeConnector.flakeModule
          ]
          ++ modules
        );
      evaluatesWith =
        modules:
        (builtins.tryEval (builtins.deepSeq (consumerWith modules).apps.${system}.default.program true))
        .success;
      # A backend row whose name has no native backend to replace. `primary` is
      # set only when true: a backend row refuses even `primary = false`.
      nativeless = primary: {
        spindrift.connectors.nativeless = {
          knobs.nativelessToken = {
            env = "NATIVELESS_TOKEN";
            group = "dispatch";
            secret = true;
            doc = "test knob";
            flakeOption = false;
            boxEnv = false;
          };
          backend = {
            module = _: "/nativeless.wasm";
            routes = [
              {
                method = "GET";
                host = "example.invalid";
                path = "/api";
              }
            ];
          };
        }
        // pkgs.lib.optionalAttrs primary { primary = true; };
      };
      errorFor =
        row:
        names.primaryError registry.nativeDriverNames "row1" (
          {
            shadow = false;
            driver = null;
            backend = null;
          }
          // row
        );
      messages = map errorFor [
        { shadow = true; }
        { driver = { }; }
        { }
      ];
    in
    assert assertMsg (builtins.all (
      e: e != null && pkgs.lib.hasInfix "Connector 'row1'" e
    ) messages) "every `primary` refusal must name the row";
    assert assertMsg (evaluatesWith [
      (nativeless false)
    ]) "control: the native-less row evaluates without `primary`";
    assert assertMsg (
      !(evaluatesWith [ (nativeless true) ])
    ) "`primary` on a row with no native backend must fail evaluation";
    assert assertMsg (evaluatesWith [ ]) "control: the github backend row evaluates without `primary`";
    assert assertMsg (
      !(evaluatesWith [ { spindrift.connectors.github.primary = true; } ])
    ) "`primary` on the github backend row must fail evaluation: no native backend remains";
    assert assertMsg (
      !(evaluatesWith [
        {
          spindrift.connectors.github = {
            primary = true;
            shadow = true;
          };
        }
      ])
    ) "`primary` with `shadow` must fail evaluation";
    # jira has no native adapter (connectorOnly), so its Connector row is
    # refused `primary` with the "no native backend" message.
    assert assertMsg (evaluatesWith [
      jiraConnector.flakeModule
    ]) "control: the jira Connector row evaluates without `primary`";
    assert assertMsg (
      !(evaluatesWith [
        jiraConnector.flakeModule
        { spindrift.connectors.jira.primary = true; }
      ])
    ) "`primary` on the jira row must fail evaluation: no native backend remains";
    # forgejo's native tracker is gone (issue #7122): like github and jira it
    # is connectorOnly, so its row is refused `primary`.
    assert assertMsg (evaluatesWith [
      forgejoConnector.flakeModule
    ]) "control: the forgejo Connector row evaluates without `primary`";
    assert assertMsg (
      !(evaluatesWith [
        forgejoConnector.flakeModule
        { spindrift.connectors.forgejo.primary = true; }
      ])
    ) "`primary` on the forgejo row must fail evaluation: no native backend remains";
    assert assertMsg (
      !(evaluatesWith [ { spindrift.connectors.claude.primary = true; } ])
    ) "`primary` on the claude driver row must fail evaluation: it has no native Strategy";
    assert assertMsg (
      names.primaryError registry.nativeDriverNames "claude" {
        shadow = false;
        driver = { };
        backend = null;
      } != null
    ) "a Driver row with no native Strategy of its name must be refused";
    assert assertMsg (
      names.primaryError registry.nativeDriverNames "opencode" {
        shadow = false;
        driver = { };
        backend = null;
      } == null
    ) "control: a Driver row with a native Strategy must be accepted";
    pkgs.runCommand "connector-driver-rows-primary-refusals" { } "touch $out";
}
