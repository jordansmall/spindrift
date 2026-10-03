# Eval-level pin (issue #4280): the fixture names the Nix derivation lays down
# and the names the Go resolver expects (seamtest/fixtures.json) must agree,
# so a rename on either side fails here instead of as a skipped or missing
# fixture at test time.
{ pkgs, fixtures, ... }:
let
  inherit (pkgs.lib) assertMsg concatStringsSep subtractLists;

  mismatch =
    derivNames: goNames:
    let
      onlyNix = subtractLists goNames derivNames;
      onlyGo = subtractLists derivNames goNames;
    in
    {
      inherit onlyNix onlyGo;
      ok = onlyNix == [ ] && onlyGo == [ ];
    };

  describe =
    m:
    "only in nix/seam-fixtures.nix: [${concatStringsSep ", " m.onlyNix}]; only in seamtest/fixtures.json: [${concatStringsSep ", " m.onlyGo}]";

  actual = mismatch (builtins.attrNames fixtures.seamFixtures.files) (
    builtins.fromJSON (builtins.readFile ../../cmd/launcher/internal/seamtest/fixtures.json)
  );
  toy = mismatch [ "a" "b" ] [ "b" "c" ];
in
{
  seam-fixtures-names-match-resolver =
    assert assertMsg actual.ok "seam fixture names drifted: ${describe actual}";
    pkgs.runCommand "seam-fixtures-names-match-resolver" { } "touch $out";

  seam-fixtures-mismatch-detects-drift =
    assert assertMsg (
      !toy.ok && toy.onlyNix == [ "a" ] && toy.onlyGo == [ "c" ]
    ) "mismatch must report names present on only one side";
    pkgs.runCommand "seam-fixtures-mismatch-detects-drift" { } "touch $out";
}
