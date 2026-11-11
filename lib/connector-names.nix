# The error for a Connector row name registered by more than one module
# (ADR 0073). Pure so nix/checks/connectors.nix can pin the text:
# builtins.tryEval cannot see thrown messages. `defs` is the module system's
# definition list; each carries the `file` of the module that wrote it. A def
# that sets nothing but `shadow` is a Consumer's switch on a registered row
# (issue #6797), not a second registration.
rec {
  duplicateError =
    name: defs:
    let
      n = builtins.length defs;
    in
    if n < 2 then
      null
    else
      "perSystem.spindrift.connectors.${name} is registered ${
        if n == 2 then "twice" else "${toString n} times"
      }: by ${builtins.concatStringsSep " and by " (map (d: d.file) defs)}";

  # `type`, an attrsOf element type, made to throw `duplicateError` on a second
  # definition instead of merging. Not `types.unique`: its message is multi-line
  # and names no sources. `substSubModules` must re-wrap: the module system
  # rebuilds a submodule type through it, which would drop the override.
  uniqueRow =
    type:
    type
    // {
      merge =
        loc: defs:
        let
          switchOnly = d: builtins.isAttrs d.value && builtins.attrNames d.value == [ "shadow" ];
          err = duplicateError (builtins.elemAt loc (builtins.length loc - 1)) (
            builtins.filter (d: !(switchOnly d)) defs
          );
        in
        if err != null then throw err else type.merge loc defs;
      substSubModules = m: uniqueRow (type.substSubModules m);
    };
}
