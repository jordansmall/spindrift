# Issue #3613: true iff every needle sits on one comment line or across two
# adjacent ones. A reflow of a pointer comment splits it across at most two
# lines (each needle fits inside the wrap width), so a two-line window passes
# every legitimate rewrap. Matching the joined block instead let the names sit
# in unrelated sentences and still satisfy the check.
{ lib }:
needles: lines:
let
  n = builtins.length lines;
  windows =
    if n == 0 then
      [ ]
    else if n == 1 then
      [ lines ]
    else
      builtins.genList (i: [
        (builtins.elemAt lines i)
        (builtins.elemAt lines (i + 1))
      ]) (n - 1);
  hasAll =
    w:
    let
      text = lib.concatStringsSep "\n" w;
    in
    builtins.all (needle: lib.hasInfix needle text) needles;
in
builtins.any hasAll windows
