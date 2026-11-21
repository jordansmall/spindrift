# The runner-family rows (ADR 0069, decision 1): one row per family of the
# `runtime` knob, each in its own sibling file and listed in order below.
# lib/runners/index.nix derives the runtime enum and the runtime-to-family
# lookup from them, so a runtime value is named in exactly one place.

# Field meanings live on the Descriptor in
# cmd/launcher/internal/runner/family.go; `goName` names the exported Go var
# and its Kind constant (OCI, KindOCI). A new field is one rowFields entry
# (lib/renderers.nix) plus the struct field, guarded by
# TestDescriptorFieldsMatchNixRows; `nix run .#regen` renders families_gen.go.
# The Nix-only fields (nixOnlyFields): `runArtifacts` returns the row's
# run-document keys for lib/preambles.nix; `imageAttrName` is the default
# flake package name lib/mkHarness.nix exposes the artifact under.

# A row may also carry `knobs`, env-schema entries keyed by env-schema key,
# which lib/row-knobs.nix folds into lib/env-schema.nix; an env name or key
# shared with another declaration fails eval (ADR 0068).

# A plain list with no `{ lib }:` wrapper, matching lib/dispatch-kinds/default.nix.
# Order is load-bearing: the concatenated `runtimes` are the runtime option's
# enum and the quickstart's first-listed value. Keep it an explicit list:
# builtins.readDir would sort the files by name and silently reorder them.
[
  (import ./oci.nix)
  (import ./bwrap.nix)
]
