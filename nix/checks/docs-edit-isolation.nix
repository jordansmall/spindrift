# Issue #5154: a docs-only edit must rebuild only the checks that read that
# doc. Re-evaluates the checkset off a copy of the repo minus two docs files
# and asserts the narrowed checks keep their drvPaths. Subtree copies are
# content-addressed, so a check moves only if it really consumes a perturbed
# file or stringifies the whole source root (the leak this pins).
{
  pkgs,
  checksAt,
  baselineChecks,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    filter
    hasPrefix
    hasSuffix
    head
    sort
    any
    attrNames
    attrValues
    ;

  # README.md is left out: go.nix copies it as a single file, so removing it
  # makes launcher-go-test throw at eval instead of moving.
  # Any ADR would do; picking one from the directory survives an ADR rename.
  adrDir = ../../docs/adr;
  adrName = head (
    sort builtins.lessThan (
      filter (n: builtins.match "[0-9]{4}-.*[.]md" n != null) (attrNames (builtins.readDir adrDir))
    )
  );
  perturbedPaths = [
    (toString ../../docs/reference.md)
    "${toString adrDir}/${adrName}"
  ];
  missing = filter (p: !builtins.pathExists p) perturbedPaths;

  perturbedRoot = builtins.path {
    path = ../..;
    name = "source";
    filter = path: _type: !(builtins.elem path perturbedPaths);
  };

  perturbed = (checksAt perturbedRoot).checks;

  families = {
    "bats-shard-*" = hasPrefix "bats-shard-";
    # checksAt passes the outer flake's `config` through un-re-rooted, so a
    # docs leak reaching flakemodule-equivalence or flakemodule-dogfood-drivers
    # only via config.packages.spindrift / config.apps is invisible here.
    "flakemodule-*" = hasPrefix "flakemodule-";
    "baked-skills-*-gen" = n: hasPrefix "baked-skills-" n && hasSuffix "-gen" n;
  };
  baselineNames = attrNames baselineChecks;
  emptyFamilies = filter (f: !(any families.${f} baselineNames)) (attrNames families);
  mustNotMove = filter (n: any (f: f n) (attrValues families)) baselineNames;
  movers = filter (n: baselineChecks.${n}.drvPath != perturbed.${n}.drvPath) mustNotMove;

  # Genuine reader of docs/ (nix/checks/go.nix); if it does not move, the
  # perturbation never reached the checkset and the pin is vacuous.
  docsReaderControl = "launcher-go-test";
in
{
  docs-edit-isolation =
    assert assertMsg (
      missing == [ ]
    ) "docs-edit-isolation perturbs a file that no longer exists: ${concatStringsSep ", " missing}";
    assert assertMsg (
      movers == [ ]
    ) "a docs-only edit moved checks that should not read docs: ${concatStringsSep ", " movers}";
    assert assertMsg (
      baselineChecks.${docsReaderControl}.drvPath != perturbed.${docsReaderControl}.drvPath
    ) "${docsReaderControl} did not move under the docs perturbation -- the perturbation is vacuous";
    assert assertMsg (emptyFamilies == [ ])
      "docs-edit-isolation matched no checks for: ${concatStringsSep ", " emptyFamilies} -- the prefixes are stale";
    pkgs.runCommand "docs-edit-isolation" { } "touch $out";
}
