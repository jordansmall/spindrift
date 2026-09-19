# Maps each flat structural knob name to its domain-tree path segments. Both
# lib/flakeModule.nix and nix/checks/schema-drift.nix's
# flake-nixpath-exhaustive-disjoint check read this one file, so the mapping
# lives here only (ADR 0037, issue #2184).
{
  driver = [
    "agents"
    "driver"
  ];
  prompt = [
    "agents"
    "prompt"
  ];
  skills = [
    "agents"
    "skills"
  ];
  roster = [
    "agents"
    "models"
    "roster"
  ];
  runtime = [
    "infra"
    "runtime"
  ];
  packages = [
    "infra"
    "image"
    "packages"
  ];
  prefetch = [
    "infra"
    "image"
    "prefetch"
  ];
  extraClosures = [
    "infra"
    "image"
    "extraClosures"
  ];
  nixInBox = [
    "infra"
    "nix"
    "inBox"
  ];
  nixStoreWritable = [
    "infra"
    "nix"
    "storeWritable"
  ];
  nixpkgs = [
    "infra"
    "nixpkgs"
  ];
  overlays = [
    "infra"
    "overlays"
  ];
  config = [
    "infra"
    "config"
  ];
}
