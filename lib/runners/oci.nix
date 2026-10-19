# "rancher" is an alias for Rancher Desktop's containerd mode (ADR 0027).
{
  name = "oci";
  runtimes = [
    "podman"
    "docker"
    "rancher"
  ];
  artifact = "oci-image";
}
