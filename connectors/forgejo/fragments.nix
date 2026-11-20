# The prompt fragment files the forgejo Connector contributes to the tracker
# and forge axes (issue #6867): `dir` is laid out `<folder>/<fragment>` like
# templates/default/prompts/fragments, `shipped` declares which folders carry
# each file (lib/fragment-paths.nix's Connector contribution shape).
{
  name = "forgejo";
  dir = ./fragments;
  shipped = {
    "filer-dedup.md" = [ "forgejo" ];
    "filer-direct-file.md" = [ "forgejo" ];
    "filer-direct-label.md" = [ "forgejo" ];
    "fix-ci-read.md" = [ "forgejo" ];
    "if-blocked-pr.md" = [ "forgejo" ];
    "issue-blocked-comment.md" = [ "forgejo" ];
    "open-pr-create.md" = [ "forgejo" ];
    "research-outcome-landing.md" = [ "forgejo" ];
    "research-verdict.md" = [ "forgejo" ];
  };
}
