# Shared image-build helpers: realise the OCI image derivation on the host
# (or fall back to an ephemeral Nix container) and load it into the runtime.
#
# Fragment: included verbatim by mkHarness.nix into the `build` and `run`
# commands (OCI mode only). The preamble defines IMAGE_ARCHIVE, IMAGE_DRV,
# RUNTIME, NIX_BUILDER_IMAGE, NIX_VOLUME, and FLAKE_IMAGE_ATTR before this.

load_image() {
  local archive="$1"
  echo "==> loading spindrift image from $archive"
  "$RUNTIME" load -i "$archive"
  echo "==> done: spindrift:latest"
}

# Build the image inside an ephemeral Nix container and load the tarball. The
# named /nix volume keeps rebuilds incremental; the built tarball is copied out
# to a host-visible path so the host runtime can load it.
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

# Realise and load the spindrift image. Tries the host first; falls back to an
# ephemeral Nix container; fails with an actionable message if neither works.
build_box_image() {
  # 1. Realise the baked image derivation on the host (Linux, or darwin with a
  #    Linux builder). '^*' selects the derivation's outputs.
  if nix build "${IMAGE_DRV}^*" --no-link >/dev/null 2>&1; then
    echo "==> realised image derivation on the host"
    load_image "$IMAGE_ARCHIVE"
    return 0
  fi

  # 2. Host can't realise it — fall back to the container, if the runtime is here.
  if command -v "$RUNTIME" >/dev/null 2>&1; then
    build_in_container
    return 0
  fi

  # 3. Neither path is possible.
  fail_no_builder
}
