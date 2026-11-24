# The flake module's view of lib/removed-knobs.nix, a pure function over the
# table so nix/checks/equivalence.nix can feed it a fixture row (an env-only
# row omits flakePath) and assert by value. `opt` is the hidden option the tree
# entries carry.
{ lib }:
removedKnobs:
let
  # An env-only row (no flake option) has no flakePath; the tree entries read
  # this set, and `hits` guards its flakePath read with it.
  flakeRows = lib.filterAttrs (_: row: row ? flakePath) removedKnobs;
in
{
  treeEntries =
    opt:
    lib.mapAttrsToList (_: row: {
      path = row.flakePath;
      inherit opt;
    }) flakeRows;

  # A set removed knob, by its flake path or its legacy settings alias, is a
  # hard error naming the knob; `!= null` catches an explicit `false` too.
  hits =
    cfg:
    lib.concatLists (
      lib.mapAttrsToList (
        key: row:
        lib.optional (flakeRows ? ${key} && lib.attrByPath row.flakePath null cfg != null) {
          at = "perSystem.spindrift.${lib.concatStringsSep "." row.flakePath}";
          inherit (row) message;
        }
        ++
          lib.optional (row ? legacySection && (cfg.settings.${row.legacySection}.${key} or null) != null)
            {
              at = "perSystem.spindrift.settings.${row.legacySection}.${key}";
              inherit (row) message;
            }
      ) removedKnobs
    );
}
