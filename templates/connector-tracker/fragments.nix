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
    # A Connector declaring the tracker axis owes these three, which core ships
    # for its own trackers only (issue #7647).
    "filer-dedup.md" = [ "connectorname" ];
    "pr-body-issue-ref.md" = [ "connectorname" ];
    "pr-body-no-issue-ref.md" = [ "connectorname" ];
  };
}
