# Eval-level checks for lib/skills.nix's skill-name validation (issue #5022):
# a `{ name; src; }` entry's name is spliced into the bake scripts, so it must
# be one safe path component.
{
  pkgs,
  nixpkgs,
  system,
  flake-parts,
  ...
}:
let
  skillsLib = import ../../lib/skills.nix;
  inherit (pkgs.lib) assertMsg hasInfix;

  badNames = [
    ""
    "."
    ".."
    "team/foo"
    "../x"
    "a b"
    "a;b"
    "$(x)"
  ];
  goodNames = [
    "caveman"
    "code-review"
    "foo.bar"
    "A_1"
  ];
  entry = name: {
    inherit name;
    src = "body";
  };
  badEntryReport =
    name:
    let
      r = skillsLib.validateSkillsResult [ (entry name) ];
    in
    r.ok == false
    && hasInfix (builtins.toJSON name) r.message
    && hasInfix skillsLib.namePattern r.message;

  throws = v: !(builtins.tryEval (builtins.deepSeq v v)).success;
  # Forcing agentFiles' drvPath evaluates the image's skill-baking script, the
  # path both bake sites share.
  harnessWith =
    skills:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system skills;
      packages = p: [ p.hello ];
    }).internals.agentFiles.drvPath;

  # flake-parts `debug = true` exposes the per-system module eval, so the
  # skills option's own type check runs without the mkHarness backstop.
  optionSkills =
    skills:
    (flake-parts.lib.mkFlake
      {
        inputs = {
          inherit nixpkgs;
          self.outPath = ../../.;
        };
      }
      {
        systems = [ system ];
        debug = true;
        imports = [ ../../lib/flakeModule.nix ];
        perSystem.spindrift = {
          packages = p: [ p.hello ];
          agents.skills = skills;
        };
      }
    ).allSystems.${system}.config.spindrift.agents.skills;
in
{
  skills-name-validation =
    assert assertMsg (builtins.all badEntryReport badNames)
      "validateSkillsResult must reject every unsafe name with its JSON and the name pattern in the message";
    assert assertMsg (builtins.all (
      n: (skillsLib.validateSkillsResult [ (entry n) ]).ok
    ) goodNames) "validateSkillsResult must accept safe names";
    assert assertMsg (
      !(builtins.any skillsLib.isValidName [
        ""
        "."
        ".."
      ])
      && skillsLib.isValidName "caveman"
    ) "isValidName must reject empty, \".\" and \"..\" and accept a plain name";
    assert assertMsg (skillsLib.validateSkillsResult [ ]).ok "an empty skills list is valid";
    # Path and derivation entries take the other bake branch; only attrset
    # content entries have a name to validate.
    assert assertMsg
      (skillsLib.validateSkillsResult [
        ./.
        "../x"
        (pkgs.writeText "x" "")
      ]).ok
      "path, string and derivation entries must not be name-checked";
    assert assertMsg (hasInfix "entry 1 "
      (skillsLib.validateSkillsResult [
        (entry "ok")
        (entry "../x")
      ]).message
    ) "the message must name the offending entry's index";
    assert assertMsg (
      !(skillsLib.validateSkillsResult [
        { src = "no name"; }
      ]).ok
    ) "a content entry with no name must be rejected";
    assert assertMsg (
      !(skillsLib.validateSkillsResult [
        (entry 3)
      ]).ok
    ) "a non-string name must be rejected";
    pkgs.runCommand "skills-name-validation" { } "touch $out";

  skills-validation-throws =
    assert assertMsg (throws (
      skillsLib.validateSkills [ (entry "../x") ]
    )) "validateSkills must throw on an unsafe name";
    assert assertMsg (
      !throws (skillsLib.validateSkills [ (entry "ok") ])
    ) "validateSkills must accept a safe name";
    assert assertMsg (throws (harnessWith [
      (entry "../x")
    ])) "mkHarness must throw on an unsafe skill name";
    assert assertMsg (
      !throws (harnessWith [ (entry "ok") ])
    ) "mkHarness must still evaluate with a safe skill name";
    assert assertMsg (throws (optionSkills [
      (entry "..")
    ])) "the skills option type must reject \"..\"";
    assert assertMsg (throws (optionSkills [
      (entry ".")
    ])) "the skills option type must reject \".\"";
    assert assertMsg (
      !throws (optionSkills [ (entry "ok") ])
    ) "the skills option must accept a safe name";
    pkgs.runCommand "skills-validation-throws" { } "touch $out";
}
