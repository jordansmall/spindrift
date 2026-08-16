# Baked-skill name list drift guards (issue #2532): lib/baked-skills.nix is
# the single source the skill-baked probe list, driver-exec flags, and
# promptassembly Env fields/Gates map render from -- these guards assert
# each committed generated span still matches lib/renderers.nix's
# renderBakedSkill* output, and that adding a row to the list flows through
# every renderer without touching any consumer file.
{ pkgs, ... }:
let
  renderers = import ../../lib/renderers.nix;
  bakedSkills = import ../../lib/baked-skills.nix;

  # Isolates the text strictly between a literal begin/end marker line pair,
  # mirroring nix/checks/schema-drift.nix's assertDefaultModelsDocOk (which
  # itself mirrors nix/regen.nix's write_between) so this guard can never
  # drift from what write_between actually replaces.
  between =
    {
      src,
      file,
      begin,
      end,
    }:
    let
      beginMarker = begin + "\n";
      afterBegin =
        let
          parts = builtins.split beginMarker src;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 2
        else
          throw "${file}: BEGIN marker not found: ${begin}";
      committed =
        let
          parts = builtins.split end afterBegin;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 0
        else
          throw "${file}: END marker not found: ${end}";
    in
    committed;

  assertSpanOk =
    {
      file,
      begin,
      end,
      generated,
    }:
    let
      committed = between {
        src = builtins.readFile file;
        file = toString file;
        inherit begin end;
      };
      inherit (pkgs.lib) assertMsg;
    in
    assert assertMsg (committed == generated) ''
      ${toString file} generated skill-baked span (between "${begin}" / "${end}") is out of sync with lib/baked-skills.nix -- regenerate it with `nix run .#regen`
        got:  ${committed}
        want: ${generated}'';
    true;

  # Two of the five generated spans (the assembleprompt_cmd.go Env{} literal
  # assignments and the env.go struct fields) sit inside a Go struct
  # literal/struct type whose contiguous field block `gofmt -w` column-
  # aligns (colons, types) across the *whole* run, not just the inserted
  # span -- the same reason renderSchemaConfigGo/renderBackendRegistryGo's
  # own drift checks (above) gofmt-normalize before comparing instead of
  # diffing the raw renderer string directly. This mirrors that: reconstruct
  # the real file with the span replaced by the *raw* (unaligned) renderer
  # output, `gofmt -w` the whole reconstruction (exactly what `nix run
  # .#regen`'s own write_between + gofmt -w pairing does), and diff against
  # the real committed file.
  assertGoSpanGofmtOk =
    {
      name,
      file,
      begin,
      end,
      generated,
    }:
    let
      raw = pkgs.writeText "${name}.raw" generated;
    in
    pkgs.runCommand name
      {
        nativeBuildInputs = [ pkgs.go ];
        committed = file;
        inherit raw;
        beginMarker = begin;
        endMarker = end;
      }
      ''
        awk -v begin="$beginMarker" -v end="$endMarker" -v rawfile="$raw" '
          BEGIN { while ((getline line < rawfile) > 0) content = content line "\n" }
          $0 == begin { print; printf "%s", content; skip=1; next }
          $0 == end { skip=0 }
          skip { next }
          { print }
        ' "$committed" > reconstructed.go
        gofmt -w reconstructed.go
        diff reconstructed.go "$committed" \
          || { echo "${toString file} generated skill-baked span (between \"${begin}\" / \"${end}\") is out of sync with lib/baked-skills.nix -- regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';
in
{
  baked-skills-probes-gen =
    assert assertSpanOk {
      file = ../../agent/entrypoint.sh;
      begin = "  # BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT";
      end = "  # END GENERATED SKILL-BAKED PROBES";
      generated = renderers.renderBakedSkillProbesShell bakedSkills;
    };
    pkgs.runCommand "baked-skills-probes-gen" { } "touch $out";

  baked-skills-flags-gen =
    assert assertSpanOk {
      file = ../../cmd/launcher/driver-exec/assembleprompt_cmd.go;
      begin = "\t// BEGIN GENERATED SKILL-BAKED FLAGS -- nix run .#regen -- DO NOT EDIT";
      end = "\t// END GENERATED SKILL-BAKED FLAGS";
      generated = renderers.renderBakedSkillFlagsGo bakedSkills;
    };
    pkgs.runCommand "baked-skills-flags-gen" { } "touch $out";

  baked-skills-env-assign-gen = assertGoSpanGofmtOk {
    name = "baked-skills-env-assign-gen";
    file = ../../cmd/launcher/driver-exec/assembleprompt_cmd.go;
    begin = "\t\t// BEGIN GENERATED SKILL-BAKED ENV -- nix run .#regen -- DO NOT EDIT";
    end = "\t\t// END GENERATED SKILL-BAKED ENV";
    generated = renderers.renderBakedSkillEnvAssignGo bakedSkills;
  };

  baked-skills-fields-gen = assertGoSpanGofmtOk {
    name = "baked-skills-fields-gen";
    file = ../../cmd/launcher/internal/promptassembly/env.go;
    begin = "\t// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT";
    end = "\t// END GENERATED SKILL-BAKED FIELDS";
    generated = renderers.renderBakedSkillFieldsGo bakedSkills;
  };

  baked-skills-gates-gen =
    assert assertSpanOk {
      file = ../../cmd/launcher/internal/promptassembly/gates.go;
      begin = "\t// BEGIN GENERATED SKILL-BAKED GATES -- nix run .#regen -- DO NOT EDIT";
      end = "\t// END GENERATED SKILL-BAKED GATES";
      generated = renderers.renderBakedSkillGatesGo bakedSkills;
    };
    pkgs.runCommand "baked-skills-gates-gen" { } "touch $out";

  # Regression guard (issue #2532 AC2): adding a row to lib/baked-skills.nix
  # must flow through every renderer -- the probe line, the flag decl, the
  # Env-literal assignment, the struct field, and the gate assignment -- with
  # no edit to any consumer file. Injects a synthetic seventh row into a copy
  # of the real list and asserts each renderer's output contains that row's
  # generated line.
  baked-skills-add-row-guard =
    let
      inherit (pkgs.lib) assertMsg hasInfix;
      extra = {
        name = "test-skill";
        flag = "test-skill-skill-baked";
        goVar = "testSkillSkillBaked";
        field = "TestSkillSkillBaked";
        gate = "TEST_SKILL_BAKED";
      };
      withExtra = bakedSkills ++ [ extra ];
    in
    assert assertMsg (hasInfix "test-skill-skill-baked" (
      renderers.renderBakedSkillProbesShell withExtra
    )) "renderBakedSkillProbesShell did not render the injected row's probe line";
    assert assertMsg (hasInfix "test-skill-skill-baked" (
      renderers.renderBakedSkillFlagsGo withExtra
    )) "renderBakedSkillFlagsGo did not render the injected row's flag declaration";
    assert assertMsg (hasInfix "testSkillSkillBaked" (
      renderers.renderBakedSkillEnvAssignGo withExtra
    )) "renderBakedSkillEnvAssignGo did not render the injected row's Env assignment";
    assert assertMsg (hasInfix "TestSkillSkillBaked" (
      renderers.renderBakedSkillFieldsGo withExtra
    )) "renderBakedSkillFieldsGo did not render the injected row's struct field";
    assert assertMsg (hasInfix "TEST_SKILL_BAKED" (
      renderers.renderBakedSkillGatesGo withExtra
    )) "renderBakedSkillGatesGo did not render the injected row's gate assignment";
    pkgs.runCommand "baked-skills-add-row-guard" { } "touch $out";
}
