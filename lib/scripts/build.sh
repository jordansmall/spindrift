#!/usr/bin/env bash
# Load the spindrift agent image into the container runtime.
#
# The image is a nix store path baked in at build time (@imagePath@ is
# substituted by lib/mkHarness.nix). Build the image itself with
# `nix build .#spindrift`; this command only loads that store path into podman.
set -euo pipefail

IMAGE_ARCHIVE="@imagePath@"

command -v podman >/dev/null 2>&1 || {
  echo "podman not found on PATH." >&2
  exit 1
}

echo "==> loading spindrift image from $IMAGE_ARCHIVE"
podman load -i "$IMAGE_ARCHIVE"
echo "==> done: spindrift:latest"
