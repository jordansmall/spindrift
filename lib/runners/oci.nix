# "rancher" is an alias for Rancher Desktop's containerd mode (ADR 0027).
{
  name = "oci";
  goName = "OCI";
  runtimes = [
    "podman"
    "docker"
    "rancher"
  ];
  artifact = "oci-image";
  networkModes = [
    "open"
    "no-host-loopback"
    "none"
    "host"
  ];
}
