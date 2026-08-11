# The default agent roster (issue #264): a first-class, N-agent list of
# { name; model; effort; mode; description; tools; promptFile; prompt }
# entries that both Drivers (lib/drivers/claude.nix's agentsJsonTemplate,
# lib/drivers/opencode.nix's agentFilesTemplate) render from, replacing the
# four hardcoded scout/reviewer/filer/worker model-knob args each Driver
# template used to take directly. `defaultRoster` reproduces today's four
# agents byte-for-byte (same descriptions/tools/promptFile names as the
# templates previously baked in). Its primary, roster-native surface is the
# `models` attrset (issue #2426), keyed by roster entry name (scout/
# reviewer/filer/worker). A name absent from `models` inherits that agent's
# `lib/env-schema.nix` default (issue #2434) -- the same default
# `mkHarness`'s no-roster fallback path resolves through `mergedDefaults`.
# An unknown name in `models` throws at eval time, the same way
# `normalizeRoster` rejects an invalid entry name. The four legacy
# positional knobs (scoutModel/reviewModel/filerModel/workerModel) default
# to `null` -- a sentinel distinguishing "not supplied" from "supplied as
# empty" -- and still work as a lower-precedence fallback per name, since
# lib/mkHarness.nix resolves its deprecated `settings.*Model` knobs through
# them, always supplying an explicit (non-null) value. Precedence per name:
# `models.<name>` (including an explicit `""` opt-out) wins over an
# explicitly supplied legacy knob, which wins over the schema default.
# `prompt` is
# always `null` here -- entrypoint.sh injects each agent's rendered prompt at
# runtime from `promptFile`, never at eval time (see agent/entrypoint.sh's
# generic prompt-injection loop). `effort`, like `model`, is an optional
# pass-through on the general roster schema -- no normalization -- that each
# Driver forwards verbatim when set (issue #2242). `defaultRoster`
# additionally ships a fixed default `effort` per agent (scout=medium,
# reviewer=high, filer=medium, worker=high; issue #2386) as a literal on
# each entry below -- a caller assembling a custom roster by hand still gets
# no injected default, since that stays specific to `defaultRoster`'s own
# literals, not a `normalizeRoster`-level behavior.
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
      scoutModel ? null,
      reviewModel ? null,
      filerModel ? null,
      workerModel ? null,
      models ? { },
    }:
    let
      schema = import ./env-schema.nix;
      schemaDefaults = {
        scout = schema.scoutModel.default;
        reviewer = schema.reviewModel.default;
        filer = schema.filerModel.default;
        worker = schema.workerModel.default;
      };
      legacyModels = {
        scout = scoutModel;
        reviewer = reviewModel;
        filer = filerModel;
        worker = workerModel;
      };
      unknownNames = builtins.filter (n: !(legacyModels ? ${n})) (builtins.attrNames models);
      modelFor =
        name:
        if models ? ${name} then
          models.${name}
        else if legacyModels.${name} != null then
          legacyModels.${name}
        else
          schemaDefaults.${name};
    in
    if unknownNames != [ ] then
      throw "defaultRoster: models names unknown agent(s) ${builtins.toJSON unknownNames} -- expected one of ${builtins.toJSON (builtins.attrNames legacyModels)}"
    else
      [
        {
          name = "scout";
          model = modelFor "scout";
          effort = "medium";
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
          model = modelFor "reviewer";
          effort = "high";
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
          model = modelFor "filer";
          effort = "medium";
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
          model = modelFor "worker";
          effort = "high";
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
