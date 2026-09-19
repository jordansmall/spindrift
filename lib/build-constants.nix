# Single root for values duplicated across nix build sites with no drift guard.
# The four vendorHash values differ because each site's fileset vendors a
# different tree off identical go.mod/go.sum (issue #784). nixBuilderImage is
# pinned by digest for supply-chain safety; docs/reference.md cannot import
# nix, so update this file first, then its "Bumping the pin" section.
{
  launcherVendorHash = "sha256-sTY+2ubwPKONRHWMKy/3/xOQ+Q4EZski7Qiq7gJaQ2w=";
  driverExecVendorHash = "sha256-Iyy3pXHAYwXgDA85SS5ouPLmRjHWhMBOhdVMWhfWQNk=";
  # Its own field because the orchestrator's fileset never pulled in go-toml
  # the way driver-exec's did once the ecosystem rows took over the
  # committed-config parsers.
  orchestratorVendorHash = "sha256-Bh3JiWUuQEfWvapyawzC13d/wwgvxdUl11j/Zia1P10=";
  # launcher-currency's fileset excludes driver-exec, orchestrator and
  # quickstart (each an independent `package main` the launcher never imports)
  # and all _test.go files, so it vendors differently even off identical
  # go.mod/go.sum (#784, issue #2677).
  launcherCurrencyVendorHash = "sha256-q5jyNelr05+EY930FEOGo19uK5Z2UY+eYl8RebSgVG4=";
  nixBuilderImage = "docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8";
}
