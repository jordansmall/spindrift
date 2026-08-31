# The documentedFact registry (issue #2948, spec #2921): one row per
# generated marker-delimited span, where the span's committed content must
# stay byte-for-byte in sync with some Nix source of truth. A row's span can
# live in a committed doc, a template, a bash script, or a Go source file —
# most rows are checked/spliced via assertMarkedBlockOk, but two (whose span
# lives inside Go source that `gofmt -w` reformats beyond the spliced span
# itself, marked `postSplice = "gofmt"`) go via assertSplicedSpanOk instead.
# Three consumers share this one list so a span's marker literals, host
# file, and renderer call are typed exactly once:
#   - nix/checks/schema-drift.nix's shared checker derives one named drift
#     check per row via documentedFactChecks (`<row.name>`, e.g.
#     "default-models-doc") — CI granularity per row survives unchanged from
#     when each family had its own hand-written check.
#   - nix/checks/baked-skills.nix's two end-to-end regression guards look up
#     specific rows by name (rowByName) to source their marker literals
#     instead of hand-maintaining a separate copy.
#   - nix/regen.nix's marker-splice loop rewrites each row's span in place
#     between beginMarker/endMarker with `generated`, sharing the same rows
#     the checker above validates.
#
# Takes `{ lib }:` (not a plain zero-arg list like lib/backends/default.nix
# or lib/subcommands.nix) because the template-settings-block row's
# `generated` needs lib/structural-template-examples.nix, which itself
# requires `{ lib }:`.
#
# Fields:
#   name         string  becomes the schema-drift.nix check derivation name
#                        (and the pkgs.runCommand derivation name); must stay
#                        stable across a slice-2948 migration since CI
#                        granularity and any external references key off it.
#   docPath      string  path (relative to the repo root) of the host file the
#                        block lives in, e.g. "docs/reference.md". The eleven
#                        rows below span six host files today, but the field
#                        is per-row since a future block could live elsewhere
#                        (legacy-settings-mapping-doc's MIGRATING.md block is
#                        out of scope for this migration — see
#                        nix/checks/schema-drift.nix).
#   blockName    string  the marker's block name, e.g. "DEFAULT MODELS" —
#                        also interpolated into assertMarkedBlockOk's thrown
#                        messages ("BEGIN GENERATED <blockName> marker not
#                        found").
#   sourceDesc   string  human-readable name of the Nix source of truth,
#                        interpolated into the drift message ("... is out of
#                        sync with <sourceDesc>").
#   beginMarker  string  the literal begin-marker line, WITH its trailing
#                        "\n" (assertMarkedBlockOk's convention — it splits
#                        docSrc on this literal).
#   endMarker    string  the literal end-marker line, with NO trailing "\n".
#   generated    string  the exact content the block must hold, computed via
#                        the same lib/renderers.nix renderer `nix run .#regen`
#                        uses, so the check and the regenerator can never
#                        drift from each other (issue #402).
#   postSplice   string  optional, defaults to `null` when absent (read as
#                        `row.postSplice or null`; assertMarkerShape doesn't
#                        touch it). `"gofmt"` means the block lives inside Go
#                        source that `gofmt -w` reformats beyond just the
#                        spliced span (e.g. column-aligning a whole struct) --
#                        the checker and regenerator must `gofmt -w` the whole
#                        host file after splicing raw `generated` in, before
#                        comparing/writing, or a plain string diff would flag
#                        false drift.
{ lib }:
let
  renderers = import ./renderers.nix;
  defaultModelFixture = import ./default-model-fixture.nix;
  schema = import ./env-schema.nix;
  structuralTemplateExamples = import ./structural-template-examples.nix { inherit lib; };
  promptContract = import ./prompt-contract.nix;
  bakedSkills = import ./baked-skills.nix;
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
