# The Box's baked nix.conf bounds are single-sourced in lib/image.nix's
# nixConfigFile. The lore-parity and go-check-env checks need the actual
# baked values, so this parses one "<key> = N" line for them. Requiring
# exactly one whole-line match keeps the scan honest about which line
# nixConfigFile bakes; a second one would win or lose by position alone.
key:
let
  imageNixText = builtins.readFile ../../lib/image.nix;
  imageNixLines = builtins.filter builtins.isString (builtins.split "\n" imageNixText);
  pattern = "[[:space:]]*${key} = ([0-9]+)[[:space:]]*";
  lineMatches = builtins.filter (l: builtins.match pattern l != null) imageNixLines;
in
if lineMatches == [ ] then
  throw "nix/checks/image-nix-setting.nix: no \"${key} = N\" line found anywhere in lib/image.nix -- has nixConfigFile been renamed or restructured?"
else if builtins.length lineMatches > 1 then
  throw "nix/checks/image-nix-setting.nix: found ${toString (builtins.length lineMatches)} \"${key} = N\" lines in lib/image.nix -- cannot tell which one nixConfigFile bakes."
else
  builtins.head (builtins.match pattern (builtins.head lineMatches))
