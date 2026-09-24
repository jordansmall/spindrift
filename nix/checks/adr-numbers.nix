# Eval-level pins over docs/adr/'s four-digit ADR number prefix (issue #3631).
# A malformed entry name is silently *skipped* by collisionsIn's grouping
# (it never joins a group, meaningful or not) rather than mis-grouped under
# some meaningless key, so without the well-formedness pin a badly named ADR
# escapes the uniqueness invariant entirely instead of tripping it.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    filter
    filterAttrs
    groupBy
    mapAttrsToList
    substring
    ;

  isWellFormedName = name: builtins.match "[0-9]{4}-.*\\.md" name != null;

  malformedNames = names: filter (name: !(isWellFormedName name)) names;

  collisionsIn =
    names: filterAttrs (_: group: builtins.length group > 1) (groupBy (name: substring 0 4 name) names);

  describeCollision = prefix: group: "${prefix} (${concatStringsSep ", " group})";

  entries = builtins.readDir ../../docs/adr;
  names = builtins.attrNames entries;
  isRegular = name: entries.${name} == "regular";

  malformed = filter (name: !(isRegular name) || !(isWellFormedName name)) names;
  wellFormedNames = filter (name: isRegular name && isWellFormedName name) names;
  collisions = collisionsIn wellFormedNames;
in
{
  adr-numbers-well-formed =
    assert assertMsg (
      malformed == [ ]
    ) "docs/adr/ entries must be regular files named NNNN-....md: ${concatStringsSep ", " malformed}";
    pkgs.runCommand "adr-numbers-well-formed" { } "touch $out";

  adr-numbers-unique =
    assert assertMsg (collisions == { })
      "docs/adr/ files share a four-digit number prefix: ${concatStringsSep "; " (mapAttrsToList describeCollision collisions)}";
    pkgs.runCommand "adr-numbers-unique" { } "touch $out";

  adr-numbers-collisions-in-detects-shared-prefix =
    let
      found = collisionsIn [
        "0001-foo.md"
        "0001-bar.md"
        "0002-baz.md"
      ];
    in
    assert assertMsg (
      found == {
        "0001" = [
          "0001-foo.md"
          "0001-bar.md"
        ];
      }
    ) "collisionsIn must report a colliding pair under its shared four-digit prefix";
    pkgs.runCommand "adr-numbers-collisions-in-detects-shared-prefix" { } "touch $out";

  adr-numbers-malformed-names-detects-bad-shape =
    let
      found = malformedNames [
        "0001-good.md"
        "not-a-valid-name.md"
      ];
    in
    assert assertMsg (
      found == [ "not-a-valid-name.md" ]
    ) "malformedNames must report a name without a four-digit prefix";
    pkgs.runCommand "adr-numbers-malformed-names-detects-bad-shape" { } "touch $out";
}
