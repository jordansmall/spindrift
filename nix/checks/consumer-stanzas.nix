# Eval-level pins for nix/consumer-stanzas.nix, the composer that joins each
# backend row's `consumerStanza` with its Connector's knobs (issue #7415).
# tryEval exposes only success or failure, never the thrown text, so the
# "message names the row and the knob" pins read the composer's pure `errors`.
{ pkgs, ... }:
let
  inherit (pkgs.lib) assertMsg hasInfix;
  renderers = import ../../lib/renderers.nix;
  envSchema = import ../../lib/env-schema.nix;
  structuralExamples = import ../../lib/structural-template-examples.nix { inherit (pkgs) lib; };
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
      connectorData = null;
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

  # One pairing, a jira tracker with the github forge it writes through:
  # rendering only these stanzas must carry exactly their knobs and secrets,
  # none of forgejo's, local's or git's. Picked by row facts, not names, so
  # backend-name-guard holds (ADR 0068).
  trackerOnly = pkgs.lib.findFirst (
    r: (r.validAsTracker or false) && !(r.validAsCodeForge or false)
  ) null registry;
  pairedNames = map (r: r.name) (
    builtins.filter (
      r: r == trackerOnly || pkgs.lib.toUpper r.name == trackerOnly.trackerAxisWrite
    ) registry
  );
  pairing = builtins.filter (s: builtins.elem s.name pairedNames) real;
  # How a settings knob shows in the rendered block: its leaf as `leaf = `, and
  # a dotted nixSubPath's namespace as `ns = {`.
  settingsForms =
    stanzas:
    builtins.concatMap (
      s:
      pkgs.lib.mapAttrsToList (
        k: e:
        let
          segs = pkgs.lib.splitString "." (e.nixSubPath or k);
        in
        {
          leaf = "${pkgs.lib.last segs} = ";
          ns = if builtins.length segs > 1 then "${builtins.head segs} = {" else null;
        }
      ) s.settings
    ) stanzas;
  formsOf =
    stanzas:
    builtins.filter (f: f != null) (
      builtins.concatMap (f: [
        f.leaf
        f.ns
      ]) (settingsForms stanzas)
    );
  pairingSettingsForms = formsOf pairing;
  # A form an unselected row shares with a selected one (both name a `baseURL`)
  # is not evidence of leakage, so only forms no selected row emits count.
  droppedSettingsForms = builtins.filter (f: !(builtins.elem f pairingSettingsForms)) (
    formsOf (builtins.filter (s: !(builtins.elem s pairing)) real)
  );
  envExample = renderers.renderHarnessEnvExample envSchema pairing;
  settingsBlock = renderers.renderTemplateSettingsBlock envSchema pairing structuralExamples;
  envNames = map (pkgs.lib.removeSuffix "=") (
    builtins.filter (l: builtins.match "[A-Z][A-Z0-9_]*=" l != null) (
      pkgs.lib.splitString "\n" envExample
    )
  );
  backendOwned = builtins.concatMap secretEnvs real;
  pairingOwned = builtins.sort builtins.lessThan (
    builtins.filter (n: builtins.elem n backendOwned) envNames
  );
in
{
  consumer-stanzas =
    assert assertMsg (
      map (s: s.name) real == map (r: r.name) registry
    ) "consumer-stanzas: the composition must hold one entry per backend row, in registry order";
    assert assertMsg
      (builtins.all (
        s: s ? displayName && s ? settings && s ? secrets && s ? flakeInput && s ? connectorData
      ) real)
      "consumer-stanzas: every entry must carry displayName, settings, secrets, flakeInput and connectorData";
    assert assertMsg (
      map (s: s.name) (builtins.filter (s: s.secrets == [ ]) real) == [
        "git"
        "local"
      ]
    ) "consumer-stanzas: only the core backends without a token (git, local) may list no secret";
    assert assertMsg (
      secretEnvs (builtins.head real) == [ "GH_TOKEN" ]
    ) "consumer-stanzas: github lists its row token alone, which is its Connector's credential";
    assert assertMsg
      (
        (builtins.head real).credential == "GH_TOKEN"
        && builtins.all (p: (p.fst.credential == null) == (p.snd.core or false)) (
          pkgs.lib.zipLists real registry
        )
      )
      "consumer-stanzas: github's credential is its core token, and only a core backend (no Connector) has none";
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
    assert assertMsg (
      secretEnvs (pkgs.lib.findFirst (s: s.name == trackerOnly.name) null real) == [
        "JIRA_TOKEN"
        "JIRA_EMAIL"
      ]
    ) "consumer-stanzas: a core-knob Basic identity follows its row token in the stanza's secrets";
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
    assert assertMsg
      (
        pairingOwned == [
          "GH_TOKEN"
          "JIRA_EMAIL"
          "JIRA_TOKEN"
        ]
      )
      "consumer-stanzas: a jira+github pairing's env example must list exactly their secrets, got ${toString pairingOwned}";
    assert assertMsg (builtins.all (n: builtins.elem n envNames) [
      "ANTHROPIC_API_KEY"
      "CLAUDE_CODE_OAUTH_TOKEN"
    ]) "consumer-stanzas: a pairing's env example must keep the core secrets";
    assert assertMsg (builtins.all (k: hasInfix k settingsBlock)
      pairingSettingsForms
    ) "consumer-stanzas: a pairing's settings block must carry its selected rows' settings knobs";
    assert assertMsg (
      droppedSettingsForms != [ ] && builtins.all (k: !(hasInfix k settingsBlock)) droppedSettingsForms
    ) "consumer-stanzas: a pairing's settings block must drop the unselected rows' settings knobs";
    pkgs.runCommand "consumer-stanzas" { } "touch $out";
}
