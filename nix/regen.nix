# One-shot regenerator for every schema-generated artifact (issue #402):
# `nix run .#regen` renders templates/default/harness.env.example,
# cmd/launcher/flagtable_gen.go, docs/flake-options.md,
# cmd/launcher/internal/driver/drivernames_gen.go,
# cmd/launcher/internal/agentpaths/agentpaths_gen.go,
# cmd/launcher/quickstart/quickstart_runtime_gen.go,
# cmd/launcher/quickstart/quickstart_paths_gen.go,
# cmd/launcher/subcommands_gen.go,
# cmd/launcher/internal/outcome/status_gen.go,
# cmd/launcher/internal/backend/registry_gen.go,
# cmd/launcher/internal/doctor/labelmeta_gen.go (lib/labels.nix, issue
# #2528), tests/box_env_gen.bash, tests/default_models_gen.bash,
# cmd/launcher/defaultmodels_gen_test.go, the generated section of
# templates/default/flake.nix's commented-out `settings` example, the
# generated section of docs/reference.md's Default models table, the
# generated `models` sub-block of docs/reference.md's `settings = { ... }`
# example, the generated `issueDiscovery`/`lifecycleLabels` and `branches`/
# `concurrency` sub-blocks of that same example (issue #2537),
# agent/entrypoint.sh's generated skill-baked probe block, and the generated
# skill-baked flags/Env-assignments/fields/gates spans of
# cmd/launcher/driver-exec/assembleprompt_cmd.go,
# cmd/launcher/internal/promptassembly/env.go, and
# cmd/launcher/internal/promptassembly/gates.go (lib/baked-skills.nix, issue
# #2532), from their respective Nix sources, and writes them into the
# working tree. Calls the
# exact same renderers as the nix/checks.nix drift guards (lib/renderers.nix),
# so resolving a source-edit conflict is: fix the Nix source, run this, commit.
#
# This is spindrift's own dev workflow, not consumer surface — it is not
# wired into env-schema.nix or the generated flake-options reference.
#
# One schema-derived artifact is deliberately out of scope: the man page
# (lib/mkHarness.nix manpageRoff) is rebuilt fresh from the schema on every
# `nix flake check` run; there is no committed copy to drift, so there is
# nothing to regenerate.
#
# templates/default/flake.nix's commented-out `settings` example used to be
# hand-curated (its knob order didn't follow schema declaration order) with
# its own drift check flagging missing sections/knobs to hand-add. As of
# issue #520 it is fully regen-owned and exhaustive (every flakeOption knob,
# with its doc string) between its BEGIN/END GENERATED SETTINGS EXAMPLE
# markers — a new knob needs no hand-edit here, only in lib/env-schema.nix
# and this regen run.
{ pkgs }:
let
  renderers = import ../lib/renderers.nix;
  schema = import ../lib/env-schema.nix;
  envExample = renderers.renderHarnessEnvExample schema;
  flagTable = renderers.renderFlagTableGo schema;
  schemaConfigFile = renderers.renderSchemaConfigGo schema;
  flakeOptionsDoc = renderers.renderFlakeOptionsDoc schema;
  boxEnvFixture = renderers.renderSetBoxEnvFixture schema;
  templateSettingsBlock = renderers.renderTemplateSettingsBlock schema;
  driverRegistry = import ../lib/drivers/default.nix { inherit (pkgs) lib; };
  driverNamesFile = renderers.renderDriverNamesGo driverRegistry.entries;
  agentPaths = import ../lib/agent-paths.nix;
  agentPathsFile = renderers.renderAgentPathsGo agentPaths;
  runtimeValues = import ../lib/runtime-values.nix;
  quickstartRuntimeFile = renderers.renderQuickstartRuntimeGo runtimeValues;
  quickstartPathTable = import ../lib/quickstart-path-table.nix;
  quickstartPathsFile = renderers.renderQuickstartPathsGo quickstartPathTable;
  subcommands = import ../lib/subcommands.nix;
  subcommandsFile = renderers.renderSubcommandsGo subcommands;
  promptContract = import ../lib/prompt-contract.nix;
  outcomeStatusGoFile = renderers.renderOutcomeStatusGo promptContract.outcomeStatusSets;
  researchStatusPipe = renderers.renderOutcomeStatusPipe (
    builtins.filter (s: s != "blocked") (promptContract.outcomeStatusesFor "research")
  );
  backendRegistry = import ../lib/backends/default.nix;
  backendRegistryFile = renderers.renderBackendRegistryGo backendRegistry;
  labelRegistry = import ../lib/labels.nix;
  labelRegistryFile = renderers.renderLabelRegistryGo labelRegistry;
  defaultModelFixture = import ../lib/default-model-fixture.nix;
  defaultModelFixtureBash = renderers.renderDefaultModelFixtureBash defaultModelFixture;
  defaultModelFixtureGo = renderers.renderDefaultModelFixtureGo defaultModelFixture;
  defaultModelsDoc = renderers.renderDefaultModelsDoc defaultModelFixture;
  settingsExampleModelsDoc = renderers.renderSettingsExampleModelsDoc defaultModelFixture;
  settingsExampleLabelsDoc = renderers.renderSettingsExampleLabelsDoc schema;
  settingsExampleConfigDoc = renderers.renderSettingsExampleConfigDoc schema;
  bakedSkills = import ../lib/baked-skills.nix;
  bakedSkillProbesShell = renderers.renderBakedSkillProbesShell bakedSkills;
  bakedSkillFlagsGo = renderers.renderBakedSkillFlagsGo bakedSkills;
  bakedSkillEnvAssignGo = renderers.renderBakedSkillEnvAssignGo bakedSkills;
  bakedSkillFieldsGo = renderers.renderBakedSkillFieldsGo bakedSkills;
  bakedSkillGatesGo = renderers.renderBakedSkillGatesGo bakedSkills;
  inherit (pkgs.lib) escapeShellArg;
in
pkgs.writeShellApplication {
  name = "regen";
  runtimeInputs = [
    pkgs.git
    pkgs.gawk
    pkgs.go
  ];
  text = ''
    root="$(git rev-parse --show-toplevel)"
    if [ ! -f "$root/lib/env-schema.nix" ]; then
      echo "regen: $root doesn't look like the spindrift repo (no lib/env-schema.nix); refusing to write" >&2
      exit 1
    fi

    write() {
      printf '%s' "$2" > "$root/$1"
      echo "regenerated $1"
    }

    # Replaces the lines strictly between (and preserving) a literal
    # begin/end marker line pair with $4, for a generated section embedded
    # in an otherwise hand-written file.
    write_between() {
      local file="$root/$1" begin="$2" end="$3" content="$4"
      awk -v begin="$begin" -v end="$end" -v content="$content" '
        $0 == begin { print; printf "%s", content; skip=1; next }
        $0 == end { skip=0 }
        skip { next }
        { print }
      ' "$file" > "$file.regen-tmp" && mv "$file.regen-tmp" "$file"
      echo "regenerated $1 (generated section)"
    }

    write templates/default/harness.env.example ${escapeShellArg envExample}
    write cmd/launcher/flagtable_gen.go ${escapeShellArg flagTable}
    write cmd/launcher/schemaconfig_gen.go ${escapeShellArg schemaConfigFile}
    gofmt -w "$root/cmd/launcher/schemaconfig_gen.go"
    write docs/flake-options.md ${escapeShellArg flakeOptionsDoc}
    write cmd/launcher/internal/driver/drivernames_gen.go ${escapeShellArg driverNamesFile}
    write cmd/launcher/internal/agentpaths/agentpaths_gen.go ${escapeShellArg agentPathsFile}
    write cmd/launcher/quickstart/quickstart_runtime_gen.go ${escapeShellArg quickstartRuntimeFile}
    write cmd/launcher/quickstart/quickstart_paths_gen.go ${escapeShellArg quickstartPathsFile}
    write cmd/launcher/subcommands_gen.go ${escapeShellArg subcommandsFile}
    write cmd/launcher/internal/outcome/status_gen.go ${escapeShellArg outcomeStatusGoFile}
    gofmt -w "$root/cmd/launcher/internal/outcome/status_gen.go"
    write cmd/launcher/internal/backend/registry_gen.go ${escapeShellArg backendRegistryFile}
    gofmt -w "$root/cmd/launcher/internal/backend/registry_gen.go"
    write cmd/launcher/internal/doctor/labelmeta_gen.go ${escapeShellArg labelRegistryFile}
    gofmt -w "$root/cmd/launcher/internal/doctor/labelmeta_gen.go"
    write tests/box_env_gen.bash ${escapeShellArg boxEnvFixture}
    write tests/default_models_gen.bash ${escapeShellArg defaultModelFixtureBash}
    write cmd/launcher/defaultmodels_gen_test.go ${escapeShellArg defaultModelFixtureGo}
    gofmt -w "$root/cmd/launcher/defaultmodels_gen_test.go"
    write_between templates/default/flake.nix \
      ${escapeShellArg "            # BEGIN GENERATED SETTINGS EXAMPLE -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "            # END GENERATED SETTINGS EXAMPLE"} \
      ${escapeShellArg templateSettingsBlock}
    write_between agent/entrypoint.sh \
      ${escapeShellArg "# BEGIN GENERATED OUTCOME STATUS WORDS -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "# END GENERATED OUTCOME STATUS WORDS"} \
      ${escapeShellArg (
        "# shellcheck disable=SC2034 # consumed by _subst's envsubst allowlist, wired in a later slice (issue #2504)\n"
        + "RESEARCH_STATUS_ENUM=\""
        + researchStatusPipe
        + "\"\n"
      )}
    write_between docs/reference.md \
      ${escapeShellArg "<!-- BEGIN GENERATED DEFAULT MODELS -- nix run .#regen -- DO NOT EDIT -->"} \
      ${escapeShellArg "<!-- END GENERATED DEFAULT MODELS -->"} \
      ${escapeShellArg defaultModelsDoc}
    write_between docs/reference.md \
      ${escapeShellArg "  # BEGIN GENERATED SETTINGS EXAMPLE MODELS -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "  # END GENERATED SETTINGS EXAMPLE MODELS"} \
      ${escapeShellArg settingsExampleModelsDoc}
    write_between docs/reference.md \
      ${escapeShellArg "  # BEGIN GENERATED SETTINGS EXAMPLE LABELS -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "  # END GENERATED SETTINGS EXAMPLE LABELS"} \
      ${escapeShellArg settingsExampleLabelsDoc}
    write_between docs/reference.md \
      ${escapeShellArg "  # BEGIN GENERATED SETTINGS EXAMPLE CONFIG -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "  # END GENERATED SETTINGS EXAMPLE CONFIG"} \
      ${escapeShellArg settingsExampleConfigDoc}
    write_between agent/entrypoint.sh \
      ${escapeShellArg "  # BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "  # END GENERATED SKILL-BAKED PROBES"} \
      ${escapeShellArg bakedSkillProbesShell}
    write_between cmd/launcher/driver-exec/assembleprompt_cmd.go \
      ${escapeShellArg "\t// BEGIN GENERATED SKILL-BAKED FLAGS -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "\t// END GENERATED SKILL-BAKED FLAGS"} \
      ${escapeShellArg bakedSkillFlagsGo}
    write_between cmd/launcher/driver-exec/assembleprompt_cmd.go \
      ${escapeShellArg "\t\t// BEGIN GENERATED SKILL-BAKED ENV -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "\t\t// END GENERATED SKILL-BAKED ENV"} \
      ${escapeShellArg bakedSkillEnvAssignGo}
    gofmt -w "$root/cmd/launcher/driver-exec/assembleprompt_cmd.go"
    write_between cmd/launcher/internal/promptassembly/env.go \
      ${escapeShellArg "\t// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "\t// END GENERATED SKILL-BAKED FIELDS"} \
      ${escapeShellArg bakedSkillFieldsGo}
    gofmt -w "$root/cmd/launcher/internal/promptassembly/env.go"
    write_between cmd/launcher/internal/promptassembly/gates.go \
      ${escapeShellArg "\t// BEGIN GENERATED SKILL-BAKED GATES -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "\t// END GENERATED SKILL-BAKED GATES"} \
      ${escapeShellArg bakedSkillGatesGo}
    gofmt -w "$root/cmd/launcher/internal/promptassembly/gates.go"
  '';
}
