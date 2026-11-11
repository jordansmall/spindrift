# Single root for values duplicated across nix build sites with no drift guard.
# The five vendorHash values differ because each site's fileset vendors a
# different tree off identical go.mod/go.sum (issue #784). nixBuilderImage is
# pinned by digest for supply-chain safety; docs/reference.md cannot import
# nix, so update this file first, then its "Bumping the pin" section.
{
  launcherVendorHash = "sha256-TPg1c/v5ro/WaB6TZyIm1DtMX2fERmeN0uBDsaTCYFY=";
  driverExecVendorHash = "sha256-SXj9iFpbN2/m0mSWgWIhKbXggSfEhxpYwE579rwgsVI=";
  # Its own field because the orchestrator's fileset never pulled in go-toml
  # the way driver-exec's did once the ecosystem Strategies took over the
  # committed-config parsers.
  orchestratorVendorHash = "sha256-Q69DfDw8n0hY9ycr++kjB6Q4auVFKiAeoZYXw3kSaH4=";
  # Equal to driverExecVendorHash today (same vendored tree) but its own field:
  # the two filesets drift independently.
  boxVendorHash = "sha256-SXj9iFpbN2/m0mSWgWIhKbXggSfEhxpYwE579rwgsVI=";
  # launcher-currency's fileset excludes driver-exec, orchestrator, quickstart
  # and daemon (each an independent `package main` the launcher never
  # imports), internal/daemon (no non-test package outside daemon imports
  # it, issue #3538), and all _test.go files -- so it vendors differently
  # even off identical go.mod/go.sum (#784, issue #2677).
  launcherCurrencyVendorHash = "sha256-Ow1K70wECsJ+kQIw/aBwDbWKL3rnX9cqTt4ye+UurRQ=";
  nixBuilderImage = "docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8";
}
