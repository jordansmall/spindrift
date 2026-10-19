# Derives the runtime enum and the runtime-to-row lookup from the runner rows
# (lib/runners/default.nix). `rows` is an argument only so
# nix/checks/schema-drift.nix can feed it a malformed row set. A runtime listed
# by two rows, or a row with an unknown artifact, throws when `runtimes`,
# `forRuntime`, or `rows` is forced, so a bad family never reaches the enum, the
# harness, or the generated Go. So does a row listing a network mode outside the
# NETWORK_MODE enum. `unsupportedNetworkModeMessage` is the one wording for a
# mode a row does not list.
{
  rows ? import ./default.nix,
}:
let
  artifacts = [
    "oci-image"
    "store-closure"
  ];
  networkModes = (import ../env-schema.nix).networkMode.choices;
  runtimes = builtins.concatMap (r: r.runtimes) rows;
  duplicates = builtins.filter (
    rt: builtins.length (builtins.filter (x: x == rt) runtimes) > 1
  ) runtimes;
  badArtifacts = builtins.filter (r: !(builtins.elem r.artifact artifacts)) rows;
  badNetworkModes = builtins.filter (
    r: !(builtins.all (m: builtins.elem m networkModes) r.networkModes)
  ) rows;
  checkedRows =
    if duplicates != [ ] then
      throw "lib/runners: runtime ${builtins.head duplicates} is listed by more than one runner row"
    else if badArtifacts != [ ] then
      let
        bad = builtins.head badArtifacts;
      in
      throw "lib/runners: runner row ${bad.name} has artifact \"${bad.artifact}\", expected one of: ${builtins.concatStringsSep ", " artifacts}"
    else if badNetworkModes != [ ] then
      let
        bad = builtins.head badNetworkModes;
        badMode = builtins.head (builtins.filter (m: !(builtins.elem m networkModes)) bad.networkModes);
      in
      throw "lib/runners: runner row ${bad.name} has network mode \"${badMode}\", expected one of: ${builtins.concatStringsSep ", " networkModes}"
    else
      rows;
in
{
  # cmd/launcher/internal/runner/family.go's CheckNetworkMode builds the same
  # text for the launch gate, so change both together.
  unsupportedNetworkModeMessage =
    row: mode:
    "NETWORK_MODE=${mode} is unsupported on runner family ${row.name}, which supports: ${builtins.concatStringsSep ", " row.networkModes}";
  inherit networkModes;
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
