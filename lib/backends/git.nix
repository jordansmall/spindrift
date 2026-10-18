# The git backend row: pure data only (issue #5633). The registry
# (./default.nix) documents the fields and fixes this row's order.
{
  name = "git";
  goVar = "Git";
  validAsCodeForge = true;
  inBoxGHCredentialHelper = true;
}
