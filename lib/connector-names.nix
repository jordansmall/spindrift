# The error for a Connector row name registered by more than one module
# (ADR 0073). Pure so nix/checks/connectors.nix can pin the text:
# builtins.tryEval cannot see thrown messages. `defs` is the module system's
# definition list; each carries the `file` of the module that wrote it. A def
# that sets nothing but `shadow`, its rate `shadowEvery` or `primary` is a
# Consumer's switch on a registered row (issues #6797, #6810, #6998), not a
# second registration.
rec {
  # Names of the backends with a native adapter. A `connectorOnly` backend
  # (jira) has none, so its Connector row cannot be `primary`. One source for
  # the module and nix/checks/connector-driver-rows.nix.
  nativeBackendNames = map (b: b.name) (
    builtins.filter (b: !(b.connectorOnly or false)) (import ./backends/default.nix)
  );

  # Why a row's `primary` (issues #6998, #6999) cannot stand, or null.
  # `nativeNames` is the native backend registry's names, `nativeDriverNames`
  # the core Driver registry's. A row with a `driver` is checked as a Driver, one
  # with a `backend` as a backend, and a row with both as each.
  primaryError =
    nativeNames: nativeDriverNames: name: r:
    if r.shadow then
      "Connector '${name}': `primary` and `shadow` are exclusive on one row"
    else if r.driver == null && r.backend == null then
      "Connector '${name}': `primary` needs a `backend` row to answer reads"
    else if r.driver != null && !(builtins.elem name nativeDriverNames) then
      "Connector '${name}': `primary` needs a native Driver of the same name to replace; none is registered"
    else if r.backend != null && !(builtins.elem name nativeNames) then
      "Connector '${name}': `primary` needs a native backend of the same name to replace; none is registered"
    else
      null;

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
            && builtins.all (n: n == "shadow" || n == "shadowEvery" || n == "primary") (
              builtins.attrNames d.value
            );
          err = duplicateError (builtins.elemAt loc (builtins.length loc - 1)) (
            builtins.filter (d: !(switchOnly d)) defs
          );
        in
        if err != null then throw err else type.merge loc defs;
      substSubModules = m: uniqueRow (type.substSubModules m);
    };
}
