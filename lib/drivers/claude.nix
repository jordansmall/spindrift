# The claude Driver: pure data only (ADR 0009, issue #624). The registry
# (./default.nix) validates this entry's shape and renders it into what
# lib/mkHarness.nix bakes into the image. The bats harness sources
# mkHarness.internals.driverPreambleFile, byte-identical to the preamble the
# image bakes in, so the suite exercises the same bytes (issue #433).
{ lib }:
{
  name = "claude";

  package = pkgs: pkgs.claude-code;

  bin = "claude";

  # Space-separated so driver-exec can word-split these (strings.Fields).
  # --disallowedTools removes the tools that promise a later re-invocation the
  # headless runner never makes (issue #1609; #1542 lost a run when the Driver
  # backgrounded its test gate behind ScheduleWakeup). Keep the tool names one
  # comma-separated token: driver-exec/args.go splits this value on whitespace.
  flagsCommon = "--verbose --output-format stream-json --dangerously-skip-permissions --disallowedTools ScheduleWakeup,CronCreate,CronDelete,CronList,RemoteTrigger,Monitor";

  # Issue #2011: claude then omits run_in_background from the Bash, Agent/Task,
  # and PowerShell input schemas, so the model cannot park a headless turn on a
  # notification a one-shot `claude -p` never receives. --disallowedTools reaches
  # a tool name, never a parameter. reject-background-bash.sh still covers
  # shell-level backgrounding (`&`, nohup, setsid, coproc).
  envCommon = {
    CLAUDE_CODE_DISABLE_BACKGROUND_TASKS = "1";
  };

  # Issue #4409: Claude Code caps a Bash call at 10 minutes by default; these
  # are the two env vars that raise the default and the max. A Consumer's
  # Bash-timeout knob is exported under each name.
  bashTimeoutEnv = [
    "BASH_DEFAULT_TIMEOUT_MS"
    "BASH_MAX_TIMEOUT_MS"
  ];

  skillsDirRelative = ".claude/skills";

  # Issue #427/#447/#448, ADR 0009: the launcher mounts a writable per-issue
  # host directory over this so a fix pass resumes the initial run's session.
  # A Driver that omits this attribute gets no per-issue cache and no mount,
  # so it has no resumable session state.
  sessionCacheDirRelative = ".claude/projects";

  # Rendered at eval time by builtins.toJSON (ADR 0007 tier-1) so model names
  # never reach bash as interpolated strings. Takes the whole roster (issue
  # #264); lib/mkHarness.nix already dropped empty-model entries through
  # rosterLib.dropOptedOut, so e.model is non-empty. `prompt` stays "" because
  # cmd/launcher/internal/promptassembly injects it at runtime; claude's schema
  # has no `mode` key.
  agentsJsonTemplate =
    { roster }:
    let
      agents = lib.listToAttrs (
        map (e: {
          name = e.name;
          value = {
            description = e.description or "";
            prompt = "";
            tools = e.tools or [ ];
            model = e.model;
          }
          // (if (e.effort or "") != "" then { effort = e.effort; } else { });
        }) roster
      );
    in
    if agents == { } then "" else builtins.toJSON agents;

  # claude declares subagents through agentsJsonTemplate's --agents flag, not
  # the on-disk agents/*.md files opencode uses, so lib/image.nix bakes none.
  agentFilesTemplate = _: { };

  # claude's CLI argv shape (ADR 0009, issue #2534). driver-exec assembles the
  # invocation in the order listed.
  argvShape = {
    promptStyle = "flag";
    promptFlag = "-p";
    modelFlag = "--model";
    modelOmitEmpty = false;
    agentsFlag = "--agents";
    effortFlag = "--effort";
    order = [
      "prompt"
      "model"
      "agents"
      "session"
      "driverFlags"
      "effort"
    ];
  };
}
