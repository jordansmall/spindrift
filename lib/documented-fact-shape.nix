# Shared by lib/documented-facts.nix (self-validates every row at
# construction) and nix/checks/schema-drift.nix's
# documented-fact-marker-shape-guard (exercises it directly against
# synthetic rows), so the marker-shape contract and its regression test can
# never drift from each other (issue #2948).
#
# nix/regen.nix's write_between matches an awk record ($0, which awk has
# already stripped of its own trailing newline) against a marker literal --
# so beginMarker must carry exactly the one trailing "\n" that its own call
# site strips back off (removeSuffix "\n"), and endMarker must carry none,
# or write_between's `$0 == end` never matches, `skip` runs to EOF, and
# everything in the doc after the begin marker is silently deleted on the
# next `nix run .#regen`. The same failure mode follows from generated
# itself: write_between splices it in verbatim right after the begin-marker
# line, so if generated doesn't end with "\n" the end-marker text lands
# glued onto generated's last line instead of starting its own -- `$0 ==
# end` then never matches on the next `nix run .#regen`, and this row's
# block gets silently truncated to EOF exactly the same way.
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
