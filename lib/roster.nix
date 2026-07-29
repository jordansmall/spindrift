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
  # Normalizes a roster list before any Driver consumes it (issue #2152 slice
  # A): validates each entry's name and injects a promptFile default for any
  # entry that omits one, so every Driver-facing consumer can assume every
  # entry already carries a promptFile rather than re-deriving the default
  # itself. Deliberately does no escaping (a later slice's concern) and never
  # filters by model -- that stays the Drivers' job (see
  # drivers-opencode-agent-files-omits-empty-model in nix/checks/drivers.nix).
  normalizeRoster =
    roster:
    let
      inherit (lib) foldl' imap0;
      step =
        acc:
        { idx, e }:
        if !(e ? name) then
          throw "normalizeRoster: entry ${toString idx} is missing a name -- every roster entry must set name"
        else if builtins.match "[a-z0-9-]+" e.name == null then
          throw "normalizeRoster: entry ${toString idx} has an invalid name ${builtins.toJSON e.name} -- names must match [a-z0-9-]+"
        else if acc.seen ? ${e.name} then
          throw "normalizeRoster: duplicate name ${builtins.toJSON e.name} at entries ${toString acc.seen.${e.name}} and ${toString idx}"
        else
          {
            seen = acc.seen // {
              ${e.name} = idx;
            };
            out = acc.out ++ [ (if e ? promptFile then e else e // { promptFile = "${e.name}-prompt.md"; }) ];
          };
      # An empty roster is a deliberate agent-less image (issue #2152) -- the
      # fold's base case naturally returns [] without ever throwing, no
      # special-case needed.
      result = foldl' step {
        seen = { };
        out = [ ];
      } (imap0 (idx: e: { inherit idx e; }) roster);
    in
    result.out;

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
