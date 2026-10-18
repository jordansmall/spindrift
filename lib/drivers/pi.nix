# The pi Driver: pure data only (ADR 0009, ADR 0066). ./default.nix validates
# this entry's shape and renders it into what lib/mkHarness.nix bakes into the
# image.
{ ... }:
{
  name = "pi";

  # nixpkgs' own build, from the Consumer's locked nixpkgs: no overlay, no
  # vendoring.
  package = pkgs: pkgs.pi-coding-agent;

  bin = "pi";

  # No credential rule: pi's auth knob is out of scope for this row.
  credentials = [ ];

  # pi model ids are `<provider>/<model>`, so the bare-worker gate (issue
  # #5331) and the bare-model eval failure (issue #5355) apply as for opencode.
  providerNamespacedModels = true;

  # No subagent is provisioned under pi yet, so lib/mkHarness.nix drops the
  # scout, worker and filer roles and bakes their flags false.
  subagents = false;

  # Only the flags shared by every invocation; driver-exec adds --model,
  # --thinking and the positional prompt around them.
  flagsCommon = "--print --mode json --no-approve";

  # pi reads skills from its agent directory (PI_CODING_AGENT_DIR, default
  # ~/.pi/agent), not from claude's.
  skillsDirRelative = ".pi/agent/skills";

  # pi keeps sessions at sessions/<one dir per cwd>/<id>.jsonl under its agent
  # dir. The per-issue mount takes the sessions subdir, not .pi/agent, or it
  # would shadow auth.json and skills/ beside it (ADR 0009 amendment, issue
  # #5481).
  sessionCacheDirRelative = ".pi/agent/sessions";

  # Keeps pi from checking for updates or reporting telemetry from inside the
  # Box; the piseam integration tests set the same two.
  envCommon = {
    PI_SKIP_VERSION_CHECK = "1";
    PI_TELEMETRY = "0";
  };

  # pi has no --agents equivalent and no subagent files are baked yet.
  agentsJsonTemplate = { roster }: "";
  agentFilesTemplate = { roster }: { };

  # agentsFlag is omitted. Effort values pass through to --thinking
  # unnormalised.
  argvShape = {
    promptStyle = "positional";
    modelFlag = "--model";
    modelOmitEmpty = true;
    effortFlag = "--thinking";
    order = [
      "driverFlags"
      "model"
      "effort"
      "session"
      "prompt"
    ];
  };
}
