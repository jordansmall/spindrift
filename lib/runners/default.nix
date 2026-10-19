# The runner-family rows (ADR 0069, decision 1): one row per family of the
# `runtime` knob, each in its own sibling file and listed in order below.
# lib/runners/index.nix derives the runtime enum and the runtime-to-family
# lookup from them, so a runtime value is named in exactly one place.

# Field meanings live on the Descriptor in
# cmd/launcher/internal/runner/family.go, except `goName`, which names the
# exported Go var and its Kind constant (OCI, KindOCI). Adding a field is one
# entry in rowFields (lib/renderers.nix) plus the struct field, guarded by
# TestDescriptorFieldsMatchNixRows. `nix run .#regen` renders the rows into
# families_gen.go.

# A plain list with no `{ lib }:` wrapper, matching lib/dispatch-kinds/default.nix.
# Order is load-bearing: the concatenated `runtimes` are the runtime option's
# enum and the quickstart's first-listed value. Keep it an explicit list:
# builtins.readDir would sort the files by name and silently reorder them.
[
  (import ./oci.nix)
  (import ./bwrap.nix)
]
