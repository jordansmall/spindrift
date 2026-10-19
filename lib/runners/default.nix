# The runner-family rows (ADR 0069, decision 1): one row per family of the
# `runtime` knob, each in its own sibling file and listed in order below.
# lib/runners/index.nix derives the runtime enum and the runtime-to-family
# lookup from them, so a runtime value is named in exactly one place.

# Attributes: `name` is the family string the launcher receives as RUNNER_KIND;
# `runtimes` lists the operator-facing runtime values the family serves (a
# runtime may appear in only one row); `artifact` is the flake package the
# family exposes under imageAttrName ("oci-image" or "store-closure").

# A plain list with no `{ lib }:` wrapper, matching lib/dispatch-kinds/default.nix.
# Order is load-bearing: the concatenated `runtimes` are the runtime option's
# enum and the quickstart's first-listed value. Keep it an explicit list:
# builtins.readDir would sort the files by name and silently reorder them.
[
  (import ./oci.nix)
  (import ./bwrap.nix)
]
