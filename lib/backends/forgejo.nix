# The forgejo backend row: pure data only (issue #5633). The registry
# (./default.nix) documents the fields and fixes this row's order.
{
  name = "forgejo";
  goVar = "Forgejo";
  validAsTracker = true;
  validAsCodeForge = true;
  tokenEnvVar = "FORGEJO_TOKEN";
  doctorTokenHint = "FORGEJO_TOKEN";
  doctorSlugHint = "FORGEJO_BASE_URL";
  outboxRelayCapable = true;
  relayCapable = true;
  hostPostingCapable = true;
  labelRegistry = true;
  trackerAxisRead = "FORGEJO";
  trackerAxisWrite = "FORGEJO";
  trackerAxisFiler = "FORGEJO";
  forgeBackend = "FORGEJO";
}
