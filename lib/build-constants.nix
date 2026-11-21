# Single root for values duplicated across nix build sites with no drift guard.
# The six vendorHash values differ because each site's fileset vendors a
# different tree off identical go.mod/go.sum (issue #784). nixBuilderImage is
# pinned by digest for supply-chain safety; docs/reference.md cannot import
# nix, so update this file first, then its "Bumping the pin" section.
{
  launcherVendorHash = "sha256-NKs16IMPgutW4b454RXZhoch/qbQmA8W1PadS+zImaM=";
  driverExecVendorHash = "sha256-ORguP1625BQ59YKDB2xN4yRivQF0YQE5pgs7PLqa+ug=";
  # Its own field because the orchestrator's fileset never pulled in go-toml
  # the way driver-exec's did once the ecosystem Strategies took over the
  # committed-config parsers.
  orchestratorVendorHash = "sha256-pnhMTBmZqWrW1ylLFjXIHoTr/d3Xh5bqIB1hSf4hVDg=";
  # Equal to driverExecVendorHash today (same vendored tree) but its own field:
  # the two filesets drift independently.
  boxVendorHash = "sha256-ORguP1625BQ59YKDB2xN4yRivQF0YQE5pgs7PLqa+ug=";
  # launcher-currency's narrower fileset (lib/harness-core.nix's
  # launcherCurrencyFileset, issue #2677) can vendor differently off identical
  # go.mod/go.sum (#784); equal to launcherVendorHash today, but the two
  # filesets drift independently.
  launcherCurrencyVendorHash = "sha256-NKs16IMPgutW4b454RXZhoch/qbQmA8W1PadS+zImaM=";
  # nix/checks/go.nix's vendorFor tree: cmd/launcher without the Drivers,
  # runner families, Ecosystem packages, bundled backends, bundle and binaries.
  launcherCoreVendorHash = "sha256-tWbNVQDiICMlMDCEN563OWGrgl8PL9XDBk36bMNJk68=";
  nixBuilderImage = "docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8";
}
