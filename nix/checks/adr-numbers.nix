# Eval-level pins over docs/adr/'s four-digit ADR number prefix (issue #3631).
# The uniqueness pin only ever sees wellFormed, so without the well-formedness
# pin a badly named ADR escapes it instead of tripping it.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    filterAttrs
    groupBy
    mapAttrsToList
    partition
    substring
    ;

  isWellFormedName = name: builtins.match "[0-9]{4}-.*\\.md" name != null;

  partitionEntries =
    adrEntries:
    let
      parts = partition (name: adrEntries.${name} == "regular" && isWellFormedName name) (
        builtins.attrNames adrEntries
      );
    in
    {
      wellFormed = parts.right;
      malformed = parts.wrong;
    };

  collisionsIn =
    candidates:
    filterAttrs (_: group: builtins.length group > 1) (groupBy (name: substring 0 4 name) candidates);

  describeCollision = prefix: group: "${prefix} (${concatStringsSep ", " group})";

  inherit (partitionEntries (builtins.readDir ../../docs/adr)) wellFormed malformed;
  collisions = collisionsIn wellFormed;
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

  adr-numbers-partition-entries-detects-bad-shape =
    let
      found = partitionEntries {
        "0001-good.md" = "regular";
        "not-a-valid-name.md" = "regular";
        "0002-dir.md" = "directory";
      };
    in
    assert assertMsg (
      found == {
        wellFormed = [ "0001-good.md" ];
        malformed = [
          "0002-dir.md"
          "not-a-valid-name.md"
        ];
      }
    ) "partitionEntries must reject a non-regular entry and a name without a four-digit prefix";
    pkgs.runCommand "adr-numbers-partition-entries-detects-bad-shape" { } "touch $out";
}
