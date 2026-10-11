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

  names = rows: builtins.filter (n: rows.${n}.driverValued or false) (builtins.attrNames rows);
  envKeys = names schema;
  structuralKeys = names structuralDoc;

  splitDots = s: builtins.filter builtins.isString (builtins.split "\\." s);
  # Drop the leading "agents" domain: per-Driver entries nest under
  # agents.drivers.<name>, not under agents.
  agentsRelative = segments: builtins.tail segments;

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

  join = builtins.concatStringsSep ", ";
  quote = n: "\"${n}\"";
  rest = p: builtins.substring (builtins.stringLength "agents.") (builtins.stringLength p) p;
  moveHint = p: "agents.drivers.<name>.${rest p}";
  moveHints = ps: join (map (p: "${p} -> ${moveHint p}") ps);

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
    in
    if declared == [ ] then
      if defaultDriver != null then
        fail "perSystem.spindrift.agents.defaultDriver names ${quote defaultDriver}, but perSystem.spindrift.agents.drivers is empty -- declare perSystem.spindrift.agents.drivers.${defaultDriver}"
      else
        ok null
    else if builtins.length declared > 1 && driverValuedSet != [ ] then
      fail "perSystem.spindrift.agents.drivers declares several Drivers (${declaredList}), so a Driver-valued setting cannot be set at top level (${join driverValuedSet}) -- move each under every Driver it applies to: ${moveHints driverValuedSet}"
    else if setPaths != [ ] then
      fail "perSystem.spindrift.agents.drivers is set alongside the single-Driver form (${join setPaths}) -- move each Driver-valued setting under agents.drivers.<name>.<rest> (${moveHints driverValuedSet}) and drop agents.driver, since the attribute name is the Driver"
    else if defaultDriver == null then
      if builtins.length declared == 1 then
        ok (builtins.head declared)
      else
        fail "perSystem.spindrift.agents.defaultDriver is required when several Drivers are declared (${declaredList}) -- set perSystem.spindrift.agents.defaultDriver to one of them"
    else if !(drivers ? ${defaultDriver}) then
      fail "perSystem.spindrift.agents.defaultDriver is ${quote defaultDriver}, which is not a declared Driver (${declaredList}) -- declare perSystem.spindrift.agents.drivers.${defaultDriver} or set agents.defaultDriver to one of the declared names"
    else
      ok defaultDriver;
in
{
  inherit resolve paths;
  # Marked schema keys, split by origin: `env` rows live in lib/env-schema.nix
  # (mkHarness args by flat key), `structural` in lib/structural-options-doc.nix.
  driverValued = {
    env = envKeys;
    structural = structuralKeys;
  };
}
