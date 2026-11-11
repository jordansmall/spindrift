# Issue #6796: a Connector's Driver row (spindrift.connectors.<name>.driver)
# reaches mkHarness as `driverRows`. A row outside the core registry travels in
# the launcher input document and the Box's driver preamble; every row is held
# to the registry's own shape rules, and fails closed where a Go-side or
# generated list would silently disagree with it.
{
  pkgs,
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
  claudeConnector = (import ../../connectors/claude).lib.connector { core = import ../../core; };
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
          and (.[0] | del(.module, .shadow)) == $facts[0]
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
}
