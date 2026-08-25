# Maps each flat byName knob name to its list of domain-tree path segments.
# Consumed by lib/flakeModule.nix's byNameTreeEntries and by
# nix/checks/schema-drift.nix's flake-nixpath-exhaustive-disjoint check
# (issue #2731; mirrors the #2184 precedent lib/structural-paths.nix set).
# Other hardcoded copies of this same path survive elsewhere in the repo
# and are not yet migrated to read from here (tracked separately).
{
  byName = [
    "agents"
    "models"
    "byName"
  ];
}
