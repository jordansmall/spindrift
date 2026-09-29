# Single declaration of the built-in butler Chores (ADR 0056, issue #3991).
# lib/env-schema.nix's butlerChoreClasses default, the generated Go builtins
# list (cmd/launcher/internal/chore/builtins_gen.go), and docs all derive from
# this one list. List order is load-bearing: it fixes classesDefault's byte
# layout, which the generated docs and flag table render verbatim. This list
# declares names and default classes only -- prompt files resolve by name
# from mkHarness's choresDir (read by, e.g., the BUTLER_CHORES eval assertion
# and the image copy), which a Consumer may replace.
let
  chores = [
    {
      name = "bugs";
      classes = [
        "error-handling"
        "resource-leak"
      ];
    }
    {
      name = "refactor";
      classes = [ "dead-code" ];
    }
    {
      name = "docs-drift";
      classes = [ "stale-reference" ];
    }
  ];
  names = map (c: c.name) chores;
  classesDefault = builtins.concatStringsSep " " (
    map (c: "${c.name}=${builtins.concatStringsSep "," c.classes}") chores
  );
in
{
  inherit names classesDefault;
}
