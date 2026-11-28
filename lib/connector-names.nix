# The errors for a Connector registered a second time or outside its data
# (ADR 0073, issue #7562). Pure so nix/checks/connector-registration.nix can
# pin the text: builtins.tryEval cannot see thrown messages. A definition list
# is the module system's; each def carries the `file` of the module that wrote
# it. A def that sets nothing but `shadow`, its rate `shadowEvery` or `primary`
# is a Consumer's switch on a registered row (issues #6797, #6810, #6998), not
# a registration.
rec {
  # Wording and `file` shared by the refusals and the data-built definitions.
  dataFile = "spindrift.connectorData";
  dataOnlyRule = "registers only as data, through ${dataFile}";

  # The Consumer switches a backend row refuses (issue #7456), in the order
  # the refusal lists them. One source for the module's `definedSwitches` and
  # the switches `dataRow` rejects as stray keys.
  switchNames = [
    "shadow"
    "shadowEvery"
    "primary"
  ];

  # Why a backend row's `field` naming env-schema `key` cannot stand, or null
  # (issues #6805, #7498), over one attrset of its named arguments. `ownKnob` is the row's `knobs.${key}` and
  # `coreEntry` the merged env schema's `${key}`, each null when absent. A core
  # knob is no backend knob, except that a credential may name the token knob
  # of the core backend of the row's own name, matched by the `tokenEnvVar` its
  # descriptor declares in `backends` (the core registry), and an identity its
  # `identityEnvVar` knob (issue #7501); that entry is then checked as an own
  # knob is. The name match is only a pre-filter, as there is no pkgs here to
  # tell a first-party row from a third-party one (see `derivedCredentialEnv`).
  backendKnobError =
    {
      backends,
      name,
      field,
      secret,
      key,
      ownKnob,
      coreEntry,
    }:
    let
      fail = why: "Connector '${name}': backend ${field} '${key}' ${why}";
      ownCoreKnob =
        coreEntry != null
        && (coreEntry.env or null) != null
        && builtins.any (
          b:
          b.name == name
          && (
            (field == "credential" && (b.tokenEnvVar or null) == coreEntry.env)
            || (field == "identity" && (b.identityEnvVar or null) == coreEntry.env)
          )
        ) backends;
      knob =
        if ownKnob != null then
          ownKnob
        else if ownCoreKnob then
          coreEntry
        else
          null;
    in
    if knob == null then
      fail "is not in this Connector's `knobs` (a core knob cannot be a backend ${field})"
    else if secret && !(knob.secret or false) then
      fail "is not a secret knob (`secret = true` required)"
    else if (knob.boxEnv or false) != false then
      fail "must be host-only (`boxEnv = false` required)"
    else if !(knob ? env) then
      fail "has no `env`"
    else
      null;

  # A literal https origin: scheme and host[:port] only, so core appends the
  # repository path itself (the Go side, backendrows, refuses the rest).
  isGitOrigin = v: builtins.isString v && builtins.match "https://[^/?#@[:space:]]+" v != null;

  # Whether `backends` (the core registry) lists `name` as a Code Forge backend.
  isCodeForgeRow =
    backends: name: builtins.any (b: b.name == name && (b.validAsCodeForge or false)) backends;

  # Why a backend row's `gitRemote` cannot stand, or null. `value` is the
  # declaration (null when absent), `knobKnown` whether it names an env-schema
  # knob. Only a row of a Code Forge backend must declare one.
  gitRemoteError =
    {
      backends,
      name,
      value,
      knobKnown,
    }:
    let
      fail = why: "Connector '${name}': backend gitRemote '${value}' ${why}";
      isForge = isCodeForgeRow backends name;
    in
    if value == null then
      if isForge then
        "Connector '${name}': a Code Forge backend row must declare `gitRemote` (a literal https origin, or the env-schema key of the knob holding the forge's https base URL)"
      else
        null
    else if builtins.substring 0 8 value == "https://" then
      if isGitOrigin value then
        null
      else
        fail "is not a bare https origin (`https://host[:port]`, no path, query or userinfo)"
    else if knobKnown then
      null
    else
      fail "is neither an https origin nor an env-schema knob";

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

  # The credential variable core derives for a third-party backend row from its
  # name alone: `my-thing` -> `MY_THING_TOKEN` (issue #7560). Only a row mkHarness
  # marks first-party (its wasm is the batteries build of that name) names its
  # own credential; `backendCredentialError` refuses any other row's.
  derivedCredentialEnv =
    name:
    let
      chars = s: builtins.genList (i: builtins.substring i 1 s) (builtins.stringLength s);
      lower = chars "abcdefghijklmnopqrstuvwxyz-";
      upper = chars "ABCDEFGHIJKLMNOPQRSTUVWXYZ_";
    in
    builtins.replaceStrings lower upper name + "_TOKEN";

  # Why a mkHarness backend row's credential cannot stand, or null; the rule is
  # on `derivedCredentialEnv`. `coreEnvs` maps each core knob's env to its
  # env-schema key; `otherEnvs` lists `{ row; field; env; }` for every other
  # backend row's credential, identity, base and Git remote variable.
  backendCredentialError =
    {
      name,
      firstParty,
      row,
      coreEnvs,
      otherEnvs,
    }:
    let
      fail = why: "mkHarness: backendRows.${name} ${why}";
      derived = derivedCredentialEnv name;
      clash = builtins.filter (o: o.env == derived) otherEnvs;
    in
    if row ? firstParty then
      fail "sets `firstParty`; mkHarness derives the mark itself from the batteries flake's own Connector wasm, so no row may set it"
    else if firstParty then
      if builtins.isString (row.credentialEnv or null) && row.credentialEnv != "" then
        null
      else
        fail "must carry a non-empty string `credentialEnv` (a first-party row names its own backend's credential)"
    else if row ? credentialEnv then
      fail "is third-party and cannot name its own `credentialEnv` (a Connector's `backend.credential`); core derives its credential variable as `${derived}`"
    else if builtins.match "[A-Z_][A-Z0-9_]*" derived == null then
      fail "is third-party and its derived credential variable `${derived}` is not a valid environment variable name; rename the row"
    else if coreEnvs ? ${derived} then
      fail "is third-party and its derived credential variable `${derived}` is core knob `${coreEnvs.${derived}}`'s env; rename the row"
    else if clash != [ ] then
      fail "is third-party and its derived credential variable `${derived}` is already backendRows.${(builtins.head clash).row}'s `${(builtins.head clash).field}`; rename the row"
    else
      null;

  # The axes a third-party backend row may serve (issue #7647).
  backendAxes = [
    "tracker"
    "forge"
  ];

  # Why a mkHarness backend row's `axes` and `displayName` cannot stand, or
  # null. A first-party row keeps both in core's backend rows and sets neither;
  # a third-party row has no other source and sets both.
  backendFactsError =
    {
      name,
      firstParty,
      row,
    }:
    let
      fail = why: "mkHarness: backendRows.${name} ${why}";
      axes = row.axes or null;
      unknown = builtins.filter (a: !(builtins.isString a && builtins.elem a backendAxes)) axes;
      show = a: if builtins.isString a then "'${a}'" else "a ${builtins.typeOf a}";
      known = builtins.concatStringsSep " and " backendAxes;
      duplicate = builtins.filter (a: builtins.length (builtins.filter (b: b == a) axes) > 1) axes;
    in
    if firstParty then
      if row ? axes then
        fail "is a first-party row; its axes are core's backend row facts, so it must not set `axes`"
      else if row ? displayName then
        fail "is a first-party row; its display name is core's backend row fact, so it must not set `displayName`"
      else
        null
    else if !(row ? axes) then
      fail "is third-party and must declare `axes`, a non-empty list drawn from ${known}"
    else if !(builtins.isList axes) || axes == [ ] then
      fail "`axes` must be a non-empty list drawn from ${known}"
    else if unknown != [ ] then
      fail "`axes` names ${builtins.concatStringsSep ", " (map show unknown)}; the axes are ${known}"
    else if duplicate != [ ] then
      fail "`axes` names ${show (builtins.head duplicate)} twice; list each axis once"
    else if !(row ? displayName) then
      fail "is third-party and must declare `displayName`, a non-empty string"
    else if !(builtins.isString row.displayName) || row.displayName == "" then
      fail "`displayName` must be a non-empty string"
    else
      null;

  # Why a row cannot take a first-party backend's name, or null: a first-party
  # name always means the first-party Connector (ADR 0073).
  firstPartyNameError =
    {
      name,
      firstParty,
      firstPartyNames,
    }:
    if firstParty || !builtins.elem name firstPartyNames then
      null
    else
      "mkHarness: backendRows.${name} is not the first-party ${name} Connector; a first-party backend name always means the first-party Connector (ADR 0073), so a third-party Connector must take another name";

  # Why a derived credential variable is refused because another Connector's
  # knob carries it (the host would attach that value to the row's routes), or
  # null.
  derivedCredentialClashError =
    {
      name,
      derived,
      owner,
    }:
    if owner == null then
      null
    else
      "Connector '${name}': its derived credential variable `${derived}` is already a knob of Connector '${owner}'; rename the row";

  # Why a Connector name cannot be registered by more than one definer, or
  # null. `defs` is the module system's definition list; each carries the
  # `file` of the module that wrote it.
  duplicateError =
    name: defs:
    let
      n = builtins.length defs;
      files = map (d: d.file) defs;
      sameFile = builtins.all (f: f == builtins.head files) files;
      by =
        if sameFile then
          "${
            if n == 2 then "both times" else "every time"
          } by ${builtins.head files} (one module registers it more than once: import it once and list it once)"
        else
          "by ${builtins.concatStringsSep " and by " files}";
    in
    if n < 2 then
      null
    else
      "Connector '${name}' is registered ${
        if n == 2 then "twice" else "${toString n} times"
      }: ${by}; a Connector registers once, as data, and a name has one definer";

  # Why the definitions of `spindrift.connectorData` cannot stand, or null.
  # `defs` is the option's definition list as the module system saw it
  # (`definitionsWithLocations`): `{ file; value = [ data ... ]; }` per
  # definer. It reads only `data.name`; the option's merge validates each item
  # and throws exactly this result.
  registrationError =
    defs:
    let
      items = builtins.concatMap (
        d:
        map (data: {
          inherit (d) file;
          inherit (data) name;
        }) d.value
      ) defs;
      byName = builtins.groupBy (i: i.name) items;
      twice = builtins.filter (n: builtins.length byName.${n} > 1) (builtins.attrNames byName);
    in
    if twice == [ ] then null else duplicateError (builtins.head twice) byName.${builtins.head twice};

  # Why a definition of `spindrift.connectors.<name>` is refused: it sets
  # `keys`, which only the Connector's data may declare.
  rowOutsideDataError =
    name: file: keys:
    "Connector '${name}': ${file} sets ${
      builtins.concatStringsSep ", " (map (k: "'${k}'") keys)
    } on spindrift.connectors.${name}; a Connector row ${dataOnlyRule}, and spindrift.connectors.${name} takes only the switches ${builtins.concatStringsSep ", " switchNames}";

  # Why a definition of `spindrift.connectors.<name>` is refused when no data
  # registers `name`: a switch has no row to apply to.
  unregisteredRowError =
    name: file:
    "Connector '${name}': ${file} defines spindrift.connectors.${name}, but no Connector '${name}' is registered; a Connector ${dataOnlyRule}, and spindrift.connectors.${name} takes only switches on a registered row";

  # Why `spindrift.connectorData` cannot carry a priority below the default:
  # an override would discard another definer's registration. `registered` is
  # the Connector names in the list that survived.
  dataOverrideError =
    registered:
    "spindrift.connectorData was overridden (mkForce or mkOverride) and no longer carries every registration, leaving ${
      if registered == [ ] then
        "no Connector"
      else
        "only ${builtins.concatStringsSep ", " (map (n: "'${n}'") registered)}"
    }; no definition may discard another's registration, so write a plain list";

  # `type`, an attrsOf element type, made to take from a definition only the
  # switches, and to build the row itself from `dataOf name`, the Connector's
  # data row or null. A row is the Connector's data, so no definition of it can
  # replace or merge into it, mkForce included: a whole-row override is refused
  # for the keys it sets, and a switch-only one (which may drop core's `{ }`)
  # leaves the data row intact. `substSubModules` must re-wrap: the module
  # system rebuilds a submodule type through it, which would drop the override.
  dataRow =
    dataOf: type:
    type
    // {
      merge =
        loc: defs:
        let
          name = builtins.elemAt loc (builtins.length loc - 1);
          strayKeys =
            d:
            if builtins.isAttrs d.value then
              builtins.filter (k: !(builtins.elem k switchNames)) (builtins.attrNames d.value)
            else
              [ "<a module function>" ];
          bad = builtins.filter (d: strayKeys d != [ ]) defs;
          data = dataOf name;
        in
        if bad != [ ] then
          throw (rowOutsideDataError name (builtins.head bad).file (strayKeys (builtins.head bad)))
        else
          type.merge loc (
            defs
            ++ (
              if data == null then
                [ ]
              else
                [
                  {
                    file = dataFile;
                    value = data;
                  }
                ]
            )
          );
      substSubModules = m: dataRow dataOf (type.substSubModules m);
    };

  # Whether a definition's value survives the module system's property
  # discharge (lib.modules dischargeProperties): `mkIf false` and an empty
  # `mkMerge` leave no definition behind. An override is not discharged:
  # filterOverrides unwraps it and keeps its content defined, so
  # `mkForce (mkIf false ...)` is still a row; mkOrder likewise counts as live.
  live =
    v:
    if !(builtins.isAttrs v) then
      true
    else if v._type or "" == "if" then
      v.condition && live v.content
    else if v._type or "" == "merge" then
      builtins.any live v.contents
    else
      true;

  # The file of the first definition that keeps row `n` alive, which is the
  # one that made a name outside the registered data a stray.
  strayDefiner =
    defs: n: (builtins.head (builtins.filter (d: d.value ? ${n} && live d.value.${n}) defs)).file;

  # `type`, the attrsOf of `dataRow` rows, made to carry exactly the rows in
  # `registered` (the data by name). The row names come from here, appended
  # after the module system's priority filtering, rather than from a
  # definition that `mkForce` of the whole set could discard. A name outside
  # `registered` that survives the merge is refused rather than given a row;
  # one whose definitions all discharge (`mkIf false`) is dropped as attrsOf
  # drops it.
  registeredRowSet =
    registered: type:
    type
    // {
      merge =
        loc: defs:
        let
          merged = type.merge loc (
            defs
            ++ [
              {
                file = dataFile;
                value = builtins.mapAttrs (_: _: { }) registered;
              }
            ]
          );
          stray = builtins.filter (n: !(registered ? ${n})) (builtins.attrNames merged);
        in
        if stray != [ ] then
          throw (unregisteredRowError (builtins.head stray) (strayDefiner defs (builtins.head stray)))
        else
          merged;
      substSubModules = m: registeredRowSet registered (type.substSubModules m);
    };

  # A second declaration of `option` would replace the type that enforces
  # the data-only and one-definer rules.
  redeclaredError =
    option: declarations:
    "${option} must be declared once, by core, but is declared in ${builtins.concatStringsSep ", " declarations}: a second declaration would replace the type that enforces that a Connector ${dataOnlyRule}, and that a name has one definer. Register the Connector in spindrift.connectorData instead.";

  # `value` of `option`, refused when more than one module declares it. Used as
  # the option's `apply`: a second declaration's type can win the merge, but
  # core's `apply` cannot be dropped, since nixpkgs refuses a second `apply`.
  declaredOnce =
    opt: value:
    if builtins.length opt.declarations == 1 then
      value
    else
      throw (redeclaredError (builtins.concatStringsSep "." opt.loc) opt.declarations);
}
