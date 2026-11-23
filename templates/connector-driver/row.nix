# connectorname's Driver row. No core row names it, so this is the whole row,
# not core's plus `module`: fill in the CLI the Driver runs in the Box. The
# closed attribute set and the argvShape rules are in lib/drivers/default.nix.
# `module` is a function of pkgs so the host and the Box each build it with
# their own pkgs.
{ core, ... }:
{
  name = "connectorname";

  # The CLI the Box runs, and its flags common to every invocation. `package`
  # and `bin` are placeholders: point `package` at one that provides `bin`.
  package = pkgs: pkgs.hello;
  bin = "connectorname";
  flagsCommon = "";

  skillsDirRelative = ".claude/skills";
  agentsJsonTemplate = _: "";
  agentFilesTemplate = _: { };

  # How the launcher assembles the CLI's argv.
  argvShape = {
    promptStyle = "positional";
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

  # The env a missing credential is reported for. Each name must be one core's
  # generated scrub lists know (mkHarness refuses any other).
  credentials = [
    {
      env = [ "ANTHROPIC_API_KEY" ];
      remedy = "set ANTHROPIC_API_KEY";
    }
  ];

  module = pkgs: "${import ./package.nix { inherit pkgs core; }}/connectorname.wasm";
}
