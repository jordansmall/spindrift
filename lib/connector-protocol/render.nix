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
    elemAt
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

  # checkFields for a row that may leave some of its optional keys out.
  checkKeys =
    what: row: allowed: required:
    if !isAttrs row then
      fail "${what}: must be an attribute set"
    else
      let
        missing = filter (f: !(row ? ${f})) required;
        unknown = filter (f: !(elem f allowed)) (attrNames row);
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

  # The shared admit-case table: a rule may have no cases yet (it then stays
  # visible as a rule with no rows), but each case states exactly one verdict.
  admitCaseChecks =
    ac:
    let
      caseChecks =
        rule: c:
        let
          what = "admitCases rule ${rule} case ${toJSON (c.name or "?")}";
          verdicts = filter (f: c ? ${f}) [
            "admit"
            "refuse"
          ];
        in
        [
          (checkKeys what c
            [
              "name"
              "method"
              "url"
              "admit"
              "refuse"
            ]
            [ "name" "method" "url" ]
          )
          (
            if isString c.name && c.name != "" && isString c.method && isString c.url then
              true
            else
              fail "${what}: name, method and url must be strings, name non-empty"
          )
          (
            if length verdicts != 1 then
              fail "${what}: must have exactly one of admit or refuse"
            else if c ? admit && c.admit != true then
              fail "${what}: admit must be true"
            else if c ? refuse && (!isString c.refuse || c.refuse == "") then
              fail "${what}: refuse must be a non-empty string"
            else
              true
          )
        ];
      ruleChecks =
        r:
        let
          what = "admitCases rule ${toJSON (r.name or "?")}";
        in
        [
          (checkFields what r [
            "name"
            "doc"
            "cases"
          ])
          (checkName what "[a-z][A-Za-z]*" r.name)
          (checkDoc what r.doc)
          (if isList r.cases then true else fail "${what}: cases must be a list")
          (noDups "${what} cases" (map (c: c.name or "?") r.cases))
        ]
        ++ concatMap' (caseChecks (toJSON r.name)) r.cases;
    in
    [
      (checkFields "admitCases" ac [
        "routes"
        "rules"
      ])
      (noDups "admitCases rules" (map (r: r.name or "?") ac.rules))
    ]
    ++ concatMap' (
      r:
      let
        what = "admitCases route ${toJSON (r.path or "?")}";
      in
      [
        (checkFields what r [
          "method"
          "host"
          "path"
        ])
        (
          if isString r.method && match "[A-Z]+" r.method != null then
            true
          else
            fail "${what}: method must match [A-Z]+"
        )
        (if isString r.host && r.host != "" then true else fail "${what}: host must be a non-empty string")
        (
          if isString r.path && match "/.*" r.path != null then
            true
          else
            fail "${what}: path must start with /"
        )
      ]
    ) ac.routes
    ++ concatMap' ruleChecks ac.rules;

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
          "reasons"
          "hostLine"
          "statusCategories"
          "transitionOutcomes"
          "resultChecks"
          "admitCases"
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
        (noDups "reasons" (map (e: e.name) desc.reasons))
        (noDups "statusCategories" (map (e: e.name) desc.statusCategories))
        (noDups "transitionOutcomes" (map (e: e.name) desc.transitionOutcomes))
        (checkFields "hostLine" desc.hostLine [
          "type"
          "opPassStart"
          "roles"
          "passRoles"
        ])
        (checkName "hostLine type" "[a-z]+(_[a-z]+)*" desc.hostLine.type)
        (checkName "hostLine opPassStart" "[a-z]+(_[a-z]+)*" desc.hostLine.opPassStart)
        (noDups "hostLine roles" (map (r: r.name) desc.hostLine.roles))
      ]
      ++ concatMap' (e: entryChecks "reason ${toJSON (e.name or "?")}" "[a-z][A-Za-z]*" e) desc.reasons
      ++ concatMap' (
        e: entryChecks "statusCategory ${toJSON (e.name or "?")}" "[a-z]+" e
      ) desc.statusCategories
      ++ concatMap' (
        e: entryChecks "transitionOutcome ${toJSON (e.name or "?")}" "[a-z]+" e
      ) desc.transitionOutcomes
      ++ concatMap' (
        r: entryChecks "hostLine role ${toJSON (r.name or "?")}" "[a-z]+" r
      ) desc.hostLine.roles
      ++ concatMap' (pass: [
        (checkName "hostLine pass" "[a-z]+(-[a-z]+)*" pass)
        (
          if elem desc.hostLine.passRoles.${pass} (map (r: r.name) desc.hostLine.roles) then
            true
          else
            fail "hostLine pass ${toJSON pass}: role ${toJSON desc.hostLine.passRoles.${pass}} is not declared"
        )
      ]) (attrNames desc.hostLine.passRoles)
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
          "progress"
          "doc"
        ])
        (checkName "event" "[a-z]+(_[a-z]+)*" e.name)
        (if isBool e.guest then true else fail "event ${e.name}: guest must be a bool")
        (if isBool e.progress then true else fail "event ${e.name}: progress must be a bool")
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
      ++ concatMap' (methodChecks desc) desc.methods
      ++ admitCaseChecks desc.admitCases;
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
      "ProgressEventKinds"
      "Retry"
      "Method"
      "Methods"
      "Reasons"
      "HostLineType"
      "HostLineOpPassStart"
      "PassRoles"
      "WireFields"
      "WireEnums"
      "TransitionOutcomes"
    ]
    ++ ids "Cap" (allCapNames desc)
    ++ ids "Class" (map (c: c.name) desc.errorClasses)
    ++ ids "" (attrNames desc.limits)
    ++ ids "Event" (map (e: e.name) desc.events)
    ++ ids "Check" (attrNames desc.resultChecks)
    ++ ids "Method" (map (m: m.name) desc.methods)
    ++ ids "Reason" (map (e: e.name) desc.reasons)
    ++ ids "Role" (map (r: r.name) desc.hostLine.roles)
    ++ ids "StatusCategory" (map (e: e.name) desc.statusCategories)
    ++ ids "Transition" (map (e: e.name) desc.transitionOutcomes);

  # --- Go -------------------------------------------------------------------
  goString = toJSON;
  constBlock = rows: "const (\n" + concat rows + ")\n\n";
  commented = ident: doc: "// ${ident}: ${doc}\n";

  # --- SDK wire structs -------------------------------------------------------
  # What a struct's field set is held to, and how it is spelled, is in sdk.nix.
  structKeys = [
    "name"
    "type"
    "params"
    "doc"
    "tagless"
    "fields"
  ];
  fieldKeys = [
    "json"
    "go"
    "type"
    "omitempty"
    "doc"
    "embed"
  ];
  initialisms = {
    Api = "API";
    Url = "URL";
    Id = "ID";
    Sha = "SHA";
  };
  # "headSha" -> "HeadSHA": camelCase words, each in Go case, initialisms upper.
  goFieldName =
    j:
    concat (
      map (
        p:
        let
          w = if isList p then head p else upperFirst p;
        in
        initialisms.${w} or w
      ) (filter (p: p != "") (split "([A-Z][a-z0-9]*)" j))
    );
  fieldSpec = f: if isString f then { json = f; } else f;
  # A doc is lines; the blank ones stay a bare `//`.
  docLines =
    indent: doc:
    let
      ls = filter isString (split "\n" doc);
      n = length ls;
      kept = if n > 0 && elemAt ls (n - 1) == "" then genList (elemAt ls) (n - 1) else ls;
    in
    concat (map (l: if l == "" then "${indent}//\n" else "${indent}// ${l}\n") kept);
  isObjectType =
    ty:
    elem (ty.kind or null) [
      "object"
      "open"
    ];

  # Every object shape of the description by the key WireFields uses: a types row
  # by its name, a method's params by "<method>.params".
  wireShapes =
    desc:
    listToAttrs (
      map (n: {
        name = n;
        value = desc.types.${n}.fields;
      }) (filter (n: isObjectType desc.types.${n}) (attrNames desc.types))
      ++ concatMap' (
        m:
        if isObjectType m.params then
          [
            {
              name = "${m.name}.params";
              value = m.params.fields;
            }
          ]
        else
          [ ]
      ) desc.methods
    );

  # Every enum the description declares at an object field, by the dotted path to
  # the field, values sorted: a shape key as wireShapes spells it (a method's
  # result is "<method>.result"), then ".<field>" down through nested objects.
  # A ref is not followed (the type row has its own key). A list or map item that
  # holds an enum is refused, not skipped: WireEnums and the agreement tests would
  # silently lack it.
  wireEnums =
    desc:
    let
      at =
        path: ty0:
        let
          ty = if ty0.kind == "optional" || ty0.kind == "nullable" then ty0.inner else ty0;
        in
        if ty.kind == "enum" then
          [
            {
              name = path;
              value = builtins.sort builtins.lessThan ty.values;
            }
          ]
        else if isObjectType ty then
          concatMap' (f: at "${path}.${f}" ty.fields.${f}) (attrNames ty.fields)
        else if ty.kind == "list" || ty.kind == "map" then
          let
            found = if ty.kind == "list" then at "${path}[]" ty.items else at "${path}{}" ty.values;
          in
          if found == [ ] then
            [ ]
          else
            fail "enum at ${(head found).name} sits in a list or map item, which WireEnums does not cover"
        else
          [ ];
    in
    listToAttrs (
      concatMap' (n: at n desc.types.${n}) (attrNames desc.types)
      ++ concatMap' (m: at "${m.name}.params" m.params ++ at "${m.name}.result" m.result) desc.methods
    );

  # The Go source of one package's maps from the SDK's Go value of a description
  # enum to its wire name, after checking each binding against the enum.
  sdkEnums =
    desc: pkg: enums:
    let
      all = wireEnums desc;
      keys = [
        "var"
        "path"
        "type"
        "names"
      ];
      where = e: "sdk binding ${pkg}.enums ${toJSON (e.var or "?")}";
      one =
        e:
        let
          wire = all.${e.path} or (fail "${where e}: path ${toJSON e.path} names no enum of the description");
          bound = attrNames e.names;
          missing = filter (v: !elem v bound) wire;
          extra = filter (v: !elem v wire) bound;
        in
        seq
          (deepSeq [
            (checkKeys (where e) e keys keys)
            (checkName (where e) "[a-z][A-Za-z0-9]*" e.var)
            (checkName "${where e} type" "[A-Z][A-Za-z0-9]*" e.type)
            (map (v: checkName "${where e} name of ${toJSON v}" "[A-Z][A-Za-z0-9]*" e.names.${v}) bound)
            (
              if missing == [ ] then
                true
              else
                fail "${where e}: names no Go value for ${toJSON (head missing)}, which the description's enum has (${concatStringsSep ", " wire})"
            )
            (
              if extra == [ ] then
                true
              else
                fail "${where e}: ${toJSON (head extra)} is not in the description's enum (${concatStringsSep ", " wire})"
            )
          ] true)
          {
            text =
              "// ${e.var} maps each ${e.type} to its wire name.\n"
              + "var ${e.var} = map[${e.type}]string{${
                concatStringsSep ", " (map (v: "${e.names.${v}}: ${goString v}") wire)
              }}\n\n";
            idents = [ e.var ];
          };
      parts = map one enums;
    in
    {
      text = concat (map (p: p.text) parts);
      idents = concatMap' (p: p.idents) parts;
    };

  # The Go source of one package's bound structs and the imports its field types
  # need, after checking each against the description's shape.
  sdkStructs =
    desc: pkg: structs:
    let
      where = s: "sdk binding ${pkg}.structs ${toJSON (s.name or "?")}";
      byName =
        s: n:
        let
          m = filter (o: o.name == n) structs;
        in
        if m == [ ] then fail "${where s}: embeds ${toJSON n}, which ${pkg} does not bind" else head m;
      shapeOf =
        s:
        let
          shape =
            if (s ? type) == (s ? params) then
              fail "${where s}: needs exactly one of type and params"
            else if s ? type then
              desc.types.${s.type} or (fail "${where s}: type ${toJSON s.type} is not a description type")
            else
              let
                ms = filter (m: m.name == s.params) desc.methods;
              in
              if ms == [ ] then
                fail "${where s}: params ${toJSON s.params} is not a description method"
              else
                (head ms).params;
        in
        if isObjectType shape then shape.fields else fail "${where s}: the shape is not an object";
      # The JSON field names the struct covers, an embedded struct's included.
      namesOf =
        s:
        concatMap' (
          f0:
          let
            f = fieldSpec f0;
          in
          if f ? embed then namesOf (byName s f.embed) else [ f.json ]
        ) s.fields;
      goTypeOf =
        s: ty:
        let
          k = ty.kind;
          go = goTypeOf s;
        in
        if k == "string" || k == "enum" then
          "string"
        else if k == "boolean" then
          "bool"
        else if k == "integer" then
          "int"
        else if k == "number" then
          "float64"
        else if k == "any" then
          "any"
        else if k == "list" then
          "[]${go ty.items}"
        else if k == "map" then
          "map[string]${go ty.values}"
        else if k == "optional" || k == "nullable" then
          go ty.inner
        else if k == "ref" then
          let
            m = filter (o: (o.type or null) == ty.name) structs;
          in
          if m == [ ] then
            fail "${where s}: no ${pkg} struct is bound to type ${toJSON ty.name}; give the field a type"
          else
            (head m).name
        else
          fail "${where s}: no Go type for a ${k}; give the field a type";
      fieldType = s: f: f.type or (goTypeOf s (shapeOf s).${f.json});
      realFields = s: filter (f: !(f ? embed)) (map fieldSpec s.fields);
      checks =
        s:
        let
          shape = shapeOf s;
          bound = namesOf s;
          want = attrNames shape;
          missing = filter (n: !(shape ? ${n})) bound;
          omitted = filter (n: !elem n bound) want;
        in
        [
          (checkKeys (where s) s structKeys [
            "name"
            "fields"
          ])
          (checkName (where s) "[A-Za-z][A-Za-z0-9]*" s.name)
          (noDups "${where s} fields" bound)
          (
            if missing == [ ] then
              true
            else
              fail "${where s}: field ${toJSON (head missing)} is not in the description's shape (${concatStringsSep ", " want})"
          )
          (
            if omitted == [ ] then
              true
            else
              fail "${where s}: the description's field ${toJSON (head omitted)} is left out; the description governs the field set"
          )
        ]
        ++ map (
          f: checkKeys "${where s} field" f fieldKeys (if f ? embed then [ "embed" ] else [ "json" ])
        ) (map fieldSpec s.fields)
        ++ map (f: if isString (f.type or "") then true else fail "${where s}: type must be a string") (
          realFields s
        )
        ++ [ (seq (map (f: fieldType s f) (realFields s)) true) ];
      fieldLine =
        s: f:
        if f ? embed then
          "${f.embed}\n"
        else
          let
            name = f.go or (if s.tagless or false then f.json else goFieldName f.json);
            tag =
              if s.tagless or false then
                ""
              else
                " `json:\"${f.json}${if f.omitempty or false then ",omitempty" else ""}\"`";
          in
          (if f ? doc then docLines "\t" f.doc else "") + "${name} ${fieldType s f}${tag}\n";
      # A documented field is set off by a blank line, as the SDK's own are.
      fieldsText =
        s:
        concat (
          genList (
            i:
            let
              f = fieldSpec (elemAt s.fields i);
            in
            (if i > 0 && (f.doc or null) != null then "\n" else "") + fieldLine s f
          ) (length s.fields)
        );
      structText =
        s: (if s ? doc then docLines "" s.doc else "") + "type ${s.name} struct {\n${fieldsText s}}\n\n";
      types = concatMap' (s: map (f: fieldType s f) (realFields s)) structs;
      uses = sub: filter (t: length (split sub t) > 1) types != [ ];
    in
    seq (deepSeq (concatMap' checks structs) null) (
      seq (noDups "${pkg} structs" (map (s: s.name) structs)) {
        text = concat (map structText structs);
        idents = map (s: s.name) structs;
        imports =
          (if uses "json\\.RawMessage" then [ "encoding/json" ] else [ ])
          ++ (if uses "time\\.Time" then [ "time" ] else [ ]);
      }
    );

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
      shapes = wireShapes desc;
      enums = wireEnums desc;
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
    + "// ProgressEventKinds lists the kinds that show the agent working past an earlier error.\n"
    + "var ProgressEventKinds = ${
      goList (map (e: "Event${goIdent e.name}") (filter (e: e.progress) desc.events))
    }\n\n"
    + "// The Reason a driver Pattern carries.\n"
    + constBlock (
      map (
        e: commented "Reason${goIdent e.name}" e.doc + "Reason${goIdent e.name} = ${goString e.name}\n"
      ) desc.reasons
    )
    + "// Reasons lists every Reason, in the order the protocol declares them.\n"
    + "var Reasons = ${goList (map (e: "Reason${goIdent e.name}") desc.reasons)}\n\n"
    + "// The tokens of the host line a Driver's decoder reads.\n"
    + constBlock [
      "HostLineType = ${goString desc.hostLine.type}\n"
      "HostLineOpPassStart = ${goString desc.hostLine.opPassStart}\n"
    ]
    + "// The attribution roles core stamps on events.\n"
    + constBlock (
      map (
        r: commented "Role${goIdent r.name}" r.doc + "Role${goIdent r.name} = ${goString r.name}\n"
      ) desc.hostLine.roles
    )
    + "// PassRoles maps a pass name to the role its pass_start op starts.\n"
    + "var PassRoles = map[string]string{\n"
    + concat (
      map (p: "${goString p}: Role${goIdent desc.hostLine.passRoles.${p}},\n") (
        attrNames desc.hostLine.passRoles
      )
    )
    + "}\n\n"
    + "// The categories of a tracker.statuses entry that core reads.\n"
    + constBlock (
      map (
        e:
        commented "StatusCategory${goIdent e.name}" e.doc
        + "StatusCategory${goIdent e.name} = ${goString e.name}\n"
      ) desc.statusCategories
    )
    + "// The outcomes of tracker.transition that are not an error.\n"
    + constBlock (
      map (
        e:
        commented "Transition${goIdent e.name}" e.doc + "Transition${goIdent e.name} = ${goString e.name}\n"
      ) desc.transitionOutcomes
    )
    + "// TransitionOutcomes lists every outcome, in the order the protocol declares them.\n"
    + "var TransitionOutcomes = ${
      goList (map (e: "Transition${goIdent e.name}") desc.transitionOutcomes)
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
    + "}\n\n"
    + "// WireFields lists the JSON field names of each object shape the protocol\n"
    + "// describes, sorted: a shared type under its name, a method's params under\n"
    + "// \"<method>.params\". Core's own wire structs are held to it by agreement tests.\n"
    + "var WireFields = map[string][]string{\n"
    + concat (
      map (k: "${goString k}: ${goList (map goString (attrNames shapes.${k}))},\n") (attrNames shapes)
    )
    + "}\n\n"
    + "// WireEnums lists the values of each enum the protocol describes at an object\n"
    + "// field, sorted, under the field's dotted path: a shape key as WireFields spells\n"
    + "// it (a method's result is \"<method>.result\"), then \".<field>\". Core's own\n"
    + "// name maps are held to it by agreement tests.\n"
    + "var WireEnums = map[string][]string{\n"
    + concat (map (k: "${goString k}: ${goList (map goString enums.${k})},\n") (attrNames enums))
    + "}\n";

  # --- SDK ------------------------------------------------------------------
  # The SDK cannot import core, so each of its packages gets the protocol facts it
  # speaks as a protocol_gen.go of its own. `binding` (sdk.nix) says only how a
  # description value is spelled in Go where the derived spelling is not the SDK's
  # published one; it never holds a protocol value.
  sdkPackages = [
    "connector"
    "event"
    "forge"
    "tracker"
    "driver"
  ];
  # Per package, the binding fields it takes and the description values each
  # one's keys must name.
  sdkBindingKeys = desc: {
    connector.limits = attrNames desc.limits;
    event = { };
    forge = {
      caps = capNames desc "forge";
      methods = methodNames desc "forge";
    };
    tracker = {
      caps = capNames desc "tracker";
      methods = methodNames desc "tracker";
    };
    driver = {
      caps = capNames desc "driver";
      methods = methodNames desc "driver";
      roles = map (r: r.name) desc.hostLine.roles;
    };
  };
  methodNames = desc: axis: map (m: m.name) (filter (m: axisOf m.name == axis) desc.methods);

  checkSdkBinding =
    desc: binding:
    let
      keys = sdkBindingKeys desc;
      exported = what: name: checkName what "[A-Z][A-Za-z0-9]*" name;
      # A field is a list of description values (limits) or an attribute set
      # mapping a description value to its exported Go name.
      fieldChecks =
        pkg: field:
        let
          what = "sdk binding ${pkg}.${field}";
          v = binding.${pkg}.${field};
          known = keys.${pkg}.${field};
          unknown = filter (k: !elem k known) (if isList v then v else attrNames v);
        in
        [
          (
            if unknown == [ ] then
              true
            else
              fail "${what}: ${toJSON (head unknown)} is not a description value (one of ${concatStringsSep ", " known})"
          )
        ]
        ++ (if isList v then [ ] else map (k: exported "${what}.${k}" v.${k}) (attrNames v));
      pkgChecks =
        pkg:
        let
          fields = attrNames (binding.${pkg} or { });
          kinds = [
            "structs"
            "enums"
          ];
          allowed = attrNames keys.${pkg} ++ kinds;
          bad = filter (f: !elem f allowed) fields;
        in
        [
          (
            if bad == [ ] then
              true
            else
              fail "sdk binding ${pkg}: unknown field(s): ${concatStringsSep ", " bad}"
          )
        ]
        ++ concatMap' (fieldChecks pkg) (filter (f: !elem f kinds && elem f allowed) fields);
      unknownPkgs = filter (p: !elem p sdkPackages) (attrNames binding);
    in
    deepSeq (
      [
        (
          if unknownPkgs == [ ] then
            true
          else
            fail "sdk binding: unknown package(s): ${concatStringsSep ", " unknownPkgs}"
        )
      ]
      ++ concatMap' pkgChecks sdkPackages
    ) true;

  renderSDK =
    description: binding:
    let
      desc = validate description;
      ok = checkSdkBinding desc binding;
      bound = pkg: field: binding.${pkg}.${field} or { };
      limits = binding.connector.limits or [ ];
      structsOf = pkg: binding.${pkg}.structs or [ ];
      enumsOf = pkg: binding.${pkg}.enums or [ ];
      built = listToAttrs (
        map (pkg: {
          name = pkg;
          value =
            let
              s = sdkStructs desc pkg (structsOf pkg);
              e = sdkEnums desc pkg (enumsOf pkg);
            in
            s
            // {
              text = s.text + e.text;
              idents = s.idents ++ e.idents;
            };
        }) sdkPackages
      );
      header =
        pkg:
        "// Code generated by nix/regen.nix from lib/connector-protocol/*.nix. DO NOT EDIT.\n\npackage ${pkg}\n\n"
        + (
          if built.${pkg}.imports == [ ] then
            ""
          else
            "import (\n" + concat (map (i: "\t${goString i}\n") built.${pkg}.imports) + ")\n\n"
        );
      suffixOf =
        axis: name:
        if name == axis then axis else substring (stringLength axis + 1) (stringLength name) name;
      # A name the binding spells out keeps it; any other is unexported, so only
      # what the SDK publishes is part of its API.
      capIdent = axis: c: (bound axis "caps").${c.name} or "cap${goIdent (suffixOf axis c.name)}";
      methodIdent =
        axis: m: (bound axis "methods").${m.name} or "method${goIdent (suffixOf axis m.name)}";
      axisConsts =
        axis:
        let
          caps = desc.capabilities.${axis};
          methods = filter (m: axisOf m.name == axis) desc.methods;
        in
        {
          idents = map (capIdent axis) caps ++ map (methodIdent axis) methods;
          text =
            "// The capability names a ${axis} Connector advertises.\n"
            + constBlock (
              map (c: commented (capIdent axis c) c.doc + "${capIdent axis c} = ${goString c.name}\n") caps
            )
            + "// The method names of the ${axis} axis.\n"
            + constBlock (
              map (
                m: commented (methodIdent axis m) m.doc + "${methodIdent axis m} = ${goString m.name}\n"
              ) methods
            );
        };
      typedConsts =
        ty: prefix: entries:
        constBlock (
          map (
            e:
            commented "${prefix}${goIdent e.name}" e.doc
            + "${prefix}${goIdent e.name} ${ty} = ${goString e.name}\n"
          ) entries
        );
      untypedConsts =
        prefix: entries:
        constBlock (
          map (
            e:
            commented "${prefix}${goIdent e.name}" e.doc + "${prefix}${goIdent e.name} = ${goString e.name}\n"
          ) entries
        );
      files = {
        connector = {
          idents = [
            "protocolVersion"
            "Class"
          ]
          ++ map (c: goIdent c.name) desc.errorClasses
          ++ map goIdent limits;
          text =
            "// protocolVersion is the protocol version the SDK speaks. It is a var, not a\n"
            + "// const, so a test fixture can override it with\n"
            + "// -ldflags=-X spindrift.dev/sdk/connector.protocolVersion=<version>.\n"
            + "var protocolVersion = ${goString desc.version}\n\n"
            + "// Class is the canonical error class a Connector reports with a failure. Core\n"
            + "// decides on retries from the class alone, so it must describe the failure and\n"
            + "// not the primitive that raised it.\n"
            + "type Class string\n\n"
            + constBlock (
              map (
                c: commented (goIdent c.name) c.doc + "${goIdent c.name} Class = ${goString c.name}\n"
              ) desc.errorClasses
            )
            + constBlock (
              map (
                n:
                commented (goIdent n) desc.limits.${n}.doc + "${goIdent n} = ${toString desc.limits.${n}.value}\n"
              ) limits
            );
        };
        event = {
          idents = [
            "Kind"
            "IsProgress"
          ]
          ++ map (e: "Kind${goIdent e.name}") desc.events;
          text =
            "// Kind names one canonical event a Driver's decoder derives from a Pass log\n"
            + "// line (ADR 0065). Drivers translate their CLI's own grammar into these kinds\n"
            + "// at their own boundary; the shared readers (classification, usage, result\n"
            + "// text, transcript, heartbeat) consume only Events and never see a grammar.\n"
            + "type Kind string\n\n"
            + typedConsts "Kind" "Kind" desc.events
            + "// IsProgress reports whether k shows the agent working past an earlier error:\n"
            + "// an error followed by such an event was recovered from. KindUsage is\n"
            + "// deliberately not progress: it can close the step whose error was just\n"
            + "// emitted.\n"
            + "func IsProgress(k Kind) bool {\n"
            + "\tswitch k {\n"
            + "\tcase ${
              concatStringsSep ", " (map (e: "Kind${goIdent e.name}") (filter (e: e.progress) desc.events))
            }:\n"
            + "\t\treturn true\n\t}\n\treturn false\n}\n\n";
        };
        forge = axisConsts "forge";
        tracker =
          let
            axis = axisConsts "tracker";
          in
          {
            idents =
              axis.idents
              ++ map (e: "StatusCategory${goIdent e.name}") desc.statusCategories
              ++ map (e: "Transition${goIdent e.name}") desc.transitionOutcomes;
            text =
              axis.text
              + "// The categories of a Status that the SDK names.\n"
              + untypedConsts "StatusCategory" desc.statusCategories
              + "// The outcomes of a Transition that is not an error.\n"
              + untypedConsts "Transition" desc.transitionOutcomes;
          };
        driver =
          let
            axis = axisConsts "driver";
            roleIdent = r: (bound "driver" "roles").${r.name} or "${goIdent r.name}Role";
            roles = desc.hostLine.roles;
          in
          {
            idents =
              axis.idents
              ++ [
                "Reason"
                "passRoles"
                "hostLineType"
                "hostLineOpPassStart"
              ]
              ++ map (e: goIdent e.name) desc.reasons
              ++ map roleIdent roles;
            text =
              axis.text
              + "// Reason classifies why a Pattern matched.\n"
              + "type Reason string\n\n"
              + typedConsts "Reason" "" desc.reasons
              + "// The tokens of the host line HostLine reads.\n"
              + constBlock [
                "hostLineType = ${goString desc.hostLine.type}\n"
                "hostLineOpPassStart = ${goString desc.hostLine.opPassStart}\n"
              ]
              + "// The attribution roles core stamps on events. A Driver's decoder uses them to\n"
              + "// attribute the events it emits.\n"
              + constBlock (
                map (r: commented (roleIdent r) r.doc + "${roleIdent r} = ${goString r.name}\n") roles
              )
              + "// passRoles maps a pass to the role its pass_start op starts.\n"
              + "var passRoles = map[string]string{\n"
              + concat (
                map (p: "${goString p}: ${roleIdent { name = desc.hostLine.passRoles.${p}; }},\n") (
                  attrNames desc.hostLine.passRoles
                )
              )
              + "}\n\n";
          };
      };
    in
    seq ok (
      listToAttrs (
        map (pkg: {
          name = "sdk/${pkg}/protocol_gen.go";
          value = seq (noDups "sdk/${pkg} Go identifiers" (files.${pkg}.idents ++ built.${pkg}.idents)) (
            header pkg + files.${pkg}.text + built.${pkg}.text
          );
        }) sdkPackages
      )
    );

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
  inherit renderGo renderSchema renderSDK;
}
