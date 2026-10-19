# The Dispatch kind descriptor rows (ADR 0056, 2026-10-18 amendment; issue
# #5668): one row per kind, each in its own sibling file and listed in order
# below. lib/renderers.nix's renderDispatchKindsGo writes them into
# cmd/launcher/internal/dispatchkind/kinds_gen.go on `nix run .#regen`.

# Adding a field is one entry in renderDispatchKindsGo's rowFields table plus
# the matching field on the hand-written Descriptor struct in
# cmd/launcher/internal/dispatchkind/dispatchkind.go;
# TestDescriptorFieldsMatchNixRows fails if the two disagree.

# Attributes are camelCase mirrors of the Descriptor fields. Enum-valued
# attributes (keying, labels, settle, contract, tracker, daemonPriority,
# demandSource, enablement) hold the Go constant name verbatim. preflight is
# { when = "none"; } | { when = "named"; flag = ...; } | { when = "drawn";
# flag = ...; }, rendered as the matching dispatchkind constructor; the
# renderer throws on an unknown `when` or a missing flag. goVar names the
# exported Go variable. Exactly one row sets `default = true`; that kind is
# dispatchkind.Default.

# An omitted bool, string, prompts member, or statuses list renders as its Go
# zero value (by omission in the generated file).

# A plain list with no `{ lib }:` wrapper, matching lib/backends/default.nix.
# Order is load-bearing: it is dispatchkind.All's order, the daemon's default
# pool order. Keep it an explicit list: builtins.readDir would sort the files
# by name and silently reorder the pool.
[
  (import ./work.nix)
  (import ./research.nix)
  (import ./butler.nix)
  (import ./recover.nix)
]
