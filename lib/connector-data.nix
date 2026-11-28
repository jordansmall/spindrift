# A Connector registered as data: a flat attribute set core alone turns into
# its `spindrift.connectors.<name>` row, so the Connector ships no flake-parts
# module of its own. A Consumer lists it in `spindrift.connectorData`.
#
#   { name; module; routes; axes; displayName; credential; scheme; identity;
#     base; gitRemote; knobs; fragments; driver; }
#
# `name` is required. `module` (a function of pkgs returning the wasm path)
# declares a backend row and needs `routes`; `scheme`, `identity`, `base` and
# `gitRemote` belong to that row. A third-party row also declares `axes` (a
# non-empty subset of tracker and forge) and `displayName`; a first-party row
# sets neither. Only mkHarness can tell the two apart, so it enforces both and
# neither is required here. Only a first-party row names a `credential`;
# a third-party row's is derived from `name`. `knobs`, `fragments` and `driver`
# are the row's own fields. `driver` is a row set, or a function of
# nixpkgs `lib` returning one: a core-derived row
# (`(core.lib.driverEntries { inherit lib; }).opencode // { module }`) needs a
# `lib` a data file does not have in scope. Deep checks (Route rules, scheme
# enum, credential ownership) stay with core's row types and validators; this
# file only refuses what those would let through silently, because the row
# type is freeform: an unknown key, a wrongly typed field, backend fields
# without `module`, or a backend row missing `routes`.
#
# Builtins only. No reader applies `module` or touches `driver.module`, and
# `rowsOf` leaves a function `driver` an unforced thunk, so a reader that hands
# the Connector a stub core `{ }` can still read its knobs and backend row.
let
  # Field -> the `builtins.typeOf` values it may hold.
  fields = {
    name = [ "string" ];
    module = [ "lambda" ];
    routes = [ "list" ];
    axes = [ "list" ];
    displayName = [ "string" ];
    credential = [ "string" ];
    scheme = [ "string" ];
    identity = [
      "string"
      "null"
    ];
    base = [
      "string"
      "null"
    ];
    gitRemote = [
      "string"
      "null"
    ];
    knobs = [ "set" ];
    fragments = [
      "set"
      "null"
    ];
    driver = [
      "set"
      "lambda"
      "null"
    ];
  };
  backendOnly = [
    "routes"
    "axes"
    "displayName"
    "credential"
    "scheme"
    "identity"
    "base"
    "gitRemote"
  ];
  backendRequired = [ "routes" ];

  display = {
    set = "attribute set";
    lambda = "function";
  };
  show = t: display.${t} or t;
  quote = k: "'${k}'";
  list = ks: builtins.concatStringsSep ", " (map quote ks);
  has = data: k: builtins.hasAttr k data;

  # The first thing wrong with `data`, worded for the author, or null.
  problem =
    data:
    if !builtins.isAttrs data then
      "Connector data must be an attribute set, got ${builtins.typeOf data}"
    else if !(data ? name) || !builtins.isString data.name || data.name == "" then
      "Connector data needs a 'name': a non-empty string, the row's name in spindrift.connectors"
      + (if data ? name then " ('name' is ${builtins.typeOf data.name})" else "")
    else
      let
        label = "Connector '${data.name}'";
        keys = builtins.attrNames data;
        unknown = builtins.filter (k: !(fields ? ${k})) keys;
        badType = builtins.filter (k: !builtins.elem (builtins.typeOf data.${k}) fields.${k}) (
          builtins.filter (k: fields ? ${k}) keys
        );
        stray = builtins.filter (has data) backendOnly;
        missing = builtins.filter (k: !has data k) backendRequired;
      in
      if unknown != [ ] then
        "${label}: unknown key${
          if builtins.length unknown > 1 then "s" else ""
        } ${list unknown} in its connector data (known: ${list (builtins.attrNames fields)}); connector data declares only its own row, never another spindrift option"
      else if badType != [ ] then
        let
          k = builtins.head badType;
        in
        "${label}: ${quote k} must be ${
          builtins.concatStringsSep " or " (map show fields.${k})
        }, got ${builtins.typeOf data.${k}}"
      else if !(data ? module) && stray != [ ] then
        "${label}: ${list stray} belong to a backend row, which 'module' declares; add 'module' or drop ${list stray}"
      else if data ? module && missing != [ ] then
        "${label}: a backend row ('module' set) also needs ${list missing}"
      else
        null;

  validate =
    data:
    let
      p = problem data;
    in
    if p == null then data else throw p;

  resolveDriver = lib: d: if builtins.isFunction d then d lib else d;

  # The attrs of `spindrift.connectors.<name>` the data declares, absent
  # fields omitted so the row keeps its option defaults.
  row =
    lib: data:
    let
      optional = k: if data ? ${k} && data.${k} != null then { ${k} = data.${k}; } else { };
      pick = builtins.foldl' (acc: k: acc // optional k) { };
    in
    pick [
      "knobs"
      "fragments"
    ]
    // (if data.driver or null == null then { } else { driver = resolveDriver lib data.driver; })
    // (
      if data ? module then
        {
          # The row's own `routes` repeats the backend's table, as a
          # hand-written Connector module sets both.
          inherit (data) routes;
          backend = {
            inherit (data) module routes;
          }
          // pick [
            "axes"
            "displayName"
            "credential"
            "scheme"
            "identity"
            "base"
            "gitRemote"
          ];
        }
      else
        { }
    );

  # The readers below take a `lib.connector` entry: its `data` is the whole
  # registration. None applies a wasm `module`.
  dataOf =
    entry:
    validate (
      entry.data
        or (throw "a lib.connector entry with no `data` (attributes: ${toString (builtins.attrNames entry)}): a Connector registers as data (lib/connector-data.nix), not as a flake module")
    );

  # `{ <name> = <row>; }`, the shape of the flake module's `spindrift.connectors`.
  # `lib` is only used to apply a function `driver`.
  rowsOf =
    lib: entry:
    let
      d = dataOf entry;
    in
    {
      ${d.name} = row lib d;
    };

  fragmentsOf = entry: (dataOf entry).fragments or null;

  driverRowOf = lib: entry: resolveDriver lib ((dataOf entry).driver or null);
in
{
  inherit
    problem
    validate
    row
    rowsOf
    fragmentsOf
    driverRowOf
    ;
}
