# Pins the image compressor per fixture (issue #7522). The launcher loads the
# shipped images, so they stay gzip; check-only fixtures are only untarred by
# image checks and skip the gzip. Eval-only: `.name` carries the tarball
# extension (`spindrift.tar.gz` vs `spindrift.tar`) without building anything.
{ pkgs, fixtures, ... }:
let
  inherit (pkgs.lib) assertMsg hasSuffix concatStringsSep;
  shipped = {
    inherit (fixtures)
      harness
      dogfoodOpencodeHarness
      dogfoodPiHarness
      dogfoodBwrapHarness
      dogfoodBwrapPiHarness
      ;
  };
  checkOnly = {
    inherit (fixtures)
      nonRustHarness
      forgejoHarness
      forgejoForgeHarness
      forgejoBothHarness
      jiraHarness
      extraClosuresHarness
      nixStoreWritableHarness
      opencodeHarness
      piHarness
      ;
  };
  shippedNames = {
    connectorSandboxImageEscape = fixtures.connectorSandboxImageEscape.name;
  }
  // builtins.mapAttrs (_: h: h.image.name) shipped;
  checkOnlyNames = builtins.mapAttrs (_: h: h.image.name) checkOnly;
  bad =
    suffix: names:
    builtins.filter (n: !hasSuffix suffix names.${n}) (builtins.attrNames names);
  badShipped = bad ".tar.gz" shippedNames;
  badCheckOnly = bad ".tar" checkOnlyNames;
in
{
  image-compression =
    assert assertMsg (badShipped == [ ])
      "shipped images must stay gzip (.tar.gz), the launcher loads them: ${concatStringsSep ", " badShipped}";
    assert assertMsg (badCheckOnly == [ ])
      "check-only fixture images must be uncompressed (.tar): ${concatStringsSep ", " badCheckOnly}";
    pkgs.runCommand "image-compression" { } "touch $out";
}
