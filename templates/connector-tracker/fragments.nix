# The prompt fragment files the connectorname Connector contributes to the
# tracker axis (issue #6857): `dir` is laid out `<folder>/<fragment>` like
# templates/default/prompts/fragments, `shipped` declares which folders carry
# each file (lib/fragment-paths.nix's Connector contribution shape).
{
  name = "connectorname";
  dir = ./fragments;
  shipped = {
    # The Connector registers no comment writer, so the shared root step naming
    # a forge token would be wrong for it. Drop this entry once it posts comments.
    "issue-blocked-comment-relay.md" = [ "connectorname" ];
  };
}
