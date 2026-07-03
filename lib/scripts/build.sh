# Realise the spindrift agent image, then load it into the container runtime.
#
# Body fragment: nix (lib/mkHarness.nix) wraps it with writeShellApplication,
# prepending the shebang, `set -euo pipefail`, the pinned runtimeInputs PATH, and
# a preamble defining IMAGE_ARCHIVE, IMAGE_DRV, RUNTIME, NIX_BUILDER_IMAGE,
# NIX_VOLUME, FLAKE_IMAGE_ATTR, and the build-image.sh helper functions.
#
# The image is a Linux (OCI) derivation. Realise it on the host (Linux, or darwin
# with a Linux builder); failing that, build it inside an ephemeral Nix container
# on the required runtime, from the Consumer flake in $PWD. Pure eval guarantees
# the in-container build yields the exact store path baked in above.
#
# Run this from your Consumer flake's directory.

build_box_image
