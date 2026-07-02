# Realise the spindrift agent image, then load it into the container runtime.
#
# This is a body fragment: nix (lib/mkHarness.nix) wraps it with
# writeShellApplication, which prepends the shebang, `set -euo pipefail`, the
# pinned runtimeInputs PATH, and a nix-rendered preamble that defines
# IMAGE_ARCHIVE (the baked image store path), IMAGE_DRV (the baked image .drv
# path, context-discarded), RUNTIME (podman or docker), NIX_BUILDER_IMAGE (the
# ephemeral Nix container used as a fallback Linux builder), NIX_VOLUME (the
# named /nix volume that keeps that fallback incremental), and FLAKE_IMAGE_ATTR
# (the Linux image attribute to build inside it).
#
# The image is a Linux (OCI) derivation. We first try to realise it on the host
# — which works on Linux, or on darwin with a configured Linux builder — and
# load the resulting store path. When the host has no Linux builder (the stock
# mac case), we fall back to building it inside an ephemeral Nix container on
# the runtime we already require, from the Consumer flake in $PWD. Pure eval
# guarantees the in-container build yields the exact store path baked in above.
# If neither path is possible we exit non-zero with an actionable message.
#
# Run this from your Consumer flake's directory (the same $PWD convention
# harness.env uses).

load_image() {
  local archive="$1"
  echo "==> loading spindrift image from $archive"
  "$RUNTIME" load -i "$archive"
  echo "==> done: spindrift:latest"
}

# Build the image inside an ephemeral Nix container and load the resulting
# tarball. A named volume for /nix keeps rebuilds incremental across runs; the
# Consumer flake is mounted from $PWD, and the built tarball is copied out to a
# host-visible path so the host runtime can load it.
build_in_container() {
  local tar="$PWD/.spindrift-image.tar"
  local pathfile=".spindrift-image-path"

  echo "==> no host Linux builder; building the image inside a $NIX_BUILDER_IMAGE container"
  echo "    (reusing the '$NIX_VOLUME' volume for /nix so rebuilds are incremental)"

  if ! "$RUNTIME" run --rm \
    -v "$NIX_VOLUME:/nix" \
    -v "$PWD:/workspace" \
    -w /workspace \
    "$NIX_BUILDER_IMAGE" \
    sh -euc "nix --extra-experimental-features 'nix-command flakes' build '$FLAKE_IMAGE_ATTR' --print-out-paths --no-link >$pathfile && cp \"\$(cat $pathfile)\" .spindrift-image.tar"; then
    echo "==> container build failed — see the $RUNTIME output above." >&2
    rm -f "$tar" "$PWD/$pathfile"
    exit 1
  fi

  load_image "$tar"
  rm -f "$tar" "$PWD/$pathfile"
}

fail_no_builder() {
  cat >&2 <<EOF
==> cannot build the spindrift image.

The image is a Linux (OCI) derivation, and this host can neither realise it
directly nor fall back to a container build:

  * No Linux builder: 'nix build' could not realise the image. On macOS, enable
    nix-darwin's 'nix.linux-builder.enable = true;', or point nix at a remote
    Linux builder via 'nix.buildMachines' / '--builders'.

  * No container runtime: '$RUNTIME' was not found on PATH. Install it (or set
    'runtime = "docker"' in your mkHarness call) so 'build' can build the image
    inside an ephemeral Nix container.

Run 'build' from your Consumer flake's directory.
EOF
  exit 1
}

# 1. Try to realise the baked image derivation on the host. Succeeds on Linux,
#    or on darwin with a Linux builder; the '^*' selects the derivation's
#    outputs. On success the image is realised at IMAGE_ARCHIVE.
if nix build "${IMAGE_DRV}^*" --no-link >/dev/null 2>&1; then
  echo "==> realised image derivation on the host"
  load_image "$IMAGE_ARCHIVE"
  exit 0
fi

# 2. Host can't realise it — fall back to the container, if the runtime is here.
if command -v "$RUNTIME" >/dev/null 2>&1; then
  build_in_container
  exit 0
fi

# 3. Neither path is possible.
fail_no_builder
