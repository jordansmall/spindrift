# The oci runner row: data plus the env knobs it owns (issue #5686, ADR 0068).
# The registry (./default.nix) documents the fields and fixes this row's order;
# lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
# "rancher" is an alias for Rancher Desktop's containerd mode (ADR 0027).
{
  name = "oci";
  goName = "OCI";
  runtimes = [
    "podman"
    "docker"
    "rancher"
  ];
  artifact = "oci-image";
  # The flake package the Harness exposes this family's artifact under by default.
  imageAttrName = "agent-image";
  networkModes = [
    "open"
    "no-host-loopback"
    "none"
    "host"
  ];

  # The run-document keys only this family renders (ADR 0069 decision 7); the
  # keys both families share live in lib/preambles.nix. It also carries the
  # build-time vars so EnsureReady can build the image on demand when it is
  # absent: the workflow is `build` first, but `run` must still handle a
  # missing image on any machine.
  runArtifacts =
    {
      flakeImageAttr,
      imagePath,
      imageHash,
      imageName,
      imageDrv,
      nixBuilderImage,
      ...
    }:
    {
      FLAKE_IMAGE_ATTR = flakeImageAttr;
      IMAGE_ARCHIVE = imagePath;
      IMAGE_TAG = "${imageName}:${imageHash}";
      IMAGE_DRV = imageDrv;
      NIX_BUILDER_IMAGE = nixBuilderImage;
      NIX_VOLUME = "spindrift-nix";
    };

  knobs = {
    podmanNetwork = {
      env = "PODMAN_NETWORK";
      group = "infra";
      doc = "--network value for podman run; empty applies no flag (podman NAT default); set to 'pasta' to restrict egress";
      flakeOption = true;
      nixSubPath = "network.podman";
      boxEnv = false;
    };
  };
}
