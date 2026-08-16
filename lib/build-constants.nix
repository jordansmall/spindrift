# Hand-typed roots for values duplicated across nix build sites with no
# existing drift guard: two Go-module vendorHash values (issue #784 — the
# vendored tree differs between launcherBin's full cmd/launcher fileset
# and driverExecBin/orchestrator's narrower one, off identical
# go.mod/go.sum) and the nix-builder fallback image, pinned by digest for
# supply-chain safety (lib/mkHarness.nix's nixBuilderImage). Every nix
# site that needs one of these imports this file instead of retyping the
# value; docs/reference.md can't import nix, so it carries the
# nixBuilderImage digest as a pinned copy in two places (the option-surface
# table and the "Bumping the pin" section) — update this file first, then
# both. cmd/launcher/internal/runner/oci_test.go also carries a copy of the
# digest, best-effort only: its check doesn't require literal equality.
{
  launcherVendorHash = "sha256-1rl00SlOdcXyd2kpgiX8C+sOsDbewLQedzDJZq98L3w=";
  driverExecVendorHash = "sha256-uaAaQReAf8PCq/TNWetYyYinj+BeUaiaL4zm/fpJPBA=";
  nixBuilderImage = "docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8";
}
