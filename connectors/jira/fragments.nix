# The prompt fragment files the jira Connector contributes to the tracker axis
# (issue #6857): `dir` is laid out `<folder>/<fragment>` like
# templates/default/prompts/fragments, `shipped` declares which folders carry
# each file (lib/fragment-paths.nix's Connector contribution shape).
{
  name = "jira";
  dir = ./fragments;
  shipped = {
    # Jira has no in-Box comment client, so the shared root step naming a
    # forge token would be wrong for it.
    "issue-blocked-comment-relay.md" = [ "jira" ];
  };
}
