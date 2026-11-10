# The fixture Connector's flake-parts module: registers one row that owns one
# env knob, in the shape of a core row's `knobs` (lib/chores/*.nix).
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
}
