# Exactly-one-on fragment-pair declarations for lib/fragments.nix (issue
# #3219). Before this module, a pair like REVIEW_LOOP_INLINE/
# REVIEW_LOOP_ORCHESTRATOR relied entirely on the *downstream* computation
# strategy (both gates derived from one boolean knob) to guarantee exactly
# one renders -- nothing in the registry itself said the two rows were a
# pair, so a future row-edit that broke that computation would only surface
# as a rendering bug, not an eval-time error. `inverseOf` lets a row declare
# that relationship on the registry side: pure builtins only (no `pkgs`),
# since lib/fragments.nix is imported with no arguments and must stay
# argument-free itself.
let
  # Order-preserving "append unless `key` was already seen". builtins has no
  # dedup primitive and this file cannot reach for `lib.unique`, so the fold
  # lives here once instead of being re-spelled at each use below.
  uniqueBy =
    key:
    builtins.foldl' (acc: x: if builtins.elem (key x) (map key acc) then acc else acc ++ [ x ]) [ ];

  pairKey = p: p.on + " " + p.off;

  # The deduplicated list of `{ on; off; }` pairs declared across rows,
  # derived from every row carrying `inverseOf`. Two rows both declaring the
  # same off-gate the inverse of the same on-gate collapse to one pair;
  # `validate` is what rejects the case where they disagree.
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

  # Validates every `inverseOf` declaration in rows, throwing a message
  # naming the offending gate on the first violation found; returns `true`
  # when every declared pair is well-formed. Each rule below is a distinct
  # configuration that would make both pair members render together, or
  # neither render, at some value of the shared knob:
  validate =
    rows:
    let
      declared = builtins.filter (r: r ? inverseOf) rows;
      gateNames = map (r: r.gate) rows;
      inverseOfGateNames = map (r: r.gate) declared;

      checkRow =
        ok: r:
        if r.inverseOf == r.gate then
          # Same gate on both members -> both render together, always.
          throw
            "lib/fragment-pairs.nix: row '${r.gate}' declares inverseOf itself; a pair's on-gate and off-gate must differ"
        else if !(builtins.elem r.inverseOf gateNames) then
          # No on-member -> when the on-gate is true, neither member renders.
          throw
            "lib/fragment-pairs.nix: row '${r.gate}' declares inverseOf '${r.inverseOf}', which is not the gate of any registry row"
        else if builtins.elem r.inverseOf inverseOfGateNames then
          # inverse-of-an-inverse -> the on-gate's truth value is not itself
          # a single boolean knob, so "exactly one on" no longer follows.
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
        # Two different on-gates -> the off member can render alongside
        # whichever on-gate is currently true.
        if builtins.length ons > 1 then
          throw "lib/fragment-pairs.nix: off-gate '${offGate}' is declared the inverse of more than one on-gate: ${builtins.concatStringsSep ", " ons}"
        else
          ok;
    in
    # Seeding the fold with `rowsOk` is what orders the two phases: foldl' is
    # strict in its accumulator, so every per-row throw above fires before
    # any off-gate throw here. A registry with both kinds of defect reports
    # the row-level message, which names the one bad row.
    builtins.foldl' checkOffGate rowsOk offGates;
}
