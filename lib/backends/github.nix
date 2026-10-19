# The github backend row: pure data only (issue #5633). The registry
# (./default.nix) documents the fields and fixes this row's order.
{
  name = "github";
  goVar = "GitHub";
  displayName = "GitHub";
  validAsTracker = true;
  validAsCodeForge = true;
  tokenEnvVar = "GH_TOKEN";
  outboxRelayCapable = true;
  inBoxOpenPRQueryable = true;
  inBoxGHCredentialHelper = true;
  relayCapable = true;
  hostPostingCapable = true;
  labelRegistry = true;
  quickstart = {
    hosts = [ "github.com" ];
    axes = [
      "tracker"
      "forge"
    ];
  };
}
