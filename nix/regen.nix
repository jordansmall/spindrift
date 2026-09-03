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
  # write_between as a named, shared shell-function string (mirroring
  # spliceShellFn/regenRowScript's own extraction) so
  # nix/checks/schema-drift.nix's regen-write-between-preserves-mode check
  # (issue #3128) can exercise the real function against a fixture file
  # instead of a hand-mirrored copy that could silently drift from what
  # `nix run .#regen` actually runs.
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
  # Every write/write_between call site below passes a rendered artifact
  # (Markdown, Go source, docs) through escapeShellArg into a single-quoted
  # shell word -- any `$`, backtick, or apostrophe inside it is a literal
  # byte of that artifact, never meant to expand, so ShellCheck's SC2016
  # ("expressions don't expand in single quotes") is a false positive at
  # every one of these call sites. These helpers emit the directive as part
  # of the call it covers: the exclusion stays one line per call site (a
  # genuine SC2016 elsewhere in this script is still caught, unlike a
  # file-wide directive) and a future generated-content call site cannot
  # silently omit it.
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
  # Named so nix/checks/schema-drift.nix's regen-postsplice-dispatch-guard
  # can call this exact function against synthetic rows (issue #2949 review
  # finding) instead of a hand-mirrored reimplementation -- a typo in a
  # row's postSplice field (wrong case, misspelling) would otherwise
  # silently take the no-gofmt branch with nothing catching it.
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
    # write_between's `chmod --reference` (issue #3128) is a GNU coreutils
    # extension BSD/macOS chmod lacks -- writeShellApplication only prepends
    # runtimeInputs to $PATH, it doesn't supply coreutils itself, so without
    # this pin `nix run .#regen` resolves the ambient /bin/chmod on darwin
    # (apps.regen is not isLinux-gated) and dies on the unrecognised flag.
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
# `//` only adds an attribute here -- it doesn't touch the derivation's
# outPath/build behavior, so flake.nix's `${import ./nix/regen.nix { ... }}`
# string coercion (which resolves via outPath) is unaffected. This exposes
# regenRowScript so nix/checks/schema-drift.nix's
# regen-postsplice-dispatch-guard can call the exact function `nix run
# .#regen` uses, not a hand-mirrored reimplementation (issue #2949 review
# finding). writeBetweenShellFn (issue #3128) is exposed the same way so
# nix/checks/schema-drift.nix's regen-write-between-preserves-mode check can
# exercise the real write_between function's mode-preservation fix.
// {
  inherit regenRowScript writeBetweenShellFn;
}
