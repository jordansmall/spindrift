# The runtime knob's valid values (ADR 0027). "rancher" is an alias for
# Rancher Desktop's containerd mode. lib/flakeModule.nix's `runtime` option
# enum and cmd/launcher/internal/runner/runtimevalues_gen.go both import this
# list, so they cannot drift.
[
  "podman"
  "docker"
  "rancher"
  "bwrap"
]
