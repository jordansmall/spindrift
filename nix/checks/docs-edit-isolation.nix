# Issue #5154: a docs-only edit must rebuild only the checks that read that
# doc. Re-evaluates the checkset off a copy of the repo minus three docs files
# and asserts the narrowed checks keep their drvPaths. Subtree copies are
# content-addressed, so a check moves only if it really consumes a perturbed
# file or stringifies the whole source root (the leak this pins). A second
# copy minus only README.md proves README.md itself still reaches its reader.
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

  readme = toString ../../README.md;

  # Any ADR would do; picking one from the directory survives an ADR rename.
  adrDir = ../../docs/adr;
  adrName = head (
    sort builtins.lessThan (
      filter (n: builtins.match "[0-9]{4}-.*[.]md" n != null) (attrNames (builtins.readDir adrDir))
    )
  );
  perturbedPaths = [
    (toString ../../docs/reference.md)
    readme
    "${toString adrDir}/${adrName}"
  ];
  missing = filter (p: !builtins.pathExists p) perturbedPaths;

  checksWithout =
    paths:
    (checksAt (
      builtins.path {
        path = ../..;
        name = "source";
        filter = path: _type: !(builtins.elem path paths);
      }
    )).checks;

  perturbed = checksWithout perturbedPaths;

  # docs/reference.md alone moves docsReaderControl, so a README-only
  # regression would hide behind it; this root removes only README.md.
  readmeOnly = checksWithout [ readme ];

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

  # Genuine reader of docs/ and README.md (nix/checks/go.nix); if it does not
  # move, the perturbation never reached the checkset and the pin is vacuous.
  docsReaderControl = "launcher-binaries-go-test";
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
    assert assertMsg
      (baselineChecks.${docsReaderControl}.drvPath != readmeOnly.${docsReaderControl}.drvPath)
      "${docsReaderControl} did not move when only README.md was removed -- README.md no longer reaches it";
    assert assertMsg (emptyFamilies == [ ])
      "docs-edit-isolation matched no checks for: ${concatStringsSep ", " emptyFamilies} -- the prefixes are stale";
    pkgs.runCommand "docs-edit-isolation" { } "touch $out";
}
