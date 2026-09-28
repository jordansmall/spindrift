# The Box's baked cores bound is single-sourced in lib/image.nix's
# nixConfigFile. Both nix-checks-lore-parity.nix (which checks the lore
# texts quote it correctly) and go.nix's go-check-env check (which checks
# GOMAXPROCS's fallback matches it) need the actual baked value, so this
# module parses it once for both to import. Requiring exactly one whole-line
# "cores = N" match keeps the scan honest about which line nixConfigFile
# bakes; a second one would win or lose by position alone.
let
  imageNixText = builtins.readFile ../../lib/image.nix;
  imageNixLines = builtins.filter builtins.isString (builtins.split "\n" imageNixText);
  coresLineMatches = builtins.filter (
    l: builtins.match "[[:space:]]*cores = [0-9]+[[:space:]]*" l != null
  ) imageNixLines;
in
if coresLineMatches == [ ] then
  throw "nix/checks/image-nix-cores.nix: no \"cores = N\" line found anywhere in lib/image.nix -- has nixConfigFile been renamed or restructured?"
else if builtins.length coresLineMatches > 1 then
  throw "nix/checks/image-nix-cores.nix: found ${toString (builtins.length coresLineMatches)} \"cores = N\" lines in lib/image.nix -- cannot tell which one nixConfigFile bakes."
else
  builtins.head (
    builtins.match "[[:space:]]*cores = ([0-9]+)[[:space:]]*" (builtins.head coresLineMatches)
  )
