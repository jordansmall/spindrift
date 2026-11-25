# A Connector registered as data: a flat attribute set core alone turns into
# its `spindrift.connectors.<name>` row, so the Connector ships no flake-parts
# module of its own.
#
#   { name; module; routes; credential; scheme; identity; base; gitRemote;
#     knobs; fragments; driver; }
#
# `name` is required. `module` (a function of pkgs returning the wasm path)
# declares a backend row and needs `routes`; `scheme`, `identity`, `base` and
# `gitRemote` belong to that row. Only a first-party row names a `credential`;
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

  # `coreModule` is core's flake-parts module (a path or module value). The key
  # keeps a Consumer importing the Connector twice from registering it twice.
  # The module registers the data and nothing else: core builds the row.
  toFlakeModule =
    coreModule: data:
    let
      d = validate data;
    in
    {
      key = "spindrift-connector-${d.name}";
      imports = [ coreModule ];
      spindrift.connectorData = [ d ];
    };

  # The readers below take a `lib.connector` entry in either form: a data
  # Connector's `data`, or the entry-level declarations (`flakeModule`,
  # `fragments`, `driverRow`) of one not yet moved to data. No caller branches
  # on the form. None applies a wasm `module`.

  # `{ <name> = <row>; }`, the shape of a flake module's `spindrift.connectors`.
  # `moduleArgs` is what an entry-form flake module is applied to; a data entry
  # reads only its `lib`, and only to apply a function `driver`.
  rowsOf =
    moduleArgs: entry:
    if entry ? data then
      let
        d = validate entry.data;
      in
      {
        ${d.name} = row (moduleArgs.lib
          or (throw "connector ${d.name}: a function `driver` needs rowsOf's module args to carry `lib`")
        ) d;
      }
    else
      (entry.flakeModule moduleArgs).spindrift.connectors or { };

  fragmentsOf =
    entry: if entry ? data then (validate entry.data).fragments or null else entry.fragments or null;

  driverRowOf =
    lib: entry:
    if entry ? data then
      resolveDriver lib ((validate entry.data).driver or null)
    else if entry ? driverRow then
      entry.driverRow lib
    else
      null;
in
{
  inherit
    problem
    validate
    row
    toFlakeModule
    rowsOf
    fragmentsOf
    driverRowOf
    ;
}
