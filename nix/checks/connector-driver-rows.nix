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
  fixtureConnector = (import ../fixtures/connector).lib.connector { core = import ../../core; };
  fixtureRow =
    (import ../fixtures/connector/module.nix { core = import ../../core; })
    .spindrift.connectors.fixture.driver;
  withRows = rows: mkHarness { driverRows = rows; };
  withFixture = mkHarness {
    driver = "fixture";
    driverRows.fixture = fixtureRow;
  };
  plain = mkHarness { };
  # The dogfood's primary Connectors: backend rows carry `primary` themselves,
  # Driver rows are named in mkHarness's `primaryDrivers`. Derived so no
  # backend name is spelled in a test (ADR 0068, backend-name-guard).
  backendPrimaries = fixtures.dogfoodBackendPrimaries;
  driverPrimaries = fixtures.dogfoodDriverPrimaries;
  claudeConnector = (import ../../connectors/claude).lib.connector { core = import ../../core; };
  githubConnector = (import ../../connectors/github).lib.connector { core = import ../../core; };
  githubProgramWith =
    modules:
    (fixtures.connectorConsumerVia (
      [
        ../../lib/flakeModule.nix
        githubConnector.flakeModule
      ]
      ++ modules
    )).apps.${system}.default.program;
  claudeRow = claudeConnector.driverRow pkgs.lib;
  shadowOn = mkHarness {
    driverRows.claude = claudeRow;
    shadowDrivers = [ "claude" ];
  };
  shadowOff = mkHarness { driverRows.claude = claudeRow; };
  shadowOther = mkHarness {
    driver = "fixture";
    driverRows = {
      claude = claudeRow;
      fixture = fixtureRow;
    };
    shadowDrivers = [ "claude" ];
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
        shadowDrivers = [ "claude" ];
      };
      renamed = withRows {
        renamed = claudeRow // {
          name = "renamed";
        };
      };
      claudeWasmDir = builtins.unsafeDiscardStringContext (builtins.dirOf (claudeRow.module pkgs));
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
    assert assertMsg (preambleEvaluates shadowOn) "control: the batteries claude row must evaluate";
    assert assertMsg (preambleEvaluates swapped)
      "control: a batteries row with another module must evaluate";
    assert assertMsg (evaluates renamed) "control: a renamed batteries row must evaluate";
    pkgs.runCommand "connector-driver-rows-first-party-mark" { nativeBuildInputs = [ pkgs.jq ]; } ''
      line=$(grep '^DRIVER_ROWS_FILE=' ${shadowOn.internals.driverPreambleFile})
      eval "$line"
      jq -e 'length == 1 and .[0].name == "claude" and .[0].firstParty == true' "$DRIVER_ROWS_FILE"

      # The batteries row's name with a Consumer's wasm, and the batteries wasm
      # under a Consumer's name, are not first-party.
      jq -e 'length == 1 and .[0].name == "claude" and (.[0] | has("firstParty") | not)' ${swapped.internals.driverRowsFile}
      jq -e 'length == 1 and .[0].name == "renamed" and (.[0] | has("firstParty") | not)' ${renamed.internals.driverRowsFile}
      jq -e '[.drivers[] | has("firstParty")] | any | not' ${renamed.internals.runInputDocumentFile}

      # A third-party Connector's row, and the input document, stay unmarked.
      jq -e 'length == 1 and (.[0] | has("firstParty") | not)' ${withFixture.internals.driverRowsFile}
      jq -e '[.drivers[] | has("firstParty")] | any | not' ${withFixture.internals.runInputDocumentFile}
      touch $out
    '';

  connector-driver-rows-core-name-must-match-core =
    assert assertMsg (evaluates (withRows {
      claude = registry.entries.claude;
    })) "control: a Connector row equal to core's claude row must evaluate";
    assert assertMsg (
      !(evaluates (withRows {
        claude = driftedClaude;
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
      plainDoc = plain.internals.runInputDocumentFile;
    in
    assert assertMsg (
      h.driverEntry.name == "fixture"
    ) "the Harness's own Driver must resolve through the Connector row";
    pkgs.runCommand "connector-driver-rows-outside-row-reaches-document-and-box"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        doc=${h.runInputDocumentFile}
        jq -e '.drivers | length == 1 and .[0].name == "fixture"' "$doc"
        wasm=$(jq -r '.drivers[0].module' "$doc")
        test -f "$wasm"
        case "$wasm" in */driver.wasm) ;; *) echo "unexpected module $wasm" >&2; exit 1 ;; esac

        line=$(grep '^DRIVER_ROWS_FILE=' ${h.driverPreambleFile})
        eval "$line"
        jq -e --arg w "$wasm" '. == [{name: "fixture", credentials: [{env: ["ANTHROPIC_API_KEY"], remedy: "set ANTHROPIC_API_KEY"}], module: $w}]' "$DRIVER_ROWS_FILE"

        # No Connector Driver rows: no `drivers` key and no preamble line, so
        # existing documents and images stay byte-identical.
        jq -e 'has("drivers") | not' ${plainDoc}
        if grep -q DRIVER_ROWS_FILE ${plain.internals.driverPreambleFile}; then exit 1; fi
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
        jq -e '.drivers | length == 1 and .[0].name == "fixture"' "$doc"
        touch $out
      '';

  # Issue #6797: shadow mode. A Connector row naming a core Driver and carrying
  # a `module` can ask for its wasm to replay each Pass log beside the native
  # Strategy, which stays authoritative. Only the Box sees the shadow row.
  connector-driver-rows-shadow-reaches-box-only =
    let
      on = shadowOn.internals;
      facts = pkgs.writeText "claude-facts.json" (
        builtins.toJSON (registry.descriptorFacts registry.entries.claude)
      );
    in
    pkgs.runCommand "connector-driver-rows-shadow-reaches-box-only" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        line=$(grep '^DRIVER_ROWS_FILE=' ${on.driverPreambleFile})
        eval "$line"
        jq -e --slurpfile facts ${facts} '
          length == 1 and .[0].shadow == true
          and (.[0].module | endswith("/claude.wasm"))
          and .[0].firstParty == true
          and (.[0] | del(.module, .shadow, .firstParty)) == $facts[0]
        ' "$DRIVER_ROWS_FILE"
        test -f "$(jq -r '.[0].module' "$DRIVER_ROWS_FILE")"

        # The host never runs the shadow, so its document carries no row.
        jq -e 'has("drivers") | not' ${on.runInputDocumentFile}
        touch $out
      '';

  connector-driver-rows-shadow-off-is-unchanged =
    assert assertMsg (
      shadowOff.internals.driverPreambleFile.drvPath == plain.internals.driverPreambleFile.drvPath
    ) "a Connector row without `shadow` must leave the Box preamble unchanged";
    assert assertMsg (
      shadowOff.internals.runInputDocumentFile.drvPath == plain.internals.runInputDocumentFile.drvPath
    ) "a Connector row without `shadow` must leave the input document unchanged";
    pkgs.runCommand "connector-driver-rows-shadow-off-is-unchanged" { } "touch $out";

  # claude is shadowed but the Harness runs the fixture Driver: its rows file
  # carries the fixture row alone, so the image never bakes claude.wasm.
  connector-driver-rows-shadow-only-for-selected-driver =
    pkgs.runCommand "connector-driver-rows-shadow-only-for-selected-driver"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq -e 'length == 1 and .[0].name == "fixture" and (.[0] | has("shadow") | not)' \
          ${shadowOther.internals.driverRowsFile}
        touch $out
      '';

  connector-driver-rows-shadow-needs-core-driver-with-module =
    assert assertMsg (preambleEvaluates shadowOn) "control: a core row with a module must shadow";
    assert assertMsg (
      !(preambleEvaluates (mkHarness {
        shadowDrivers = [ "claude" ];
        driverRows.claude = registry.entries.claude;
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
        shadowDrivers = [ "claude" ];
      }))
    ) "shadowing a Connector with no row must fail closed";
    pkgs.runCommand "connector-driver-rows-shadow-needs-core-driver-with-module" { } "touch $out";

  # The flake option is the switch; mkHarness only hears of it when on, and the
  # Connector's own module may be the only other writer of its row.
  connector-driver-rows-flake-module-shadow-option =
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
      shadowed = consumerWith [ { spindrift.connectors.claude.shadow = true; } ];
      programOf = c: c.apps.${system}.default.program;
    in
    assert assertMsg (
      programOf (consumerWith [ ]) == fixtures.moduleConsumer.apps.${system}.default.program
    ) "the claude Connector without `shadow` must leave the module's app unchanged";
    assert assertMsg (
      programOf shadowed != programOf (consumerWith [ ])
    ) "`shadow` on the claude Connector must reach mkHarness";
    pkgs.runCommand "connector-driver-rows-flake-module-shadow-option" { } "touch $out";

  # Issues #6797, #6810, #6998, #6999: the dogfood runs claude (a Driver row)
  # and github (a backend row) as primary and shadows nothing. The real flake's
  # default app (flake.nix's `spindrift.connectors` through the module) must
  # equal the direct mirror's, whose Box rows file and input document carry
  # `primary` and no `shadow`; the app
  # program embeds both, so dropping flake.nix's wiring changes it. The mirror
  # alone would not notice: it reads the nix/dogfood-*.nix lists itself.
  connector-driver-rows-dogfood-shadows =
    assert assertMsg (config.apps.default.program == fixtures.harness.apps.default.program)
      "flake.nix must wire the nix/dogfood-shadow.nix and nix/dogfood-primary.nix Connectors: the dogfood app differs from the mirror (fixtures.harness)";
    pkgs.runCommand "connector-driver-rows-dogfood-shadows" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e --argjson names '${builtins.toJSON driverPrimaries}' '
        ($names | index("claude")) != null
          and all(.[]; has("shadow") | not)
          and ([.[] | select(.name as $n | $names | index($n))] as $rows
            | ($rows | length) == ($names | length)
              and all($rows[]; .primary == true and .firstParty == true and has("module")))
      ' ${fixtures.harness.internals.driverRowsFile}
      ${fixtures.runInputDocOf config.apps.default.program}
      jq -e --argjson names '${builtins.toJSON driverPrimaries}' '
        ([.drivers[]? | select(.name as $n | $names | index($n))] as $rows
          | ($rows | length) == ($names | length)
            and all($rows[]; .primary == true and has("module")))
          and all((.drivers // [])[]; has("shadow") | not)
      ' "$doc"
      jq -e --argjson names '${builtins.toJSON backendPrimaries}' '
        [.backends[] | select(.name as $n | $names | index($n))] as $rows
        | ($names | length) > 0
          and ($rows | length) == ($names | length)
          and all($rows[]; .primary == true and (has("shadow") | not) and (has("shadowEvery") | not))
      ' "$doc"
      touch $out
    '';

  # Issue #7020: the dogfood proves the Connector sandbox on real traffic
  # before it becomes mandatory. Each side must carry the knob in the document
  # settings itself; the whole-program parity above would not say which knob
  # diverged, and would pass if both sides dropped it.
  connector-driver-rows-dogfood-sandbox =
    pkgs.runCommand "connector-driver-rows-dogfood-sandbox" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf config.apps.default.program}
        jq -e '.settings.CONNECTOR_SANDBOX == "1"' "$doc" >/dev/null \
          || { echo "dogfood flake app: CONNECTOR_SANDBOX is not \"1\"" >&2; exit 1; }
        ${fixtures.runInputDocOf fixtures.harness.apps.default.program}
        jq -e '.settings.CONNECTOR_SANDBOX == "1"' "$doc" >/dev/null \
          || { echo "fixtures mirror: CONNECTOR_SANDBOX is not \"1\"" >&2; exit 1; }
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
              and .credentialKnob == "forgejoConnectorToken"
              and (has("primary") | not) and (has("shadow") | not))' "$doc"
        touch $out
      '';

  # Issue #6998: `primary` is a switch on a registered backend row; on, the
  # row's entry says so. Off, the github row is not registered at all
  # (connector-driver-rows-github-row-gated-on-switches).
  connector-driver-rows-primary-reaches-document =
    pkgs.runCommand "connector-driver-rows-primary-reaches-document"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf (githubProgramWith [ { spindrift.connectors.github.primary = true; } ])}
        jq -e --argjson names '${builtins.toJSON backendPrimaries}' '[.backends[] | select(.name as $n | $names | index($n))]
          | length == ($names | length) and all(.[]; .primary == true and (has("shadow") | not))' "$doc"
        touch $out
      '';

  # Issue #6912: the github Connector registers its backend row only with
  # `shadow` or `primary` on. Both off, the document has no `backends` key;
  # `shadow` on, it carries one row with its sampling, Route table, and the
  # accepted baseline doctor diffs that table against.
  connector-driver-rows-github-row-gated-on-switches =
    pkgs.runCommand "connector-driver-rows-github-row-gated-on-switches"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf (githubProgramWith [ ])}
        jq -e 'has("backends") | not' "$doc"
        ${fixtures.runInputDocOf (githubProgramWith [ { spindrift.connectors.github.shadow = true; } ])}
        jq -e --slurpfile b ${../../spindrift-connector-routes.json} '
          .backends | length == 1 and (.[0]
          | .shadow == true and has("shadowEvery")
            and (.routes | length) > 0
            and .acceptedRoutes == $b[0].github)' "$doc"
        touch $out
      '';

  # Issue #6999: `primary` on a `driver` row reaches both sides. The launcher
  # input document (host) and the Box's rows file each carry the claude row
  # with `primary`, its module and, in the Box, the first-party mark. Off, no
  # `primary` key appears anywhere, so the Consumer's outputs are unchanged.
  connector-driver-rows-primary-driver =
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
      programOf = c: c.apps.${system}.default.program;
      primaryBox = mkHarness {
        driverRows.claude = claudeRow;
        primaryDrivers = [ "claude" ];
      };
      plainBox = mkHarness { driverRows.claude = claudeRow; };
      bothBox = mkHarness {
        driverRows.claude = claudeRow;
        shadowDrivers = [ "claude" ];
        primaryDrivers = [ "claude" ];
      };
      outsideBox = mkHarness {
        driverRows.fixture = fixtureRow;
        primaryDrivers = [ "fixture" ];
      };
      moduleless = mkHarness {
        driverRows.claude = removeAttrs claudeRow [ "module" ];
        primaryDrivers = [ "claude" ];
      };
    in
    assert assertMsg (evaluates primaryBox) "control: a primary claude row must evaluate";
    assert assertMsg (
      !(evaluates bothBox)
    ) "a row both shadowed and primary must fail closed in mkHarness";
    assert assertMsg (
      !(evaluates outsideBox)
    ) "`primary` on a Driver outside the core registry must fail closed";
    assert assertMsg (!(evaluates moduleless)) "`primary` on a row without a module must fail closed";
    pkgs.runCommand "connector-driver-rows-primary-driver" { nativeBuildInputs = [ pkgs.jq ]; } ''
      ${fixtures.runInputDocOf (
        programOf (consumerWith [ { spindrift.connectors.claude.primary = true; } ])
      )}
      jq -e '[.drivers[] | select(.name == "claude")]
        | length == 1 and all(.[]; .primary == true and has("module") and (has("shadow") | not))' "$doc"
      jq -e '[.[] | select(.name == "claude")]
        | length == 1 and all(.[]; .primary == true and has("module") and .firstParty == true and (has("shadow") | not))' \
        ${primaryBox.internals.driverRowsFile}
      ${fixtures.runInputDocOf (programOf (consumerWith [ ]))}
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
      # A backend row whose name has no native backend to replace.
      nativeless = primary: {
        spindrift.connectors.nativeless = {
          inherit primary;
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
            credential = "nativelessToken";
          };
        };
      };
      errorFor =
        row:
        names.primaryError [ "github" ] [ "claude" ] "row1" (
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
        { backend = { }; }
      ];
    in
    assert assertMsg (builtins.all (
      e: e != null && pkgs.lib.hasInfix "Connector 'row1'" e
    ) messages) "every `primary` refusal must name the row";
    assert assertMsg (
      errorFor {
        backend = { };
        driver = null;
      } != null
    ) "a row with no native backend of its name must be refused";
    assert assertMsg (
      names.primaryError [ "github" ] [ "claude" ] "claude" {
        shadow = false;
        driver = { };
        backend = { };
      } != null
    ) "a row with both a driver and a backend must still pass the backend checks";
    assert assertMsg (
      names.primaryError [ "row1" ] [ "claude" ] "row1" {
        shadow = false;
        driver = null;
        backend = { };
      } == null
    ) "control: a backend row with a native backend must be accepted";
    assert assertMsg (evaluatesWith [
      (nativeless false)
    ]) "control: the native-less row evaluates without `primary`";
    assert assertMsg (
      !(evaluatesWith [ (nativeless true) ])
    ) "`primary` on a row with no native backend must fail evaluation";
    assert assertMsg (evaluatesWith [
      { spindrift.connectors.github.primary = true; }
    ]) "control: `primary` on the github backend row must evaluate";
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
    assert assertMsg (evaluatesWith [
      { spindrift.connectors.claude.primary = true; }
    ]) "control: `primary` on the claude driver row must evaluate";
    pkgs.runCommand "connector-driver-rows-primary-refusals" { } "touch $out";
}
