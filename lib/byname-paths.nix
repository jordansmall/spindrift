# Maps each flat byName knob name to its list of domain-tree path segments.
# Consumed by lib/flakeModule.nix's byNameTreeEntries, lib/renderers.nix's
# structural-options doc, lib/structural-template-examples.nix, and
# nix/checks/schema-drift.nix's flake-nixpath-exhaustive-disjoint check
# (issue #2731; mirrors the #2184 precedent lib/structural-paths.nix set).
{
  byName = [
    "agents"
    "models"
    "byName"
  ];
}
