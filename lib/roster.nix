# The default agent roster (issue #264): a first-class, N-agent list of
# { name; model; mode; description; tools; promptFile; prompt } entries that
# both Drivers (lib/drivers/claude.nix's agentsJsonTemplate,
# lib/drivers/opencode.nix's agentFilesTemplate) render from, replacing the
# four hardcoded scout/reviewer/filer/worker model-knob args each Driver
# template used to take directly. `defaultRoster` reproduces today's four
# agents byte-for-byte (same descriptions/tools/promptFile names as the
# templates previously baked in), parameterized only by the four legacy
# model knobs so lib/mkHarness.nix can keep exposing them
# (scoutModel/reviewModel/filerModel/workerModel, deprecated but still
# supported) while resolving them into a roster under the hood. `prompt` is
# always `null` here -- entrypoint.sh injects each agent's rendered prompt at
# runtime from `promptFile`, never at eval time (see agent/entrypoint.sh's
# generic prompt-injection loop).
{ lib }:
{
  defaultRoster =
    {
      scoutModel,
      reviewModel,
      filerModel,
      workerModel,
    }:
    [
      {
        name = "scout";
        model = scoutModel;
        mode = "subagent";
        description = "Map relevant files, seams, and tests; return a structured brief";
        tools = [
          "Read"
          "Bash"
          "WebFetch"
          "WebSearch"
          "Glob"
          "Grep"
        ];
        promptFile = "scout-prompt.md";
        prompt = null;
      }
      {
        name = "reviewer";
        model = reviewModel;
        mode = "subagent";
        description = "Review the branch diff for spec compliance and coding standards";
        tools = [
          "Read"
          "Bash"
          "WebFetch"
          "Agent"
        ];
        promptFile = "review-prompt.md";
        prompt = null;
      }
      {
        name = "filer";
        model = filerModel;
        mode = "subagent";
        description = "File issues from a review's non-blocking findings, best-effort";
        tools = [
          "Read"
          "Bash"
          "WebFetch"
        ];
        promptFile = "filer-prompt.md";
        prompt = null;
      }
      {
        name = "worker";
        model = workerModel;
        mode = "subagent";
        description = "Implement a scoped slice of work delegated to it, with full implement-capable tools";
        tools = [
          "Read"
          "Bash"
          "Edit"
          "Write"
          "Glob"
          "Grep"
          "WebFetch"
        ];
        promptFile = "worker-prompt.md";
        prompt = null;
      }
    ];
}
