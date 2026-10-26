# Pins for the dogfood pi Driver's self-hosted roster (issue #6049): every
# role runs GLM on wadsworth's GPU. The scout and filer moved off the NPU
# because its 32k context overflowed on #6003's scout. The Provider name
# matches the PI_MODELS_CONTENT the operator supplies; no github-copilot tier
# remains, so PI_AUTH_CONTENT is no longer needed.
{
  pkgs,
  config,
  ...
}:
let
  inherit (pkgs.lib)
    all
    assertMsg
    attrNames
    filter
    ;
  pi = config.spindrift.agents.drivers.pi.models;
  byName = builtins.listToAttrs (
    map (r: {
      inherit (r) name;
      value = r;
    }) pi.roster
  );
  glm = "wadsworth/glm-5.3-flash";
  roles = [
    "scout"
    "filer"
    "worker"
    "reviewer"
    "review-axis"
  ];
  wrong = filter (n: (byName.${n}.model or null) != glm) roles;
  # GLM-5.3-Flash's chat template reads only low and high; any other effort,
  # medium included, falls through to its slowest max setting.
  efforts = map (n: byName.${n}.effort or null) roles;
in
{
  dogfood-pi-roster-self-hosted =
    assert assertMsg (pi.default == glm && wrong == [ ] && all (r: r.model == glm) pi.roster)
      "every role in the dogfood pi Driver must use ${glm}, got default ${pi.default} and mismatched roles: ${builtins.toJSON wrong}";
    pkgs.runCommand "dogfood-pi-roster-self-hosted" { } "touch $out";

  dogfood-pi-roster-glm-efforts-low-or-high =
    assert assertMsg (all
      (
        e:
        builtins.elem e [
          "low"
          "high"
        ]
      )
      efforts
    ) "every role in the dogfood pi roster must pin effort low or high, got ${builtins.toJSON efforts}";
    pkgs.runCommand "dogfood-pi-roster-glm-efforts-low-or-high" { } "touch $out";

  # The scout and filer run on the same GPU as the coordinator; low keeps
  # their many short turns cheap.
  dogfood-pi-roster-subagents-low =
    assert assertMsg ((byName.scout.effort or null) == "low" && (byName.filer.effort or null) == "low")
      "the dogfood pi scout and filer must pin effort low, got scout ${byName.scout.effort or "unset"} and filer ${byName.filer.effort or "unset"}";
    pkgs.runCommand "dogfood-pi-roster-subagents-low" { } "touch $out";
}
