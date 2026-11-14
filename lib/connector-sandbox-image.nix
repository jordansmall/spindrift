# The Connector sandbox image (issue #7014): connector-host alone, so a
# Connector runs in a container holding nothing it could use beyond the one
# static binary. It declares no `config.Env` on purpose: the Box image's Env
# (PATH, HOME, CA bundle, ...) would hand a sandboxed Connector ambient
# configuration it never needs: the module and every Host call cross stdio.
{
  pkgs,
  # The Linux build of connector-host (lib/harness-core.nix), not the host's.
  connectorHost,
  # Overridden only by the escape-test image (issue #7017), so loading it never
  # replaces the real one.
  name ? "spindrift-connector-sandbox",
}:
pkgs.dockerTools.buildLayeredImage {
  inherit name;
  tag = "latest";
  contents = [ connectorHost ];
  config = {
    Entrypoint = [ "${connectorHost}/bin/connector-host" ];
    # Numeric, so no /etc/passwd is needed in an image that has no /etc.
    User = "65534:65534";
    WorkingDir = "/";
  };
}
