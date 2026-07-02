# Load the spindrift agent image into the container runtime.
#
# This is a body fragment: nix (lib/mkHarness.nix) wraps it with
# writeShellApplication, which prepends the shebang, `set -euo pipefail`, the
# pinned runtimeInputs PATH, and a nix-rendered preamble that defines
# IMAGE_ARCHIVE (the baked image store path) and RUNTIME (podman or docker).
# Build the image itself with `nix build .#spindrift`; this command only loads
# that store path into the configured container runtime.

command -v "$RUNTIME" >/dev/null 2>&1 || {
  echo "$RUNTIME not found on PATH." >&2
  exit 1
}

echo "==> loading spindrift image from $IMAGE_ARCHIVE"
"$RUNTIME" load -i "$IMAGE_ARCHIVE"
echo "==> done: spindrift:latest"
