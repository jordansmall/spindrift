# Pure resolver and Driver-valued knob set for the multi-Driver consumer flake
# (ADR 0064, issue #5052). Plain builtins only so it imports standalone, like
# lib/renderers.nix. The Driver-valued set is derived from the `driverValued`
# markers on lib/env-schema.nix and lib/structural-options-doc.nix rows, so
# the marking is written once and every consumer of it reads this file.
let
  schema = import ./env-schema.nix;
  structuralDoc = import ./structural-options-doc.nix;
  structuralPaths = import ./structural-paths.nix;
  byNamePaths = import ./byname-paths.nix;
  nixpath = import ./nixpath.nix;
  deprecationNote = import ./driver-deprecation-note.nix;

  names = rows: builtins.filter (n: rows.${n}.driverValued or false) (builtins.attrNames rows);
  envKeys = names schema;
  structuralKeys = names structuralDoc;

  splitDots = s: builtins.filter builtins.isString (builtins.split "\\." s);
  # Drop the leading "agents" domain: per-Driver entries nest under
  # agents.drivers.<name>, not under agents.
  agentsRelative = segments: builtins.tail segments;

  # The Driver name generated examples and docs render a Driver-valued setting
  # under, so no consumer hard-codes a second spelling of it.
  exampleDriver = "claude";

  # Flat schema key (the mkHarness arg) -> path segments under agents.
  paths =
    builtins.listToAttrs (
      map (n: {
        name = n;
        value = agentsRelative (splitDots (nixpath n schema.${n}));
      }) envKeys
    )
    // builtins.listToAttrs (
      map (n: {
        name = n;
        value = agentsRelative (structuralPaths.${n} or byNamePaths.${n});
      }) structuralKeys
    );

  dotted = builtins.mapAttrs (_: segs: "agents." + builtins.concatStringsSep "." segs) paths;
  driverValuedDotted = builtins.attrValues dotted;

  # A path is Driver-valued when it is a marked knob or sits beneath one
  # (e.g. agents.models.byName.scout).
  isDriverValuedPath =
    p:
    builtins.any (
      d: p == d || builtins.substring 0 (builtins.stringLength d + 1) p == d + "."
    ) driverValuedDotted;

  optional = cond: x: if cond then [ x ] else [ ];
  join = builtins.concatStringsSep ", ";
  quote = n: "\"${n}\"";
  # Paths arrive bare (agents.x.y); every message names them by the
  # consumer-facing option path.
  full = p: "perSystem.spindrift.${p}";
  fullList = ps: join (map full ps);
  rest = p: builtins.substring (builtins.stringLength "agents.") (builtins.stringLength p) p;
  moveHintFor = name: p: full "agents.drivers.${name}.${rest p}";
  moveHint = moveHintFor "<name>";
  moveHints = ps: join (map (p: "${full p} -> ${moveHint p}") ps);
  moveClause = ps: if ps == [ ] then "" else " (${moveHints ps})";

  fail = error: {
    inherit error;
    default = null;
  };
  ok = default: {
    error = null;
    inherit default;
  };

  resolve =
    {
      drivers,
      defaultDriver,
      topLevel,
    }:
    let
      declared = builtins.attrNames drivers;
      setPaths = map (t: t.path) topLevel;
      driverValuedSet = builtins.filter isDriverValuedPath setPaths;
      declaredList = join (map quote declared);
      fixClause = builtins.concatStringsSep " and " (
        optional (driverValuedSet != [ ])
          "move each Driver-valued setting under ${full "agents.drivers.<name>.<rest>"}${moveClause driverValuedSet}"
        ++ optional (builtins.elem "agents.driver" setPaths) "drop ${full "agents.driver"}, since the attribute name is the Driver"
      );
    in
    if declared == [ ] then
      if defaultDriver != null then
        fail "perSystem.spindrift.agents.defaultDriver names ${quote defaultDriver}, but perSystem.spindrift.agents.drivers is empty -- declare perSystem.spindrift.agents.drivers.${defaultDriver}"
      else
        ok null
    else if builtins.length declared > 1 && driverValuedSet != [ ] then
      fail "perSystem.spindrift.agents.drivers declares several Drivers (${declaredList}), so a Driver-valued setting cannot be set at top level (${fullList driverValuedSet}) -- move each under every Driver it applies to: ${moveHints driverValuedSet}"
    else if setPaths != [ ] then
      fail "perSystem.spindrift.agents.drivers is set alongside the single-Driver form (${fullList setPaths}) -- ${fixClause}"
    else if defaultDriver == null then
      if builtins.length declared == 1 then
        ok (builtins.head declared)
      else
        fail "perSystem.spindrift.agents.defaultDriver is required when several Drivers are declared (${declaredList}) -- set perSystem.spindrift.agents.defaultDriver to one of them"
    else if !(drivers ? ${defaultDriver}) then
      fail "perSystem.spindrift.agents.defaultDriver is ${quote defaultDriver}, which is not a declared Driver (${declaredList}) -- declare perSystem.spindrift.agents.drivers.${defaultDriver} or set ${full "agents.defaultDriver"} to one of the declared names"
    else
      ok defaultDriver;

  # One warning per single-Driver setting, naming where it goes now. `name` is
  # the Driver the shorthand resolves to, so the new path is exact.
  deprecations =
    { name, topLevel }:
    map (
      t:
      if t.path == "agents.driver" then
        "${full t.path} is deprecated; declare ${full "agents.drivers.${name}"} instead (the attribute name is the Driver; with several Drivers also set ${full "agents.defaultDriver"}) (ADR 0064)"
      else
        "${full t.path} is deprecated; use ${moveHintFor name t.path} (ADR 0064)"
    ) topLevel;

  # A Driver-valued setting's full path segments (leading "agents"), placed
  # under one Driver's entry: agents.<rest> -> agents.drivers.<name>.<rest>.
  # Callers holding a schema entry go through placeEntry instead.
  underDriver =
    name: segments:
    if builtins.head segments != "agents" then
      throw "lib/driver-map.nix underDriver: expected a path under agents, got ${builtins.concatStringsSep "." segments}"
    else
      [
        "agents"
        "drivers"
        name
      ]
      ++ builtins.tail segments;

  isDriverValued = entry: entry.driverValued or false;

  # Places a path by its schema entry's driverValued mark: Driver-valued rows
  # move under the Driver, every other row keeps its path.
  placeEntry =
    driverName: entry: segments:
    if isDriverValued entry then underDriver driverName segments else segments;

  # A schema knob's placed canonical flake path: its nixpath, moved under the
  # Driver when the entry is Driver-valued. Name this, never the bare nixpath.
  placedPath =
    driverName: key: entry:
    builtins.concatStringsSep "." (placeEntry driverName entry (splitDots (nixpath key entry)));

  # Where a flat structural shim's replacement lives: a Driver-valued knob
  # goes straight to its Driver entry, not via the deprecated top-level path.
  driverEntryPath =
    flatName: segments:
    if
      segments == [
        "agents"
        "driver"
      ]
    then
      [
        "agents"
        "drivers"
        "<name>"
      ]
    else
      placeEntry "<name>" (structuralDoc.${flatName} or { }) segments;
in
{
  inherit
    resolve
    paths
    deprecations
    exampleDriver
    deprecationNote
    underDriver
    isDriverValued
    placeEntry
    placedPath
    driverEntryPath
    ;
  # Where the module declares the map and its default-Driver name; read by
  # lib/flakeModule.nix and nix/checks/schema-drift.nix's
  # flake-nixpath-exhaustive-disjoint check, like lib/byname-paths.nix.
  optionPaths = {
    drivers = [
      "agents"
      "drivers"
    ];
    defaultDriver = [
      "agents"
      "defaultDriver"
    ];
  };
  # Marked schema keys, split by origin: `env` rows live in lib/env-schema.nix
  # (mkHarness args by flat key), `structural` in lib/structural-options-doc.nix.
  driverValued = {
    env = envKeys;
    structural = structuralKeys;
  };
}
