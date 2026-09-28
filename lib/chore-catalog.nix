# Single declaration of the built-in butler Chores (ADR 0056, issue #3991).
# lib/env-schema.nix's butlerChoreClasses default, the generated Go builtins
# list (cmd/launcher/internal/chore/builtins_gen.go), and docs all derive from
# this one list. List order is load-bearing: it fixes classesDefault's byte
# layout, which the generated docs and flag table render verbatim.
let
  chores = [
    {
      name = "bugs";
      prompt = ../templates/default/prompts/chores/bugs.md;
      classes = [
        "error-handling"
        "resource-leak"
      ];
    }
    {
      name = "refactor";
      prompt = ../templates/default/prompts/chores/refactor.md;
      classes = [ "dead-code" ];
    }
    {
      name = "docs-drift";
      prompt = ../templates/default/prompts/chores/docs-drift.md;
      classes = [ "stale-reference" ];
    }
  ];
  names = map (c: c.name) chores;
  classesDefault = builtins.concatStringsSep " " (
    map (c: "${c.name}=${builtins.concatStringsSep "," c.classes}") chores
  );
in
{
  inherit chores names classesDefault;
}
