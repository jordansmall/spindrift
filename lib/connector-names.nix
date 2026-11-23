# The error for a Connector row name registered by more than one module
# (ADR 0073). Pure so nix/checks/connectors.nix can pin the text:
# builtins.tryEval cannot see thrown messages. `defs` is the module system's
# definition list; each carries the `file` of the module that wrote it. A def
# that sets nothing but `shadow`, its rate `shadowEvery` or `primary` is a
# Consumer's switch on a registered row (issues #6797, #6810, #6998), not a
# second registration.
rec {
  # The Consumer switches a backend row refuses (issue #7456), in the order
  # the refusal lists them. One source for the module's `definedSwitches` and
  # `uniqueRow`'s switch-only defs.
  switchNames = [
    "shadow"
    "shadowEvery"
    "primary"
  ];

  # Why a row's `primary` (issues #6998, #6999) cannot stand, or null.
  # `nativeDriverNames` is the core Driver registry's. A backend row never
  # reaches here: checkedConnectors refuses its switches first, so only the
  # `driver` checks remain.
  primaryError =
    nativeDriverNames: name: r:
    if r.shadow then
      "Connector '${name}': `primary` and `shadow` are exclusive on one row"
    else if r.driver == null && r.backend == null then
      "Connector '${name}': `primary` needs a `driver` row to replace the native Driver"
    else if r.driver != null && !(builtins.elem name nativeDriverNames) then
      "Connector '${name}': `primary` needs a native Driver of the same name to replace; none is registered"
    else
      null;

  # Why a backend row's Consumer switches cannot stand, or null. `set` is the
  # names of `shadow`, `shadowEvery` and `primary` the Consumer defined on the
  # row (issue #7456): every backend is Connector-only, so none has a native
  # backend to shadow or replace and the setting only misleads.
  backendSwitchError =
    name: set:
    if set == [ ] then
      null
    else
      "Connector '${name}': `${builtins.concatStringsSep "`, `" set}` cannot be set on a backend row: ${name} is Connector-only, with no native backend to shadow or replace. Delete the setting from spindrift.connectors.${name}.";

  duplicateError =
    name: defs:
    let
      n = builtins.length defs;
    in
    if n < 2 then
      null
    else
      "spindrift.connectors.${name} is registered ${
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
          switchOnly =
            d:
            builtins.isAttrs d.value
            && builtins.attrNames d.value != [ ]
            && builtins.all (n: builtins.elem n switchNames) (builtins.attrNames d.value);
          err = duplicateError (builtins.elemAt loc (builtins.length loc - 1)) (
            builtins.filter (d: !(switchOnly d)) defs
          );
        in
        if err != null then throw err else type.merge loc defs;
      substSubModules = m: uniqueRow (type.substSubModules m);
    };
}
