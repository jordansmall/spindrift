# Derives the runtime enum and the runtime-to-row lookup from the runner rows
# (lib/runners/default.nix). `rows` is an argument only so
# nix/checks/schema-drift.nix can feed it a malformed row set. A runtime listed
# by two rows, or a row with an unknown artifact, throws when `runtimes`,
# `forRuntime`, or `rows` is forced, so a bad family never reaches the enum, the
# harness, or the generated Go.
{
  rows ? import ./default.nix,
}:
let
  artifacts = [
    "oci-image"
    "store-closure"
  ];
  runtimes = builtins.concatMap (r: r.runtimes) rows;
  duplicates = builtins.filter (
    rt: builtins.length (builtins.filter (x: x == rt) runtimes) > 1
  ) runtimes;
  badArtifacts = builtins.filter (r: !(builtins.elem r.artifact artifacts)) rows;
  checkedRows =
    if duplicates != [ ] then
      throw "lib/runners: runtime ${builtins.head duplicates} is listed by more than one runner row"
    else if badArtifacts != [ ] then
      let
        bad = builtins.head badArtifacts;
      in
      throw "lib/runners: runner row ${bad.name} has artifact \"${bad.artifact}\", expected one of: ${builtins.concatStringsSep ", " artifacts}"
    else
      rows;
in
{
  rows = checkedRows;
  runtimes = builtins.seq checkedRows runtimes;
  forRuntime =
    runtime:
    let
      matches = builtins.filter (r: builtins.elem runtime r.runtimes) checkedRows;
    in
    if matches == [ ] then
      throw "lib/runners: no runner row lists runtime ${runtime}"
    else
      builtins.head matches;
}
