# The prompt fragment files the forgejo Connector contributes to the tracker
# and forge axes (issue #6867): `dir` is laid out `<folder>/<fragment>` like
# templates/default/prompts/fragments, `shipped` declares which folders carry
# each file (lib/fragment-paths.nix's Connector contribution shape).
{
  name = "forgejo";
  dir = ./fragments;
  shipped = {
    "filer-dedup.md" = [ "forgejo" ];
    "fix-ci-read.md" = [ "forgejo" ];
  };
}
