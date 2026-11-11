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
  # An outside row is a full lib/drivers row plus `module`. Its credential env
  # must already be one the generated scrub lists know (core's set).
  fixtureRow = {
    name = "fixture";
    package = p: p.hello;
    bin = "fixture-cli";
    flagsCommon = "--fixture";
    skillsDirRelative = ".fixture/skills";
    agentsJsonTemplate = _: "{}";
    agentFilesTemplate = _: { };
    argvShape = {
      promptStyle = "flag";
      promptFlag = "-p";
      modelFlag = "--model";
      modelOmitEmpty = false;
      effortFlag = "--effort";
      order = [
        "prompt"
        "model"
        "session"
        "driverFlags"
        "effort"
      ];
    };
    credentials = [
      {
        env = [ "ANTHROPIC_API_KEY" ];
        remedy = "set ANTHROPIC_API_KEY";
      }
    ];
    module = p: "${fixtures.connectorWasm}/driver.wasm";
  };
  withRows = rows: mkHarness { driverRows = rows; };
  withFixture = mkHarness {
    driver = "fixture";
    driverRows.fixture = fixtureRow;
  };
  plain = mkHarness { };
  # Forces the validation chain without a build.
  evaluates =
    h: (builtins.tryEval (builtins.seq h.internals.runInputDocumentFile.drvPath true)).success;
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
        ! grep -q DRIVER_ROWS_FILE ${plain.internals.driverPreambleFile}
        touch $out
      '';

  # The flake module hands the Connector row's `driver` to mkHarness, and only
  # when some Connector declares one.
  connector-driver-rows-flake-module-forwards-row =
    let
      consumerWith = modules: fixtures.connectorConsumerVia ([ ../../lib/flakeModule.nix ] ++ modules);
      withRow = consumerWith [ { spindrift.connectors.fixture.driver = fixtureRow; } ];
      without = consumerWith [ ];
      programOf = c: c.apps.${system}.default.program;
    in
    assert assertMsg (
      programOf without == fixtures.moduleConsumer.apps.${system}.default.program
    ) "with no Connector Driver row the module's app must be unchanged";
    pkgs.runCommand "connector-driver-rows-flake-module-forwards-row"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        doc=$(grep -o '/nix/store/[a-z0-9]*-launcher-run-input.json' ${programOf withRow} | head -n 1)
        jq -e '.drivers | length == 1 and .[0].name == "fixture"' "$doc"
        touch $out
      '';
}
