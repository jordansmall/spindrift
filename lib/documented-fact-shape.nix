# Shared by lib/documented-facts.nix and nix/checks/schema-drift.nix's
# documented-fact-marker-shape-guard, so the marker-shape contract and its
# regression test cannot drift (issue #2948). nix/regen.nix matches awk
# records, which carry no trailing newline, against the markers and strips
# beginMarker's newline itself, so a wrong shape below truncates the doc to EOF.
let
  hasSuffix =
    suffix: s:
    let
      lenS = builtins.stringLength s;
      lenSuffix = builtins.stringLength suffix;
    in
    lenS >= lenSuffix && builtins.substring (lenS - lenSuffix) lenSuffix s == suffix;
in
{
  assertMarkerShape =
    row:
    if !(hasSuffix "\n" row.beginMarker) then
      throw "documented-facts row '${row.name}': beginMarker must end with a trailing newline, or nix/regen.nix's write_between silently truncates the doc after this marker"
    else if hasSuffix "\n" row.endMarker then
      throw "documented-facts row '${row.name}': endMarker must not end with a trailing newline, or nix/regen.nix's write_between silently truncates the doc after this marker"
    else if !(hasSuffix "\n" row.generated) then
      throw "documented-facts row '${row.name}': generated must end with a trailing newline, or nix/regen.nix's write_between glues the end marker onto generated's last line and silently truncates the doc after this marker"
    else
      row;
}
