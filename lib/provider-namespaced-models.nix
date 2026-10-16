# The check behind a Driver's `providerNamespacedModels` fact (issue #5355):
# every roster entry's model, and the default model, must be a
# `<provider>/<model>` id. Pure so a check can read the message that a
# `throw` inside mkHarness would hide from builtins.tryEval.
{ lib }:
let
  namespaced = lib.hasInfix "/";

  # An empty default passes: opencode omits `-m` (modelOmitEmpty) and uses its
  # own default. Roster entries are already past the #392 opt-out drop.
  offenders =
    {
      roster,
      defaultModel,
    }:
    map (e: {
      role = e.name;
      inherit (e) model;
    }) (lib.filter (e: !(namespaced e.model)) roster)
    ++ lib.optional (defaultModel != "" && !(namespaced defaultModel)) {
      role = "default";
      model = defaultModel;
    };

  message =
    { role, model }:
    "spindrift: the ${role} model ${builtins.toJSON model} is not a `<provider>/<model>` id, which this Driver requires (issue #5355); set it to a namespaced id${
      lib.optionalString (role != "default") ", or to \"\" to opt the role out"
    }.";
in
{
  inherit namespaced offenders message;
}
