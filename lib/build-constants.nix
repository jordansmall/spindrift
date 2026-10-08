# Single root for values duplicated across nix build sites with no drift guard.
# The five vendorHash values differ because each site's fileset vendors a
# different tree off identical go.mod/go.sum (issue #784). nixBuilderImage is
# pinned by digest for supply-chain safety; docs/reference.md cannot import
# nix, so update this file first, then its "Bumping the pin" section.
{
  launcherVendorHash = "sha256-quNchKKaDo3DJ/i1E1cHWydV7LEsHIhrYzTdlsJunxA=";
  driverExecVendorHash = "sha256-B+f60Z75QmXknZ4xcamH0EmicueGr3FIZOQlBIu+ios=";
  # Its own field because the orchestrator's fileset never pulled in go-toml
  # the way driver-exec's did once the ecosystem rows took over the
  # committed-config parsers.
  orchestratorVendorHash = "sha256-73g/WCY+uR44pYb8wqac5lyIIvHt5ABuPadwB9NRe/0=";
  # Equal to driverExecVendorHash today (same vendored tree) but its own field:
  # the two filesets drift independently.
  boxVendorHash = "sha256-B+f60Z75QmXknZ4xcamH0EmicueGr3FIZOQlBIu+ios=";
  # launcher-currency's fileset excludes driver-exec, orchestrator, quickstart
  # and daemon (each an independent `package main` the launcher never
  # imports), internal/daemon (no non-test package outside daemon imports
  # it, issue #3538), and all _test.go files -- so it vendors differently
  # even off identical go.mod/go.sum (#784, issue #2677).
  launcherCurrencyVendorHash = "sha256-xFNZTE4ya6RpHPeVoSrh6u4nn1UtI2cLiUXB7MB2Ceg=";
  nixBuilderImage = "docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8";
}
