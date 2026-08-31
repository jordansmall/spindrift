# One-shot regenerator for every schema-generated artifact (issue #402):
# `nix run .#regen` renders templates/default/harness.env.example,
# cmd/launcher/flagtable_gen.go, docs/flake-options.md,
# cmd/launcher/internal/driver/drivernames_gen.go,
# cmd/launcher/internal/agentpaths/agentpaths_gen.go,
# cmd/launcher/internal/runner/runtimevalues_gen.go,
# cmd/launcher/quickstart/quickstart_paths_gen.go,
# cmd/launcher/subcommands_gen.go,
# cmd/launcher/internal/outcome/status_gen.go,
# cmd/launcher/internal/outcome/markerchannels_gen.go,
# cmd/launcher/internal/backend/registry_gen.go,
# cmd/launcher/internal/doctor/labelmeta_gen.go (lib/labels.nix, issue
# #2528), tests/box_env_gen.bash, tests/default_models_gen.bash,
# cmd/launcher/defaultmodels_gen_test.go, the generated section of
# templates/default/flake.nix's commented-out `settings` example, every
# documented-fact row's generated block in docs/reference.md (the Default
# models table and the `models`/`issueDiscovery`+`lifecycleLabels`/
# `branches`+`concurrency` sub-blocks of its `settings = { ... }` example;
# lib/documented-facts.nix, issue #2948),
# MIGRATING.md's generated legacy settings alias -> domain path table (issue
# #2558), agent/entrypoint.sh's generated skill-baked probe block, and the
# generated skill-baked flags/Env-assignments/fields/gates spans of
# cmd/launcher/driver-exec/assembleprompt_cmd.go,
# cmd/launcher/internal/promptassembly/env.go, and
# cmd/launcher/internal/promptassembly/gates.go (lib/baked-skills.nix, issue
# #2532), and cmd/launcher/internal/promptassembly/boxenv_gen.go
# (lib/promptassembly-boxenv.nix, issue #2979), from their respective Nix
# sources, and writes them into the working tree. Calls the
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
  structuralOptionsDoc = import ../lib/structural-options-doc.nix;
  structuralPaths = import ../lib/structural-paths.nix;
  envExample = renderers.renderHarnessEnvExample schema;
  flagTable = renderers.renderFlagTableGo schema;
  schemaConfigFile = renderers.renderSchemaConfigGo schema;
  flakeOptionsDoc = renderers.renderFlakeOptionsDocFull schema structuralOptionsDoc structuralPaths;
  boxEnvFixture = renderers.renderSetBoxEnvFixture schema;
  driverRegistry = import ../lib/drivers/default.nix { inherit (pkgs) lib; };
  driverNamesFile = renderers.renderDriverNamesGo driverRegistry.entries;
  agentPaths = import ../lib/agent-paths.nix;
  agentPathsFile = renderers.renderAgentPathsGo agentPaths;
  runtimeValues = import ../lib/runtime-values.nix;
  runtimeValuesFile = renderers.renderRuntimeValuesGo runtimeValues;
  quickstartPathTable = import ../lib/quickstart-path-table.nix;
  quickstartPathsFile = renderers.renderQuickstartPathsGo quickstartPathTable;
  subcommands = import ../lib/subcommands.nix;
  subcommandsFile = renderers.renderSubcommandsGo subcommands;
  promptContract = import ../lib/prompt-contract.nix;
  outcomeStatusGoFile = renderers.renderOutcomeStatusGo promptContract.outcomeStatusSets;
  markerChannelsGoFile = renderers.renderMarkerChannelsGo promptContract.markerChannels;
  backendRegistry = import ../lib/backends/default.nix;
  backendRegistryFile = renderers.renderBackendRegistryGo backendRegistry;
  labelRegistry = import ../lib/labels.nix;
  labelRegistryFile = renderers.renderLabelRegistryGo labelRegistry;
  defaultModelFixture = import ../lib/default-model-fixture.nix;
  defaultModelFixtureBash = renderers.renderDefaultModelFixtureBash defaultModelFixture;
  defaultModelFixtureGo = renderers.renderDefaultModelFixtureGo defaultModelFixture;
  legacySettingsSection = import ../lib/legacy-settings-section.nix;
  legacySettingsMappingDoc = renderers.renderLegacySettingsMappingDoc legacySettingsSection schema;
  promptAssemblyBoxEnv = import ../lib/promptassembly-boxenv.nix;
  promptAssemblyBoxEnvFile = renderers.renderPromptAssemblyBoxEnvGo promptAssemblyBoxEnv;
  documentedFacts = import ../lib/documented-facts.nix { inherit (pkgs) lib; };
  # The shared marker-splice implementation (issue #2949) backing
  # write_between below -- also imported by nix/checks/schema-drift.nix and
  # nix/checks/baked-skills.nix, so this file no longer hand-mirrors its own
  # copy of the awk-based marker-splitting logic.
  documentedFactChecker = import ../lib/documented-fact-checker.nix { inherit pkgs; };
  inherit (documentedFactChecker) spliceShellFn;
  inherit (pkgs.lib) escapeShellArg concatStrings removeSuffix;
  # Named so nix/checks/schema-drift.nix's regen-postsplice-dispatch-guard
  # can call this exact function against synthetic rows (issue #2949 review
  # finding) instead of a hand-mirrored reimplementation -- a typo in a
  # row's postSplice field (wrong case, misspelling) would otherwise
  # silently take the no-gofmt branch with nothing catching it.
  regenRowScript =
    row:
    ''
      write_between ${escapeShellArg row.docPath} \
        ${escapeShellArg (removeSuffix "\n" row.beginMarker)} \
        ${escapeShellArg row.endMarker} \
        ${escapeShellArg row.generated}
    ''
    + (
      if (row.postSplice or null) == "gofmt" then
        ''
          gofmt -w "$root/"${escapeShellArg row.docPath}
        ''
      else
        ""
    );
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

    ${spliceShellFn}

    # Replaces the lines strictly between (and preserving) a literal
    # begin/end marker line pair with $4, for a generated section embedded
    # in an otherwise hand-written file -- delegates to the shared `splice`
    # function above instead of its own awk pipeline.
    write_between() {
      local file="$root/$1" begin="$2" end="$3" content="$4"
      local content_file
      content_file="$(mktemp)"
      # Under `set -e`, a failing `splice` (e.g. a missing marker) aborts the
      # whole script before reaching the unconditional `rm -f` further down,
      # leaking the mktemp'd file. A RETURN trap doesn't help here -- `set -e`
      # kills the script directly on the failing command without ever
      # "returning" from this function. An EXIT trap does fire, but by the
      # time it runs, this function's `local content_file` has already gone
      # out of scope, so a trap body that references "$content_file" (single
      # quotes, expanded when the trap fires) sees it empty. Double-quoting
      # here expands $content_file immediately, baking the real path into the
      # trap command literally, so it survives the function's local scope
      # unwinding. Verified: a RETURN trap and a single-quoted EXIT trap were
      # both tried and both left the temp file behind on a forced `splice`
      # failure; only this form actually removes it.
      trap "rm -f '$content_file'" EXIT
      printf '%s' "$content" > "$content_file"
      splice "$file" "$begin" "$end" "$content_file" "$file.regen-tmp"
      mv "$file.regen-tmp" "$file"
      rm -f "$content_file"
      trap - EXIT
      echo "regenerated $1 (generated section)"
    }

    write templates/default/harness.env.example ${escapeShellArg envExample}
    write cmd/launcher/flagtable_gen.go ${escapeShellArg flagTable}
    write cmd/launcher/schemaconfig_gen.go ${escapeShellArg schemaConfigFile}
    gofmt -w "$root/cmd/launcher/schemaconfig_gen.go"
    write docs/flake-options.md ${escapeShellArg flakeOptionsDoc}
    write cmd/launcher/internal/driver/drivernames_gen.go ${escapeShellArg driverNamesFile}
    write cmd/launcher/internal/agentpaths/agentpaths_gen.go ${escapeShellArg agentPathsFile}
    write cmd/launcher/internal/runner/runtimevalues_gen.go ${escapeShellArg runtimeValuesFile}
    write cmd/launcher/quickstart/quickstart_paths_gen.go ${escapeShellArg quickstartPathsFile}
    write cmd/launcher/subcommands_gen.go ${escapeShellArg subcommandsFile}
    write cmd/launcher/internal/outcome/status_gen.go ${escapeShellArg outcomeStatusGoFile}
    gofmt -w "$root/cmd/launcher/internal/outcome/status_gen.go"
    write cmd/launcher/internal/outcome/markerchannels_gen.go ${escapeShellArg markerChannelsGoFile}
    gofmt -w "$root/cmd/launcher/internal/outcome/markerchannels_gen.go"
    write cmd/launcher/internal/backend/registry_gen.go ${escapeShellArg backendRegistryFile}
    gofmt -w "$root/cmd/launcher/internal/backend/registry_gen.go"
    write cmd/launcher/internal/doctor/labelmeta_gen.go ${escapeShellArg labelRegistryFile}
    gofmt -w "$root/cmd/launcher/internal/doctor/labelmeta_gen.go"
    write tests/box_env_gen.bash ${escapeShellArg boxEnvFixture}
    write tests/default_models_gen.bash ${escapeShellArg defaultModelFixtureBash}
    write cmd/launcher/defaultmodels_gen_test.go ${escapeShellArg defaultModelFixtureGo}
    gofmt -w "$root/cmd/launcher/defaultmodels_gen_test.go"
    ${concatStrings (map regenRowScript documentedFacts)}
    write_between MIGRATING.md \
      ${escapeShellArg "<!-- BEGIN GENERATED LEGACY SETTINGS MAPPING -- nix run .#regen -- DO NOT EDIT -->"} \
      ${escapeShellArg "<!-- END GENERATED LEGACY SETTINGS MAPPING -->"} \
      ${escapeShellArg legacySettingsMappingDoc}
    write cmd/launcher/internal/promptassembly/boxenv_gen.go ${escapeShellArg promptAssemblyBoxEnvFile}
    gofmt -w "$root/cmd/launcher/internal/promptassembly/boxenv_gen.go"
  '';
}
# `//` only adds an attribute here -- it doesn't touch the derivation's
# outPath/build behavior, so flake.nix's `${import ./nix/regen.nix { ... }}`
# string coercion (which resolves via outPath) is unaffected. This exposes
# regenRowScript so nix/checks/schema-drift.nix's
# regen-postsplice-dispatch-guard can call the exact function `nix run
# .#regen` uses, not a hand-mirrored reimplementation (issue #2949 review
# finding).
// {
  inherit regenRowScript;
}
