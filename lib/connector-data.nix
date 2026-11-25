# A Connector registered as data: a flat attribute set core alone turns into
# its `spindrift.connectors.<name>` row, so the Connector ships no flake-parts
# module of its own.
#
#   { name; module; routes; credential; scheme; identity; base; gitRemote;
#     knobs; fragments; driver; }
#
# `name` is required. `module` (a function of pkgs returning the wasm path)
# declares a backend row and needs `routes` and `credential`; `scheme`,
# `identity`, `base` and `gitRemote` belong to that row. `knobs`, `fragments`
# and `driver` are the row's own fields. Deep checks (Route rules, scheme enum,
# credential ownership) stay with core's row types and validators; this file
# only refuses what those would let through silently, because the row type is
# freeform: an unknown key or a wrongly typed field.
#
# Builtins only, and it never forces `module` or `driver.module`, so a reader
# that hands the Connector a stub core `{ }` can still read a row.
let
  # Field -> the `builtins.typeOf` values it may hold.
  fields = {
    name = [ "string" ];
    module = [ "lambda" ];
    routes = [ "list" ];
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
      "null"
    ];
  };
  backendOnly = [
    "routes"
    "credential"
    "scheme"
    "identity"
    "base"
    "gitRemote"
  ];
  backendRequired = [
    "routes"
    "credential"
  ];

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
      "Connector data needs a `name`: a non-empty string, the row's name in spindrift.connectors"
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
        } ${list unknown} in its connector data (known: ${list (builtins.attrNames fields)})"
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

  # The attrs of `spindrift.connectors.<name>` the data declares, absent
  # fields omitted so the row keeps its option defaults.
  row =
    data:
    let
      optional = k: if data ? ${k} && data.${k} != null then { ${k} = data.${k}; } else { };
      pick = builtins.foldl' (acc: k: acc // optional k) { };
    in
    pick [
      "knobs"
      "driver"
      "fragments"
    ]
    // (
      if data ? module then
        {
          # The row's own `routes` repeats the backend's table, as a
          # hand-written Connector module sets both.
          inherit (data) routes;
          backend = {
            inherit (data) module routes credential;
          }
          // pick [
            "scheme"
            "identity"
            "base"
            "gitRemote"
          ];
        }
      else
        { }
    );

  # `coreModule` is core's flake-parts module (a path or module value). The key
  # matches what a hand-written Connector module carries, so a Consumer that
  # imports the Connector twice dedups instead of failing "registered twice".
  module =
    coreModule: data:
    let
      d = validate data;
    in
    {
      key = "spindrift-connector-${d.name}";
      imports = [ coreModule ];
      spindrift.connectors.${d.name} = row d;
    };

  # For readers that take no module: `{ <name> = <row>; }`, the shape of a
  # flake module's `spindrift.connectors`, from a `lib.connector` entry's
  # `data`, or null for an entry without one so the reader falls back to its
  # module.
  rowOf = entry: if entry ? data then { ${entry.data.name} = row (validate entry.data); } else null;
in
{
  inherit
    fields
    problem
    validate
    row
    module
    rowOf
    ;
}
