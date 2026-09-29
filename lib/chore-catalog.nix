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
      # Patch rung (ADR 0057): a chore's patch-eligible subset of its
      # classes above, [] until a class earns patch trust.
      patchClasses = [ ];
    }
    {
      name = "refactor";
      classes = [ "dead-code" ];
      patchClasses = [ ];
    }
    {
      name = "docs-drift";
      classes = [ "stale-reference" ];
      patchClasses = [ "stale-reference" ];
    }
  ];
  names = map (c: c.name) chores;
  classesDefault = builtins.concatStringsSep " " (
    map (c: "${c.name}=${builtins.concatStringsSep "," c.classes}") chores
  );
  # Same rendering as classesDefault, but only for a chore with a non-empty
  # patchClasses -- an entry naming a chore with [] would fail
  # env-schema.nix's own grammar (parseClasses rejects an empty class list).
  patchClassesDefault = builtins.concatStringsSep " " (
    map (c: "${c.name}=${builtins.concatStringsSep "," c.patchClasses}") (
      builtins.filter (c: c.patchClasses != [ ]) chores
    )
  );
in
{
  inherit names classesDefault patchClassesDefault;
}
