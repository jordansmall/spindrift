# The fixture Connector's flake-parts module: registers one row that owns one
# env knob, in the shape of a core row's `knobs` (lib/chores/*.nix), and a
# Driver row (issue #6796) whose wasm this Connector builds itself.
{ core }:
{
  # Depends on the core it was handed; the module system dedups by path, so a
  # Consumer that imports the same core module is unaffected.
  imports = [ core.flakeModules.default ];

  spindrift.connectors.fixture.knobs.fixtureGreeting = {
    env = "FIXTURE_GREETING";
    group = "dispatch";
    default = "hello";
    doc = "greeting the fixture Connector announces; host-only, exists to prove a Connector row's knob reaches the env schema";
    flakeOption = false;
    boxEnv = false;
  };

  # A full lib/drivers row plus `module`. Its package and CLI only need to
  # evaluate; the credential env must be one core's generated scrub lists know.
  spindrift.connectors.fixture.driver = {
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
    module = pkgs: "${import ./package.nix { inherit pkgs core; }}/driver.wasm";
  };
}
