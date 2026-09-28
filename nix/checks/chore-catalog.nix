# Eval-level pins for lib/chore-catalog.nix, the single declaration of the
# built-in butler Chores (ADR 0056, issue #3991).
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    sort
    lessThan
    hasSuffix
    removeSuffix
    ;
  catalog = import ../../lib/chore-catalog.nix;
  choresDir = ../../templates/default/prompts/chores;
  promptStems = sort lessThan (
    map (removeSuffix ".md") (
      builtins.filter (n: hasSuffix ".md" n) (builtins.attrNames (builtins.readDir choresDir))
    )
  );
  catalogNamesSorted = sort lessThan catalog.names;
in
{
  chore-catalog-prompt-paths-exist =
    let
      bad = builtins.filter (
        c: c.prompt != (choresDir + "/${c.name}.md") || !builtins.pathExists c.prompt
      ) catalog.chores;
    in
    assert assertMsg (bad == [ ])
      "every lib/chore-catalog.nix entry's prompt must exist at templates/default/prompts/chores/<name>.md, offenders: ${
        concatStringsSep ", " (map (c: c.name) bad)
      }";
    pkgs.runCommand "chore-catalog-prompt-paths-exist" { } "touch $out";

  chore-catalog-names-match-prompt-dir =
    assert assertMsg (catalogNamesSorted == promptStems)
      "lib/chore-catalog.nix's names (${concatStringsSep ", " catalogNamesSorted}) must equal templates/default/prompts/chores/*.md stems (${concatStringsSep ", " promptStems}) -- a prompt file added/removed without a matching catalog entry (or vice versa)";
    pkgs.runCommand "chore-catalog-names-match-prompt-dir" { } "touch $out";
}
