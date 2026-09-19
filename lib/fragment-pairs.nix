# Exactly-one-on fragment-pair declarations for lib/fragments.nix (issue #3219).
# `inverseOf` lets a row declare on the registry side that it is the off-member
# of a pair, so a broken pair fails at eval time instead of surfacing later as a
# rendering bug. Pure builtins only, no `pkgs`: lib/fragments.nix imports this
# with no arguments and must stay argument-free.
let
  # builtins has no dedup primitive and this file cannot reach `lib.unique`, so
  # this fold appends a value unless `key` was already seen, preserving order.
  uniqueBy =
    key:
    builtins.foldl' (acc: x: if builtins.elem (key x) (map key acc) then acc else acc ++ [ x ]) [ ];

  pairKey = p: p.on + " " + p.off;

  # Two rows declaring the same off-gate to be the inverse of the same on-gate
  # collapse to one pair; `validate` rejects the case where they disagree.
  pairsOf =
    rows:
    uniqueBy pairKey (
      map (r: {
        on = r.inverseOf;
        off = r.gate;
      }) (builtins.filter (r: r ? inverseOf) rows)
    );
in
{
  inherit pairsOf;

  # Each rule below rejects a configuration that would make both pair members
  # render together, or neither render, at some value of the shared knob.
  validate =
    rows:
    let
      declared = builtins.filter (r: r ? inverseOf) rows;
      gateNames = map (r: r.gate) rows;
      inverseOfGateNames = map (r: r.gate) declared;

      checkRow =
        ok: r:
        if r.inverseOf == r.gate then
          # With one gate on both members, both members always render together.
          throw
            "lib/fragment-pairs.nix: row '${r.gate}' declares inverseOf itself; a pair's on-gate and off-gate must differ"
        else if !(builtins.elem r.inverseOf gateNames) then
          # With no on-member, neither member renders when the on-gate is true.
          throw
            "lib/fragment-pairs.nix: row '${r.gate}' declares inverseOf '${r.inverseOf}', which is not the gate of any registry row"
        else if builtins.elem r.inverseOf inverseOfGateNames then
          # In an inverse-of-an-inverse chain the on-gate is not itself a
          # single boolean knob, so "exactly one on" no longer follows.
          throw
            "lib/fragment-pairs.nix: row '${r.gate}' declares inverseOf '${r.inverseOf}', but '${r.inverseOf}' itself carries inverseOf -- inverse-of-an-inverse chains are not a single boolean knob"
        else
          ok;

      rowsOk = builtins.foldl' checkRow true declared;

      pairs = pairsOf rows;
      offGates = uniqueBy (g: g) (map (p: p.off) pairs);

      checkOffGate =
        ok: offGate:
        let
          # `pairs` is deduplicated by (on, off), so a second entry for this
          # off-gate can only mean a second, different on-gate.
          ons = map (p: p.on) (builtins.filter (p: p.off == offGate) pairs);
        in
        # Two different on-gates let the off member render alongside whichever
        # on-gate is currently true.
        if builtins.length ons > 1 then
          throw "lib/fragment-pairs.nix: off-gate '${offGate}' is declared the inverse of more than one on-gate: ${builtins.concatStringsSep ", " ons}"
        else
          ok;
    in
    # Seeding the fold with `rowsOk` orders the two phases: foldl' is strict in
    # its accumulator, so every per-row throw fires before any off-gate throw.
    # A registry with both kinds of defect reports the row-level message, which
    # names the one bad row.
    builtins.foldl' checkOffGate rowsOk offGates;
}
