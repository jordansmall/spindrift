# The prompt fragment files the connectorname Connector contributes to the
# forge axis (issue #6867): `dir` is laid out `<folder>/<fragment>`
# like templates/default/prompts/fragments, `shipped` declares which folders
# carry each file (lib/fragment-paths.nix's Connector contribution shape).
{
  name = "connectorname";
  dir = ./fragments;
  shipped = {
    "fix-ci-read.md" = [ "connectorname" ];
  };
}
