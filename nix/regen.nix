# One-shot regenerator (issue #402): `nix run .#regen` renders every
# schema-generated artifact from its Nix source and writes it into the working
# tree. It calls the same renderers (lib/renderers.nix) as the nix/checks.nix
# drift guards, so resolving a source-edit conflict is: fix the Nix source,
# run this, commit.

# This is spindrift's own dev workflow, not a consumer-facing option: it is
# not wired into env-schema.nix or the generated flake-options reference.

# The man page (lib/mkHarness.nix manpageRoff) is deliberately out of scope.
# Every `nix flake check` rebuilds it from the schema, so no committed copy
# exists to drift.

# templates/default/flake.nix's commented-out `settings` example is regen-owned
# and exhaustive between its BEGIN/END GENERATED SETTINGS EXAMPLE markers
# (issue #520), so a new knob needs an edit only in lib/env-schema.nix plus a
# regen run.
{ pkgs }:
let
  renderers = import ../lib/renderers.nix;
  schema = import ../lib/env-schema.nix;
  structuralOptionsDoc = import ../lib/structural-options-doc.nix;
  structuralPaths = import ../lib/structural-paths.nix;
  byNamePaths = import ../lib/byname-paths.nix;
  envExample = renderers.renderHarnessEnvExample schema;
  flagTable = renderers.renderFlagTableGo schema;
  schemaConfigFile = renderers.renderSchemaConfigGo schema;
  flakeOptionsDoc =
    renderers.renderFlakeOptionsDocFull schema structuralOptionsDoc structuralPaths
      byNamePaths;
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
  # write_between below builds on this shared marker-splice implementation
  # (issue #2949). nix/checks/schema-drift.nix and nix/checks/baked-skills.nix
  # import the same one, so no file hand-mirrors the marker-splitting logic.
  documentedFactChecker = import ../lib/documented-fact-checker.nix { inherit pkgs; };
  inherit (documentedFactChecker) spliceShellFn;
  inherit (pkgs.lib) escapeShellArg concatStrings removeSuffix;
  # write_between lives in a named shell-function string so that
  # nix/checks/schema-drift.nix's regen-write-between-preserves-mode check
  # (issue #3128) exercises the real function against a fixture, not a copy
  # that could drift from what `nix run .#regen` runs.
  writeBetweenShellFn = ''
    # Replaces the lines strictly between (and preserving) a literal
    # begin/end marker line pair with $4, for a generated section embedded
    # in an otherwise hand-written file -- delegates to the shared `splice`
    # function above instead of its own awk pipeline.
    write_between() {
      local file="$root/$1" begin="$2" end="$3" content="$4"
      # Under `set -e`, a failing `splice` (e.g. a missing marker) aborts the
      # whole script before reaching the unconditional `rm -f` further down,
      # leaking the mktemp'd file. An EXIT trap is what catches that, but its
      # body only sees a variable that is still in scope when the trap fires
      # -- a `local` would already have unwound with the function, so
      # content_file is deliberately script-scope (no `local`) here, letting
      # a single-quoted trap body ('$content_file' expanded at fire time, not
      # at trap-registration time) resolve it correctly.
      content_file="$(mktemp)"
      trap 'rm -f "$content_file"' EXIT
      printf '%s' "$content" > "$content_file"
      splice "$file" "$begin" "$end" "$content_file" "$file.regen-tmp"
      # `splice` writes $file.regen-tmp fresh under the default umask, so a
      # plain `mv` onto $file would silently drop an executable bit (issue
      # #3128: agent/entrypoint.sh is 100755, regen kept turning it 100644 on
      # every no-op run). chmod --reference before the mv instead of
      # rewriting $file in place, so the replace stays atomic -- a mid-write
      # failure can never leave $file truncated.
      chmod --reference="$file" "$file.regen-tmp"
      mv "$file.regen-tmp" "$file"
      rm -f "$content_file"
      trap - EXIT
      echo "regenerated $1 (generated section)"
    }
  '';
  # escapeShellArg puts each rendered artifact in a single-quoted shell word,
  # where a `$` or backtick is a literal byte that must not expand, so
  # ShellCheck's SC2016 is a false positive at every call site below. These
  # helpers emit the directive per call so ShellCheck still catches a genuine
  # SC2016 elsewhere in this script, and a new call site cannot omit it.
  disableSC2016 = call: ''
    # shellcheck disable=SC2016
    ${call}'';
  # `path` is a ready-made shell word, not a bare path: callers that already
  # hold an escaped path pass it through unchanged.
  writeGenerated = path: content: disableSC2016 "write ${path} ${escapeShellArg content}";
  writeBetweenGenerated =
    path: begin: end: content:
    disableSC2016 ''
      write_between ${path} \
        ${escapeShellArg begin} \
        ${escapeShellArg end} \
        ${escapeShellArg content}'';
  # regenRowScript is named so nix/checks/schema-drift.nix's
  # regen-postsplice-dispatch-guard can call this exact function against
  # synthetic rows (issue #2949 review finding). A typo in a row's postSplice
  # field would otherwise silently take the no-gofmt branch uncaught.
  regenRowScript =
    row:
    writeBetweenGenerated (escapeShellArg row.docPath) (removeSuffix "\n" row.beginMarker) row.endMarker
      row.generated
    + "\n"
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
    # write_between's `chmod --reference` (issue #3128) is a GNU extension that
    # BSD/macOS chmod lacks, and writeShellApplication supplies no coreutils of
    # its own. Without this pin, `nix run .#regen` on darwin (apps.regen is not
    # isLinux-gated) resolves /bin/chmod and dies on the unrecognised flag.
    pkgs.coreutils
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

    ${writeBetweenShellFn}

    ${writeGenerated "templates/default/harness.env.example" envExample}
    ${writeGenerated "cmd/launcher/flagtable_gen.go" flagTable}
    ${writeGenerated "cmd/launcher/schemaconfig_gen.go" schemaConfigFile}
    gofmt -w "$root/cmd/launcher/schemaconfig_gen.go"
    ${writeGenerated "docs/flake-options.md" flakeOptionsDoc}
    ${writeGenerated "cmd/launcher/internal/driver/drivernames_gen.go" driverNamesFile}
    ${writeGenerated "cmd/launcher/internal/agentpaths/agentpaths_gen.go" agentPathsFile}
    ${writeGenerated "cmd/launcher/internal/runner/runtimevalues_gen.go" runtimeValuesFile}
    ${writeGenerated "cmd/launcher/quickstart/quickstart_paths_gen.go" quickstartPathsFile}
    ${writeGenerated "cmd/launcher/subcommands_gen.go" subcommandsFile}
    ${writeGenerated "cmd/launcher/internal/outcome/status_gen.go" outcomeStatusGoFile}
    gofmt -w "$root/cmd/launcher/internal/outcome/status_gen.go"
    ${writeGenerated "cmd/launcher/internal/outcome/markerchannels_gen.go" markerChannelsGoFile}
    gofmt -w "$root/cmd/launcher/internal/outcome/markerchannels_gen.go"
    ${writeGenerated "cmd/launcher/internal/backend/registry_gen.go" backendRegistryFile}
    gofmt -w "$root/cmd/launcher/internal/backend/registry_gen.go"
    ${writeGenerated "cmd/launcher/internal/doctor/labelmeta_gen.go" labelRegistryFile}
    gofmt -w "$root/cmd/launcher/internal/doctor/labelmeta_gen.go"
    ${writeGenerated "tests/box_env_gen.bash" boxEnvFixture}
    ${writeGenerated "tests/default_models_gen.bash" defaultModelFixtureBash}
    ${writeGenerated "cmd/launcher/defaultmodels_gen_test.go" defaultModelFixtureGo}
    gofmt -w "$root/cmd/launcher/defaultmodels_gen_test.go"
    ${concatStrings (map regenRowScript documentedFacts)}
    ${writeBetweenGenerated "MIGRATING.md"
      "<!-- BEGIN GENERATED LEGACY SETTINGS MAPPING -- nix run .#regen -- DO NOT EDIT -->"
      "<!-- END GENERATED LEGACY SETTINGS MAPPING -->"
      legacySettingsMappingDoc
    }
    ${writeGenerated "cmd/launcher/internal/promptassembly/boxenv_gen.go" promptAssemblyBoxEnvFile}
    gofmt -w "$root/cmd/launcher/internal/promptassembly/boxenv_gen.go"
  '';
}
# `//` only adds an attribute, leaving outPath alone, so flake.nix's
# `${import ./nix/regen.nix { ... }}` string coercion still resolves. The two
# attributes let nix/checks/schema-drift.nix's regen-postsplice-dispatch-guard
# (issue #2949) and regen-write-between-preserves-mode (issue #3128) exercise
# the exact functions `nix run .#regen` uses.
// {
  inherit regenRowScript writeBetweenShellFn;
}
