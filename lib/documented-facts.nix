# The documentedFact registry (issue #2948, spec #2921): one row per
# generated marker-delimited block embedded in a committed doc, where the
# block's committed content must stay byte-for-byte in sync with some Nix
# source of truth. Two consumers share this one list so a block's marker
# literals, source doc, and renderer call are typed exactly once:
#   - nix/checks/schema-drift.nix's shared checker derives one named drift
#     check per row via assertMarkedBlockOk (`<row.name>`, e.g.
#     "default-models-doc") — CI granularity per row survives unchanged from
#     when each family had its own hand-written check.
#   - nix/regen.nix's marker-splice loop rewrites each row's block in place
#     between beginMarker/endMarker with `generated`, sharing the same rows
#     the checker above validates.
#
# A plain list (no `{ lib }:`/`{ pkgs }:` wrapper), mirroring
# lib/backends/default.nix and lib/subcommands.nix, so it stays importable
# with zero arguments.
#
# Fields:
#   name         string  becomes the schema-drift.nix check derivation name
#                        (and the pkgs.runCommand derivation name); must stay
#                        stable across a slice-2948 migration since CI
#                        granularity and any external references key off it.
#   docPath      string  path (relative to the repo root) of the doc the
#                        block lives in, e.g. "docs/reference.md". All four
#                        rows below share one doc today, but the field is
#                        per-row since a future block could live elsewhere
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
let
  renderers = import ./renderers.nix;
  defaultModelFixture = import ./default-model-fixture.nix;
  schema = import ./env-schema.nix;
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
]
