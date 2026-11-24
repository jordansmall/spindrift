# Renders the Connector protocol description (default.nix) into its two
# artifacts: `renderGo` yields protocol_gen.go, `renderSchema` yields
# protocol.schema.json. Both validate first, so a malformed row fails the build
# whichever artifact asked. (The `connector-protocol-rows` check in
# nix/checks/schema-drift.nix also reads the description directly.) Pure
# builtins, like lib/renderers.nix, so a bare `nix eval` can exercise it.
let
  inherit (builtins)
    attrNames
    concatStringsSep
    deepSeq
    elem
    filter
    foldl'
    genList
    hasAttr
    head
    isAttrs
    isBool
    isInt
    isList
    isString
    length
    listToAttrs
    map
    mapAttrs
    match
    seq
    split
    stringLength
    substring
    toJSON
    toString
    ;

  fail = msg: throw "lib/connector-protocol: ${msg}";
  axes = [
    "tracker"
    "forge"
    "driver"
  ];
  concat = concatStringsSep "";

  upperOf =
    let
      lower = "abcdefghijklmnopqrstuvwxyz";
      upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";
    in
    listToAttrs (
      genList (i: {
        name = substring i 1 lower;
        value = substring i 1 upper;
      }) 26
    );
  upperFirst =
    s:
    let
      c = substring 0 1 s;
    in
    (upperOf.${c} or c) + substring 1 (stringLength s) s;
  # "tool_call" -> "ToolCall", "driver.decodeRaw" -> "DriverDecodeRaw".
  goIdent = s: concat (map upperFirst (filter isString (split "[._-]" s)));

  firstDup =
    xs:
    foldl' (found: x: if found != null || length (filter (y: y == x) xs) < 2 then found else x) null xs;
  noDups =
    what: xs:
    let
      d = firstDup xs;
    in
    if d == null then true else fail "${what}: ${toJSON d} is declared twice";

  checkFields =
    what: row: wanted:
    if !isAttrs row then
      fail "${what}: must be an attribute set"
    else
      let
        missing = filter (f: !(row ? ${f})) wanted;
        unknown = filter (f: !(elem f wanted)) (attrNames row);
      in
      if missing != [ ] then
        fail "${what}: missing field(s): ${concatStringsSep ", " missing}"
      else if unknown != [ ] then
        fail "${what}: unknown field(s): ${concatStringsSep ", " unknown}"
      else
        true;

  # A doc becomes a Go line comment and a schema description: one line, non-empty.
  checkDoc =
    what: doc:
    if !isString doc || doc == "" || length (split "\n" doc) > 1 then
      fail "${what}: doc must be a non-empty single line"
    else
      true;
  checkName =
    what: re: name:
    if isString name && match re name != null then
      true
    else
      fail "${what}: name ${toJSON name} does not match ${re}";

  # --- types -> JSON Schema -------------------------------------------------
  typeSchema =
    types: where: ty:
    let
      go = typeSchema types;
      kind = ty.kind or (fail "${where}: not a type (an attribute set with a `kind`)");
      scalar = {
        type = kind;
      };
      objectSchema =
        closed:
        let
          fields = ty.fields;
          names = attrNames fields;
          inner = f: if fields.${f}.kind == "optional" then fields.${f}.inner else fields.${f};
          required = filter (f: fields.${f}.kind != "optional") names;
        in
        {
          type = "object";
        }
        // (
          if names == [ ] then
            { }
          else
            {
              properties = listToAttrs (
                map (f: {
                  name = f;
                  value = go "${where}.${f}" (inner f);
                }) names
              );
            }
        )
        // (if required == [ ] then { } else { inherit required; })
        // (if closed then { additionalProperties = false; } else { });
    in
    if
      elem kind [
        "string"
        "integer"
        "number"
        "boolean"
        "null"
      ]
    then
      scalar
    else if kind == "any" then
      { }
    else if kind == "list" then
      {
        type = "array";
        items = go "${where}[]" ty.items;
      }
    else if kind == "map" then
      {
        type = "object";
        additionalProperties = go "${where}{}" ty.values;
      }
    else if kind == "enum" then
      if isList ty.values && ty.values != [ ] && filter (v: !isString v) ty.values == [ ] then
        {
          type = "string";
          enum = ty.values;
        }
      else
        fail "${where}: enum needs a non-empty list of strings"
    else if kind == "ref" then
      if hasAttr ty.name types then
        { "$ref" = "#/$defs/${ty.name}"; }
      else
        fail "${where}: $ref to undeclared type ${toJSON ty.name}"
    else if kind == "nullable" then
      {
        oneOf = [
          (go where ty.inner)
          { type = "null"; }
        ];
      }
    else if kind == "object" then
      objectSchema true
    else if kind == "open" then
      objectSchema false
    else if kind == "optional" then
      fail "${where}: optional is only valid as an object field"
    else if kind == "raw" then
      fail "${where}: raw is only valid as a raw method's params"
    else
      fail "${where}: unknown type kind ${toJSON kind}";

  paramsSchema =
    types: where: ty:
    if (ty.kind or null) == "raw" then
      {
        type = "string";
        description = ty.doc;
      }
    else
      typeSchema types where ty;

  # --- validation -----------------------------------------------------------
  axisOf = name: head (split "\\." name);
  capNames = desc: axis: map (c: c.name) desc.capabilities.${axis};
  allCapNames = desc: concatMap' (axis: capNames desc axis) axes;
  concatMap' = f: xs: foldl' (acc: x: acc ++ f x) [ ] xs;

  methodChecks =
    desc: m:
    let
      what = "method ${toJSON (m.name or "?")}";
      axis = axisOf m.name;
      retry = m.retry;
    in
    [
      (checkFields what m [
        "name"
        "capability"
        "params"
        "result"
        "retry"
        "resultCheck"
        "raw"
        "doc"
      ])
      (
        if match "(tracker|forge|driver)\\.[A-Za-z][A-Za-z0-9]*" m.name != null then
          true
        else
          fail "${what}: name must be <axis>.<method> with axis one of ${concatStringsSep ", " axes}"
      )
      (
        if m.capability == null || elem m.capability (capNames desc axis) then
          true
        else
          fail "${what}: capability ${toJSON m.capability} is not declared on the ${axis} axis"
      )
      (checkFields "${what} retry" retry [
        "safe"
        "doneOn"
      ])
      (if isBool retry.safe then true else fail "${what}: retry.safe must be a bool")
      (
        let
          unknown = filter (c: !elem c (map (c: c.name) desc.errorClasses)) retry.doneOn;
        in
        if !isList retry.doneOn then
          fail "${what}: retry.doneOn must be a list"
        else if unknown != [ ] then
          fail "${what}: retry.doneOn names unknown class(es): ${concatStringsSep ", " unknown}"
        else if retry.doneOn != [ ] && !retry.safe then
          fail "${what}: retry.doneOn on a method that is not retry-safe"
        else
          true
      )
      (
        if hasAttr m.resultCheck desc.resultChecks then
          true
        else
          fail "${what}: unknown resultCheck ${toJSON m.resultCheck}"
      )
      (
        if !isBool m.raw then
          fail "${what}: raw must be a bool"
        else if m.raw != ((m.params.kind or null) == "raw") then
          fail "${what}: raw = ${toJSON m.raw} disagrees with params being a raw type"
        else
          true
      )
      (checkDoc what m.doc)
      (seq (paramsSchema desc.types "${what} params" m.params) true)
      (seq (typeSchema desc.types "${what} result" m.result) true)
    ];

  entryChecks = what: re: e: [
    (checkFields what e [
      "name"
      "doc"
    ])
    (checkName what re e.name)
    (checkDoc what e.doc)
  ];

  validate =
    desc:
    let
      checks = [
        (checkFields "description" desc [
          "version"
          "capabilities"
          "errorClasses"
          "limits"
          "events"
          "resultChecks"
          "types"
          "methods"
        ])
        (
          if isString desc.version && match "[0-9]+\\.[0-9]+\\.[0-9]+" desc.version != null then
            true
          else
            fail "version must be MAJOR.MINOR.PATCH"
        )
        (
          if attrNames desc.capabilities == (builtins.sort builtins.lessThan axes) then
            true
          else
            fail "capabilities must have exactly the axes ${concatStringsSep ", " axes}"
        )
        (noDups "capabilities" (allCapNames desc))
        (noDups "errorClasses" (map (c: c.name) desc.errorClasses))
        (noDups "events" (map (e: e.name) desc.events))
        (noDups "methods" (map (m: m.name) desc.methods))
      ]
      ++ concatMap' (
        axis:
        concatMap' (
          c: entryChecks "capability ${toJSON (c.name or "?")}" "${axis}(\\.[A-Za-z][A-Za-z0-9]*)*" c
        ) desc.capabilities.${axis}
      ) axes
      ++ concatMap' (
        c: entryChecks "errorClass ${toJSON (c.name or "?")}" "[a-z]+(-[a-z]+)*" c
      ) desc.errorClasses
      ++ concatMap' (e: [
        (checkFields "event ${toJSON (e.name or "?")}" e [
          "name"
          "guest"
          "doc"
        ])
        (checkName "event" "[a-z]+(_[a-z]+)*" e.name)
        (if isBool e.guest then true else fail "event ${e.name}: guest must be a bool")
        (checkDoc "event ${e.name}" e.doc)
      ]) desc.events
      ++ concatMap' (
        n:
        let
          l = desc.limits.${n};
        in
        [
          (checkFields "limit ${n}" l [
            "value"
            "doc"
          ])
          (checkName "limit" "[a-z][A-Za-z0-9]*" n)
          (if isInt l.value && l.value > 0 then true else fail "limit ${n}: value must be a positive integer")
          (checkDoc "limit ${n}" l.doc)
        ]
      ) (attrNames desc.limits)
      ++ concatMap' (n: [
        (checkName "resultCheck" "[a-z][A-Za-z0-9]*" n)
        (checkDoc "resultCheck ${n}" desc.resultChecks.${n})
      ]) (attrNames desc.resultChecks)
      ++ concatMap' (n: [
        (checkName "type" "[a-z][A-Za-z0-9]*" n)
        (seq (typeSchema desc.types "type ${n}" desc.types.${n}) true)
      ]) (attrNames desc.types)
      ++ concatMap' (methodChecks desc) desc.methods;
    in
    seq (deepSeq checks null) (
      # Identifier collisions are checked once the rows are known good.
      seq (noDups "Go identifiers" (map (i: i.ident) (identifiers desc))) desc
    );

  # Every top-level Go name the generated file declares, so a collision between
  # (say) two capabilities that differ only in punctuation fails here, not in
  # the Go compiler.
  identifiers =
    desc:
    let
      ids = prefix: names: map (n: { ident = prefix + goIdent n; }) names;
    in
    map (n: { ident = n; }) [
      "Version"
      "Axis"
      "AxisTracker"
      "AxisForge"
      "AxisDriver"
      "Capabilities"
      "Classes"
      "EventKinds"
      "GuestEventKinds"
      "Retry"
      "Method"
      "Methods"
    ]
    ++ ids "Cap" (allCapNames desc)
    ++ ids "Class" (map (c: c.name) desc.errorClasses)
    ++ ids "" (attrNames desc.limits)
    ++ ids "Event" (map (e: e.name) desc.events)
    ++ ids "Check" (attrNames desc.resultChecks)
    ++ ids "Method" (map (m: m.name) desc.methods);

  # --- Go -------------------------------------------------------------------
  goString = toJSON;
  constBlock = rows: "const (\n" + concat rows + ")\n\n";
  commented = ident: doc: "// ${ident}: ${doc}\n";

  renderGo =
    description:
    let
      desc = validate description;
      classConst = name: "Class${goIdent name}";
      capConst = name: "Cap${goIdent name}";
      allCaps = concatMap' (axis: map (c: c // { inherit axis; }) desc.capabilities.${axis}) axes;
      limitNames = attrNames desc.limits;
      checkNames = attrNames desc.resultChecks;
      goList = items: "[]string{${concatStringsSep ", " items}}";
      axisConst = axis: "Axis${goIdent axis}";
      methodEntry =
        m:
        let
          id = "Method${goIdent m.name}";
          fields = concatStringsSep ", " (
            [
              "Name: ${id}"
              "Axis: ${axisConst (axisOf m.name)}"
            ]
            ++ (if m.capability == null then [ ] else [ "Capability: ${capConst m.capability}" ])
            ++ (
              if m.retry.safe || m.retry.doneOn != [ ] then
                [
                  "Retry: Retry{${
                    concatStringsSep ", " (
                      (if m.retry.safe then [ "Safe: true" ] else [ ])
                      ++ (if m.retry.doneOn == [ ] then [ ] else [ "DoneOn: ${goList (map classConst m.retry.doneOn)}" ])
                    )
                  }}"
                ]
              else
                [ ]
            )
            ++ [ "ResultCheck: Check${goIdent m.resultCheck}" ]
            ++ (if m.raw then [ "Raw: true" ] else [ ])
          );
        in
        "${id}: {${fields}},\n";
    in
    "// Code generated by nix/regen.nix from lib/connector-protocol/*.nix. DO NOT EDIT.\n"
    + "\n"
    + "// Package connectorprotocol is the Connector protocol as data: the version,\n"
    + "// capability and method names, error classes, limits and event kinds core\n"
    + "// speaks, rendered from lib/connector-protocol. Core's own constants are held\n"
    + "// to these by agreement tests, not derived from them.\n"
    + "package connectorprotocol\n\n"
    + "// Version is the protocol version.\n"
    + "const Version = ${goString desc.version}\n\n"
    + "// Axis is a family of Connector: its name is the prefix of its methods.\n"
    + "type Axis string\n\n"
    + constBlock (map (a: "${axisConst a} Axis = ${goString a}\n") axes)
    + "// The capability names a Connector advertises.\n"
    + constBlock (
      map (c: commented (capConst c.name) c.doc + "${capConst c.name} = ${goString c.name}\n") allCaps
    )
    + "// Capabilities maps every capability name to its axis.\n"
    + "var Capabilities = map[string]Axis{\n"
    + concat (map (c: "${capConst c.name}: ${axisConst c.axis},\n") allCaps)
    + "}\n\n"
    + "// The canonical error classes.\n"
    + constBlock (
      map (
        c: commented (classConst c.name) c.doc + "${classConst c.name} = ${goString c.name}\n"
      ) desc.errorClasses
    )
    + "// Classes lists every error class, in the order the protocol declares them.\n"
    + "var Classes = ${goList (map (c: classConst c.name) desc.errorClasses)}\n\n"
    + "// The limits on a call into a Connector; each name carries its unit, bytes when\n"
    + "// it has none.\n"
    + constBlock (
      map (
        n:
        commented (goIdent n) desc.limits.${n}.doc + "${goIdent n} = ${toString desc.limits.${n}.value}\n"
      ) limitNames
    )
    + "// The canonical event kinds.\n"
    + constBlock (
      map (
        e: commented "Event${goIdent e.name}" e.doc + "Event${goIdent e.name} = ${goString e.name}\n"
      ) desc.events
    )
    + "// EventKinds lists every event kind.\n"
    + "var EventKinds = ${goList (map (e: "Event${goIdent e.name}") desc.events)}\n\n"
    + "// GuestEventKinds lists the kinds a Driver's decoder may emit.\n"
    + "var GuestEventKinds = ${
      goList (map (e: "Event${goIdent e.name}") (filter (e: e.guest) desc.events))
    }\n\n"
    + "// The result checks core applies to a method's answer.\n"
    + constBlock (
      map (
        n: commented "Check${goIdent n}" desc.resultChecks.${n} + "Check${goIdent n} = ${goString n}\n"
      ) checkNames
    )
    + "// The method names.\n"
    + constBlock (
      map (
        m: commented "Method${goIdent m.name}" m.doc + "Method${goIdent m.name} = ${goString m.name}\n"
      ) desc.methods
    )
    + "// Retry says how core may repeat a failed call. Safe is true when repeating a\n"
    + "// transient failure is harmless; DoneOn lists the classes that, answered on a\n"
    + "// retry, mean the earlier attempt succeeded.\n"
    + "type Retry struct {\n\tSafe   bool\n\tDoneOn []string\n}\n\n"
    + "// Method is one protocol method. Capability is \"\" for a method with none, and\n"
    + "// Raw marks a method whose request is raw bytes rather than JSON params.\n"
    + "type Method struct {\n\tName        string\n\tAxis        Axis\n\tCapability  string\n\tRetry       Retry\n\tResultCheck string\n\tRaw         bool\n}\n\n"
    + "// Methods maps every method name to its row.\n"
    + "var Methods = map[string]Method{\n"
    + concat (map methodEntry desc.methods)
    + "}\n";

  # --- JSON Schema ----------------------------------------------------------
  # builtins.toJSON is compact and has no pretty form, so print here: two-space
  # indent, keys sorted (attrNames), so the file is byte-stable and diffable.
  pretty =
    depth: v:
    let
      pad = n: concat (genList (_: "  ") n);
      inner = pad (depth + 1);
    in
    if isAttrs v then
      if v == { } then
        "{}"
      else
        "{\n"
        + concatStringsSep ",\n" (
          map (k: "${inner}${toJSON k}: ${pretty (depth + 1) v.${k}}") (attrNames v)
        )
        + "\n${pad depth}}"
    else if isList v then
      if v == [ ] then
        "[]"
      else
        "[\n" + concatStringsSep ",\n" (map (x: inner + pretty (depth + 1) x) v) + "\n${pad depth}]"
    else
      toJSON v;

  renderSchema =
    description:
    let
      desc = validate description;
      methodSchema = m: {
        capability = m.capability;
        params = paramsSchema desc.types "${m.name} params" m.params;
        result = typeSchema desc.types "${m.name} result" m.result;
      };
    in
    pretty 0 {
      "$schema" = "https://json-schema.org/draft/2020-12/schema";
      "$id" = "https://spindrift.dev/connector-protocol/${desc.version}/schema.json";
      title = "spindrift Connector protocol ${desc.version}";
      "$defs" = mapAttrs (n: ty: typeSchema desc.types "type ${n}" ty) desc.types;
      methods = listToAttrs (
        map (m: {
          name = m.name;
          value = methodSchema m;
        }) desc.methods
      );
    }
    + "\n";
in
{
  inherit renderGo renderSchema;
}
