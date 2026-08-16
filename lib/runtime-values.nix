# The runtime knob's valid values (ADR 0027): OCI runtimes (podman/docker,
# rancher an alias for Rancher Desktop's containerd mode) or the
# daemonless bubblewrap runner. Single root for lib/flakeModule.nix's
# `runtime` option enum and the quickstart wizard's generated choice list
# (cmd/launcher/quickstart/quickstart_runtime_gen.go) — imported by both so
# they can't drift.
[
  "podman"
  "docker"
  "rancher"
  "bwrap"
]
