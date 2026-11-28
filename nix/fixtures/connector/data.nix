# The fixture Connector as data (ADR 0073): one row that owns env knobs, in the
# shape of a core row's `knobs` (lib/chores/*.nix), a Driver row (issue #6796)
# whose wasm this Connector builds itself, and a network backend row (issue
# #6805). Core turns it into the `spindrift.connectors.fixture` row.
{ core }:
{
  name = "fixture";

  knobs = {
    fixtureGreeting = {
      env = "FIXTURE_GREETING";
      group = "dispatch";
      default = "hello";
      doc = "greeting the fixture Connector announces; host-only, exists to prove a Connector row's knob reaches the env schema";
      flakeOption = false;
      boxEnv = false;
    };

    # The backend row's credential knob: the row names none, core derives
    # FIXTURE_TOKEN from the row name; this knob's env matches it, so it shows
    # as the row's `credentialKnob`.
    fixtureToken = {
      env = "FIXTURE_TOKEN";
      group = "dispatch";
      secret = true;
      doc = "bearer token the fixture network Connector's backend row authorizes with; host-only, exists to prove a Connector row's credential knob reaches the backend row";
      flakeOption = false;
      boxEnv = false;
    };
  };

  # A full lib/drivers row plus `module`. Its package and CLI only need to
  # evaluate; the credential env must be one core's generated scrub lists know.
  driver = {
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
      sessionFlags = [ "--session-id" ];
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
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/driver.wasm";
  };

  # A network Connector's backend row (issue #6805): the Route table the host
  # enforces. It names no credential; core derives FIXTURE_TOKEN (see
  # fixtureToken).
  module = pkgs: "${import ./package.nix { inherit pkgs core; }}/network.wasm";
  axes = [ "tracker" ];
  # Declaring an axis means serving it (issue #7647).
  fragments = {
    name = "fixture";
    dir = ./fragments;
    shipped = {
      "filer-dedup.md" = [ "fixture" ];
      "pr-body-issue-ref.md" = [ "fixture" ];
      "pr-body-no-issue-ref.md" = [ "fixture" ];
    };
  };
  displayName = "Fixture";
  routes = [
    {
      method = "GET";
      host = "api.fixture.test";
      path = "/repos";
    }
  ];
}
