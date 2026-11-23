# Eval-level pins for nix/consumer-stanzas.nix, the composer that joins each
# backend row's `consumerStanza` with its Connector's knobs (issue #7415).
# tryEval exposes only success or failure, never the thrown text, so the
# "message names the row and the knob" pins read the composer's pure `errors`.
{ pkgs, ... }:
let
  inherit (pkgs.lib) assertMsg hasInfix;
  compose = args: import ../consumer-stanzas.nix ({ inherit (pkgs) lib; } // args);
  real = (compose { }).stanzas;
  registry = import ../../lib/backends;

  token = {
    env = "STUB_TOKEN";
    doc = "stub core token";
    secret = true;
  };
  schema = {
    stubToken = token;
  };
  secretKnob = {
    env = "STUB_CONNECTOR_TOKEN";
    doc = "stub connector token";
    secret = true;
  };
  emailKnob = {
    env = "STUB_CONNECTOR_EMAIL";
    doc = "stub connector email";
  };
  mkRow = connectorKnobs: {
    name = "stub";
    displayName = "Stub";
    tokenEnvVar = "STUB_TOKEN";
    consumerStanza = {
      inherit connectorKnobs;
      flakeInput = null;
      moduleImport = null;
    };
  };
  connectorRows.stub.knobs = {
    stubConnectorToken = secretKnob;
    stubConnectorEmail = emailKnob;
  };
  fixture = rows: {
    backendRows = rows;
    inherit connectorRows schema;
  };

  good =
    (compose (fixture [
      (mkRow [
        "stubConnectorToken"
        "stubConnectorEmail"
      ])
    ])).stanzas;
  omitsSecret = fixture [ (mkRow [ "stubConnectorEmail" ]) ];
  unknownKnob = fixture [ (mkRow [ "stubConnectorTokne" ]) ];
  noStanza = fixture [ (builtins.removeAttrs (mkRow [ ]) [ "consumerStanza" ]) ];
  # A backend Connector (one declaring `backend`) with no backend row, beside a
  # non-backend Connector that has none by design and must be ignored.
  orphanConnector =
    (fixture [
      (mkRow [
        "stubConnectorToken"
        "stubConnectorEmail"
      ])
    ])
    // {
      connectorRows = connectorRows // {
        ghost = {
          knobs = { };
          backend.credential = "ghostToken";
        };
        driverOnly.knobs = { };
      };
    };
  fails = args: !(builtins.tryEval (builtins.deepSeq (compose args).stanzas true)).success;
  messages = args: builtins.concatStringsSep "\n" (compose args).errors;
  secretEnvs = s: map (e: e.env) s.secrets;
in
{
  consumer-stanzas =
    assert assertMsg (
      map (s: s.name) real == map (r: r.name) registry
    ) "consumer-stanzas: the composition must hold one entry per backend row, in registry order";
    assert assertMsg
      (builtins.all (
        s: s ? displayName && s ? settings && s ? secrets && s ? flakeInput && s ? moduleImport
      ) real)
      "consumer-stanzas: every entry must carry displayName, settings, secrets, flakeInput and moduleImport";
    assert assertMsg (
      map (s: s.name) (builtins.filter (s: s.secrets == [ ]) real) == [
        "git"
        "local"
      ]
    ) "consumer-stanzas: only the core backends without a token (git, local) may list no secret";
    assert assertMsg (
      secretEnvs (builtins.head real) == [
        "GH_TOKEN"
        "SPINDRIFT_GITHUB_CONNECTOR_TOKEN"
      ]
    ) "consumer-stanzas: github lists its row token, then its Connector's credential";
    assert assertMsg (builtins.all (
      s: builtins.all (k: k.flakeOption or false) (builtins.attrValues s.settings)
    ) real) "consumer-stanzas: settings must hold only flakeOption knobs";
    assert assertMsg (
      secretEnvs (builtins.head good) == [
        "STUB_TOKEN"
        "STUB_CONNECTOR_TOKEN"
        "STUB_CONNECTOR_EMAIL"
      ]
    ) "consumer-stanzas: secrets hold the row token then the listed connector knobs, in listed order";
    assert assertMsg (fails omitsSecret)
      "consumer-stanzas: a Connector secret knob the stanza omits must fail eval";
    assert assertMsg (
      hasInfix "lib/backends/stub.nix" (messages omitsSecret)
      && hasInfix "stubConnectorToken" (messages omitsSecret)
    ) "consumer-stanzas: the omitted-secret message must name the row file and the knob";
    assert assertMsg (fails unknownKnob)
      "consumer-stanzas: a listed knob the Connector lacks must fail eval";
    assert assertMsg (
      hasInfix "lib/backends/stub.nix" (messages unknownKnob)
      && hasInfix "stubConnectorTokne" (messages unknownKnob)
    ) "consumer-stanzas: the unknown-knob message must name the row file and the knob";
    assert assertMsg (fails orphanConnector)
      "consumer-stanzas: a backend Connector matching no backend row must fail eval";
    assert assertMsg (
      hasInfix "backend Connector ghost" (messages orphanConnector)
      && !(hasInfix "driverOnly" (messages orphanConnector))
    ) "consumer-stanzas: the orphan message must name the Connector and ignore non-backend Connectors";
    assert assertMsg (fails noStanza) "consumer-stanzas: a row without a consumerStanza must fail eval";
    pkgs.runCommand "consumer-stanzas" { } "touch $out";
}
