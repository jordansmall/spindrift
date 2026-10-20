# The check behind a Driver's `effortOnModelLevels` fact (issue #5483): the
# Driver sends a role's effort as a `<model>:<level>` suffix on its model, so
# an effort with no model to ride on would be dropped silently, and a level the
# Driver does not know is sent through as part of the model id (ADR 0009
# amendment). A model that already ends in a known `:<level>` keeps it and the
# role's effort (still validated) is dropped, so the suffix is never doubled
# (issue #5811). Pure so a check can read the message that a `throw` inside
# mkHarness would hide from builtins.tryEval.
{ lib }:
let
  # A role without a model of its own rides the coordinator's, as at run time.
  # Roster entries are already past the #392 opt-out drop, which keeps its
  # meaning here: a role opted out with `model = ""` loses its effort too.
  resolve =
    {
      roster,
      levels,
      coordinatorModel,
    }:
    map (
      e:
      let
        effort = e.effort or "";
        model = if (e.model or "") != "" then e.model else coordinatorModel;
        suffixMatch = builtins.match "(.*):([^:]*)" model;
        baseModel = if suffixMatch == null then null else builtins.head suffixMatch;
        ownLevel = if suffixMatch == null then null else builtins.elemAt suffixMatch 1;
      in
      if effort == "" then
        e
      else if model == "" then
        throw (message.noModel e.name effort)
      else if !(lib.elem effort levels) then
        throw (message.badLevel e.name effort levels)
      else if ownLevel != null && lib.elem ownLevel levels then
        e
        // {
          model = baseModel;
          effort = ownLevel;
        }
      else
        e // { inherit model; }
    ) roster;

  message = {
    noModel =
      role: effort:
      "spindrift: the ${role} role sets effort ${builtins.toJSON effort} but has no model and the coordinator has none to fall back to; this Driver sends effort as a suffix on the model, so set a model on the role or the default model, or clear the effort (issue #5483).";
    badLevel =
      role: effort: levels:
      "spindrift: the ${role} role's effort ${builtins.toJSON effort} is not one of ${lib.concatStringsSep ", " levels}; this Driver would send it as part of the model id (issue #5483).";
  };
in
{
  inherit resolve message;
}
