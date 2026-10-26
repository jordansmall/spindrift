# Pins for the dogfood pi Driver's self-hosted roster (issue #6049): GLM on
# wadsworth's GPU for the coordinator, worker and reviewer, qwen3.5:4b on its
# NPU for the scout and filer. The Provider names match the PI_MODELS_CONTENT
# the operator supplies; no github-copilot tier remains, so PI_AUTH_CONTENT is
# no longer needed.
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
    hasPrefix
    ;
  pi = config.spindrift.agents.drivers.pi.models;
  byName = builtins.listToAttrs (
    map (r: {
      inherit (r) name;
      value = r;
    }) pi.roster
  );
  glm = "wadsworth/glm-5.3-flash";
  npu = "npu/qwen3.5:4b";
  want = {
    scout = npu;
    filer = npu;
    worker = glm;
    reviewer = glm;
    review-axis = glm;
  };
  wrong = filter (n: (byName.${n}.model or null) != want.${n}) (attrNames want);
  # GLM-5.3-Flash's chat template reads only low and high; any other effort,
  # medium included, falls through to its slowest max setting.
  glmEfforts = map (n: byName.${n}.effort or null) (filter (n: want.${n} == glm) (attrNames want));
in
{
  dogfood-pi-roster-self-hosted =
    assert assertMsg
      (pi.default == glm && wrong == [ ] && all (r: !(hasPrefix "github-copilot/" r.model)) pi.roster)
      "the dogfood pi Driver must use ${glm} for the coordinator, worker and reviewer and ${npu} for the scout and filer, got default ${pi.default} and mismatched roles: ${builtins.toJSON wrong}";
    pkgs.runCommand "dogfood-pi-roster-self-hosted" { } "touch $out";

  dogfood-pi-roster-glm-efforts-low-or-high =
    assert assertMsg
      (all (
        e:
        builtins.elem e [
          "low"
          "high"
        ]
      ) glmEfforts)
      "every GLM role in the dogfood pi roster must pin effort low or high, got ${builtins.toJSON glmEfforts}";
    pkgs.runCommand "dogfood-pi-roster-glm-efforts-low-or-high" { } "touch $out";
}
