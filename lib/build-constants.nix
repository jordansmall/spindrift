# Hand-typed roots for values duplicated across nix build sites with no
# existing drift guard: four Go-module vendorHash values (issue #784 —
# the vendored tree differs between launcherBin's full cmd/launcher fileset
# and each of driverExecBin's, orchestrator's and launcher-currency's
# narrower ones, off identical go.mod/go.sum) and the nix-builder fallback
# image, pinned by digest for supply-chain safety (lib/mkHarness.nix's
# nixBuilderImage). Every nix site that needs one of these imports this
# file instead of retyping the value; docs/reference.md can't import nix,
# so it carries the nixBuilderImage digest as a pinned copy in two places
# (the option-surface table and the "Bumping the pin" section) — update
# this file first, then both. cmd/launcher/internal/runner/oci_test.go
# also carries a copy of the digest, best-effort only: its check doesn't
# require literal equality.
{
  launcherVendorHash = "sha256-sTY+2ubwPKONRHWMKy/3/xOQ+Q4EZski7Qiq7gJaQ2w=";
  driverExecVendorHash = "sha256-Iyy3pXHAYwXgDA85SS5ouPLmRjHWhMBOhdVMWhfWQNk=";
  # orchestratorVendorHash is its own field because the orchestrator's
  # fileset carries no ecosystem dependency: it never pulled in go-toml the
  # way driver-exec's did once the ecosystem rows took over the
  # committed-config parsers.
  orchestratorVendorHash = "sha256-Bh3JiWUuQEfWvapyawzC13d/wwgvxdUl11j/Zia1P10=";
  # launcher-currency's fileset excludes driver-exec/orchestrator/quickstart
  # (each an independent `package main` never imported by the launcher
  # itself) and all _test.go files, narrower than launcherBin's full
  # cmd/launcher tree -- so it vendors differently even off identical
  # go.mod/go.sum, the same reason driverExecVendorHash above is its own
  # field rather than reusing launcherVendorHash (#784, issue #2677).
  launcherCurrencyVendorHash = "sha256-q5jyNelr05+EY930FEOGo19uK5Z2UY+eYl8RebSgVG4=";
  nixBuilderImage = "docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8";
}
