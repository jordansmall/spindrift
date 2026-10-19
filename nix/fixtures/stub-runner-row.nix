# A runner row standing in for a family nobody wrote a test for; it names no
# real family or runtime. Shared by the stub-row checks in
# nix/checks/preambles.nix and nix/checks/schema-drift.nix.
{
  name = "stub";
  goName = "Stub";
  runtimes = [ "stub-runtime" ];
  artifact = "oci-image";
  # The flake package the Harness exposes this family's artifact under by default.
  imageAttrName = "agent-image";
  networkModes = [ "open" ];
  # Renders FLAKE_IMAGE_ATTR in a shape no real row uses, proving the key is
  # the row's to shape.
  runArtifacts =
    { flakeImageAttr, ... }:
    {
      FLAKE_IMAGE_ATTR = "stub:${flakeImageAttr}";
      STUB_ONLY_KEY = "x";
    };
}
