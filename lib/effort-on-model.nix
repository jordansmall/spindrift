# The check behind a Driver's `effortOnModelLevels` fact (issue #5483): the
# Driver sends a role's effort as a `<model>:<level>` suffix on its model, so a
# level the Driver does not know is sent through as part of the model id (ADR
# 0009 amendment). A model that already ends in a known `:<level>` keeps it and
# the role's effort (still validated) is dropped, so the suffix is never
# doubled (issue #5811). Pure so a check can read the message that a `throw`
# inside mkHarness would hide from builtins.tryEval.
{ lib }:
let
  # Roster entries are already past the #392 opt-out drop, so every entry has a
  # model and a role opted out with `model = ""` loses its effort too.
  applyEffort =
    { roster, levels }:
    map (
      e:
      let
        effort = e.effort or "";
        suffixMatch = builtins.match "(.*):([^:]*)" e.model;
        baseModel = if suffixMatch == null then null else builtins.head suffixMatch;
        ownLevel = if suffixMatch == null then null else builtins.elemAt suffixMatch 1;
      in
      if effort == "" then
        e
      else if !(lib.elem effort levels) then
        throw (message.badLevel e.name effort levels)
      else if ownLevel != null && lib.elem ownLevel levels then
        e
        // {
          model = baseModel;
          effort = ownLevel;
        }
      else
        e
    ) roster;

  message = {
    badLevel =
      role: effort: levels:
      "spindrift: the ${role} role's effort ${builtins.toJSON effort} is not one of ${lib.concatStringsSep ", " levels}; this Driver would send it as part of the model id (issue #5483).";
  };
in
{
  inherit applyEffort message;
}
