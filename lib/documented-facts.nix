# The documentedFact registry (issue #2948, spec #2921): one row per generated
# marker-delimited span whose content must stay byte-for-byte in sync with a Nix
# source of truth. One shared list so each span's marker literals, host file, and
# renderer call are typed once: schema-drift.nix derives a drift check per row,
# baked-skills.nix looks rows up by name, and regen.nix splices each span in place.

# A row's name is its drift-check derivation name and must stay stable, because CI
# granularity and outside references key off it. beginMarker carries a trailing
# "\n" and endMarker does not, matching how assertMarkedBlockOk splits the file.

# postSplice = "gofmt" marks a span inside Go source that `gofmt -w` reformats past
# the span itself, so both checker and regenerator must run `gofmt -w` over the
# whole file before comparing or writing, or a string diff reports false drift.

# Each renderer here is the one `nix run .#regen` calls, so the check and the
# regenerator cannot drift apart (issue #402).
{ lib }:
let
  renderers = import ./renderers.nix;
  defaultModelFixture = import ./default-model-fixture.nix;
  schema = import ./env-schema.nix;
  structuralTemplateExamples = import ./structural-template-examples.nix { inherit lib; };
  promptContract = import ./prompt-contract.nix;
  bakedSkills = import ./baked-skills.nix;
  structuralPaths = import ./structural-paths.nix;
  byNamePaths = import ./byname-paths.nix;
  buildConstants = import ./build-constants.nix;
  rosterDefaults = (import ./roster-schema-defaults.nix { inherit lib; }).rosterDefaults;
  rosterNames = map (e: e.name) ((import ./roster.nix { inherit lib; }).defaultRoster { });
  inherit (import ./documented-fact-shape.nix) assertMarkerShape;
in
map assertMarkerShape [
  {
    name = "default-models-doc";
    docPath = "docs/reference.md";
    blockName = "DEFAULT MODELS";
    sourceDesc = "lib/default-model-fixture.nix";
    beginMarker = "<!-- BEGIN GENERATED DEFAULT MODELS -- nix run .#regen -- DO NOT EDIT -->\n";
    endMarker = "<!-- END GENERATED DEFAULT MODELS -->";
    generated = renderers.renderDefaultModelsDoc defaultModelFixture;
  }
  {
    name = "roster-doc-flake-path";
    docPath = "docs/reference.md";
    blockName = "ROSTER FLAKE PATH";
    sourceDesc = "lib/structural-paths.nix's roster entry";
    beginMarker = "<!-- BEGIN GENERATED ROSTER FLAKE PATH -- nix run .#regen -- DO NOT EDIT -->\n";
    endMarker = "<!-- END GENERATED ROSTER FLAKE PATH -->";
    generated = renderers.renderRosterFlakePathDoc structuralPaths.roster;
  }
  {
    name = "roster-doc-efforts";
    docPath = "docs/reference.md";
    blockName = "ROSTER EFFORTS";
    sourceDesc = "lib/roster-schema-defaults.nix's rosterDefaults";
    beginMarker = "<!-- BEGIN GENERATED ROSTER EFFORTS -- nix run .#regen -- DO NOT EDIT -->\n";
    endMarker = "<!-- END GENERATED ROSTER EFFORTS -->";
    generated = renderers.renderRosterEffortsDoc rosterDefaults rosterNames;
  }
  {
    name = "dogfood-doc-filer-pin-guard";
    docPath = "docs/reference.md";
    blockName = "DOGFOOD FILER PIN";
    # The "-guard" suffix is historical and stays for name stability: the separate
    # guard derivation it named is gone, and this row is now the drift check.
    sourceDesc = "lib/default-model-fixture.nix's dogfoodPins.filer";
    beginMarker = "<!-- BEGIN GENERATED DOGFOOD FILER PIN -- nix run .#regen -- DO NOT EDIT -->\n";
    endMarker = "<!-- END GENERATED DOGFOOD FILER PIN -->";
    generated = renderers.renderDogfoodFilerPinDoc defaultModelFixture;
  }
  {
    name = "dogfood-doc-models-guard";
    docPath = "docs/reference.md";
    blockName = "DOGFOOD MODELS";
    # The "-guard" suffix is historical and stays for name stability: the separate
    # guard derivation it named is gone, and this row is now the drift check.
    sourceDesc = "lib/default-model-fixture.nix's schemaDefaults";
    beginMarker = "<!-- BEGIN GENERATED DOGFOOD MODELS -- nix run .#regen -- DO NOT EDIT -->\n";
    endMarker = "<!-- END GENERATED DOGFOOD MODELS -->";
    generated = renderers.renderDogfoodModelsDoc defaultModelFixture;
  }
  {
    name = "option-surface-doc-paths";
    docPath = "docs/reference.md";
    blockName = "OPTION SURFACE TABLE";
    # The "-paths" name is narrower than the row, which now owns the whole table
    # (issue #2950), but it stays for name stability.
    sourceDesc = "lib/structural-paths.nix, lib/byname-paths.nix, and lib/build-constants.nix";
    beginMarker = "<!-- BEGIN GENERATED OPTION SURFACE TABLE -- nix run .#regen -- DO NOT EDIT -->\n";
    endMarker = "<!-- END GENERATED OPTION SURFACE TABLE -->";
    generated = renderers.renderOptionSurfaceTableDoc {
      inherit structuralPaths byNamePaths;
      nixBuilderImage = buildConstants.nixBuilderImage;
    };
  }
  {
    name = "settings-example-models-doc";
    docPath = "docs/reference.md";
    blockName = "SETTINGS EXAMPLE MODELS";
    sourceDesc = "lib/default-model-fixture.nix";
    beginMarker = "# BEGIN GENERATED SETTINGS EXAMPLE MODELS -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "# END GENERATED SETTINGS EXAMPLE MODELS";
    generated = renderers.renderSettingsExampleModelsDoc defaultModelFixture schema;
  }
  {
    name = "settings-example-labels-doc";
    docPath = "docs/reference.md";
    blockName = "SETTINGS EXAMPLE LABELS";
    sourceDesc = "lib/env-schema.nix";
    beginMarker = "# BEGIN GENERATED SETTINGS EXAMPLE LABELS -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "# END GENERATED SETTINGS EXAMPLE LABELS";
    generated = renderers.renderSettingsExampleLabelsDoc schema;
  }
  {
    name = "settings-example-config-doc";
    docPath = "docs/reference.md";
    blockName = "SETTINGS EXAMPLE CONFIG";
    sourceDesc = "lib/env-schema.nix";
    beginMarker = "# BEGIN GENERATED SETTINGS EXAMPLE CONFIG -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "# END GENERATED SETTINGS EXAMPLE CONFIG";
    generated = renderers.renderSettingsExampleConfigDoc schema;
  }
  {
    name = "template-settings-block";
    docPath = "templates/default/flake.nix";
    blockName = "SETTINGS EXAMPLE";
    sourceDesc = "lib/env-schema.nix";
    beginMarker = "            # BEGIN GENERATED SETTINGS EXAMPLE -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "            # END GENERATED SETTINGS EXAMPLE";
    generated = renderers.renderTemplateSettingsBlock schema structuralTemplateExamples;
  }
  {
    name = "outcome-status-words";
    docPath = "agent/entrypoint.sh";
    blockName = "OUTCOME STATUS WORDS";
    sourceDesc = "lib/prompt-contract.nix";
    beginMarker = "# BEGIN GENERATED OUTCOME STATUS WORDS -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "# END GENERATED OUTCOME STATUS WORDS";
    generated =
      "export RESEARCH_STATUS_ENUM=\""
      + (renderers.renderOutcomeStatusPipe (
        builtins.filter (s: s != "blocked") (promptContract.outcomeStatusesFor "research")
      ))
      + "\"\n";
  }
  {
    name = "baked-skills-probes-gen";
    docPath = "agent/entrypoint.sh";
    blockName = "SKILL-BAKED PROBES";
    sourceDesc = "lib/baked-skills.nix";
    beginMarker = "  # BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "  # END GENERATED SKILL-BAKED PROBES";
    generated = renderers.renderBakedSkillProbesShell bakedSkills;
  }
  {
    name = "baked-skills-flags-gen";
    docPath = "cmd/launcher/driver-exec/assembleprompt_cmd.go";
    blockName = "SKILL-BAKED FLAGS";
    sourceDesc = "lib/baked-skills.nix";
    beginMarker = "\t// BEGIN GENERATED SKILL-BAKED FLAGS -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "\t// END GENERATED SKILL-BAKED FLAGS";
    generated = renderers.renderBakedSkillFlagsGo bakedSkills;
  }
  {
    name = "baked-skills-env-assign-gen";
    docPath = "cmd/launcher/driver-exec/assembleprompt_cmd.go";
    blockName = "SKILL-BAKED ENV";
    sourceDesc = "lib/baked-skills.nix";
    beginMarker = "\t// BEGIN GENERATED SKILL-BAKED ENV -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "\t// END GENERATED SKILL-BAKED ENV";
    generated = renderers.renderBakedSkillEnvAssignGo bakedSkills;
    postSplice = "gofmt";
  }
  {
    name = "baked-skills-fields-gen";
    docPath = "cmd/launcher/internal/promptassembly/env.go";
    blockName = "SKILL-BAKED FIELDS";
    sourceDesc = "lib/baked-skills.nix";
    beginMarker = "\t// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "\t// END GENERATED SKILL-BAKED FIELDS";
    generated = renderers.renderBakedSkillFieldsGo bakedSkills;
    postSplice = "gofmt";
  }
  {
    name = "baked-skills-gates-gen";
    docPath = "cmd/launcher/internal/promptassembly/gates.go";
    blockName = "SKILL-BAKED GATES";
    sourceDesc = "lib/baked-skills.nix";
    beginMarker = "\t// BEGIN GENERATED SKILL-BAKED GATES -- nix run .#regen -- DO NOT EDIT\n";
    endMarker = "\t// END GENERATED SKILL-BAKED GATES";
    generated = renderers.renderBakedSkillGatesGo bakedSkills;
    postSplice = "gofmt";
  }
]
