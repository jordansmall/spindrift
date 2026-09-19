# Maps each flat byName knob name to its domain-tree path segments. Changing
# this set changes the rendered structural-options docs and templates, and the
# flake-nixpath-exhaustive-disjoint check in nix/checks/schema-drift.nix fails
# if it drifts (issue #2731, mirroring lib/structural-paths.nix from #2184).
{
  byName = [
    "agents"
    "models"
    "byName"
  ];
}
