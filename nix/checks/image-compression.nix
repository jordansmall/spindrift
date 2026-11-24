# Pins the image compressor per fixture (issue #7522). The launcher loads the
# shipped images, so they stay gzip; check-only fixtures are only untarred by
# image checks and skip the gzip. Eval-only: `.name` carries the tarball
# extension (`spindrift.tar.gz` vs `spindrift.tar`) without building anything.
{
  pkgs,
  config,
  fixtures,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    hasPrefix
    hasSuffix
    concatStringsSep
    filterAttrs
    ;
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
  # The flake's own image packages too, not only their fixture twins, so a
  # divergence between the two is caught. Linux-only: empty on darwin.
  shippedPackages = filterAttrs (n: _: hasPrefix "agent-image" n) config.packages;
  shippedNames = {
    connectorSandboxImageEscape = fixtures.connectorSandboxImageEscape.name;
  }
  // builtins.mapAttrs (_: h: h.image.name) shipped
  // builtins.mapAttrs (_: p: p.name) shippedPackages;
  checkOnlyNames = builtins.mapAttrs (_: h: h.image.name) checkOnly;
  missingSuffix =
    suffix: names: builtins.filter (n: !hasSuffix suffix names.${n}) (builtins.attrNames names);
  badShipped = missingSuffix ".tar.gz" shippedNames;
  badCheckOnly = missingSuffix ".tar" checkOnlyNames;
in
{
  image-compression =
    assert assertMsg (badShipped == [ ])
      "shipped images must stay gzip (.tar.gz), the launcher loads them: ${concatStringsSep ", " badShipped}";
    assert assertMsg (
      badCheckOnly == [ ]
    ) "check-only fixture images must be uncompressed (.tar): ${concatStringsSep ", " badCheckOnly}";
    pkgs.runCommand "image-compression" { } "touch $out";
}
